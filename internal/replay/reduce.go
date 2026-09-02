package replay

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/event"
)

// Reduce folds a run's events into its state.
//
// It is a pure function. Given the same events it returns the same state on any
// machine at any time, which is what makes a replay comparison meaningful.
//
// It refuses what it does not understand rather than skipping it:
//
//   - An event type this build does not know is an error. Ignoring it would
//     reconstruct a state that omits whatever the type meant, and report that
//     state as fact.
//   - A payload whose version this build does not know is an error, for the
//     same reason.
//   - Events out of sequence, or a second terminal event, are errors. Both mean
//     the journal is not what it claims to be.
//
// The alternative — a permissive reducer that does its best — produces a
// plausible-looking state for a corrupt journal, which is the worst possible
// outcome for a tool whose purpose is to tell you what really happened.
func Reduce(runID uuid.UUID, events []event.Event) (*RunState, error) {
	state := &RunState{RunID: runID, Status: StatusCreated, Tasks: map[string]*TaskState{}}

	var expectedSeq int64 = 1
	terminated := false

	for _, evt := range events {
		if evt.Seq != expectedSeq {
			return nil, &ErrOutOfOrder{RunID: runID, Expected: expectedSeq, Found: evt.Seq}
		}
		expectedSeq++

		if !evt.Type.Known() {
			return nil, &event.ErrUnknownType{Type: evt.Type, RunID: runID.String(), Seq: evt.Seq}
		}
		if terminated {
			return nil, &ErrEventAfterTerminal{RunID: runID, Seq: evt.Seq, Type: evt.Type}
		}
		if evt.Type.Terminal() {
			terminated = true
		}

		if err := apply(state, evt); err != nil {
			return nil, fmt.Errorf("replay: run %s seq %d (%s): %w", runID, evt.Seq, evt.Type, err)
		}
		state.EventCount++
		state.LastSeq = evt.Seq
	}
	return state, nil
}

