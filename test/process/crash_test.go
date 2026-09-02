// Package process holds the tests that spawn real ReLab binaries and kill them.
//
// These are the tests that distinguish ReLab from a project that claims
// recovery. Everything else in the suite runs the scheduler in-process, where
// "the worker died" is a function that returns early; here the worker is a real
// operating system process that receives SIGKILL and gets no chance to release
// anything, clean up, or write a final log line. Recovery has to come from lease
// expiry observed by a separate process, which is the only mechanism that also
// works when a machine loses power.
package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/store"
	"github.com/alexou8/relab/internal/testsupport"
	"github.com/alexou8/relab/internal/workflow"
	"github.com/alexou8/relab/sdk"
)

// The recovery windows are compressed so that a test observes real lease expiry
// in about a second. The relationships between them are the production ones —
// renewal at a third of the lease, LOST at five missed beats — because it is
// those relationships, not the absolute values, that the behaviour depends on.
var fastTiming = map[string]string{
	"RELAB_LEASE_DURATION":       "1500ms",
	"RELAB_LEASE_RENEW_INTERVAL": "500ms",
	"RELAB_HEARTBEAT_INTERVAL":   "200ms",
	"RELAB_REAPER_INTERVAL":      "100ms",
	"RELAB_TASK_TIMEOUT":         "30s",
}

const slowPipeline = `
name: crash-pipeline
version: 1
steps:
  - name: crunch
    handler: slow_step
    retry: {max_attempts: 3, initial_delay: 100ms, multiplier: 2, max_delay: 1s, jitter: 0}
  - name: report
    handler: summarize
    depends_on: [crunch]
`

// TestSIGKILLedWorkerLosesItsTaskAndTheRunStillSucceeds is the M2 process-level
// acceptance test.
func TestSIGKILLedWorkerLosesItsTaskAndTheRunStillSucceeds(t *testing.T) {
	env := newEnv(t)
	run := env.createRun(t, slowPipeline)

	// One worker at a time, so the process that is killed is provably the one
	// holding the task. Starting two and killing one of them at random would
	// pass whenever the survivor happened to win the claim, which is a test
	// that reports success for the wrong reason.
	victim := env.startWorker(t, "victim")
	held := env.waitForRunningTask(t, run, "crunch")
	t.Logf("crunch is running as attempt %d on worker %s; killing that process",
		held.attempt, held.workerID)

	if err := victim.kill(); err != nil {
		t.Fatalf("SIGKILL the victim: %v", err)
	}
	killedAt := time.Now()

	// Nothing was released on the way out: no deferred cleanup ran, no lease
	// was handed back, no final log line was written. Recovery has to come from
	// another process observing the lease expire.
	env.startWorker(t, "survivor")

	final := env.waitForTerminalRun(t, run, 90*time.Second)
	recovery := time.Since(killedAt)

	if final.Status != engine.RunSucceeded {
		env.dumpTimeline(t, run)
		t.Fatalf("after the worker was killed the run ended %s, want SUCCEEDED", final.Status)
	}
	t.Logf("run recovered and completed %s after the kill", recovery.Round(time.Millisecond))

	events := env.events(t, run)
	assertHasType(t, events, event.TaskLeaseExpired,
		"the killed worker's lease must be observed to expire; that is the recovery trigger")
	assertHasType(t, events, event.TaskRequeued,
		"the abandoned task must be handed back to the queue")

	// The task ran more than once — that is at-least-once delivery working as
	// documented — but never twice under the same attempt number.
	assertNoDuplicateAttempt(t, events)

	if attempts := env.attemptCount(t, run, "crunch"); attempts < 2 {
		t.Fatalf("crunch recorded %d attempts; the killed attempt and its replacement should "+
			"both be recorded", attempts)
	}
}

