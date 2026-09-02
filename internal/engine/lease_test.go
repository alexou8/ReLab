package engine_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/config"
	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/testsupport"
	"github.com/alexou8/relab/internal/workflow"
	"github.com/alexou8/relab/sdk"
)

// fanOutYAML is a wide workflow: one root and twenty independent tasks, so a
// pool of workers genuinely contends for them.
func fanOutYAML(width int) string {
	var b strings.Builder
	b.WriteString("name: wide\nversion: 1\nsteps:\n  - {name: root, handler: ok}\n")
	for i := 0; i < width; i++ {
		fmt.Fprintf(&b, "  - {name: leaf_%02d, handler: ok, depends_on: [root]}\n", i)
	}
	return b.String()
}

// TestThreeWorkersProcessAFanOutWithoutDoubleExecution is the M2 acceptance
// test. Three workers race for twenty tasks; no task may be executed twice
// concurrently, which task_attempts enforces with its primary key.
func TestThreeWorkersProcessAFanOutWithoutDoubleExecution(t *testing.T) {
	const width = 20
	f := newFixture(t, fanOutYAML(width), map[string]sdk.Handler{"ok": okHandler})
	run := f.start(engine.CreateRunOptions{})

	// Each worker is a separate identity claiming from the same queue, which is
	// what the deployed pool does; running them in goroutines rather than
	// processes keeps the assertion on the scheduler rather than on the OS.
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runner, err := engine.NewLocalRunner(f.ctx, f.eng, f.reg, discardLogger())
			if err != nil {
				errs <- err
				return
			}
			if _, err := runner.Run(f.ctx, run.ID); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("worker: %v", err)
	}

	final, err := f.eng.RunByID(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if final.Status != engine.RunSucceeded {
		t.Fatalf("run ended %s, want SUCCEEDED", final.Status)
	}

	// Every task must have exactly one recorded attempt: no double execution
	// and no unnecessary retry.
	rows, err := f.db.Conn().Query(f.ctx, `
		SELECT t.task_name, count(a.attempt)
		FROM tasks t LEFT JOIN task_attempts a ON a.task_id = t.id
		WHERE t.run_id = $1
		GROUP BY t.task_name`, run.ID)
	if err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name string
		var attempts int
		if err := rows.Scan(&name, &attempts); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if attempts != 1 {
			t.Errorf("task %s recorded %d attempts, want exactly 1", name, attempts)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if seen != width+1 {
		t.Fatalf("counted %d tasks, want %d", seen, width+1)
	}

	// Each TASK_STARTED must name a distinct (task, attempt) pair.
	events, err := f.eng.Events(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	started := map[string]bool{}
	for _, e := range events {
		if e.Type != event.TaskStarted {
			continue
		}
		var p event.TaskStartedPayload
		if err := event.Decode(e.Payload, &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		key := fmt.Sprintf("%s#%d", e.TaskName, p.Attempt)
		if started[key] {
			t.Fatalf("%s was started twice", key)
		}
		started[key] = true
	}
}

// TestConcurrentAttemptIsRefused proves the primary key on task_attempts, not
// timing, is what prevents two workers running one attempt. It forces the race
// the claim query normally makes impossible.
func TestConcurrentAttemptIsRefused(t *testing.T) {
	f := newFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler})
	run := f.start(engine.CreateRunOptions{})

	workerA := mustRegisterWorker(t, f.eng, "a")
	claimed, err := f.eng.ClaimTasksForRun(f.ctx, workerA, run.ID, 1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d tasks, want 1", len(claimed))
	}
	task := claimed[0].Task

	if err := f.eng.StartTask(f.ctx, workerA, task, "ok"); err != nil {
		t.Fatalf("first start: %v", err)
	}
	// A second start of the same attempt, as a resurrected worker would attempt.
	err = f.eng.StartTask(f.ctx, workerA, task, "ok")
	if err == nil {
		t.Fatal("the same attempt was started twice with no complaint")
	}
	if !errors.Is(err, engine.ErrConcurrentAttempt) && !errors.Is(err, engine.ErrLeaseLost) {
		t.Fatalf("second start returned %v, want ErrConcurrentAttempt or ErrLeaseLost", err)
	}
}

// TestLeaseExpiryRequeuesTheTask is the recovery path a killed worker takes:
// nothing is released on the way out, and the reaper hands the task back.
func TestLeaseExpiryRequeuesTheTask(t *testing.T) {
	f := newTimedFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler}, fastTiming())
	run := f.start(engine.CreateRunOptions{})

	ghost := mustRegisterWorker(t, f.eng, "ghost")
	claimed, err := f.eng.ClaimTasksForRun(f.ctx, ghost, run.ID, 1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d tasks, want 1", len(claimed))
	}
	if err := f.eng.StartTask(f.ctx, ghost, claimed[0].Task, "ok"); err != nil {
		t.Fatalf("start: %v", err)
	}
	// The worker now vanishes. It renews nothing and completes nothing.

	waitFor(t, 10*time.Second, func() bool {
		result, err := f.eng.Reap(f.ctx)
		if err != nil {
			t.Fatalf("reap: %v", err)
		}
		return result.TasksRequeued > 0
	})

	tasks, err := f.eng.Tasks(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("read tasks: %v", err)
	}
	var first engine.Task
	for _, task := range tasks {
		if task.Name == "first" {
			first = task
		}
	}
	if first.Status != engine.TaskReady {
		t.Fatalf("the abandoned task is %s, want READY", first.Status)
	}
	if first.WorkerID != nil {
		t.Fatalf("the abandoned task still names worker %s", first.WorkerID)
	}
	if first.LeaseExpiresAt != nil {
		t.Fatal("the abandoned task still holds a lease")
	}

	types := f.types(run.ID)
	assertContains(t, types, event.TaskLeaseExpired, event.TaskRequeued)

	// A live worker now finishes the run, which is the whole point of the
	// requeue: recovery has to actually recover.
	final := f.drive(run)
	if final.Status != engine.RunSucceeded {
		t.Fatalf("after recovery the run ended %s, want SUCCEEDED", final.Status)
	}
}

