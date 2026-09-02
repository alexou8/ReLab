package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Definition is a validated workflow. It is only ever produced by Parse, so a
// Definition in hand has already passed every check in validate.go.
type Definition struct {
	Name    string `yaml:"name"`
	Version int    `yaml:"version"`
	Steps   []Step `yaml:"steps"`

	// Hash is the sha256 of the canonical re-marshalling of the definition.
	// Hashing the canonical form rather than the original bytes means
	// reformatting a YAML file, or reordering keys within a step, does not
	// register as a new definition — but changing a retry policy does.
	Hash string `yaml:"-"`
}

// Step is one node of the DAG.
type Step struct {
	// Name identifies the step within the workflow and is what appears in the
	// event journal. It is stable across versions by convention; nothing
	// enforces that, and renaming a step makes runs of the two versions
	// incomparable under replay.
	Name string `yaml:"name"`
	// Handler names the function registered in the SDK. Handlers are looked up
	// by this string at run time, which is why an unregistered handler has to
	// be caught at registration.
	Handler string `yaml:"handler"`
	// DependsOn lists steps that must succeed before this one is scheduled.
	DependsOn []string `yaml:"depends_on,omitempty"`
	// Retry overrides the default policy for this step. Nil means the default.
	Retry *RetryPolicy `yaml:"retry,omitempty"`
	// Timeout bounds one execution of this step's handler. Zero means the
	// configured default.
	Timeout Duration `yaml:"timeout,omitempty"`
}

// RetryPolicy is exponential backoff with jitter.
//
// The fields are the whole policy: there is no "retry on these error types"
// predicate, because deciding retryability from an error string is exactly the
// kind of fragile classification that makes recovery untestable. A handler that
// knows a failure is permanent says so by returning a permanent error; see
// package sdk.
type RetryPolicy struct {
	// MaxAttempts counts the first execution. 1 means no retry.
	MaxAttempts int `yaml:"max_attempts"`
	// InitialDelay is the wait before the second attempt.
	InitialDelay Duration `yaml:"initial_delay"`
	// Multiplier grows the delay each attempt.
	Multiplier float64 `yaml:"multiplier"`
	// MaxDelay caps the growth.
	MaxDelay Duration `yaml:"max_delay"`
	// Jitter is the fraction of the computed delay that is randomised, in
	// [0, 1]. It is drawn from the run's seeded RNG, so a scenario replays
	// identically.
	Jitter float64 `yaml:"jitter"`
}

// DefaultRetryPolicy is applied to a step that declares none.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:  3,
		InitialDelay: Duration(1 * time.Second),
		Multiplier:   2,
		MaxDelay:     Duration(30 * time.Second),
		Jitter:       0.2,
	}
}

// Duration is a time.Duration that unmarshals from YAML strings like "1s" and
// "30s". yaml.v3 decodes a bare integer as nanoseconds, which silently turns
// `initial_delay: 1` into one nanosecond; this type rejects that instead.
type Duration time.Duration

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

// UnmarshalYAML decodes a duration string.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	// A bare number is the dangerous case: yaml.v3 will happily decode it into
	// a string, and `initial_delay: 1` would then mean one nanosecond in
	// time.Duration's own parsing. Reject the number before it gets that far.
	if node.Tag == "!!int" || node.Tag == "!!float" {
		return fmt.Errorf(
			"durations must be strings with a unit like \"30s\", got the bare number %s at line %d",
			node.Value, node.Line)
	}
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("durations must be strings with a unit like \"30s\", got %q at line %d",
			node.Value, node.Line)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q at line %d: %w", s, node.Line, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML emits the duration as a string, so that the canonical form used
// for hashing round trips.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Parse reads a definition, validates it, and computes its hash.
//
// knownHandlers, when non-nil, is the set of handler names the caller can
// actually execute; every step's handler must be in it. Passing nil skips that
// check, which is what `relab workflow validate` does when it is checking a
// file's shape without a handler registry to hand.
func Parse(data []byte, knownHandlers map[string]bool) (*Definition, error) {
	var def Definition
	dec := yaml.NewDecoder(newReader(data))
	// A typo in a key name is a silent behaviour change otherwise: `retires:`
	// instead of `retry:` would parse cleanly and run with default retries.
	dec.KnownFields(true)
	if err := dec.Decode(&def); err != nil {
		return nil, fmt.Errorf("workflow: parse: %w", err)
	}

	if err := def.validate(knownHandlers); err != nil {
		return nil, err
	}

	hash, err := def.canonicalHash()
	if err != nil {
		return nil, err
	}
	def.Hash = hash
	return &def, nil
}

// canonicalHash marshals the parsed definition and hashes the result, so that
// whitespace and key order do not affect identity.
func (d *Definition) canonicalHash() (string, error) {
	canonical, err := yaml.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("workflow: canonicalise %q: %w", d.Name, err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// Canonical returns the definition re-marshalled. This is what gets stored in
// workflows.definition_yaml, so that what is stored is what was hashed.
func (d *Definition) Canonical() ([]byte, error) {
	out, err := yaml.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("workflow: canonicalise %q: %w", d.Name, err)
	}
	return out, nil
}

// Step returns the named step.
func (d *Definition) Step(name string) (Step, bool) {
	for _, s := range d.Steps {
		if s.Name == name {
			return s, true
		}
	}
	return Step{}, false
}

// RetryFor returns the effective retry policy for a step.
func (d *Definition) RetryFor(name string) RetryPolicy {
	step, ok := d.Step(name)
	if !ok || step.Retry == nil {
		return DefaultRetryPolicy()
	}
	return *step.Retry
}

// Roots returns the steps with no dependencies: the tasks a run starts with.
func (d *Definition) Roots() []string {
	roots := make([]string, 0, len(d.Steps))
	for _, s := range d.Steps {
		if len(s.DependsOn) == 0 {
			roots = append(roots, s.Name)
		}
	}
	return roots
}

// Dependents returns the steps that depend directly on name. It is what the
// scheduler consults when a task succeeds, to decide what became ready.
func (d *Definition) Dependents(name string) []string {
	var out []string
	for _, s := range d.Steps {
		for _, dep := range s.DependsOn {
			if dep == name {
				out = append(out, s.Name)
				break
			}
		}
	}
	return out
}

func (d *Definition) String() string {
	return fmt.Sprintf("%s v%d (%d steps)", d.Name, d.Version, len(d.Steps))
}
