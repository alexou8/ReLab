package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/sdk"
)

func TestCancelRunEndsEveryUnstartedTask(t *testing.T) {
	f := newFixture(t, diamondYAML, map[string]sdk.Handler{"ok": okHandler})
	run := f.start(engine.CreateRunOptions{})

	if err := f.eng.CancelRun(f.ctx, run.ID, "operator changed their mind"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	final, err := f.eng.RunByID(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if final.Status != engine.RunCancelled {
		t.Fatalf("run is %s, want CANCELLED", final.Status)
	}
	if final.CompletedAt == nil {
		t.Fatal("a cancelled run has no completion time")
	}
	if final.FailureReason != "operator changed their mind" {
		t.Fatalf("reason is %q, want the one given", final.FailureReason)
	}

	tasks, err := f.eng.Tasks(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("read tasks: %v", err)
	}
	for _, task := range tasks {
		if task.Status != engine.TaskDead {
			t.Errorf("task %s is %s after cancellation, want DEAD", task.Name, task.Status)
		}
	}
	assertContains(t, f.types(run.ID), event.RunCancelled)
}

func TestCancelledRunIsNotResumed(t *testing.T) {
	f := newFixture(t, diamondYAML, map[string]sdk.Handler{"ok": okHandler})
	run := f.start(engine.CreateRunOptions{})
	if err := f.eng.CancelRun(f.ctx, run.ID, "stop"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// Nothing may be claimable afterwards; a cancelled run that a worker picks
	// up again is a cancellation that did not happen.
	workerID := mustRegisterWorker(t, f.eng, "eager")
	claimed, err := f.eng.ClaimTasksForRun(f.ctx, workerID, run.ID, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %d tasks from a cancelled run, want 0", len(claimed))
	}
}

func TestCancelIsRefusedOnAFinishedRun(t *testing.T) {
	f := newFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler})
	run := f.drive(f.start(engine.CreateRunOptions{}))
	if run.Status != engine.RunSucceeded {
		t.Fatalf("setup: run is %s", run.Status)
	}

	err := f.eng.CancelRun(f.ctx, run.ID, "too late")
	if !errors.Is(err, engine.ErrRunFinished) {
		t.Fatalf("cancelling a finished run returned %v, want engine.ErrRunFinished", err)
	}

	// The recorded outcome must be untouched: rewriting a completed run's
	// history is exactly what the event log exists to make impossible.
	after, err := f.eng.RunByID(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if after.Status != engine.RunSucceeded {
		t.Fatalf("a refused cancellation changed the run to %s", after.Status)
	}
}

// TestCancelledRunsInFlightTaskIsNotRequeued proves the reaper does not restart
// work the operator asked to stop when its lease expires.
func TestCancelledRunsInFlightTaskIsNotRequeued(t *testing.T) {
	f := newTimedFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler}, fastTiming())
	run := f.start(engine.CreateRunOptions{})

	workerID := mustRegisterWorker(t, f.eng, "busy")
	claimed, err := f.eng.ClaimTasksForRun(f.ctx, workerID, run.ID, 1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.eng.StartTask(f.ctx, workerID, claimed[0].Task, "ok"); err != nil {
		t.Fatalf("start: %v", err)
	}

	if err := f.eng.CancelRun(f.ctx, run.ID, "stop"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// The lease was expired by the cancellation, so the next sweep sees it.
	if _, err := f.eng.Reap(f.ctx); err != nil {
		t.Fatalf("reap: %v", err)
	}

	tasks, err := f.eng.Tasks(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("read tasks: %v", err)
	}
	for _, task := range tasks {
		if task.Status == engine.TaskReady || task.Status == engine.TaskRetrying {
			t.Fatalf("task %s is %s after its run was cancelled; the reaper restarted work "+
				"the operator stopped", task.Name, task.Status)
		}
	}

	// The worker that was executing discovers the loss the same way it would
	// discover any other: its outcome is refused.
	err = f.eng.CompleteTask(f.ctx, workerID, claimed[0].Task, engine.Outcome{Output: []byte(`{}`)})
	if !errors.Is(err, engine.ErrLeaseLost) {
		t.Fatalf("the worker recorded its outcome with %v, want engine.ErrLeaseLost", err)
	}
}

// TestCoordinatorRestartResumesFromTheDatabase is the M3 restart-recovery
// criterion. There is nothing to resume in memory — that is the design — so the
// test proves it by throwing the engine away mid-run and building a new one.
func TestCoordinatorRestartResumesFromTheDatabase(t *testing.T) {
	f := newTimedFixture(t, diamondYAML, map[string]sdk.Handler{"ok": okHandler}, fastTiming())
	run := f.start(engine.CreateRunOptions{})

	// A worker claims and starts a task, then disappears.
	ghost := mustRegisterWorker(t, f.eng, "ghost")
	claimed, err := f.eng.ClaimTasksForRun(f.ctx, ghost, run.ID, 1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.eng.StartTask(f.ctx, ghost, claimed[0].Task, "ok"); err != nil {
		t.Fatalf("start: %v", err)
	}

	// The coordinator that saw all of this is discarded, and a completely new
	// one is built against the same database — as a redeploy would do.
	restarted, err := engine.New(f.db, engine.Options{Timing: fastTiming(), Logger: discardLogger()})
	if err != nil {
		t.Fatalf("restart engine: %v", err)
	}
	runner, err := engine.NewLocalRunner(f.ctx, restarted, f.reg, discardLogger())
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}

	// Sweeping recovers the abandoned task without any knowledge of what
	// happened before.
	waitFor(t, 10*time.Second, func() bool {
		result, err := restarted.Reap(f.ctx)
		if err != nil {
			t.Fatalf("reap: %v", err)
		}
		return result.TasksRequeued > 0
	})

	final, err := runner.Run(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("drive after restart: %v", err)
	}
	if final.Status != engine.RunSucceeded {
		t.Fatalf("the run ended %s after a coordinator restart, want SUCCEEDED", final.Status)
	}
}

// TestTaskTimeoutFailsTheAttempt proves a handler that overruns its step timeout
// is failed rather than left to hold its lease indefinitely.
func TestTaskTimeoutFailsTheAttempt(t *testing.T) {
	const yaml = `
name: slow
version: 1
steps:
  - name: dawdle
    handler: block
    timeout: "150ms"
    retry: {max_attempts: 1, initial_delay: "1ms", multiplier: 1, max_delay: "1ms", jitter: 0}
`
	f := newFixture(t, yaml, map[string]sdk.Handler{
		"block": func(ctx context.Context, _ *sdk.TaskContext) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	run := f.drive(f.start(engine.CreateRunOptions{}))

	if run.Status != engine.RunFailed {
		t.Fatalf("run ended %s, want FAILED", run.Status)
	}
	tasks, err := f.eng.Tasks(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("read tasks: %v", err)
	}
	if tasks[0].Status != engine.TaskDead {
		t.Fatalf("the timed-out task is %s, want DEAD", tasks[0].Status)
	}
	if tasks[0].Error == "" {
		t.Fatal("the timed-out task recorded no error")
	}
	t.Logf("recorded error: %s", tasks[0].Error)
}
