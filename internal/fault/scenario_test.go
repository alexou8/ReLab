package fault_test

import (
	"strings"
	"testing"

	"github.com/alexou8/relab/internal/fault"
)

const crashScenario = `
name: worker-crash-during-analyze
seed: 42
faults:
  - type: worker-crash
    target: {task: analyze}
    at: after-task-start
assert:
  run_status: SUCCEEDED
  lost_tasks: 0
  duplicate_effects: 0
  max_recovery_time_ms: 5000
  max_retries_per_task: 2
`

func TestParseScenario(t *testing.T) {
	sc, err := fault.ParseScenario([]byte(crashScenario))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sc.Name != "worker-crash-during-analyze" || sc.Seed != 42 {
		t.Fatalf("parsed %+v, want the crash scenario with seed 42", sc)
	}
	if len(sc.Faults) != 1 || sc.Faults[0].Type != fault.WorkerCrash {
		t.Fatalf("faults are %+v, want one worker-crash", sc.Faults)
	}
	if sc.Faults[0].At != fault.AfterTaskStart {
		t.Fatalf("trigger point is %q, want after-task-start", sc.Faults[0].At)
	}
	if sc.Assert.RunStatus != "SUCCEEDED" {
		t.Fatalf("assertion run_status is %q, want SUCCEEDED", sc.Assert.RunStatus)
	}
	if sc.Assert.LostTasks == nil || *sc.Assert.LostTasks != 0 {
		t.Fatal("lost_tasks: 0 was not distinguished from an absent assertion")
	}
	if !sc.Deterministic() {
		t.Fatal("a scenario with an explicit trigger point reported itself non-deterministic")
	}
	if len(sc.Hash) != 64 {
		t.Fatalf("hash is %q, want 64 hex characters", sc.Hash)
	}
}

func TestScenarioRejects(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"no seed": {`
name: x
faults: []
assert: {}
`, "seed is required"},
		"unknown fault type": {`
name: x
seed: 1
faults:
  - {type: solar-flare, at: after-task-start}
assert: {}
`, "unknown type"},
		"unknown trigger point": {`
name: x
seed: 1
faults:
  - {type: latency, at: whenever}
assert: {}
`, "unknown trigger point"},
		"neither at nor probability": {`
name: x
seed: 1
faults:
  - {type: latency}
assert: {}
`, "set either at:"},
		"both at and probability": {`
name: x
seed: 1
faults:
  - {type: latency, at: after-task-start, probability: 0.5}
assert: {}
`, "not both"},
		"probability out of range": {`
name: x
seed: 1
faults:
  - {type: latency, probability: 2}
assert: {}
`, "between 0 and 1"},
		"unknown field": {`
name: x
seed: 1
fauls: []
assert: {}
`, "field fauls not found"},
		"no name": {`
seed: 1
assert: {}
`, "name is required"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := fault.ParseScenario([]byte(tc.yaml))
			if err == nil {
				t.Fatal("an invalid scenario was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error is %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestProbabilisticScenarioIsNotDeterministic(t *testing.T) {
	sc, err := fault.ParseScenario([]byte(`
name: flaky
seed: 7
faults:
  - {type: latency, probability: 0.5}
assert: {}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sc.Deterministic() {
		t.Fatal("a probability-driven scenario reported itself deterministic; CI would accept " +
			"a test that passes or fails by luck")
	}
}

func TestHashChangesWithSemantics(t *testing.T) {
	a, err := fault.ParseScenario([]byte(crashScenario))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	b, err := fault.ParseScenario([]byte(strings.Replace(crashScenario, "seed: 42", "seed: 43", 1)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if a.Hash == b.Hash {
		t.Fatal("changing the seed did not change the scenario hash")
	}
}
