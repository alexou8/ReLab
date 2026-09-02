// Package faultengine wires package fault to package engine.
//
// It exists to keep the dependency acyclic: the executor needs to consult an
// injector, and the injector needs to write FAULT_INJECTED events through the
// engine. Rather than have the two packages import each other, engine declares
// the small interfaces it needs and this package satisfies them.
package faultengine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/fault"
	"github.com/alexou8/relab/internal/store"
)

// Source builds and caches one injector per run.
//
// A worker executes tasks from several runs at once, and each run has its own
// scenario and seed, so the injector cannot be process-wide. The cache is
// per-worker and lives as long as the process; a run's injector is small and
// runs are finite.
type Source struct {
	engine    *engine.Engine
	scenarios ScenarioLookup

	mu        sync.Mutex
	injectors map[uuid.UUID]*fault.Injector
	// absent remembers the runs that have no scenario, so a run without faults
	// costs one map lookup rather than a database round trip per trigger point.
	absent map[uuid.UUID]bool
}

// ScenarioLookup resolves a scenario by the name recorded on a run.
//
// Scenarios are files, and a worker does not necessarily have the file the run
// was started with. The lookup is supplied by whoever starts the worker: the
// test runner passes the scenario it loaded, and a plain `relab worker` passes
// one that finds nothing, so a worker cannot silently run a different version
// of a scenario than the one the run recorded.
type ScenarioLookup func(name, hash string) (*fault.Scenario, bool)

// NewSource returns a Source.
func NewSource(e *engine.Engine, lookup ScenarioLookup) *Source {
	return &Source{
		engine:    e,
		scenarios: lookup,
		injectors: map[uuid.UUID]*fault.Injector{},
		absent:    map[uuid.UUID]bool{},
	}
}

// For returns the trigger points for a run, or nil when it has no scenario.
func (s *Source) For(ctx context.Context, runID uuid.UUID) (engine.TriggerPoints, error) {
	s.mu.Lock()
	if s.absent[runID] {
		s.mu.Unlock()
		return nil, nil
	}
	if injector, ok := s.injectors[runID]; ok {
		s.mu.Unlock()
		return adapter{injector}, nil
	}
	s.mu.Unlock()

	run, err := s.engine.RunByID(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.ScenarioName == "" || s.scenarios == nil {
		s.remember(runID, nil)
		return nil, nil
	}
	scenario, ok := s.scenarios(run.ScenarioName, "")
	if !ok {
		// The run recorded a scenario this worker does not have. Running it
		// without faults would silently produce a passing result for a test
		// that never ran, so it is an error.
		return nil, fmt.Errorf(
			"faultengine: run %s was started under scenario %q, which this worker does not have",
			runID, run.ScenarioName)
	}

	injector := fault.NewInjector(scenario, runID, run.Seed, engine.DerivedRand, s.recorder(runID))
	s.remember(runID, injector)
	return adapter{injector}, nil
}

func (s *Source) remember(runID uuid.UUID, injector *fault.Injector) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if injector == nil {
		s.absent[runID] = true
		return
	}
	s.injectors[runID] = injector
}

// recorder returns the callback that writes FAULT_INJECTED.
//
// It commits in its own transaction, before the fault takes effect. That
// ordering is what makes worker-crash observable at all: the process is about
// to be SIGKILLed, and an event written afterwards would never exist.
func (s *Source) recorder(runID uuid.UUID) fault.RecordFunc {
	return func(ctx context.Context, f fault.Firing) error {
		return s.engine.DB().InTx(ctx, func(ctx context.Context, tx store.Conn) error {
			var params json.RawMessage
			if len(f.Params) > 0 && string(f.Params) != "null" {
				params = f.Params
			}
			_, err := event.Append(ctx, tx, runID, event.FaultInjectedPayload{
				FaultType:  string(f.Type),
				FaultPoint: string(f.Point),
				Scenario:   f.Scenario,
				Seed:       f.Seed,
				Draw:       f.Draw,
				Params:     params,
			}, event.Meta{TaskName: f.TaskName})
			return err
		})
	}
}

// Injector returns the injector built for a run, if one has been built. The
// test runner uses it to report how many faults actually fired.
func (s *Source) Injector(runID uuid.UUID) *fault.Injector {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.injectors[runID]
}

// adapter narrows *fault.Injector to the interface the executor declares, and
// converts the string trigger point back into fault.Point.
type adapter struct {
	injector *fault.Injector
}

func (a adapter) Point(ctx context.Context, point, taskName string, attempt int) error {
	return a.injector.Point(ctx, fault.Point(point), taskName, attempt)
}

func (a adapter) ShouldDuplicate(taskName string, attempt int) bool {
	return a.injector.ShouldDuplicate(taskName, attempt)
}

// StaticLookup returns a ScenarioLookup serving one scenario, which is what
// `relab test` uses.
func StaticLookup(scenario *fault.Scenario) ScenarioLookup {
	return func(name, hash string) (*fault.Scenario, bool) {
		if scenario == nil || scenario.Name != name {
			return nil, false
		}
		if hash != "" && scenario.Hash != hash {
			return nil, false
		}
		return scenario, true
	}
}
