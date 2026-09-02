package engine_test

import (
	"errors"
	"testing"

	"github.com/alexou8/relab/internal/engine"
)

func TestRunTransitions(t *testing.T) {
	legal := []struct{ from, to engine.RunStatus }{
		{engine.RunCreated, engine.RunQueued},
		{engine.RunCreated, engine.RunCancelled},
		{engine.RunQueued, engine.RunRunning},
		{engine.RunQueued, engine.RunFailed},
		{engine.RunQueued, engine.RunCancelled},
		{engine.RunRunning, engine.RunSucceeded},
		{engine.RunRunning, engine.RunFailed},
		{engine.RunRunning, engine.RunCancelled},
	}
	for _, tc := range legal {
		if err := tc.from.Transition(tc.to); err != nil {
			t.Errorf("%s -> %s should be legal: %v", tc.from, tc.to, err)
		}
	}

	illegal := []struct{ from, to engine.RunStatus }{
		{engine.RunCreated, engine.RunRunning},   // must be queued first
		{engine.RunCreated, engine.RunSucceeded}, // cannot skip execution
		{engine.RunRunning, engine.RunQueued},    // no going back
		{engine.RunSucceeded, engine.RunRunning}, // terminal
		{engine.RunFailed, engine.RunSucceeded},  // terminal
		{engine.RunCancelled, engine.RunRunning}, // terminal
		{engine.RunSucceeded, engine.RunFailed},  // terminal
	}
	for _, tc := range illegal {
		err := tc.from.Transition(tc.to)
		if err == nil {
			t.Errorf("%s -> %s should be illegal", tc.from, tc.to)
			continue
		}
		var ill *engine.IllegalTransitionError
		if !errors.As(err, &ill) {
			t.Errorf("%s -> %s returned %T, want *engine.IllegalTransitionError", tc.from, tc.to, err)
		}
	}
}

func TestTaskTransitions(t *testing.T) {
	legal := []struct {
		from, to engine.TaskStatus
		why      string
	}{
		{engine.TaskPending, engine.TaskReady, "a dependency succeeded"},
		{engine.TaskReady, engine.TaskLeased, "a worker won the claim"},
		{engine.TaskLeased, engine.TaskRunning, "the handler was entered"},
		{engine.TaskRunning, engine.TaskSucceeded, "the handler returned nil"},
		{engine.TaskRunning, engine.TaskFailed, "the handler returned an error"},
		{engine.TaskFailed, engine.TaskRetrying, "attempts remain"},
		{engine.TaskRetrying, engine.TaskReady, "the backoff elapsed"},
		{engine.TaskFailed, engine.TaskDead, "attempts are exhausted"},
		{engine.TaskLeased, engine.TaskReady, "the lease expired before the handler started"},
		{engine.TaskRunning, engine.TaskReady, "the lease expired under a live worker"},
		{engine.TaskPending, engine.TaskDead, "the run was cancelled"},
		{engine.TaskReady, engine.TaskDead, "the run was cancelled"},
	}
	for _, tc := range legal {
		if err := tc.from.Transition(tc.to); err != nil {
			t.Errorf("%s -> %s should be legal (%s): %v", tc.from, tc.to, tc.why, err)
		}
	}

	illegal := []struct {
		from, to engine.TaskStatus
		why      string
	}{
		{engine.TaskPending, engine.TaskLeased, "a pending task is not in the claim index"},
		{engine.TaskPending, engine.TaskRunning, "cannot start without being claimed"},
		{engine.TaskReady, engine.TaskRunning, "cannot start without a lease"},
		{engine.TaskReady, engine.TaskSucceeded, "cannot succeed without running"},
		{engine.TaskLeased, engine.TaskSucceeded, "cannot succeed without entering the handler"},
		{engine.TaskSucceeded, engine.TaskReady, "terminal"},
		{engine.TaskSucceeded, engine.TaskRunning, "terminal"},
		{engine.TaskDead, engine.TaskReady, "terminal"},
		{engine.TaskDead, engine.TaskRetrying, "terminal"},
		{engine.TaskRetrying, engine.TaskRunning, "must return to READY and be claimed again"},
		{engine.TaskFailed, engine.TaskReady, "a retry passes through RETRYING so the backoff is recorded"},
	}
	for _, tc := range illegal {
		if err := tc.from.Transition(tc.to); err == nil {
			t.Errorf("%s -> %s should be illegal (%s)", tc.from, tc.to, tc.why)
		}
	}
}