// TestRevokedWorkerCannotOverwriteTheNewerAttempt is the correctness property
// behind lease expiry: a worker that comes back must not clobber the attempt
// that was handed to someone else.
func TestRevokedWorkerCannotOverwriteTheNewerAttempt(t *testing.T) {
	f := newTimedFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler}, fastTiming())
	run := f.start(engine.CreateRunOptions{})

	stale := mustRegisterWorker(t, f.eng, "stale")
	claimed, err := f.eng.ClaimTasksForRun(f.ctx, stale, run.ID, 1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	task := claimed[0].Task
	if err := f.eng.StartTask(f.ctx, stale, task, "ok"); err != nil {
		t.Fatalf("start: %v", err)
	}

	waitFor(t, 10*time.Second, func() bool {
		result, err := f.eng.Reap(f.ctx)
		if err != nil {
			t.Fatalf("reap: %v", err)
		}
		return result.TasksRequeued > 0
	})

	// The stale worker wakes up and tries to record success for its attempt.
	err = f.eng.CompleteTask(f.ctx, stale, task, engine.Outcome{Output: []byte(`{"stale":true}`)})
	if !errors.Is(err, engine.ErrLeaseLost) {
		t.Fatalf("a revoked worker recorded its outcome with %v, want engine.ErrLeaseLost", err)
	}

	tasks, err := f.eng.Tasks(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("read tasks: %v", err)
	}
	for _, tk := range tasks {
		if tk.Name == "first" && string(tk.Output) == `{"stale":true}` {
			t.Fatal("the revoked worker's output was written over the newer attempt")
		}
	}
}

// TestWorkerBecomesSuspectBeforeLost proves one missed heartbeat never costs a
// worker its tasks.
func TestWorkerBecomesSuspectBeforeLost(t *testing.T) {
	timing := fastTiming()
	f := newTimedFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler}, timing)

	id := mustRegisterWorker(t, f.eng, "quiet")

	// After enough silence for one missed beat but not three, the worker is
	// still healthy.
	time.Sleep(timing.HeartbeatInterval + timing.HeartbeatInterval/2)
	if _, err := f.eng.Reap(f.ctx); err != nil {
		t.Fatalf("reap: %v", err)
	}
	if got := workerStatus(t, f, id); got != engine.WorkerHealthy {
		t.Fatalf("after one missed heartbeat the worker is %s, want HEALTHY: one missed "+
			"heartbeat is a GC pause, not a failure", got)
	}

	waitFor(t, 10*time.Second, func() bool {
		if _, err := f.eng.Reap(f.ctx); err != nil {
			t.Fatalf("reap: %v", err)
		}
		return workerStatus(t, f, id) == engine.WorkerSuspect
	})

	waitFor(t, 10*time.Second, func() bool {
		if _, err := f.eng.Reap(f.ctx); err != nil {
			t.Fatalf("reap: %v", err)
		}
		return workerStatus(t, f, id) == engine.WorkerLost
	})

	// A lost worker must not be able to heartbeat its way back: its leases have
	// been handed on, and two owners of one task is the failure mode the whole
	// design exists to prevent.
	err := f.eng.Heartbeat(f.ctx, id, 0)
	if !errors.Is(err, engine.ErrWorkerLost) {
		t.Fatalf("a lost worker's heartbeat returned %v, want engine.ErrWorkerLost", err)
	}
}

