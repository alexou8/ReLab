package fault

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Injector decides whether a fault fires at a given point, and applies it.
//
// One Injector serves one run. It is safe for concurrent use, because a worker
// executes several tasks at once and each of them reaches trigger points
// independently.
type Injector struct {
	scenario *Scenario
	runID    uuid.UUID
	// derive builds a deterministic random source for a named draw. It is
	// supplied rather than constructed here so that the engine's own derivation
	// is used and a scenario reproduces identically wherever it runs.
	derive func(seed int64, parts ...string) *rand.Rand
	// record is called before a fault takes effect. Recording first is
	// essential for worker-crash: the process is about to die, and an event
	// written afterwards would never exist.
	record RecordFunc

	mu    sync.Mutex
	draws map[string]int64
	fired int
}

// RecordFunc writes a FAULT_INJECTED event. It must return only after the event
// is durably committed.
type RecordFunc func(ctx context.Context, f Firing) error

// Firing describes a fault that is about to take effect.
type Firing struct {
	RunID    uuid.UUID
	TaskName string
	Type     Type
	Point    Point
	Scenario string
	Seed     int64
	Draw     int64
	Params   json.RawMessage
}

// NewInjector returns an injector for one run under one scenario.
func NewInjector(scenario *Scenario, runID uuid.UUID, seedOverride int64,
	derive func(int64, ...string) *rand.Rand, record RecordFunc) *Injector {
	if scenario != nil && seedOverride != 0 {
		// The run's seed wins over the scenario file's, so `--seed` can
		// reproduce one particular run of a scenario.
		scenario = scenario.withSeed(seedOverride)
	}
	return &Injector{
		scenario: scenario,
		runID:    runID,
		derive:   derive,
		record:   record,
		draws:    map[string]int64{},
	}
}

func (s *Scenario) withSeed(seed int64) *Scenario {
	clone := *s
	clone.Seed = seed
	return &clone
}

// Point evaluates the trigger point for a task, applying any fault that fires.
//
// It returns an error only when the fault is one that fails the task
// (http-error, db-disconnect); faults that delay or kill return nil or do not
// return at all. A nil Injector fires nothing, so callers need no nil check.
func (i *Injector) Point(ctx context.Context, point Point, taskName string, attempt int) error {
	if i == nil || i.scenario == nil {
		return nil
	}
	for idx, f := range i.scenario.Faults {
		if !f.matches(point, taskName, attempt) {
			return nil
		}
		draw, fires := i.decide(f, idx, taskName, attempt)
		if !fires {
			continue
		}
		if err := i.fire(ctx, f, point, taskName, draw); err != nil {
			return err
		}
	}
	return nil
}

// matches reports whether a fault's target selects this task at this point.
func (f Fault) matches(point Point, taskName string, attempt int) bool {
	if f.At != "" && f.At != point {
		return false
	}
	if f.Target.Task != "" && f.Target.Task != taskName {
		return false
	}
	if f.Target.Attempt != 0 && f.Target.Attempt != attempt {
		return false
	}
	return true
}

// decide draws for a probabilistic fault, or returns true for an explicit one.
//
// The draw is derived from the run, the fault's position in the scenario, the
// task and the attempt — never from a counter of how many draws came before.
// Two workers finishing in the other order would otherwise swap their draws and
// the scenario would not reproduce.
func (i *Injector) decide(f Fault, index int, taskName string, attempt int) (int64, bool) {
	key := fmt.Sprintf("%d:%s:%d", index, taskName, attempt)

	i.mu.Lock()
	defer i.mu.Unlock()
	// A draw for a given position is made once and remembered, so re-evaluating
	// the same point twice within one attempt cannot flip the decision.
	if prior, seen := i.draws[key]; seen {
		return prior, prior >= 0
	}

	if f.Probability == 0 {
		i.draws[key] = int64(index)
		return int64(index), true
	}
	rng := i.derive(i.scenario.Seed, i.runID.String(), "fault", key)
	value := rng.Float64()
	if value < f.Probability {
		draw := int64(value * 1e9)
		i.draws[key] = draw
		return draw, true
	}
	i.draws[key] = -1
	return -1, false
}

// fire records the fault and then applies it.
func (i *Injector) fire(ctx context.Context, f Fault, point Point, taskName string, draw int64) error {
	params, err := json.Marshal(f.Params)
	if err != nil {
		return fmt.Errorf("fault: encode params for %s: %w", f.Type, err)
	}
	firing := Firing{
		RunID: i.runID, TaskName: taskName, Type: f.Type, Point: point,
		Scenario: i.scenario.Name, Seed: i.scenario.Seed, Draw: draw, Params: params,
	}
	// Recorded before it takes effect. worker-crash kills the process, and an
	// event written after that would never exist.
	if i.record != nil {
		if err := i.record(ctx, firing); err != nil {
			return fmt.Errorf("fault: record %s: %w", f.Type, err)
		}
	}

	i.mu.Lock()
	i.fired++
	i.mu.Unlock()

	return apply(ctx, f)
}

// Fired returns how many faults this injector has applied.
func (i *Injector) Fired() int {
	if i == nil {
		return 0
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.fired
}

// Scenario returns the scenario in force, or nil.
func (i *Injector) Scenario() *Scenario {
	if i == nil {
		return nil
	}
	return i.scenario
}

// paramDuration reads a duration parameter, with a default.
func paramDuration(params map[string]any, key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := params[key]
	if !ok {
		return fallback, nil
	}
	s, ok := raw.(string)
	if !ok {
		return 0, fmt.Errorf("fault: %s must be a duration string like \"250ms\", got %v", key, raw)
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("fault: %s: %w", key, err)
	}
	return d, nil
}

// paramInt reads an integer parameter, with a default.
func paramInt(params map[string]any, key string, fallback int) (int, error) {
	raw, ok := params[key]
	if !ok {
		return fallback, nil
	}
	switch v := raw.(type) {
	case int:
		return v, nil
	case float64:
		return int(v), nil
	default:
		return 0, fmt.Errorf("fault: %s must be an integer, got %v", key, raw)
	}
}