func TestTransitionsReturnErrorsRatherThanPanic(t *testing.T) {
	// An illegal transition means a bug in the caller. A process recovering
	// other runs must survive it.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Transition panicked: %v", r)
		}
	}()
	if err := engine.TaskStatus("NOT_A_STATE").Transition(engine.TaskReady); err == nil {
		t.Fatal("an unknown state produced no error")
	}
	if err := engine.RunStatus("").Transition(engine.RunQueued); err == nil {
		t.Fatal("an empty state produced no error")
	}
}

func TestTerminalAndHolds(t *testing.T) {
	terminalTasks := []engine.TaskStatus{engine.TaskSucceeded, engine.TaskDead}
	for _, s := range terminalTasks {
		if !s.Terminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
	// FAILED is deliberately not terminal: it is always resolved into RETRYING
	// or DEAD in the transaction that records it.
	for _, s := range []engine.TaskStatus{engine.TaskPending, engine.TaskReady, engine.TaskLeased,
		engine.TaskRunning, engine.TaskRetrying, engine.TaskFailed} {
		if s.Terminal() {
			t.Errorf("%s should not be terminal", s)
		}
	}
	for _, s := range []engine.TaskStatus{engine.TaskLeased, engine.TaskRunning} {
		if !s.Holds() {
			t.Errorf("%s should hold a lease", s)
		}
	}
	for _, s := range []engine.TaskStatus{engine.TaskPending, engine.TaskReady,
		engine.TaskRetrying, engine.TaskSucceeded, engine.TaskDead} {
		if s.Holds() {
			t.Errorf("%s should not hold a lease", s)
		}
	}
	for _, s := range []engine.RunStatus{engine.RunSucceeded, engine.RunFailed, engine.RunCancelled} {
		if !s.Terminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
}

func TestValidRejectsUnknownStates(t *testing.T) {
	if engine.TaskStatus("BANANA").Valid() {
		t.Error("BANANA is not a task status")
	}
	if engine.RunStatus("BANANA").Valid() {
		t.Error("BANANA is not a run status")
	}
	if !engine.TaskRunning.Valid() || !engine.RunRunning.Valid() {
		t.Error("a declared state reported itself invalid")
	}
}

func TestTerminalStatesAllowNoTransition(t *testing.T) {
	// Every terminal state must be a dead end. A terminal state that quietly
	// allows a transition is how a completed run gets rewritten.
	for _, s := range []engine.TaskStatus{engine.TaskSucceeded, engine.TaskDead} {
		for _, to := range []engine.TaskStatus{engine.TaskPending, engine.TaskReady,
			engine.TaskLeased, engine.TaskRunning, engine.TaskSucceeded,
			engine.TaskFailed, engine.TaskRetrying, engine.TaskDead} {
			if s.CanTransition(to) {
				t.Errorf("terminal task state %s allows a transition to %s", s, to)
			}
		}
	}
	for _, s := range []engine.RunStatus{engine.RunSucceeded, engine.RunFailed, engine.RunCancelled} {
		for _, to := range []engine.RunStatus{engine.RunCreated, engine.RunQueued,
			engine.RunRunning, engine.RunSucceeded, engine.RunFailed, engine.RunCancelled} {
			if s.CanTransition(to) {
				t.Errorf("terminal run state %s allows a transition to %s", s, to)
			}
		}
	}
}
