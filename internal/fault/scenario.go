// Package fault loads fault scenarios and injects the faults they describe.
//
// A scenario is a YAML file naming a seed, a list of faults with their trigger
// points, and the assertions that must hold afterwards. Injection is
// deterministic: the seed drives a per-run RNG whose draws are derived from
// where they are used rather than from how many draws came before, so a
// scenario reproduces regardless of the order in which workers happened to
// finish.
package fault

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Scenario is a validated fault scenario.
type Scenario struct {
	Name   string     `yaml:"name"`
	Seed   int64      `yaml:"seed"`
	Faults []Fault    `yaml:"faults,omitempty"`
	Assert Assertions `yaml:"assert"`

	// Hash is the sha256 of the canonical re-marshalling, so a run records
	// which scenario it ran under precisely enough to detect an edit.
	Hash string `yaml:"-"`
}

// Fault is one injection.
type Fault struct {
	Type   Type   `yaml:"type"`
	Target Target `yaml:"target,omitempty"`
	// At names the trigger point. Exactly one of At or Probability must be set:
	// a scenario that fires "sometimes" cannot be a regression test.
	At Point `yaml:"at,omitempty"`
	// Probability fires the fault on a fraction of eligible trigger points,
	// drawn from the seeded RNG. It is supported for exploratory runs and
	// refused in CI, where scenarios must use an explicit At.
	Probability float64 `yaml:"probability,omitempty"`
	// Params carries type-specific settings, such as the status code for
	// http-error or the delay for latency.
	Params map[string]any `yaml:"params,omitempty"`
}

// Target narrows which task a fault applies to. An empty Target matches every
// task in the run.
type Target struct {
	Task     string `yaml:"task,omitempty"`
	Attempt  int    `yaml:"attempt,omitempty"`
	Workflow string `yaml:"workflow,omitempty"`
}

// Assertions are the properties a scenario claims should hold after the run.
type Assertions struct {
	RunStatus         string `yaml:"run_status,omitempty"`
	LostTasks         *int   `yaml:"lost_tasks,omitempty"`
	DuplicateEffects  *int   `yaml:"duplicate_effects,omitempty"`
	MaxRecoveryTimeMS *int64 `yaml:"max_recovery_time_ms,omitempty"`
	MaxRetriesPerTask *int   `yaml:"max_retries_per_task,omitempty"`
	MinRetriesPerTask *int   `yaml:"min_retries_per_task,omitempty"`
	FaultsInjected    *int   `yaml:"faults_injected,omitempty"`
}

// LoadScenario reads and validates a scenario file.
func LoadScenario(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fault: read scenario %s: %w", path, err)
	}
	sc, err := ParseScenario(data)
	if err != nil {
		return nil, fmt.Errorf("fault: %s: %w", path, err)
	}
	return sc, nil
}

// ParseScenario validates scenario bytes.
func ParseScenario(data []byte) (*Scenario, error) {
	var sc Scenario
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&sc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if err := sc.validate(); err != nil {
		return nil, err
	}
	canonical, err := yaml.Marshal(&sc)
	if err != nil {
		return nil, fmt.Errorf("canonicalise scenario %q: %w", sc.Name, err)
	}
	sum := sha256.Sum256(canonical)
	sc.Hash = hex.EncodeToString(sum[:])
	return &sc, nil
}

func (s *Scenario) validate() error {
	var problems []string
	if s.Name == "" {
		problems = append(problems, "name is required")
	}
	if s.Seed == 0 {
		problems = append(problems,
			"seed is required: a scenario without a fixed seed does not reproduce, "+
				"which is the only thing that makes it a regression test")
	}
	for i, f := range s.Faults {
		where := fmt.Sprintf("fault %d", i+1)
		if !f.Type.Known() {
			problems = append(problems, fmt.Sprintf("%s: unknown type %q (known types: %s)",
				where, f.Type, strings.Join(TypeNames(), ", ")))
		}
		switch {
		case f.At == "" && f.Probability == 0:
			problems = append(problems, fmt.Sprintf(
				"%s: set either at: (a trigger point) or probability:", where))
		case f.At != "" && f.Probability != 0:
			problems = append(problems, fmt.Sprintf(
				"%s: set at: or probability:, not both", where))
		case f.At != "" && !f.At.Known():
			problems = append(problems, fmt.Sprintf("%s: unknown trigger point %q (known points: %s)",
				where, f.At, strings.Join(PointNames(), ", ")))
		case f.Probability < 0 || f.Probability > 1:
			problems = append(problems, fmt.Sprintf(
				"%s: probability must be between 0 and 1, got %v", where, f.Probability))
		}
		if f.Target.Attempt < 0 {
			problems = append(problems, fmt.Sprintf("%s: target attempt must not be negative", where))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("scenario is invalid:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// Deterministic reports whether every fault has an explicit trigger point.
// CI requires it: a probability-driven scenario passes or fails by luck.
func (s *Scenario) Deterministic() bool {
	for _, f := range s.Faults {
		if f.Probability != 0 {
			return false
		}
	}
	return true
}
