package bench_test

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/bench"
	"github.com/alexou8/relab/internal/replay"
)

func sample(status string, duration, recovery time.Duration, tasks, retries, lost int) bench.Sample {
	return bench.Sample{
		RunID: uuid.New(), Status: status, Duration: duration,
		Recovery: recovery, Tasks: tasks, Retries: retries, Lost: lost,
	}
}

func TestSummariseComputesPercentiles(t *testing.T) {
	samples := make([]bench.Sample, 0, 100)
	for i := 1; i <= 100; i++ {
		samples = append(samples, sample(replay.StatusSucceeded,
			time.Duration(i)*time.Millisecond, 0, 4, 0, 0))
	}
	r := bench.Summarise(bench.Params{Workers: 5, Runs: 100}, samples, 10*time.Second, bench.Environment{})

	if r.RunsCompleted != 100 || r.RunsSucceeded != 100 {
		t.Fatalf("counted %d completed and %d succeeded, want 100 of each", r.RunsCompleted, r.RunsSucceeded)
	}
	if r.RunLatencyP50 != 50*time.Millisecond {
		t.Errorf("p50 is %s, want 50ms", r.RunLatencyP50)
	}
	if r.RunLatencyP95 != 95*time.Millisecond {
		t.Errorf("p95 is %s, want 95ms", r.RunLatencyP95)
	}
	if r.RunLatencyP99 != 99*time.Millisecond {
		t.Errorf("p99 is %s, want 99ms", r.RunLatencyP99)
	}
	if r.RunLatencyMax != 100*time.Millisecond {
		t.Errorf("max is %s, want 100ms", r.RunLatencyMax)
	}
	if r.RunsPerSecond != 10 {
		t.Errorf("runs per second is %v, want 10", r.RunsPerSecond)
	}
	if r.TasksPerSecond != 40 {
		t.Errorf("tasks per second is %v, want 40", r.TasksPerSecond)
	}
}

// TestRecoveryPercentilesExcludeCleanRuns is the measurement decision that
// keeps the recovery numbers honest.
func TestRecoveryPercentilesExcludeCleanRuns(t *testing.T) {
	samples := []bench.Sample{
		sample(replay.StatusSucceeded, time.Second, 0, 4, 0, 0),
		sample(replay.StatusSucceeded, time.Second, 0, 4, 0, 0),
		sample(replay.StatusSucceeded, time.Second, 0, 4, 0, 0),
		sample(replay.StatusSucceeded, time.Second, 0, 4, 0, 0),
		sample(replay.StatusSucceeded, 3*time.Second, 2*time.Second, 5, 1, 0),
	}
	r := bench.Summarise(bench.Params{}, samples, time.Second, bench.Environment{})

	// Four of five runs had nothing go wrong. Including their zeros would put
	// the median recovery time at 0 and make recovery look instantaneous.
	if r.RecoveryP50 != 2*time.Second {
		t.Fatalf("recovery p50 is %s, want 2s: runs that never went wrong must not contribute "+
			"zeros to a recovery percentile", r.RecoveryP50)
	}
	if r.RecoveryMax != 2*time.Second {
		t.Fatalf("recovery max is %s, want 2s", r.RecoveryMax)
	}
}

func TestSummariseSeparatesFailures(t *testing.T) {
	samples := []bench.Sample{
		sample(replay.StatusSucceeded, time.Second, 0, 4, 0, 0),
		sample(replay.StatusFailed, time.Second, time.Second, 3, 2, 1),
	}
	r := bench.Summarise(bench.Params{}, samples, time.Second, bench.Environment{})
	if r.RunsSucceeded != 1 || r.RunsFailed != 1 {
		t.Fatalf("counted %d succeeded and %d failed, want 1 of each", r.RunsSucceeded, r.RunsFailed)
	}
	if r.TotalRetries != 2 || r.TotalLostTasks != 1 {
		t.Fatalf("counted %d retries and %d lost, want 2 and 1", r.TotalRetries, r.TotalLostTasks)
	}
}

func TestSummariseOfNoSamples(t *testing.T) {
	r := bench.Summarise(bench.Params{Workers: 3}, nil, time.Second, bench.Environment{})
	if r.RunsCompleted != 0 || r.RunLatencyP95 != 0 {
		t.Fatalf("an empty sample set produced %+v", r)
	}
}

// TestCSVIsSelfDescribing checks that a committed results file carries the
// hardware and versions it was measured on. A throughput number without them is
// a claim rather than a measurement.
func TestCSVIsSelfDescribing(t *testing.T) {
	env := bench.Environment{
		GoVersion: "go1.25.0", OS: "linux", Arch: "amd64", NumCPU: 8,
		PostgresVersion: "16.13", RelabVersion: "v0.1.0", MeasuredAt: time.Now().UTC(),
	}
	r := bench.Summarise(bench.Params{Workers: 5, Runs: 10, FaultRate: 0.05, Workflow: "wf"},
		[]bench.Sample{sample(replay.StatusSucceeded, time.Second, 0, 4, 0, 0)},
		time.Second, env)

	var buf bytes.Buffer
	if err := bench.WriteCSV(&buf, []bench.Result{r}); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("wrote %d rows, want a header and one result", len(rows))
	}
	if len(rows[0]) != len(rows[1]) {
		t.Fatalf("header has %d columns and the row has %d", len(rows[0]), len(rows[1]))
	}

	byName := map[string]string{}
	for i, name := range rows[0] {
		byName[name] = rows[1][i]
	}
	for name, want := range map[string]string{
		"go_version": "go1.25.0", "os": "linux", "arch": "amd64",
		"num_cpu": "8", "postgres_version": "16.13", "relab_version": "v0.1.0",
		"workers": "5", "fault_rate": "0.050",
	} {
		if byName[name] != want {
			t.Errorf("column %s is %q, want %q", name, byName[name], want)
		}
	}
}

func TestSummaryTableNamesItsColumns(t *testing.T) {
	out := bench.Summary([]bench.Result{
		bench.Summarise(bench.Params{Workers: 10, FaultRate: 0.01},
			[]bench.Sample{sample(replay.StatusSucceeded, time.Second, 0, 4, 0, 0)},
			time.Second, bench.Environment{}),
	})
	for _, column := range []string{"WORKERS", "FAULT RATE", "RUNS/S", "RECOV p95", "LOST"} {
		if !strings.Contains(out, column) {
			t.Errorf("summary is missing the %s column:\n%s", column, out)
		}
	}
	if !strings.Contains(out, "1.0%") {
		t.Errorf("summary does not render the fault rate as a percentage:\n%s", out)
	}
}
