// Package examples provides the handlers the shipped example workflows use.
//
// They are deliberately trivial and deterministic: their job is to make the
// scheduler's behaviour observable, not to do interesting work. A handler that
// did real I/O would make a failed scenario ambiguous between "recovery is
// broken" and "the external service was slow", which is the ambiguity ReLab
// exists to remove.
package examples

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/alexou8/relab/sdk"
)

// Register adds every example handler to a registry.
func Register(reg *sdk.Registry) error {
	handlers := map[string]sdk.Handler{
		"import_csv":      importCSV,
		"validate_rows":   validateRows,
		"analyze":         analyze,
		"generate_report": generateReport,
		"split_input":     splitInput,
		"process_shard":   processShard,
		"merge_shards":    mergeShards,
		"slow_step":       slowStep,
		"summarize":       summarize,
	}
	for name, h := range handlers {
		if err := reg.Handle(name, h); err != nil {
			return err
		}
	}
	return nil
}

// MustRegister is Register for process start-up.
func MustRegister(reg *sdk.Registry) {
	if err := Register(reg); err != nil {
		panic(err)
	}
}

// slowStep sleeps for a configurable time and then succeeds. It exists so that
// a worker can be killed while a task is genuinely in flight: every other
// example handler finishes in microseconds, which makes "kill the worker
// mid-task" a race the test would lose most of the time.
//
// The duration comes from the environment because the process-level tests and
// the docker compose demo want different values, and a handler that reads its
// own configuration is preferable to seven handlers that each take a parameter
// the workflow format does not have.
func slowStep(ctx context.Context, tc *sdk.TaskContext) (any, error) {
	d := 2 * time.Second
	if raw := os.Getenv("RELAB_SLOW_STEP_DURATION"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return nil, sdk.Permanent(fmt.Errorf(
				"RELAB_SLOW_STEP_DURATION must be a duration with a unit, got %q: %w", raw, err))
		}
		d = parsed
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		// Respecting cancellation is the contract; a handler that ignores it
		// keeps holding a lease the coordinator has already given up on.
		return nil, ctx.Err()
	case <-timer.C:
	}
	return map[string]any{"slept_ms": d.Milliseconds(), "attempt": tc.Attempt}, nil
}

// summarize reads whatever its dependencies produced, whatever they are called.
// Every other example handler names its upstream step, which couples the
// handler to one position in one workflow; this one is the step to reuse when
// composing a new example.
func summarize(_ context.Context, tc *sdk.TaskContext) (any, error) {
	names := tc.InputNames()
	sort.Strings(names)
	summary := make(map[string]any, len(names))
	for _, name := range names {
		var value any
		if err := tc.Input(name, &value); err != nil {
			return nil, sdk.Permanent(err)
		}
		summary[name] = value
	}
	body, err := json.Marshal(summary)
	if err != nil {
		return nil, err
	}
	tc.Emit("summary.json", "application/json", body)
	return map[string]int{"inputs": len(names)}, nil
}

type rowCount struct {
	Rows int `json:"rows"`
}

func importCSV(_ context.Context, tc *sdk.TaskContext) (any, error) {
	const rows = 1000
	// The artifact is what replay compares. It is derived only from the run's
	// inputs, so two runs of the same workflow produce the same hash and a
	// divergence means something really changed.
	tc.Emit("imported.csv", "text/csv", []byte(fmt.Sprintf("rows=%d", rows)))
	return rowCount{Rows: rows}, nil
}

func validateRows(_ context.Context, tc *sdk.TaskContext) (any, error) {
	var in rowCount
	if err := tc.Input("import", &in); err != nil {
		return nil, sdk.Permanent(err)
	}
	// A row that fails validation is a property of the data, so it is derived
	// rather than random: a scenario that sometimes has 3 invalid rows and
	// sometimes 4 is not reproducible.
	invalid := in.Rows % 7
	return map[string]int{"valid": in.Rows - invalid, "invalid": invalid}, nil
}

func analyze(_ context.Context, tc *sdk.TaskContext) (any, error) {
	var in map[string]int
	if err := tc.Input("validate", &in); err != nil {
		return nil, sdk.Permanent(err)
	}
	valid := in["valid"]
	summary := map[string]any{"count": valid, "mean": float64(valid) / 2}
	body, err := json.Marshal(summary)
	if err != nil {
		return nil, err
	}
	tc.Emit("analysis.json", "application/json", body)
	return summary, nil
}

func generateReport(_ context.Context, tc *sdk.TaskContext) (any, error) {
	var in map[string]any
	if err := tc.Input("analyze", &in); err != nil {
		return nil, sdk.Permanent(err)
	}
	body := []byte(fmt.Sprintf("report: count=%v mean=%v", in["count"], in["mean"]))
	tc.Emit("report.txt", "text/plain", body)
	return map[string]int{"bytes": len(body)}, nil
}

func splitInput(_ context.Context, _ *sdk.TaskContext) (any, error) {
	return map[string]int{"shards": 3}, nil
}

func processShard(_ context.Context, tc *sdk.TaskContext) (any, error) {
	var in map[string]int
	if err := tc.Input("split", &in); err != nil {
		return nil, sdk.Permanent(err)
	}
	// Derived from the step name, so each shard produces a distinct but
	// reproducible result.
	return map[string]any{"shard": tc.TaskName, "records": 100 + len(tc.TaskName)}, nil
}

func mergeShards(_ context.Context, tc *sdk.TaskContext) (any, error) {
	total := 0
	for _, name := range []string{"shard_a", "shard_b", "shard_c"} {
		var in map[string]any
		if err := tc.Input(name, &in); err != nil {
			return nil, sdk.Permanent(err)
		}
		records, ok := in["records"].(float64)
		if !ok {
			return nil, sdk.Permanent(fmt.Errorf("shard %q reported no record count", name))
		}
		total += int(records)
	}
	tc.Emit("merged.json", "application/json", []byte(fmt.Sprintf(`{"records":%d}`, total)))
	return map[string]int{"records": total}, nil
}