// apply folds one event into the state.
//
// The switch is exhaustive over the declared event types, and its default case
// is an error rather than a no-op: adding an event type without teaching the
// reducer about it must fail loudly here, not silently produce a state that
// omits it.
//
// exhaustiveness that is the point.
//
//nolint:gocyclo // one case per event type; splitting it would hide the
func apply(s *RunState, evt event.Event) error {
	switch evt.Type {
	case event.RunCreated:
		var p event.RunCreatedPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		s.Workflow = p.WorkflowName
		s.Version = p.WorkflowVersion
		s.DefinitionHash = p.DefinitionHash
		s.Scenario = p.ScenarioName
		s.Seed = p.Seed
		s.Status = StatusCreated

	case event.RunQueued:
		var p event.RunQueuedPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		s.Status = StatusQueued

	case event.RunStarted:
		s.Status = StatusRunning

	case event.RunSucceeded:
		var p event.RunSucceededPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		s.Status = StatusSucceeded

	case event.RunFailed:
		var p event.RunFailedPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		s.Status = StatusFailed
		s.FailureReason = p.Reason
		if p.Detail != "" {
			s.FailureReason = p.Reason + ": " + p.Detail
		}

	case event.RunCancelled:
		var p event.RunCancelledPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		s.Status = StatusCancelled
		s.FailureReason = p.Reason

	case event.TaskScheduled:
		var p event.TaskScheduledPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		s.task(evt.TaskName).Status = TaskReady

	case event.TaskLeased:
		var p event.TaskLeasedPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		t := s.task(evt.TaskName)
		t.Status = TaskLeased
		// Attempts is taken from the payload rather than incremented, so a
		// journal with a missing TASK_LEASED shows a gap in the numbers instead
		// of silently renumbering the ones that remain.
		if p.Attempt > t.Attempts {
			t.Attempts = p.Attempt
		}

	case event.TaskStarted:
		var p event.TaskStartedPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		t := s.task(evt.TaskName)
		t.Status = TaskRunning
		t.Handler = p.Handler
		if p.Attempt > t.Attempts {
			t.Attempts = p.Attempt
		}
		if t.FirstStarted == nil {
			at := evt.OccurredAt
			t.FirstStarted = &at
		}

	case event.TaskSucceeded:
		var p event.TaskSucceededPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		t := s.task(evt.TaskName)
		t.Status = TaskSucceeded
		t.Output = p.Output
		t.Error = ""
		t.Artifacts = artifactsOf(p.Artifacts)
		at := evt.OccurredAt
		t.Completed = &at

	case event.TaskFailed:
		var p event.TaskFailedPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		t := s.task(evt.TaskName)
		t.Status = TaskFailed
		t.Error = p.Error
		at := evt.OccurredAt
		t.Completed = &at

	case event.TaskRetryScheduled:
		var p event.TaskRetryScheduledPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		t := s.task(evt.TaskName)
		t.Status = TaskRetrying
		t.Retries++

	case event.TaskLeaseExpired:
		var p event.TaskLeaseExpiredPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		s.task(evt.TaskName).LeaseExpiries++

	case event.TaskRequeued:
		var p event.TaskRequeuedPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		t := s.task(evt.TaskName)
		t.Status = TaskReady
		t.Requeues++

	case event.TaskDeadLettered:
		var p event.TaskDeadLetteredPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		t := s.task(evt.TaskName)
		t.Status = TaskDead
		if t.Error == "" {
			t.Error = p.Reason
		}
		at := evt.OccurredAt
		t.Completed = &at

	case event.FaultInjected:
		var p event.FaultInjectedPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		s.Faults = append(s.Faults, FaultState{
			Type: p.FaultType, Point: p.FaultPoint, Task: evt.TaskName, Draw: p.Draw,
		})

	case event.SideEffectSkipped:
		var p event.SideEffectSkippedPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		s.SkippedEffects = append(s.SkippedEffects, SkippedEffect{
			Key: p.IdempotencyKey, Task: evt.TaskName,
			Attempt: p.Attempt, FirstAttempt: p.FirstAttempt,
		})

	case event.WorkerLost:
		var p event.WorkerLostPayload
		if err := event.Decode(evt.Payload, &p); err != nil {
			return err
		}
		s.WorkersLost++

	case event.WorkerRegistered, event.WorkerHeartbeat, event.WorkerSuspect:
		// Worker liveness that did not cost the run anything. Decoded so a
		// malformed payload is still caught, but it changes no run state.
		var p map[string]any
		if err := decodeAny(evt.Payload, &p); err != nil {
			return err
		}

	default:
		return fmt.Errorf("the reducer has no case for %s; every declared event type must be "+
			"handled, or replay silently omits whatever it meant", evt.Type)
	}
	return nil
}

func artifactsOf(refs []event.ArtifactRef) []Artifact {
	if len(refs) == 0 {
		return nil
	}
	out := make([]Artifact, 0, len(refs))
	for _, r := range refs {
		out = append(out, Artifact{
			Name: r.Name, SHA256: r.SHA256, Size: r.Size, ContentType: r.ContentType,
		})
	}
	return out
}

// ErrOutOfOrder reports events that are not in contiguous sequence order.
type ErrOutOfOrder struct {
	RunID    uuid.UUID
	Expected int64
	Found    int64
}

func (e *ErrOutOfOrder) Error() string {
	return fmt.Sprintf("replay: run %s: expected event seq %d, found %d", e.RunID, e.Expected, e.Found)
}

// ErrEventAfterTerminal reports an event recorded after the run ended. A run
// has exactly one terminal event and it is the last one; anything after it
// means the journal was written to after the run was closed.
type ErrEventAfterTerminal struct {
	RunID uuid.UUID
	Seq   int64
	Type  event.Type
}

func (e *ErrEventAfterTerminal) Error() string {
	return fmt.Sprintf("replay: run %s: %s at seq %d comes after the run's terminal event",
		e.RunID, e.Type, e.Seq)
}
