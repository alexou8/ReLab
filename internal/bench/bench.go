// Package bench measures throughput and recovery under load.
//
// Every number in docs/benchmarks.md and in the README comes from here. The
// harness records the hardware, the versions and the exact parameters
// alongside the results, because a throughput figure without them is not a
// measurement — it is a claim.
package bench

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/assert"
	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/replay"
)

// Params describes one point in the benchmark matrix.
type Params struct {
	Workers   int
	Runs      int
	FaultRate float64
	Workflow  string
}

// Result is what one point measured.
//
// Latency is reported as percentiles rather than a mean. A mean hides exactly
// the behaviour a reliability benchmark is about: a system that recovers in 2
// seconds 99 times and 90 seconds once has a good mean and a bad p99, and the
// p99 is the number an operator lives with.
type Result struct {
	Params

	RunsCompleted int
	RunsSucceeded int
	RunsFailed    int

	WallClock       time.Duration
	RunsPerSecond   float64
	TasksExecuted   int
	TasksPerSecond  float64
	TotalRetries    int
	TotalLostTasks  int
	DuplicateEffect int

	RunLatencyP50 time.Duration
	RunLatencyP95 time.Duration
	RunLatencyP99 time.Duration
	RunLatencyMax time.Duration

	RecoveryP50 time.Duration
	RecoveryP95 time.Duration
	RecoveryMax time.Duration

	Environment Environment
}

// Environment records what the numbers were measured on. Benchmarks published
// without it cannot be reproduced or compared, which makes them decoration.
type Environment struct {
	GoVersion       string
	OS              string
	Arch            string
	NumCPU          int
	PostgresVersion string
	RelabVersion    string
	MeasuredAt      time.Time
}

// DescribeEnvironment collects what can be determined from the process and the
// database.
func DescribeEnvironment(ctx context.Context, eng *engine.Engine, relabVersion string) Environment {
	env := Environment{
		GoVersion:    runtime.Version(),
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		NumCPU:       runtime.NumCPU(),
		RelabVersion: relabVersion,
		MeasuredAt:   time.Now().UTC(),
	}
	var version string
	if err := eng.DB().Conn().QueryRow(ctx, `SHOW server_version`).Scan(&version); err == nil {
		env.PostgresVersion = version
	}
	return env
}

// Sample is one completed run's measurements.
type Sample struct {
	RunID    uuid.UUID
	Status   string
	Duration time.Duration
	Recovery time.Duration
	Tasks    int
	Retries  int
	Lost     int
}

// Summarise turns samples into a Result.
func Summarise(params Params, samples []Sample, wall time.Duration, env Environment) Result {
	r := Result{Params: params, WallClock: wall, Environment: env}
	if len(samples) == 0 {
		return r
	}

	durations := make([]time.Duration, 0, len(samples))
	recoveries := make([]time.Duration, 0, len(samples))
	for _, s := range samples {
		r.RunsCompleted++
		if s.Status == replay.StatusSucceeded {
			r.RunsSucceeded++
		} else {
			r.RunsFailed++
		}
		r.TasksExecuted += s.Tasks
		r.TotalRetries += s.Retries
		r.TotalLostTasks += s.Lost
		durations = append(durations, s.Duration)
		// Only runs that actually had something go wrong contribute a recovery
		// time. Including the zeros from clean runs would drag every
		// percentile towards zero and make recovery look faster than it is.
		if s.Recovery > 0 {
			recoveries = append(recoveries, s.Recovery)
		}
	}

	if wall > 0 {
		r.RunsPerSecond = float64(r.RunsCompleted) / wall.Seconds()
		r.TasksPerSecond = float64(r.TasksExecuted) / wall.Seconds()
	}

	r.RunLatencyP50 = percentile(durations, 0.50)
	r.RunLatencyP95 = percentile(durations, 0.95)
	r.RunLatencyP99 = percentile(durations, 0.99)
	r.RunLatencyMax = percentile(durations, 1)

	r.RecoveryP50 = percentile(recoveries, 0.50)
	r.RecoveryP95 = percentile(recoveries, 0.95)
	r.RecoveryMax = percentile(recoveries, 1)
	return r
}

