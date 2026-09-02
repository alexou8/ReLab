package replay_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/replay"
)

// journal builds an event stream without a database, so the reducer is tested
// as the pure function it is.
type journal struct {
	t      *testing.T
	runID  uuid.UUID
	events []event.Event
	seq    int64
}

func newJournal(t *testing.T) *journal {
	return &journal{t: t, runID: uuid.New()}
}

func (j *journal) add(p event.Payload, taskName string) *journal {
	j.t.Helper()
	raw, err := event.Encode(p)
	if err != nil {
		j.t.Fatalf("encode %s: %v", p.Type(), err)
	}
	j.seq++
	j.events = append(j.events, event.Event{
		RunID: j.runID, Seq: j.seq, Type: p.Type(), TaskName: taskName,
		Payload: raw, OccurredAt: time.Unix(1700000000+j.seq, 0).UTC(),
	})
	return j
}

func (j *journal) reduce() (*replay.RunState, error) {
	return replay.Reduce(j.runID, j.events)
}

func (j *journal) mustReduce() *replay.RunState {
	j.t.Helper()
	state, err := j.reduce()
	if err != nil {
		j.t.Fatalf("reduce: %v", err)
	}
	return state
}

// successfulRun is a two-task run that succeeded first time.
func successfulRun(t *testing.T) *journal {
	return newJournal(t).
		add(event.RunCreatedPayload{WorkflowName: "wf", WorkflowVersion: 1,
			DefinitionHash: "abc123", Seed: 42}, "").
		add(event.TaskScheduledPayload{}, "first").
		add(event.RunQueuedPayload{TaskCount: 2}, "").
		add(event.TaskLeasedPayload{Attempt: 1}, "first").
		add(event.TaskStartedPayload{Attempt: 1, Handler: "h"}, "first").
		add(event.RunStartedPayload{}, "").
		add(event.TaskSucceededPayload{Attempt: 1, Artifacts: []event.ArtifactRef{
			{Name: "out.txt", SHA256: strings.Repeat("a", 64), Size: 10, ContentType: "text/plain"},
		}}, "first").
		add(event.TaskScheduledPayload{}, "second").
		add(event.TaskLeasedPayload{Attempt: 1}, "second").
		add(event.TaskStartedPayload{Attempt: 1, Handler: "h"}, "second").
		add(event.TaskSucceededPayload{Attempt: 1}, "second").
		add(event.RunSucceededPayload{TasksSucceeded: 2}, "")
}

func TestReduceASuccessfulRun(t *testing.T) {
	state := successfulRun(t).mustReduce()

	if state.Status != replay.StatusSucceeded {
		t.Fatalf("status is %s, want SUCCEEDED", state.Status)
	}
	if state.Workflow != "wf" || state.Version != 1 || state.Seed != 42 {
		t.Fatalf("reconstructed %+v, want wf v1 seed 42", state)
	}
	if len(state.Tasks) != 2 {
		t.Fatalf("reconstructed %d tasks, want 2", len(state.Tasks))
	}
	first := state.Tasks["first"]
	if first.Status != replay.TaskSucceeded || first.Attempts != 1 {
		t.Fatalf("first is %+v, want SUCCEEDED after 1 attempt", first)
	}
	if len(first.Artifacts) != 1 || first.Artifacts[0].Name != "out.txt" {
		t.Fatalf("first's artifacts are %+v, want one out.txt", first.Artifacts)
	}
	if state.LostTasks() != 0 {
		t.Fatalf("LostTasks is %d, want 0", state.LostTasks())
	}
	if !state.Terminal() {
		t.Fatal("a run ending in RUN_SUCCEEDED is not terminal")
	}
}

func TestReduceIsDeterministic(t *testing.T) {
	// The property everything else rests on: the same events must reduce to the
	// same state, every time.
	j := successfulRun(t)
	first, err := json.Marshal(j.mustReduce())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := json.Marshal(j.mustReduce())
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(first) != string(again) {
			t.Fatalf("reduction %d differs from the first:\n%s\n%s", i, first, again)
		}
	}
}

