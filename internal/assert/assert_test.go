package assert_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/assert"
	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/fault"
	"github.com/alexou8/relab/internal/replay"
)

func ptr[T any](v T) *T { return &v }

func recoveredState() *replay.RunState {
	return &replay.RunState{
		RunID:  uuid.New(),
		Status: replay.StatusSucceeded,
		Tasks: map[string]*replay.TaskState{
			"import":  {Name: "import", Status: replay.TaskSucceeded, Attempts: 1},
			"analyze": {Name: "analyze", Status: replay.TaskSucceeded, Attempts: 2, Requeues: 1},
		},
		Faults:         []replay.FaultState{{Type: "worker-crash", Point: "after-task-start"}},
		SkippedEffects: []replay.SkippedEffect{{Key: "k", Task: "analyze"}},
	}
}

func scenario(a fault.Assertions) *fault.Scenario {
	return &fault.Scenario{Name: "crash", Seed: 42, Assert: a}
}

func TestEvaluatePasses(t *testing.T) {
	report := assert.Evaluate(scenario(fault.Assertions{
		RunStatus:         replay.StatusSucceeded,
		LostTasks:         ptr(0),
		DuplicateEffects:  ptr(0),
		MaxRetriesPerTask: ptr(2),
		FaultsInjected:    ptr(1),
	}), recoveredState(), nil, 0)

	if !report.Passed {
		t.Fatalf("assertions failed: %v", report.Failures())
	}
	if report.FinalState != replay.StatusSucceeded {
		t.Fatalf("final state is %q", report.FinalState)
	}
	if report.SkippedEffects != 1 {
		t.Fatalf("skipped effects is %d, want 1", report.SkippedEffects)
	}
}

func TestEvaluateFailsOnTerminalState(t *testing.T) {
	state := recoveredState()
	state.Status = replay.StatusFailed

	report := assert.Evaluate(scenario(fault.Assertions{RunStatus: replay.StatusSucceeded}),
		state, nil, 0)
	if report.Passed {
		t.Fatal("a failed run passed a run_status: SUCCEEDED assertion")
	}
	failures := report.Failures()
	if len(failures) != 1 || failures[0].Name != "run_status" {
		t.Fatalf("failures are %+v, want just run_status", failures)
	}
}

func TestEvaluateFailsOnLostTasks(t *testing.T) {
	state := recoveredState()
	state.Tasks["analyze"].Status = replay.TaskDead

	report := assert.Evaluate(scenario(fault.Assertions{LostTasks: ptr(0)}), state, nil, 0)
	if report.Passed {
		t.Fatal("a run with a dead task passed lost_tasks: 0")
	}
	if report.LostTasks != 1 {
		t.Fatalf("LostTasks is %d, want 1", report.LostTasks)
	}
}

func TestEvaluateFailsOnDuplicateEffects(t *testing.T) {
	report := assert.Evaluate(scenario(fault.Assertions{DuplicateEffects: ptr(0)}),
		recoveredState(), nil, 2)
	if report.Passed {
		t.Fatal("two duplicate effects passed duplicate_effects: 0")
	}
	if report.DuplicateEffects != 2 {
		t.Fatalf("DuplicateEffects is %d, want 2", report.DuplicateEffects)
	}
}

// TestFaultsInjectedGuardsAgainstAVacuousPass is the assertion that stops a
// scenario passing because nothing happened.
func TestEvaluateFaultsInjectedGuardsAgainstAVacuousPass(t *testing.T) {
	state := recoveredState()
	state.Faults = nil // the fault never fired

	report := assert.Evaluate(scenario(fault.Assertions{
		RunStatus:      replay.StatusSucceeded,
		LostTasks:      ptr(0),
		FaultsInjected: ptr(1),
	}), state, nil, 0)

	if report.Passed {
		t.Fatal("a run in which no fault fired passed a scenario asserting one did; a green " +
			"test that never ran is worse than a red one")
	}
	failures := report.Failures()
	if len(failures) != 1 || failures[0].Name != "faults_injected" {
		t.Fatalf("failures are %+v, want just faults_injected", failures)
	}
}

func TestEvaluateMinRetriesCatchesAFaultThatDidNothing(t *testing.T) {
	state := recoveredState()
	state.Tasks["analyze"].Retries = 0

	report := assert.Evaluate(scenario(fault.Assertions{MinRetriesPerTask: ptr(1)}), state, nil, 0)
	if report.Passed {
		t.Fatal("a run with no retries passed min_retries_per_task: 1")
	}
}

func TestRecoveryTimeMeasuresFromTheFirstTrouble(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	at := func(offset time.Duration, typ event.Type) event.Event {
		return event.Event{Type: typ, OccurredAt: base.Add(offset)}
	}
	events := []event.Event{
		at(0, event.RunCreated),
		at(1*time.Second, event.TaskStarted),
		at(2*time.Second, event.TaskLeaseExpired), // trouble starts here
		at(3*time.Second, event.TaskRequeued),
		at(5*time.Second, event.RunSucceeded),
	}
	if got, want := assert.RecoveryTime(events), 3*time.Second; got != want {
		t.Fatalf("RecoveryTime is %s, want %s (first lease expiry to completion)", got, want)
	}
}

func TestRecoveryTimeIsZeroWhenNothingWentWrong(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	events := []event.Event{
		{Type: event.RunCreated, OccurredAt: base},
		{Type: event.TaskSucceeded, OccurredAt: base.Add(time.Second)},
		{Type: event.RunSucceeded, OccurredAt: base.Add(2 * time.Second)},
	}
	if got := assert.RecoveryTime(events); got != 0 {
		t.Fatalf("RecoveryTime is %s for a run that never went wrong, want 0", got)
	}
}

func TestHumanOutputIsTheDocumentedFormat(t *testing.T) {
	report := assert.Evaluate(scenario(fault.Assertions{RunStatus: replay.StatusSucceeded}),
		recoveredState(), nil, 0)
	out := report.Human()

	if !strings.HasPrefix(out, "PASS crash\n") {
		t.Fatalf("output starts %q, want a PASS line naming the scenario", strings.SplitN(out, "\n", 2)[0])
	}
	for _, field := range []string{
		"recovery time", "retries", "lost tasks", "duplicate effects", "final state",
	} {
		if !strings.Contains(out, field) {
			t.Errorf("output is missing %q:\n%s", field, out)
		}
	}
}

func TestHumanOutputExplainsFailures(t *testing.T) {
	state := recoveredState()
	state.Status = replay.StatusFailed
	report := assert.Evaluate(scenario(fault.Assertions{RunStatus: replay.StatusSucceeded}),
		state, nil, 0)

	out := report.Human()
	if !strings.HasPrefix(out, "FAIL crash\n") {
		t.Fatalf("output does not start with FAIL:\n%s", out)
	}
	if !strings.Contains(out, "run_status: expected SUCCEEDED, got FAILED") {
		t.Fatalf("output does not explain the failure:\n%s", out)
	}
}

func TestNoAssertionsMeansNothingToFail(t *testing.T) {
	// A scenario with an empty assert block passes. That is correct — it
	// asserts nothing — and is why the corpus's baseline scenario asserts
	// explicitly rather than relying on defaults.
	report := assert.Evaluate(scenario(fault.Assertions{}), recoveredState(), nil, 0)
	if !report.Passed || len(report.Results) != 0 {
		t.Fatalf("an empty assert block produced %+v", report.Results)
	}
}
