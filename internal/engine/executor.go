package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/idem"
	"github.com/alexou8/relab/sdk"
)

// Executor runs claimed tasks with a handler registry.
//
// It is the shared execution path: `relab run` drives it in process and the
// worker drives it in its own process, so a single-process run and a
// distributed one produce histories with the same shape. Anything that differs
// between them — heartbeats, lease renewal — lives in the worker, not here.
type Executor struct {
	engine   *Engine
	registry *sdk.Registry
	workerID uuid.UUID
	log      *slog.Logger
}

// NewExecutor returns an Executor acting as the given worker.
func NewExecutor(e *Engine, reg *sdk.Registry, workerID uuid.UUID, log *slog.Logger) *Executor {
	if log == nil {
		log = slog.Default()
	}
	return &Executor{engine: e, registry: reg, workerID: workerID, log: log}
}

// Execute runs one claimed task to completion and records the outcome.
//
// It returns an error only when the outcome could not be recorded. A handler
// that fails is a recorded failure, not an error from Execute: the failure is
// data the system is designed to handle, and returning it here would make every
// caller re-implement the distinction.
func (x *Executor) Execute(ctx context.Context, claimed ClaimedTask) error {
	handler, ok := x.registry.Lookup(claimed.Handler)
	if !ok {
		// The workflow was registered against a registry that had this handler
		// and is now being run by a process that does not. Record it as a
		// permanent failure: retrying on this worker will never help, and the
		// dead-letter reason names the missing handler.
		return x.engine.CompleteTask(ctx, x.workerID, claimed.Task, Outcome{
			Err:       fmt.Errorf("handler %q is not registered on this worker", claimed.Handler),
			Permanent: true,
		})
	}

	if err := x.engine.StartTask(ctx, x.workerID, claimed.Task, claimed.Handler); err != nil {
		// Losing the lease between claim and start is ordinary; the task is
		// already someone else's problem.
		if errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrConcurrentAttempt) {
			return err
		}
		return err
	}

	ledger := newLedger(x.engine, claimed.Task, x.workerID)
	tc := sdk.NewTaskContext(claimed.RunID, claimed.Task.Name, claimed.Task.Attempt,
		x.workerID, claimed.Inputs, ledger)

	outcome := x.invoke(ctx, handler, tc, claimed)
	if err := x.engine.CompleteTask(ctx, x.workerID, claimed.Task, outcome); err != nil {
		return err
	}
	return nil
}

// invoke calls the handler under its timeout and converts whatever it does —
// return, error, panic, run past its deadline — into an Outcome.
func (x *Executor) invoke(ctx context.Context, handler sdk.Handler, tc *sdk.TaskContext,
	claimed ClaimedTask) (outcome Outcome) {
	start := time.Now()

	// A handler panic must not take down a worker that is holding other tasks,
	// and it must be recorded as a failure of that task rather than disappearing
	// into a process restart.
	defer func() {
		if r := recover(); r != nil {
			outcome = Outcome{
				Err: fmt.Errorf("handler panicked: %v\n%s", r, debug.Stack()),
				// A panic is a bug in the handler, not a transient condition.
				// Retrying it burns the remaining attempts to reach the same
				// line of code.
				Permanent: true,
				Duration:  time.Since(start),
			}
		}
	}()

	runCtx, cancel := context.WithTimeout(ctx, claimed.Timeout)
	defer cancel()

	result, err := handler(runCtx, tc)
	duration := time.Since(start)

	if err != nil {
		// A handler that returns ctx.Err() after its deadline should be
		// reported as a timeout, not as a generic cancellation, because the two
		// have different fixes.
		if errors.Is(err, context.DeadlineExceeded) && runCtx.Err() != nil && ctx.Err() == nil {
			err = fmt.Errorf("task exceeded its %s timeout: %w", claimed.Timeout, err)
		}
		return Outcome{
			Err:       err,
			Permanent: sdk.IsPermanent(err),
			Duration:  duration,
			Artifacts: artifactsOf(tc),
		}
	}
	// A handler that returns nil while its context is done did not finish its
	// work; treating that as success would record an output that was never
	// produced.
	if runCtx.Err() != nil {
		return Outcome{
			Err:      fmt.Errorf("handler returned after its context ended: %w", runCtx.Err()),
			Duration: duration,
		}
	}

	output, err := marshalOutput(result)
	if err != nil {
		return Outcome{Err: err, Permanent: true, Duration: duration}
	}
	return Outcome{Output: output, Artifacts: artifactsOf(tc), Duration: duration}
}

func artifactsOf(tc *sdk.TaskContext) []ArtifactRecord {
	emitted := tc.Artifacts()
	if len(emitted) == 0 {
		return nil
	}
	out := make([]ArtifactRecord, 0, len(emitted))
	for _, a := range emitted {
		out = append(out, ArtifactRecord{
			Name: a.Name, SHA256: a.SHA256, Size: a.Size, ContentType: a.ContentType,
		})
	}
	return out
}

func marshalOutput(result any) (json.RawMessage, error) {
	if result == nil {
		return nil, nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("handler output is not JSON-serialisable: %w", err)
	}
	return raw, nil
}

// ledger adapts idem.Ledger to sdk.EffectLedger, adding the one thing the SDK
// interface cannot know about: emitting SIDE_EFFECT_SKIPPED when a repeat is
// suppressed. The ledger itself has no opinion about events.
type ledger struct {
	engine   *Engine
	inner    *idem.Ledger
	task     Task
	workerID uuid.UUID
}

func newLedger(e *Engine, task Task, workerID uuid.UUID) *ledger {
	return &ledger{engine: e, inner: idem.New(e.db), task: task, workerID: workerID}
}

// Do performs fn at most once per key across every attempt of a task, and
// records the suppression of a repeat in the run's journal.
func (l *ledger) Do(ctx context.Context, key string,
	fn func(context.Context) (any, error)) (json.RawMessage, bool, error) {
	result, skipped, err := l.inner.Do(ctx, idem.Key(key), l.task.RunID, l.task.Name, fn)
	if err != nil {
		return nil, false, err
	}
	if skipped {
		if err := l.engine.appendSideEffectSkipped(ctx, l.task, key); err != nil {
			return nil, false, err
		}
	}
	return result, skipped, nil
}
