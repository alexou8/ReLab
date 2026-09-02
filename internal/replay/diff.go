package replay

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Category names a kind of divergence. The categories are the ones that mean
// something operationally: an unrecognised difference is reported under
// CategoryState rather than invented as a new category.
type Category string

// The divergence categories.
const (
	// CategoryTerminalState: the two runs ended differently. The most serious
	// category — one of them recovered and the other did not.
	CategoryTerminalState Category = "terminal-state"
	// CategoryMissingEvent: the replayed journal lacks something the original
	// had.
	CategoryMissingEvent Category = "missing-event"
	// CategoryExtraEvent: the replayed journal has something the original did not.
	CategoryExtraEvent Category = "extra-event"
	// CategoryOrdering: the same events in a different order.
	CategoryOrdering Category = "ordering"
	// CategoryArtifactHash: a task produced different bytes. This is the
	// category that catches non-determinism in handlers.
	CategoryArtifactHash Category = "artifact-hash"
	// CategoryAttemptCount: a task took a different number of attempts, meaning
	// the two runs met different failures.
	CategoryAttemptCount Category = "attempt-count"
	// CategoryState: any other reconstructed difference.
	CategoryState Category = "state"
)

// Divergence is one difference between two run states.
type Divergence struct {
	Category Category `json:"category"`
	// Subject is what differed: a task name, an artifact, or "run".
	Subject  string `json:"subject"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	// Detail explains why the difference matters, where that is not obvious.
	Detail string `json:"detail,omitempty"`
}

func (d Divergence) String() string {
	line := fmt.Sprintf("%-15s %-20s expected %s, got %s", d.Category, d.Subject, d.Expected, d.Actual)
	if d.Detail != "" {
		line += "\n" + strings.Repeat(" ", 16) + d.Detail
	}
	return line
}

// Report is the result of a comparison.
type Report struct {
	Divergences []Divergence `json:"divergences"`
}

// Match reports whether the two states agree.
func (r *Report) Match() bool { return len(r.Divergences) == 0 }

func (r *Report) add(d Divergence) { r.Divergences = append(r.Divergences, d) }

// Compare produces a divergence report between a recorded state and another.
//
// Wall-clock timings are deliberately not compared. Replay does not claim to
// reproduce them, and comparing them would make every report noise. What is
// compared is what the system claims to be deterministic about: terminal state,
// task outcomes, attempt counts, artifact hashes and the event stream's shape.
func Compare(expected, actual *RunState) *Report {
	report := &Report{}

	if expected.Status != actual.Status {
		report.add(Divergence{
			Category: CategoryTerminalState, Subject: "run",
			Expected: expected.Status, Actual: actual.Status,
			Detail: "the two runs ended differently; one recovered and the other did not",
		})
	}
	if expected.DefinitionHash != actual.DefinitionHash {
		report.add(Divergence{
			Category: CategoryState, Subject: "definition",
			Expected: short(expected.DefinitionHash), Actual: short(actual.DefinitionHash),
			Detail: "the runs used different workflow definitions, so nothing below is comparable",
		})
	}
	if expected.Seed != actual.Seed {
		report.add(Divergence{
			Category: CategoryState, Subject: "seed",
			Expected: fmt.Sprint(expected.Seed), Actual: fmt.Sprint(actual.Seed),
			Detail: "different seeds drive different fault decisions",
		})
	}

	compareTasks(report, expected, actual)
	compareFaults(report, expected, actual)

	if len(expected.SkippedEffects) != len(actual.SkippedEffects) {
		report.add(Divergence{
			Category: CategoryState, Subject: "skipped-effects",
			Expected: fmt.Sprint(len(expected.SkippedEffects)),
			Actual:   fmt.Sprint(len(actual.SkippedEffects)),
			Detail:   "the idempotency ledger suppressed a different number of repeats",
		})
	}
	return report
}

func compareTasks(report *Report, expected, actual *RunState) {
	for _, name := range expected.TaskNames() {
		want := expected.Tasks[name]
		got, present := actual.Tasks[name]
		if !present {
			report.add(Divergence{
				Category: CategoryMissingEvent, Subject: name,
				Expected: want.Status, Actual: "absent",
				Detail: "the replayed journal never mentions this task",
			})
			continue
		}
		if want.Status != got.Status {
			report.add(Divergence{
				Category: CategoryState, Subject: name,
				Expected: want.Status, Actual: got.Status,
			})
		}
		if want.Attempts != got.Attempts {
			report.add(Divergence{
				Category: CategoryAttemptCount, Subject: name,
				Expected: fmt.Sprint(want.Attempts), Actual: fmt.Sprint(got.Attempts),
				Detail: "the two runs met different failures",
			})
		}
		compareArtifacts(report, name, want, got)
	}
	for _, name := range actual.TaskNames() {
		if _, present := expected.Tasks[name]; !present {
			report.add(Divergence{
				Category: CategoryExtraEvent, Subject: name,
				Expected: "absent", Actual: actual.Tasks[name].Status,
				Detail: "the replayed journal mentions a task the original never had",
			})
		}
	}
}

// compareArtifacts is the check that catches non-deterministic handlers: same
// inputs, different bytes out.
func compareArtifacts(report *Report, task string, want, got *TaskState) {
	wantByName := map[string]Artifact{}
	for _, a := range want.Artifacts {
		wantByName[a.Name] = a
	}
	gotByName := map[string]Artifact{}
	for _, a := range got.Artifacts {
		gotByName[a.Name] = a
	}

	names := make([]string, 0, len(wantByName)+len(gotByName))
	for name := range wantByName {
		names = append(names, name)
	}
	for name := range gotByName {
		if _, dup := wantByName[name]; !dup {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	for _, name := range names {
		w, hadExpected := wantByName[name]
		g, hadActual := gotByName[name]
		switch {
		case hadExpected && !hadActual:
			report.add(Divergence{
				Category: CategoryArtifactHash, Subject: task + "/" + name,
				Expected: short(w.SHA256), Actual: "absent",
			})
		case !hadExpected && hadActual:
			report.add(Divergence{
				Category: CategoryArtifactHash, Subject: task + "/" + name,
				Expected: "absent", Actual: short(g.SHA256),
			})
		case w.SHA256 != g.SHA256:
			report.add(Divergence{
				Category: CategoryArtifactHash, Subject: task + "/" + name,
				Expected: short(w.SHA256), Actual: short(g.SHA256),
				Detail: "same inputs produced different bytes: the handler is not deterministic, " +
					"or it depends on something outside the recorded inputs",
			})
		}
	}
}

// compareFaults checks that the same faults fired in the same order. Two runs
// under one scenario and seed must inject identically, or the scenario is not a
// regression test.
func compareFaults(report *Report, expected, actual *RunState) {
	if len(expected.Faults) != len(actual.Faults) {
		report.add(Divergence{
			Category: CategoryState, Subject: "faults",
			Expected: fmt.Sprint(len(expected.Faults)), Actual: fmt.Sprint(len(actual.Faults)),
			Detail: "the same scenario and seed must inject the same faults",
		})
		return
	}
	for i := range expected.Faults {
		w, g := expected.Faults[i], actual.Faults[i]
		if w == g {
			continue
		}
		report.add(Divergence{
			Category: CategoryOrdering, Subject: fmt.Sprintf("fault[%d]", i),
			Expected: fmt.Sprintf("%s at %s on %s", w.Type, w.Point, w.Task),
			Actual:   fmt.Sprintf("%s at %s on %s", g.Type, g.Point, g.Task),
		})
	}
}

// JSON renders the report for `--json`.
func (r *Report) JSON() ([]byte, error) {
	out, err := json.MarshalIndent(map[string]any{
		"match":       r.Match(),
		"divergences": r.Divergences,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("replay: render report: %w", err)
	}
	return out, nil
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	if hash == "" {
		return "(none)"
	}
	return hash
}