func TestReduceARunThatRecovered(t *testing.T) {
	state := newJournal(t).
		add(event.RunCreatedPayload{WorkflowName: "wf", WorkflowVersion: 1, DefinitionHash: "h", Seed: 7}, "").
		add(event.TaskScheduledPayload{}, "task").
		add(event.RunQueuedPayload{TaskCount: 1}, "").
		add(event.TaskLeasedPayload{Attempt: 1}, "task").
		add(event.TaskStartedPayload{Attempt: 1, Handler: "h"}, "task").
		add(event.RunStartedPayload{}, "").
		add(event.TaskLeaseExpiredPayload{Attempt: 1}, "task").
		add(event.TaskRequeuedPayload{Attempt: 1, NextAttempt: 2, Reason: "lease expired"}, "task").
		add(event.WorkerLostPayload{MissedBeats: 5, LeasesReleased: 1}, "").
		add(event.TaskLeasedPayload{Attempt: 2}, "task").
		add(event.TaskStartedPayload{Attempt: 2, Handler: "h"}, "task").
		add(event.SideEffectSkippedPayload{IdempotencyKey: "k", Attempt: 2, FirstAttempt: 1}, "task").
		add(event.TaskSucceededPayload{Attempt: 2}, "task").
		add(event.RunSucceededPayload{TasksSucceeded: 1}, "").
		mustReduce()

	task := state.Tasks["task"]
	if task.Status != replay.TaskSucceeded {
		t.Fatalf("task is %s, want SUCCEEDED", task.Status)
	}
	if task.Attempts != 2 {
		t.Fatalf("task took %d attempts, want 2", task.Attempts)
	}
	if task.Requeues != 1 || task.LeaseExpiries != 1 {
		t.Fatalf("task has %d requeues and %d lease expiries, want 1 of each",
			task.Requeues, task.LeaseExpiries)
	}
	// Requeues and Retries are separate counts on purpose: "retried because the
	// worker died" and "retried because the handler failed" are different
	// reliability stories.
	if task.Retries != 0 {
		t.Fatalf("task recorded %d handler retries; the failure was a lost worker, not a "+
			"handler error", task.Retries)
	}
	if state.WorkersLost != 1 {
		t.Fatalf("WorkersLost is %d, want 1", state.WorkersLost)
	}
	if len(state.SkippedEffects) != 1 {
		t.Fatalf("recorded %d skipped effects, want 1", len(state.SkippedEffects))
	}
}

func TestReduceRejectsAnUnknownEventType(t *testing.T) {
	j := successfulRun(t)
	j.events[3].Type = "TASK_TELEPORTED"

	_, err := j.reduce()
	var unknown *event.ErrUnknownType
	if !errors.As(err, &unknown) {
		t.Fatalf("reduce returned %v, want *event.ErrUnknownType: ignoring an unrecognised "+
			"event would reconstruct a state that omits whatever it meant", err)
	}
}

func TestReduceRejectsAnOutOfOrderJournal(t *testing.T) {
	j := successfulRun(t)
	j.events = append(j.events[:4], j.events[5:]...) // remove seq 5

	_, err := j.reduce()
	var order *replay.ErrOutOfOrder
	if !errors.As(err, &order) {
		t.Fatalf("reduce returned %v, want *replay.ErrOutOfOrder", err)
	}
	if order.Expected != 5 || order.Found != 6 {
		t.Fatalf("reported expected=%d found=%d, want 5 and 6", order.Expected, order.Found)
	}
}

func TestReduceRejectsAnEventAfterTheTerminalOne(t *testing.T) {
	j := successfulRun(t)
	j.add(event.TaskStartedPayload{Attempt: 9, Handler: "h"}, "first")

	_, err := j.reduce()
	var after *replay.ErrEventAfterTerminal
	if !errors.As(err, &after) {
		t.Fatalf("reduce returned %v, want *replay.ErrEventAfterTerminal: a run has exactly one "+
			"terminal event and it is the last", err)
	}
}

func TestReduceRejectsAnUnversionedPayload(t *testing.T) {
	j := successfulRun(t)
	j.events[0].Payload = json.RawMessage(`{"workflow_name":"wf"}`)

	_, err := j.reduce()
	if err == nil || !strings.Contains(err.Error(), "no version field") {
		t.Fatalf("reduce returned %v, want a missing-version error", err)
	}
}