// TestKilledWorkerIsDeclaredLost proves the worker's own row reaches LOST,
// which is what releases its leases without waiting for each to expire.
func TestKilledWorkerIsDeclaredLost(t *testing.T) {
	env := newEnv(t)
	run := env.createRun(t, slowPipeline)

	victim := env.startWorker(t, "victim")
	held := env.waitForRunningTask(t, run, "crunch")
	if err := victim.kill(); err != nil {
		t.Fatalf("SIGKILL: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := env.eng.Reap(env.ctx); err != nil {
			t.Fatalf("reap: %v", err)
		}
		workers, err := env.eng.ListWorkers(env.ctx)
		if err != nil {
			t.Fatalf("list workers: %v", err)
		}
		for _, w := range workers {
			if w.ID == held.workerID && w.Status == engine.WorkerLost {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("worker %s was never declared LOST within 30s of being killed", held.workerID)
}

// TestLongTaskSurvivesManyLeasePeriods is the regression test for a bug the
// crash test found: execution was bounded by the deadline the task was claimed
// under, which made lease renewal pointless and guaranteed failure for any task
// taking longer than one lease.
func TestLongTaskSurvivesManyLeasePeriods(t *testing.T) {
	env := newEnv(t)
	run := env.createRun(t, slowPipeline)
	env.startWorker(t, "patient")

	final := env.waitForTerminalRun(t, run, 90*time.Second)
	if final.Status != engine.RunSucceeded {
		env.dumpTimeline(t, run)
		t.Fatalf("run ended %s; an 8s task under a 1.5s lease must be kept alive by renewal",
			final.Status)
	}
	if attempts := env.attemptCount(t, run, "crunch"); attempts != 1 {
		env.dumpTimeline(t, run)
		t.Fatalf("crunch recorded %d attempts, want 1: nothing failed, so nothing should have "+
			"been retried", attempts)
	}
}

// TestGracefulShutdownDoesNotStrandWork checks the ordinary path: a worker that
// receives SIGTERM finishes what it is holding, and the run completes.
func TestGracefulShutdownDoesNotStrandWork(t *testing.T) {
	env := newEnv(t)
	run := env.createRun(t, slowPipeline)

	w := env.startWorker(t, "polite")
	env.waitForRunningTask(t, run, "crunch")
	if err := w.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	// A second worker completes whatever the first did not.
	env.startWorker(t, "successor")

	final := env.waitForTerminalRun(t, run, 90*time.Second)
	if final.Status != engine.RunSucceeded {
		env.dumpTimeline(t, run)
		t.Fatalf("run ended %s after a graceful shutdown, want SUCCEEDED", final.Status)
	}
}

// --- harness -----------------------------------------------------------------

type env struct {
	ctx    context.Context
	db     *store.DB
	eng    *engine.Engine
	dsn    string
	binary string
}

type workerProc struct {
	cmd      *exec.Cmd
	hostname string
}

// kill sends SIGKILL to the worker process itself, not its group: the group
// would take the test's other children with it.
func (w *workerProc) kill() error {
	if w.cmd.Process == nil {
		return fmt.Errorf("worker %s has no process", w.hostname)
	}
	return w.cmd.Process.Signal(syscall.SIGKILL)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if testing.Short() {
		t.Skip("process-level tests spawn binaries and wait on real lease expiry; skipped in -short")
	}
	db := testsupport.DB(t)
	dsn := testsupport.DatabaseDSN(t, db)
	ctx := context.Background()

	timing, err := timingFromMap(fastTiming)
	if err != nil {
		t.Fatalf("timing: %v", err)
	}
	eng, err := engine.New(db, engine.Options{Timing: timing})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	return &env{ctx: ctx, db: db, eng: eng, dsn: dsn, binary: testsupport.BuildRelab(t)}
}

// buildBinary compiles the relab binary once per package run. The tests spawn
// the real command, not a test harness pretending to be one, because the thing
// under test is what happens when that exact process is killed.
func (e *env) createRun(t *testing.T, yaml string) uuid.UUID {
	t.Helper()
	reg := sdk.NewRegistry()
	registerExamples(t, reg)

	def, err := workflow.Parse([]byte(yaml), reg.Set())
	if err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	wf, err := e.eng.RegisterWorkflow(e.ctx, def)
	if err != nil {
		t.Fatalf("register workflow: %v", err)
	}
	run, err := e.eng.CreateRun(e.ctx, wf, def, engine.CreateRunOptions{Seed: 1})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return run.ID
}

// startWorker spawns a real worker process and registers its cleanup.
func (e *env) startWorker(t *testing.T, name string) *workerProc {
	t.Helper()
	return e.startWorkerWith(t, name, nil)
}

// startWorkerWith spawns a worker with extra environment, for the tests that
// arm a handler to misbehave.
func (e *env) startWorkerWith(t *testing.T, name string, extra map[string]string) *workerProc {
	t.Helper()
	hostname := name + "-" + uuid.NewString()[:8]

	// The command is deliberately not bound to the test context: these tests
	// kill the process themselves, and a context-bound command would be sent
	// SIGKILL by the test framework on cleanup, racing the assertions.
	cmd := exec.Command(e.binary, "worker", "--concurrency", "1") //nolint:noctx // killed explicitly by the test
	cmd.Env = append(os.Environ(),
		"RELAB_DSN="+e.dsn,
		"HOSTNAME="+hostname,
		// Long enough that a kill lands mid-task, short enough that the test
		// does not spend its budget waiting for the handler to finish.
		"RELAB_SLOW_STEP_DURATION=8s",
		"RELAB_LOG_LEVEL=warn",
	)
	for k, v := range fastTiming {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	for k, v := range extra {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = &prefixWriter{t: t, prefix: name}
	cmd.Stderr = &prefixWriter{t: t, prefix: name}
	// A process group means a killed test cannot leave orphaned workers holding
	// leases against a database the next test is about to drop.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker %s: %v", name, err)
	}
	proc := &workerProc{cmd: cmd, hostname: hostname}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
	})
	return proc
}

// heldTask identifies a task that is currently executing, and where.
type heldTask struct {
	taskID   uuid.UUID
	workerID uuid.UUID
	attempt  int
}

// waitForRunningTask blocks until the named task is RUNNING, and reports which
// worker is executing it. The worker id is what makes the crash tests
// deterministic: the process that is killed is the one the database says holds
// the task, not whichever one was started first.
func (e *env) waitForRunningTask(t *testing.T, runID uuid.UUID, taskName string) heldTask {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		tasks, err := e.eng.Tasks(e.ctx, runID)
		if err != nil {
			t.Fatalf("read tasks: %v", err)
		}
		for _, task := range tasks {
			if task.Name == taskName && task.Status == engine.TaskRunning && task.WorkerID != nil {
				return heldTask{taskID: task.ID, workerID: *task.WorkerID, attempt: task.Attempt}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.dumpTimeline(t, runID)
	t.Fatalf("task %q never reached RUNNING within 45s", taskName)
	return heldTask{}
}

func (e *env) waitForTerminalRun(t *testing.T, runID uuid.UUID, timeout time.Duration) engine.Run {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		run, err := e.eng.RunByID(e.ctx, runID)
		if err != nil {
			t.Fatalf("read run: %v", err)
		}
		if run.Status.Terminal() {
			return run
		}
		// The workers do not run the reaper; the control plane does. Driving it
		// from the test keeps the spawned processes to workers only, which is
		// what the acceptance criterion is about.
		if _, err := e.eng.Reap(e.ctx); err != nil {
			t.Fatalf("reap: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.dumpTimeline(t, runID)
	t.Fatalf("run %s did not reach a terminal state within %s", runID, timeout)
	return engine.Run{}
}

func (e *env) events(t *testing.T, runID uuid.UUID) []event.Event {
	t.Helper()
	events, err := e.eng.Events(e.ctx, runID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	return events
}

func (e *env) attemptCount(t *testing.T, runID uuid.UUID, taskName string) int {
	t.Helper()
	var count int
	err := e.db.Conn().QueryRow(e.ctx, `
		SELECT count(*) FROM task_attempts a
		JOIN tasks t ON t.id = a.task_id
		WHERE t.run_id = $1 AND t.task_name = $2`, runID, taskName).Scan(&count)
	if err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	return count
}

func (e *env) dumpTimeline(t *testing.T, runID uuid.UUID) {
	t.Helper()
	events, err := e.eng.Events(e.ctx, runID)
	if err != nil {
		t.Logf("could not read the timeline: %v", err)
		return
	}
	var b strings.Builder
	for _, evt := range events {
		fmt.Fprintf(&b, "\n  %3d %-22s %-10s %s", evt.Seq, evt.Type, evt.TaskName, evt.Payload)
	}
	t.Logf("timeline of run %s:%s", runID, b.String())
}

func assertHasType(t *testing.T, events []event.Event, want event.Type, why string) {
	t.Helper()
	for _, evt := range events {
		if evt.Type == want {
			return
		}
	}
	t.Errorf("the timeline has no %s: %s", want, why)
}

// assertNoDuplicateAttempt is the invariant that separates at-least-once
// delivery from a broken scheduler: a task may run more than once, but never
// twice under the same attempt number.
func assertNoDuplicateAttempt(t *testing.T, events []event.Event) {
	t.Helper()
	seen := map[string]bool{}
	for _, evt := range events {
		if evt.Type != event.TaskStarted {
			continue
		}
		var p event.TaskStartedPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			t.Fatalf("decode TASK_STARTED: %v", err)
		}
		key := fmt.Sprintf("%s#%d", evt.TaskName, p.Attempt)
		if seen[key] {
			t.Errorf("%s was started twice: two workers executed one attempt", key)
		}
		seen[key] = true
	}
}

// prefixWriter forwards a child process's output into the test log, so a
// failure shows what the worker was doing.
type prefixWriter struct {
	t      *testing.T
	prefix string
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			w.t.Logf("[%s] %s", w.prefix, line)
		}
	}
	return len(p), nil
}
