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