// TestLostWorkerReleasesItsLeasesImmediately proves a declared-dead worker's
// tasks do not have to serve out the remainder of their leases.
func TestLostWorkerReleasesItsLeasesImmediately(t *testing.T) {
	timing := fastTiming()
	timing.LeaseDuration = 30 * time.Second // far longer than the liveness window
	timing.LeaseRenewInterval = 5 * time.Second
	f := newTimedFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler}, timing)
	run := f.start(engine.CreateRunOptions{})

	doomed := mustRegisterWorker(t, f.eng, "doomed")
	claimed, err := f.eng.ClaimTasksForRun(f.ctx, doomed, run.ID, 1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.eng.StartTask(f.ctx, doomed, claimed[0].Task, "ok"); err != nil {
		t.Fatalf("start: %v", err)
	}

	// The worker goes silent. Its lease has 30 seconds left, but it is declared
	// lost within the liveness window and its task must come back at once.
	waitFor(t, 15*time.Second, func() bool {
		if _, err := f.eng.Reap(f.ctx); err != nil {
			t.Fatalf("reap: %v", err)
		}
		tasks, err := f.eng.Tasks(f.ctx, run.ID)
		if err != nil {
			t.Fatalf("read tasks: %v", err)
		}
		for _, tk := range tasks {
			if tk.Name == "first" && tk.Status == engine.TaskReady {
				return true
			}
		}
		return false
	})

	assertContains(t, f.types(run.ID), event.WorkerLost, event.TaskLeaseExpired, event.TaskRequeued)
}

func TestRenewLeaseOnlyRenewsWhatTheWorkerStillHolds(t *testing.T) {
	f := newTimedFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler}, fastTiming())
	run := f.start(engine.CreateRunOptions{})

	owner := mustRegisterWorker(t, f.eng, "owner")
	other := mustRegisterWorker(t, f.eng, "other")
	claimed, err := f.eng.ClaimTasksForRun(f.ctx, owner, run.ID, 1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	id := claimed[0].Task.ID

	renewed, err := f.eng.RenewLease(f.ctx, owner, []uuid.UUID{id})
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if len(renewed) != 1 || renewed[0] != id {
		t.Fatalf("the owner renewed %v, want just %s", renewed, id)
	}

	renewed, err = f.eng.RenewLease(f.ctx, other, []uuid.UUID{id})
	if err != nil {
		t.Fatalf("renew as another worker: %v", err)
	}
	if len(renewed) != 0 {
		t.Fatalf("a worker renewed %v, but it holds no leases", renewed)
	}
}

// --- helpers -----------------------------------------------------------------

// fastTiming compresses the recovery windows so that a test observes real lease
// expiry and real heartbeat thresholds in under a second, rather than mocking
// the clock and asserting on arithmetic.
func fastTiming() config.Timing {
	return config.Timing{
		LeaseDuration:      300 * time.Millisecond,
		LeaseRenewInterval: 100 * time.Millisecond,
		HeartbeatInterval:  100 * time.Millisecond,
		SuspectAfterBeats:  3,
		LostAfterBeats:     5,
		ReaperInterval:     50 * time.Millisecond,
		TaskTimeout:        10 * time.Second,
	}
}

func newTimedFixture(t *testing.T, yaml string, handlers map[string]sdk.Handler, timing config.Timing) *fixture {
	t.Helper()
	db := testsupport.DB(t)
	ctx := context.Background()

	reg := sdk.NewRegistry()
	for name, h := range handlers {
		reg.MustHandle(name, h)
	}
	def, err := workflow.Parse([]byte(yaml), reg.Set())
	if err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	eng, err := engine.New(db, engine.Options{Timing: timing, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	wf, err := eng.RegisterWorkflow(ctx, def)
	if err != nil {
		t.Fatalf("register workflow: %v", err)
	}
	runner, err := engine.NewLocalRunner(ctx, eng, reg, discardLogger())
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	return &fixture{t: t, db: db, eng: eng, reg: reg, def: def, wf: wf, ctx: ctx, runs: runner}
}

func mustRegisterWorker(t *testing.T, eng *engine.Engine, hostname string) uuid.UUID {
	t.Helper()
	id, err := eng.RegisterWorker(context.Background(), engine.WorkerRegistration{
		Hostname: hostname, Version: "test", Capacity: 4,
	})
	if err != nil {
		t.Fatalf("register worker %s: %v", hostname, err)
	}
	return id
}

func workerStatus(t *testing.T, f *fixture, id uuid.UUID) engine.WorkerStatus {
	t.Helper()
	workers, err := f.eng.ListWorkers(f.ctx)
	if err != nil {
		t.Fatalf("list workers: %v", err)
	}
	for _, w := range workers {
		if w.ID == id {
			return w.Status
		}
	}
	t.Fatalf("worker %s is not registered", id)
	return ""
}

// waitFor polls until cond holds or the deadline passes. Recovery is driven by
// wall-clock lease expiry, so the tests genuinely have to wait; the deadline is
// generous enough not to be flaky on a loaded CI runner and short enough to
// fail rather than hang.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition was not met within %s", timeout)
}
