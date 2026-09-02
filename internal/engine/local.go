package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/sdk"
)

// LocalRunner drives a run to completion inside one process.
//
// It exists so that `relab run` works without a worker pool — a reviewer's
// first command should not require three terminals — and so that the engine's
// operations have a driver that is easy to reason about in tests. It uses the
// same claim, start and complete operations a worker uses, so a run driven here
// and a run driven by the pool differ in timing, not in what they record.
type LocalRunner struct {
	engine   *Engine
	executor *Executor
	workerID uuid.UUID
	log      *slog.Logger

	// pollInterval is how long to wait when there is nothing runnable but the
	// run is not finished — which happens while a retry's backoff elapses.
	pollInterval time.Duration
}

// DriveWithFaults creates an in-process runner with a fault source attached and
// drives a run to completion.
//
// It is what `relab test` uses: one process, one run, faults injected at the
// executor's trigger points. A scenario whose faults kill the worker process
// cannot be run this way — the process being killed is this one — and those
// scenarios are run against a worker pool instead.
func (e *Engine) DriveWithFaults(ctx context.Context, runID uuid.UUID, reg *sdk.Registry,
	faults FaultSource) error {
	runner, err := NewLocalRunner(ctx, e, reg, nil)
	if err != nil {
		return err
	}
	defer runner.Close(ctx)
	runner.executor = runner.executor.WithFaults(faults)
	if _, err := runner.Run(ctx, runID); err != nil {
		return err
	}
	return nil
}

// NewLocalRunner registers an in-process worker and returns a runner.
func NewLocalRunner(ctx context.Context, e *Engine, reg *sdk.Registry, log *slog.Logger) (*LocalRunner, error) {
	if log == nil {
		log = slog.Default()
	}
	workerID, err := e.RegisterWorker(ctx, WorkerRegistration{
		Hostname: "in-process",
		Version:  "local",
		Capacity: 1,
	})
	if err != nil {
		return nil, err
	}
	return &LocalRunner{
		engine:       e,
		executor:     NewExecutor(e, reg, workerID, log),
		workerID:     workerID,
		log:          log,
		pollInterval: 50 * time.Millisecond,
	}, nil
}

// WorkerID returns the identity this runner claims tasks as.
func (r *LocalRunner) WorkerID() uuid.UUID { return r.workerID }

// Close retires the runner's worker.
//
// Without it every `relab run` left behind a worker row that the reaper would
// declare LOST some seconds later, so a table meant to show which workers had
// died filled up with processes that had simply finished. It is best-effort:
// the caller has its answer already, and a runner whose process is killed
// leaves the same row the reaper has always handled.
func (r *LocalRunner) Close(ctx context.Context) {
	stopping, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.engine.RetireWorker(stopping, r.workerID); err != nil {
		r.log.WarnContext(stopping, "could not retire the in-process worker; "+
			"the reaper will declare it lost instead", "worker_id", r.workerID, "error", err)
	}
}

// Run drives a run until it reaches a terminal state, and returns it.
//
// It claims only tasks belonging to the given run, so running one workflow
// locally does not consume work that a worker pool is meant to handle.
func (r *LocalRunner) Run(ctx context.Context, runID uuid.UUID) (Run, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Run{}, fmt.Errorf("engine: run %s interrupted: %w", runID, err)
		}

		run, err := r.engine.RunByID(ctx, runID)
		if err != nil {
			return Run{}, err
		}
		if run.Status.Terminal() {
			return run, nil
		}

		// A due retry has to be promoted before it can be claimed. In a
		// deployed system the reaper does this; here the runner does it itself
		// so that `relab run` needs nothing else running.
		if _, err := r.engine.PromoteDueRetries(ctx, 100); err != nil {
			return Run{}, err
		}

		claimed, err := r.engine.ClaimTasksForRun(ctx, r.workerID, runID, 1)
		if err != nil {
			return Run{}, err
		}
		if len(claimed) == 0 {
			// Nothing runnable. Usually a backoff is elapsing, but it can also
			// mean the run cannot progress at all — a task stranded in a state
			// nothing will move it out of. Polling such a run forever makes a
			// bug indistinguishable from slowness, so it is reported.
			progress, err := r.engine.RunProgress(ctx, runID)
			if err != nil {
				return Run{}, err
			}
			if progress.Stalled() {
				return Run{}, fmt.Errorf(
					"engine: run %s is stalled: %d of %d tasks are unfinished but none is "+
						"runnable, in flight, or waiting on a retry",
					runID, progress.Total-progress.Terminal, progress.Total)
			}
			if err := sleep(ctx, r.pollInterval); err != nil {
				return Run{}, fmt.Errorf("engine: run %s interrupted: %w", runID, err)
			}
			continue
		}

		for _, task := range claimed {
			if err := r.executor.Execute(ctx, task); err != nil {
				// Losing a lease in a single-process run means the reaper
				// intervened, which is legitimate. Anything else is fatal to
				// this call.
				if errors.Is(err, ErrLeaseLost) {
					r.log.WarnContext(ctx, "lease lost during local execution",
						"run_id", runID, "task", task.Task.Name)
					continue
				}
				return Run{}, err
			}
		}
	}
}

// sleep waits for d, or returns early when ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