// percentile returns the p-th percentile using nearest-rank, which needs no
// interpolation and is exact for the small sample counts a benchmark run
// produces.
func percentile(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(float64(len(sorted))*p+0.5) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// SampleFrom builds a Sample from a finished run's journal.
func SampleFrom(ctx context.Context, eng *engine.Engine, runID uuid.UUID) (Sample, error) {
	events, err := eng.Events(ctx, runID)
	if err != nil {
		return Sample{}, err
	}
	state, err := replay.Reduce(runID, events)
	if err != nil {
		return Sample{}, err
	}
	run, err := eng.RunByID(ctx, runID)
	if err != nil {
		return Sample{}, err
	}

	sample := Sample{
		RunID:    runID,
		Status:   state.Status,
		Recovery: assert.RecoveryTime(events),
		Tasks:    state.TotalAttempts(),
		Lost:     state.LostTasks(),
	}
	for _, t := range state.Tasks {
		sample.Retries += t.Retries
	}
	if run.CompletedAt != nil {
		sample.Duration = run.CompletedAt.Sub(run.CreatedAt)
	}
	return sample, nil
}

// CSVHeader is the column set WriteCSV emits.
var CSVHeader = []string{
	"workers", "runs", "fault_rate", "workflow",
	"runs_completed", "runs_succeeded", "runs_failed",
	"wall_clock_s", "runs_per_second", "tasks_executed", "tasks_per_second",
	"retries", "lost_tasks",
	"run_p50_ms", "run_p95_ms", "run_p99_ms", "run_max_ms",
	"recovery_p50_ms", "recovery_p95_ms", "recovery_max_ms",
	"go_version", "os", "arch", "num_cpu", "postgres_version", "relab_version", "measured_at",
}

// WriteCSV writes results with their header, so the file is self-describing and
// can be committed alongside the numbers it produced.
func WriteCSV(w io.Writer, results []Result) error {
	out := csv.NewWriter(w)
	if err := out.Write(CSVHeader); err != nil {
		return fmt.Errorf("bench: write csv header: %w", err)
	}
	for _, r := range results {
		if err := out.Write(r.row()); err != nil {
			return fmt.Errorf("bench: write csv row: %w", err)
		}
	}
	out.Flush()
	if err := out.Error(); err != nil {
		return fmt.Errorf("bench: flush csv: %w", err)
	}
	return nil
}

func (r Result) row() []string {
	ms := func(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }
	return []string{
		strconv.Itoa(r.Workers), strconv.Itoa(r.Runs),
		strconv.FormatFloat(r.FaultRate, 'f', 3, 64), r.Workflow,
		strconv.Itoa(r.RunsCompleted), strconv.Itoa(r.RunsSucceeded), strconv.Itoa(r.RunsFailed),
		strconv.FormatFloat(r.WallClock.Seconds(), 'f', 3, 64),
		strconv.FormatFloat(r.RunsPerSecond, 'f', 2, 64),
		strconv.Itoa(r.TasksExecuted),
		strconv.FormatFloat(r.TasksPerSecond, 'f', 2, 64),
		strconv.Itoa(r.TotalRetries), strconv.Itoa(r.TotalLostTasks),
		ms(r.RunLatencyP50), ms(r.RunLatencyP95), ms(r.RunLatencyP99), ms(r.RunLatencyMax),
		ms(r.RecoveryP50), ms(r.RecoveryP95), ms(r.RecoveryMax),
		r.Environment.GoVersion, r.Environment.OS, r.Environment.Arch,
		strconv.Itoa(r.Environment.NumCPU), r.Environment.PostgresVersion,
		r.Environment.RelabVersion, r.Environment.MeasuredAt.Format(time.RFC3339),
	}
}

// Summary renders a human-readable table.
func Summary(results []Result) string {
	var b []byte
	b = append(b, fmt.Sprintf("%-8s %-11s %-8s %-9s %-9s %-10s %-10s %s\n",
		"WORKERS", "FAULT RATE", "RUNS", "RUNS/S", "TASKS/S", "RUN p95", "RECOV p95", "LOST")...)
	for _, r := range results {
		b = append(b, fmt.Sprintf("%-8d %-11s %-8d %-9.2f %-9.2f %-10s %-10s %d\n",
			r.Workers,
			strconv.FormatFloat(r.FaultRate*100, 'f', 1, 64)+"%",
			r.RunsCompleted, r.RunsPerSecond, r.TasksPerSecond,
			r.RunLatencyP95.Round(time.Millisecond),
			r.RecoveryP95.Round(time.Millisecond),
			r.TotalLostTasks)...)
	}
	return string(b)
}
