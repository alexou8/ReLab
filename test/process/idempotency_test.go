package process

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/idem"

	"github.com/google/uuid"
)

const effectWorkflow = `
name: idempotent-effect
version: 1
steps:
  - name: charge
    handler: effect_then_die
    retry: {max_attempts: 3, initial_delay: 100ms, multiplier: 2, max_delay: 1s, jitter: 0}
  - name: confirm
    handler: summarize
    depends_on: [charge]
`

// TestEffectSurvivesACrashBeforeAcknowledgement is the M3 acceptance test.
//
// A handler performs a recorded side effect and then SIGKILLs its own process
// before the task can be marked done — the one window that no amount of care
// closes, because the effect is external and the acknowledgement is in
// PostgreSQL. The task is recovered by lease expiry and retried. The ledger
// must show exactly one effect, and the journal must carry the
// SIDE_EFFECT_SKIPPED that proves the retry did not repeat it.
func TestEffectSurvivesACrashBeforeAcknowledgement(t *testing.T) {
	env := newEnv(t)
	run := env.createRun(t, effectWorkflow)

	// The armed worker runs alone until it has actually performed the effect.
	// Starting a second worker alongside it would let the survivor win the
	// claim, run the task normally, and pass this test without ever exercising
	// the crash — a test that reports success for the wrong reason.
	env.startWorkerWith(t, "suicidal", map[string]string{"RELAB_EFFECT_THEN_DIE": "1"})
	env.waitForEffect(t, run, 45*time.Second)

	// The effect is now recorded and the worker that performed it is dead. Only
	// lease expiry can bring the task back.
	env.startWorker(t, "survivor")

	final := env.waitForTerminalRun(t, run, 90*time.Second)
	if final.Status != engine.RunSucceeded {
		env.dumpTimeline(t, run)
		t.Fatalf("run ended %s, want SUCCEEDED", final.Status)
	}

	ledger := idem.New(env.db)
	count, err := ledger.CountForRun(env.ctx, run)
	if err != nil {
		t.Fatalf("count effects: %v", err)
	}
	if count != 1 {
		env.dumpTimeline(t, run)
		t.Fatalf("the ledger holds %d effects, want exactly 1: a crash between performing an "+
			"effect and acknowledging it must not produce a duplicate", count)
	}

	events := env.events(t, run)
	assertHasType(t, events, event.SideEffectSkipped,
		"the retry must record that it found the effect already performed; that event is the "+
			"observable evidence that at-least-once delivery did not become a duplicate effect")
	assertHasType(t, events, event.TaskLeaseExpired,
		"the killed worker's task can only come back through lease expiry")

	// The effect was performed by the attempt that died, and the ledger's
	// record must still say so — the retry returns the first attempt's result,
	// it does not overwrite it with its own.
	record, err := ledger.Lookup(env.ctx, idem.NewKey(run, "charge", "external-charge"))
	if err != nil {
		t.Fatalf("look up the recorded effect: %v", err)
	}
	// Compared as decoded JSON: the column is jsonb, which normalises key order
	// and spacing, so a byte comparison would assert on PostgreSQL's formatting
	// rather than on the value.
	var recorded struct {
		Charged   bool `json:"charged"`
		ByAttempt int  `json:"by_attempt"`
	}
	if err := json.Unmarshal(record.Result, &recorded); err != nil {
		t.Fatalf("decode the recorded effect %s: %v", record.Result, err)
	}
	if !recorded.Charged || recorded.ByAttempt != 1 {
		t.Fatalf("the ledger recorded %+v, want the first attempt's result: the retry returns "+
			"what the first attempt recorded, it does not overwrite it with its own", recorded)
	}

	assertNoDuplicateAttempt(t, events)
}

// waitForEffect blocks until the run has recorded a side effect, which is the
// point after which the armed worker has certainly died.
func (e *env) waitForEffect(t *testing.T, runID uuid.UUID, timeout time.Duration) {
	t.Helper()
	ledger := idem.New(e.db)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		count, err := ledger.CountForRun(e.ctx, runID)
		if err != nil {
			t.Fatalf("count effects: %v", err)
		}
		if count > 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	e.dumpTimeline(t, runID)
	t.Fatalf("no side effect was recorded within %s; the armed worker never ran the task", timeout)
}
