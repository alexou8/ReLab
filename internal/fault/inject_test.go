package fault_test

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/fault"
)

// recorder collects the firings an injector reports, so a test can assert on
// what was recorded as well as on what happened.
type recorder struct {
	firings []fault.Firing
}

func (r *recorder) record(_ context.Context, f fault.Firing) error {
	r.firings = append(r.firings, f)
	return nil
}

func derive(seed int64, parts ...string) *rand.Rand {
	return engine.DerivedRand(seed, parts...)
}

func TestExplicitTriggerPointFiresExactlyOnce(t *testing.T) {
	scenario, err := fault.ParseScenario([]byte(`
name: http
seed: 5
faults:
  - {type: http-error, target: {task: validate, attempt: 1}, at: after-task-start, params: {status: 503}}
assert: {}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rec := &recorder{}
	inj := fault.NewInjector(scenario, uuid.New(), 0, derive, rec.record)
	ctx := context.Background()

	// A different task at the same point: nothing fires.
	if err := inj.Point(ctx, fault.AfterTaskStart, "import", 1); err != nil {
		t.Fatalf("the fault fired on a task it does not target: %v", err)
	}
	// The right task at a different point: nothing fires.
	if err := inj.Point(ctx, fault.AfterTaskLease, "validate", 1); err != nil {
		t.Fatalf("the fault fired at a point it does not target: %v", err)
	}
	// The right task at the right point on a later attempt: nothing fires.
	if err := inj.Point(ctx, fault.AfterTaskStart, "validate", 2); err != nil {
		t.Fatalf("the fault fired on an attempt it does not target: %v", err)
	}

	err = inj.Point(ctx, fault.AfterTaskStart, "validate", 1)
	if err == nil {
		t.Fatal("the targeted point did not fire")
	}
	if !fault.IsInjected(err) {
		t.Fatalf("the error %v is not marked as injected; assertions cannot tell a fault from "+
			"a genuine failure", err)
	}
	if inj.Fired() != 1 {
		t.Fatalf("the injector fired %d times, want 1", inj.Fired())
	}
	if len(rec.firings) != 1 {
		t.Fatalf("recorded %d firings, want 1", len(rec.firings))
	}
	if rec.firings[0].Type != fault.HTTPError || rec.firings[0].TaskName != "validate" {
		t.Fatalf("recorded %+v, want an http-error on validate", rec.firings[0])
	}
}

func TestFaultIsRecordedBeforeItTakesEffect(t *testing.T) {
	// The ordering matters most for worker-crash, where the process is about to
	// die and an event written afterwards would never exist. It is asserted on
	// http-error because that one can be observed from inside the test.
	scenario, err := fault.ParseScenario([]byte(`
name: order
seed: 1
faults:
  - {type: http-error, at: after-task-start}
assert: {}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	recordedBeforeEffect := false
	inj := fault.NewInjector(scenario, uuid.New(), 0, derive,
		func(_ context.Context, _ fault.Firing) error {
			// If the effect had already been applied, Point would have returned
			// before reaching this callback.
			recordedBeforeEffect = true
			return nil
		})
	if err := inj.Point(context.Background(), fault.AfterTaskStart, "t", 1); err == nil {
		t.Fatal("the fault did not fire")
	}
	if !recordedBeforeEffect {
		t.Fatal("the fault took effect before it was recorded")
	}
}

func TestProbabilisticFaultIsDeterministicForASeed(t *testing.T) {
	yaml := []byte(`
name: flaky
seed: 99
faults:
  - {type: http-error, probability: 0.5}
assert: {}
`)
	runID := uuid.New()

	decisions := func() []bool {
		scenario, err := fault.ParseScenario(yaml)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		inj := fault.NewInjector(scenario, runID, 0, derive, nil)
		var out []bool
		for attempt := 1; attempt <= 12; attempt++ {
			err := inj.Point(context.Background(), fault.AfterTaskStart, "t", attempt)
			out = append(out, err != nil)
		}
		return out
	}

	first := decisions()
	for i := 0; i < 5; i++ {
		again := decisions()
		for j := range first {
			if first[j] != again[j] {
				t.Fatalf("run %d decided differently at attempt %d: %v vs %v", i, j+1, first, again)
			}
		}
	}

	// The draws must actually vary, or "deterministic" is being demonstrated by
	// a fault that never fires.
	fired, skipped := 0, 0
	for _, f := range first {
		if f {
			fired++
		} else {
			skipped++
		}
	}
	if fired == 0 || skipped == 0 {
		t.Fatalf("across 12 attempts the fault fired %d times and skipped %d; a probability of "+
			"0.5 that always agrees with itself is not evidence of determinism", fired, skipped)
	}
}

func TestDecisionForOnePositionIsStable(t *testing.T) {
	// Re-evaluating the same point within one attempt must not flip the
	// decision: the executor may reach a point more than once.
	scenario, err := fault.ParseScenario([]byte(`
name: stable
seed: 3
faults:
  - {type: http-error, probability: 0.5}
assert: {}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	inj := fault.NewInjector(scenario, uuid.New(), 0, derive, nil)
	ctx := context.Background()

	first := inj.Point(ctx, fault.AfterTaskStart, "t", 1) != nil
	for i := 0; i < 8; i++ {
		if again := inj.Point(ctx, fault.AfterTaskStart, "t", 1) != nil; again != first {
			t.Fatalf("re-evaluating the same position flipped the decision from %v to %v", first, again)
		}
	}
}

func TestSeedOverrideReproducesAParticularRun(t *testing.T) {
	scenario, err := fault.ParseScenario([]byte(`
name: seeded
seed: 1
faults:
  - {type: http-error, probability: 0.5}
assert: {}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	runID := uuid.New()

	decisionsWith := func(seed int64) []bool {
		inj := fault.NewInjector(scenario, runID, seed, derive, nil)
		var out []bool
		for attempt := 1; attempt <= 10; attempt++ {
			out = append(out, inj.Point(context.Background(), fault.AfterTaskStart, "t", attempt) != nil)
		}
		return out
	}

	a := decisionsWith(4242)
	b := decisionsWith(4242)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("the same seed override decided differently at attempt %d", i+1)
		}
	}
	c := decisionsWith(9999)
	same := true
	for i := range a {
		if a[i] != c[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("a different seed produced identical decisions; the override is not reaching the RNG")
	}
}

func TestNilInjectorFiresNothing(t *testing.T) {
	var inj *fault.Injector
	if err := inj.Point(context.Background(), fault.AfterTaskStart, "t", 1); err != nil {
		t.Fatalf("a nil injector fired: %v", err)
	}
	if inj.Fired() != 0 || inj.ShouldDuplicate("t", 1) {
		t.Fatal("a nil injector reported activity")
	}
}

func TestNeedsSeparateWorkers(t *testing.T) {
	crash, err := fault.ParseScenario([]byte(`
name: crash
seed: 1
faults:
  - {type: worker-crash, at: after-task-start}
assert: {}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !crash.NeedsSeparateWorkers() {
		t.Fatal("a worker-crash scenario must not be run in the process that is asserting on it")
	}

	other, err := fault.ParseScenario([]byte(`
name: latency
seed: 1
faults:
  - {type: latency, at: after-task-start}
assert: {}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if other.NeedsSeparateWorkers() {
		t.Fatal("a latency scenario does not need separate workers")
	}
}