func TestReduceRejectsAFuturePayloadVersion(t *testing.T) {
	j := successfulRun(t)
	j.events[0].Payload = json.RawMessage(`{"v":99,"workflow_name":"wf"}`)

	_, err := j.reduce()
	if err == nil || !strings.Contains(err.Error(), "not supported by this build") {
		t.Fatalf("reduce returned %v, want an unsupported-version error: a reducer that guesses "+
			"at a payload it cannot read reconstructs the wrong state", err)
	}
}

func TestReduceHandlesEveryDeclaredEventType(t *testing.T) {
	// The reducer's switch must be exhaustive. Any declared type it has no case
	// for reaches the default and errors, which this test surfaces as a list.
	payloads := map[event.Type]event.Payload{
		event.RunCreated:         event.RunCreatedPayload{},
		event.RunQueued:          event.RunQueuedPayload{},
		event.RunStarted:         event.RunStartedPayload{},
		event.TaskScheduled:      event.TaskScheduledPayload{},
		event.TaskLeased:         event.TaskLeasedPayload{},
		event.TaskStarted:        event.TaskStartedPayload{},
		event.TaskSucceeded:      event.TaskSucceededPayload{},
		event.TaskFailed:         event.TaskFailedPayload{},
		event.TaskRetryScheduled: event.TaskRetryScheduledPayload{},
		event.TaskLeaseExpired:   event.TaskLeaseExpiredPayload{},
		event.TaskRequeued:       event.TaskRequeuedPayload{},
		event.TaskDeadLettered:   event.TaskDeadLetteredPayload{},
		event.WorkerRegistered:   event.WorkerRegisteredPayload{},
		event.WorkerHeartbeat:    event.WorkerHeartbeatPayload{},
		event.WorkerSuspect:      event.WorkerSuspectPayload{},
		event.WorkerLost:         event.WorkerLostPayload{},
		event.FaultInjected:      event.FaultInjectedPayload{},
		event.SideEffectSkipped:  event.SideEffectSkippedPayload{},
	}
	for typ, p := range payloads {
		j := newJournal(t)
		j.add(p, "task")
		if _, err := j.reduce(); err != nil {
			t.Errorf("the reducer cannot handle %s: %v", typ, err)
		}
	}

	// The terminal types are checked separately, because they may only appear
	// last.
	for _, p := range []event.Payload{
		event.RunSucceededPayload{}, event.RunFailedPayload{}, event.RunCancelledPayload{},
	} {
		j := newJournal(t)
		j.add(p, "")
		if _, err := j.reduce(); err != nil {
			t.Errorf("the reducer cannot handle %s: %v", p.Type(), err)
		}
	}
}

func TestReduceRecordsFaults(t *testing.T) {
	state := newJournal(t).
		add(event.RunCreatedPayload{WorkflowName: "wf", WorkflowVersion: 1, DefinitionHash: "h",
			Seed: 42, ScenarioName: "crash"}, "").
		add(event.FaultInjectedPayload{FaultType: "worker-crash", FaultPoint: "after-task-start",
			Scenario: "crash", Seed: 42, Draw: 1}, "analyze").
		add(event.RunFailedPayload{Reason: "tasks dead-lettered", Detail: "1 of 1"}, "").
		mustReduce()

	if state.Scenario != "crash" {
		t.Fatalf("scenario is %q, want crash", state.Scenario)
	}
	if len(state.Faults) != 1 {
		t.Fatalf("recorded %d faults, want 1", len(state.Faults))
	}
	f := state.Faults[0]
	if f.Type != "worker-crash" || f.Point != "after-task-start" || f.Task != "analyze" {
		t.Fatalf("fault is %+v, want a worker-crash after-task-start on analyze", f)
	}
	if state.FailureReason != "tasks dead-lettered: 1 of 1" {
		t.Fatalf("failure reason is %q", state.FailureReason)
	}
}

func TestReduceOfAnEmptyJournal(t *testing.T) {
	state, err := replay.Reduce(uuid.New(), nil)
	if err != nil {
		t.Fatalf("reduce: %v", err)
	}
	if state.EventCount != 0 || state.Terminal() {
		t.Fatalf("an empty journal reduced to %+v", state)
	}
}

// Small constructors used by the comparison tests, so the journal builder can
// stay generic while the tests stay readable.
func (j *journal) addRunCreated(name string, version int, hash string, seed int64) *journal {
	return j.add(event.RunCreatedPayload{
		WorkflowName: name, WorkflowVersion: version, DefinitionHash: hash, Seed: seed,
	}, "")
}
