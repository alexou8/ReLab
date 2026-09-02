// Package assert evaluates a scenario's claims against what a run actually did.
//
// Every assertion is answered from the event journal by way of the replay
// reducer, never from a counter the runtime incremented as it went. A metric
// counts what the code that increments it noticed; the journal records what
// happened. When a reliability claim is being made, the difference matters.
package assert

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/fault"
	"github.com/alexou8/relab/internal/replay"
)

// Result is one assertion's outcome.
type Result struct {
	Name     string `json:"name"`
	Passed   bool   `json:"passed"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	// Detail explains what the assertion is really about, for a reader who did
	// not write the scenario.
	Detail string `json:"detail,omitempty"`
}

// Report is the outcome of evaluating a scenario.
type Report struct {
	Scenario string   `json:"scenario"`
	Seed     int64    `json:"seed"`
	RunID    string   `json:"run_id"`
	Passed   bool     `json:"passed"`
	Results  []Result `json:"results"`

	// Observed values reported whether or not they were asserted on, because a
	// passing test that shows its numbers is far more useful than one that
	// only says PASS.
	RecoveryTime     time.Duration `json:"-"`
	RecoveryTimeMS   int64         `json:"recovery_time_ms"`
	Retries          int           `json:"retries"`
	LostTasks        int           `json:"lost_tasks"`
	DuplicateEffects int           `json:"duplicate_effects"`
	SkippedEffects   int           `json:"skipped_effects"`
	FaultsInjected   int           `json:"faults_injected"`
	FinalState       string        `json:"final_state"`
}

// Evaluate checks a scenario's assertions against a replayed run state.
//
// duplicateEffects is supplied by the caller rather than derived from the
// journal, because a duplicate effect is by definition something the ledger did
// not record — it is counted by comparing the ledger's contents against the
// number of times effects were attempted.
func Evaluate(scenario *fault.Scenario, state *replay.RunState, events []event.Event,
	duplicateEffects int) *Report {
	report := &Report{
		Scenario:         scenario.Name,
		Seed:             scenario.Seed,
		RunID:            state.RunID.String(),
		Retries:          state.MaxRetries(),
		LostTasks:        state.LostTasks(),
		DuplicateEffects: duplicateEffects,
		SkippedEffects:   len(state.SkippedEffects),
		FaultsInjected:   len(state.Faults),
		FinalState:       state.Status,
	}
	report.RecoveryTime = RecoveryTime(events)
	report.RecoveryTimeMS = report.RecoveryTime.Milliseconds()

	a := scenario.Assert

	if a.RunStatus != "" {
		report.check("run_status", state.Status == a.RunStatus, a.RunStatus, state.Status,
			"the run must reach this terminal state despite the injected faults")
	}
	if a.LostTasks != nil {
		report.check("lost_tasks", state.LostTasks() <= *a.LostTasks,
			fmt.Sprintf("at most %d", *a.LostTasks), fmt.Sprint(state.LostTasks()),
			"a lost task is work the system accepted and never completed")
	}
	if a.DuplicateEffects != nil {
		report.check("duplicate_effects", duplicateEffects <= *a.DuplicateEffects,
			fmt.Sprintf("at most %d", *a.DuplicateEffects), fmt.Sprint(duplicateEffects),
			"an external effect performed more than once despite the idempotency ledger")
	}
	if a.MaxRecoveryTimeMS != nil {
		report.check("max_recovery_time_ms", report.RecoveryTimeMS <= *a.MaxRecoveryTimeMS,
			fmt.Sprintf("at most %dms", *a.MaxRecoveryTimeMS),
			fmt.Sprintf("%dms", report.RecoveryTimeMS),
			"time from the first lease expiry to the run completing")
	}
	if a.MaxRetriesPerTask != nil {
		report.check("max_retries_per_task", state.MaxRetries() <= *a.MaxRetriesPerTask,
			fmt.Sprintf("at most %d", *a.MaxRetriesPerTask), fmt.Sprint(state.MaxRetries()),
			"more retries than this means recovery is thrashing rather than working")
	}
	if a.MinRetriesPerTask != nil {
		report.check("min_retries_per_task", state.MaxRetries() >= *a.MinRetriesPerTask,
			fmt.Sprintf("at least %d", *a.MinRetriesPerTask), fmt.Sprint(state.MaxRetries()),
			"fewer retries than this means the fault never actually caused a failure")
	}
	if a.FaultsInjected != nil {
		report.check("faults_injected", len(state.Faults) == *a.FaultsInjected,
			fmt.Sprint(*a.FaultsInjected), fmt.Sprint(len(state.Faults)),
			"a scenario that injects no fault proves nothing, however green it is")
	}

	report.Passed = true
	for _, r := range report.Results {
		if !r.Passed {
			report.Passed = false
			break
		}
	}
	return report
}

func (r *Report) check(name string, passed bool, expected, actual, detail string) {
	r.Results = append(r.Results, Result{
		Name: name, Passed: passed, Expected: expected, Actual: actual, Detail: detail,
	})
}

// RecoveryTime measures from the first sign that something went wrong to the
// run completing.
//
// "Something went wrong" means the first lease expiry, injected fault or task
// failure — not the first retry, which is already the recovery. A run that
// never went wrong has no recovery time, and reports zero.
func RecoveryTime(events []event.Event) time.Duration {
	var firstTrouble, completed time.Time
	for _, e := range events {
		switch e.Type {
		case event.TaskLeaseExpired, event.FaultInjected, event.TaskFailed, event.WorkerLost:
			if firstTrouble.IsZero() {
				firstTrouble = e.OccurredAt
			}
		case event.RunSucceeded, event.RunFailed, event.RunCancelled:
			completed = e.OccurredAt
		}
	}
	if firstTrouble.IsZero() || completed.IsZero() || completed.Before(firstTrouble) {
		return 0
	}
	return completed.Sub(firstTrouble)
}

// Human renders the report in the documented format.
func (r *Report) Human() string {
	var b strings.Builder
	verdict := "FAIL"
	if r.Passed {
		verdict = "PASS"
	}
	fmt.Fprintf(&b, "%s %s\n", verdict, r.Scenario)
	fmt.Fprintf(&b, "  recovery time      %s\n", r.RecoveryTime.Round(10*time.Millisecond))
	fmt.Fprintf(&b, "  retries            %d\n", r.Retries)
	fmt.Fprintf(&b, "  lost tasks         %d\n", r.LostTasks)
	fmt.Fprintf(&b, "  duplicate effects  %d\n", r.DuplicateEffects)
	fmt.Fprintf(&b, "  faults injected    %d\n", r.FaultsInjected)
	fmt.Fprintf(&b, "  final state        %s\n", r.FinalState)

	failures := r.Failures()
	if len(failures) > 0 {
		b.WriteString("\n")
		for _, f := range failures {
			fmt.Fprintf(&b, "  %s: expected %s, got %s\n", f.Name, f.Expected, f.Actual)
			if f.Detail != "" {
				fmt.Fprintf(&b, "    %s\n", f.Detail)
			}
		}
	}
	return b.String()
}

// Failures returns the assertions that did not hold.
func (r *Report) Failures() []Result {
	var out []Result
	for _, res := range r.Results {
		if !res.Passed {
			out = append(out, res)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
