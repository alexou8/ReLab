package engine_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/sdk"
)

// register puts a worker in the table and returns its id.
func (f *fixture) register(hostname string) uuid.UUID {
	f.t.Helper()
	id, err := f.eng.RegisterWorker(f.ctx, engine.WorkerRegistration{
		Hostname: hostname, Version: "test", Capacity: 1,
	})
	if err != nil {
		f.t.Fatalf("register worker: %v", err)
	}
	return id
}

// workerStatus reads one worker's status out of the table.
func (f *fixture) workerStatus(id uuid.UUID) engine.WorkerStatus {
	f.t.Helper()
	workers, err := f.eng.ListWorkers(f.ctx)
	if err != nil {
		f.t.Fatalf("list workers: %v", err)
	}
	for _, w := range workers {
		if w.ID == id {
			return w.Status
		}
	}
	f.t.Fatalf("worker %s is not in the table", id)
	return ""
}

// A worker that says it is leaving must be distinguishable from one that died,
// because an operator reading the table needs to know whether an absence was
// expected. Before RetireWorker existed, both looked like LOST.
func TestRetiringAWorkerDistinguishesItFromALostOne(t *testing.T) {
	f := newFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler})
	id := f.register("retiring")

	if got := f.workerStatus(id); got != engine.WorkerHealthy {
		t.Fatalf("a freshly registered worker is %s, want HEALTHY", got)
	}
	if err := f.eng.RetireWorker(f.ctx, id); err != nil {
		t.Fatalf("retire worker: %v", err)
	}
	if got := f.workerStatus(id); got != engine.WorkerStopped {
		t.Fatalf("a worker that announced its shutdown is %s, want STOPPED: a clean "+
			"stop that reads as a crash makes a real crash easy to miss", got)
	}
}

// Retiring twice is what a shutdown path racing the reaper looks like, and it
// must not be an error: a stopping process has nothing useful to do with one.
func TestRetiringAWorkerTwiceIsNotAnError(t *testing.T) {
	f := newFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler})
	id := f.register("retiring-twice")

	for i := 0; i < 2; i++ {
		if err := f.eng.RetireWorker(f.ctx, id); err != nil {
			t.Fatalf("retire worker (call %d): %v", i+1, err)
		}
	}
	if got := f.workerStatus(id); got != engine.WorkerStopped {
		t.Fatalf("worker is %s after two retirements, want STOPPED", got)
	}
}

// A retired worker must not be able to heartbeat itself back to life. Its
// leases have been released; a second owner believing it still holds them is
// exactly the duplicate execution the lease exists to prevent.
func TestARetiredWorkerCannotHeartbeatItselfBack(t *testing.T) {
	f := newFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler})
	id := f.register("zombie")

	if err := f.eng.RetireWorker(f.ctx, id); err != nil {
		t.Fatalf("retire worker: %v", err)
	}
	err := f.eng.Heartbeat(f.ctx, id, 0)
	if !errors.Is(err, engine.ErrWorkerLost) {
		t.Fatalf("heartbeat from a retired worker returned %v, want ErrWorkerLost: a "+
			"retired worker that returns to HEALTHY would own tasks someone else has", err)
	}
	if got := f.workerStatus(id); got != engine.WorkerStopped {
		t.Fatalf("worker is %s after heartbeating post-retirement, want STOPPED", got)
	}
}

// Retiring a worker the reaper has already declared LOST must leave it LOST.
// The reaper released its leases and announced the loss to the affected runs;
// rewriting the status afterwards would contradict events already in the
// journal, and a terminal event is the last word on what happened.
func TestRetiringALostWorkerLeavesItLost(t *testing.T) {
	f := newFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler})
	id := f.register("already-gone")

	// Declared lost the way the reaper declares it, by writing the row rather
	// than by waiting out five heartbeat intervals in a unit test.
	if _, err := f.db.Conn().Exec(f.ctx,
		`UPDATE workers SET status = 'LOST' WHERE id = $1`, id); err != nil {
		t.Fatalf("mark worker lost: %v", err)
	}
	if err := f.eng.RetireWorker(f.ctx, id); err != nil {
		t.Fatalf("retire worker: %v", err)
	}
	if got := f.workerStatus(id); got != engine.WorkerLost {
		t.Fatalf("a worker already declared LOST is %s after retiring, want LOST", got)
	}
}
