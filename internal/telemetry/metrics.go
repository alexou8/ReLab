package telemetry

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics is the instrument set ReLab records.
//
// The instruments are the ones that answer questions about reliability, not
// every number that could be counted. Each is listed in docs/reliability.md
// with what it means and what to conclude from it.
type Metrics struct {
	WorkflowRuns        metric.Int64Counter
	TaskExecutions      metric.Int64Counter
	TaskRetries         metric.Int64Counter
	LeaseExpirations    metric.Int64Counter
	WorkersLost         metric.Int64Counter
	DuplicateExecutions metric.Int64Counter
	SideEffectsSkipped  metric.Int64Counter
	QueueDepth          metric.Int64Gauge
	TaskLatency         metric.Float64Histogram
	RecoveryTime        metric.Float64Histogram
	RunDuration         metric.Float64Histogram
}

var (
	metricsOnce sync.Once
	metricsInst *Metrics
	metricsErr  error
)

// Meter returns the process-wide metric set, creating it on first use.
//
// It is a singleton because OpenTelemetry instruments are cheap to record on
// and comparatively expensive to create, and because two instruments with the
// same name and different descriptions are a conflict the SDK reports at export
// time — long after the code that caused it ran.
func Meter() (*Metrics, error) {
	metricsOnce.Do(func() {
		metricsInst, metricsErr = newMetrics()
	})
	return metricsInst, metricsErr
}

func newMetrics() (*Metrics, error) {
	m := otelMeter()
	var errs []error
	counter := func(name, desc, unit string) metric.Int64Counter {
		c, err := m.Int64Counter(name, metric.WithDescription(desc), metric.WithUnit(unit))
		if err != nil {
			errs = append(errs, fmt.Errorf("create counter %s: %w", name, err))
		}
		return c
	}
	histogram := func(name, desc string, buckets []float64) metric.Float64Histogram {
		h, err := m.Float64Histogram(name,
			metric.WithDescription(desc),
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(buckets...))
		if err != nil {
			errs = append(errs, fmt.Errorf("create histogram %s: %w", name, err))
		}
		return h
	}

	// Recovery is measured in seconds and the interesting range is roughly one
	// lease to a few leases, so the buckets are chosen around that rather than
	// left at the SDK's default millisecond-scale HTTP latency buckets.
	recoveryBuckets := []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120}
	taskBuckets := []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60}
	runBuckets := []float64{0.01, 0.1, 0.5, 1, 5, 10, 30, 60, 300, 900}

	inst := &Metrics{
		WorkflowRuns: counter("workflow_runs_total",
			"Runs that reached a terminal state, by status", "{run}"),
		TaskExecutions: counter("task_executions_total",
			"Task attempts that finished, by status", "{execution}"),
		TaskRetries: counter("task_retries_total",
			"Retries scheduled after a handler failure", "{retry}"),
		LeaseExpirations: counter("task_lease_expirations_total",
			"Leases observed to expire, which is the recovery trigger", "{expiration}"),
		WorkersLost: counter("worker_lost_total",
			"Workers declared dead and their leases released", "{worker}"),
		DuplicateExecutions: counter("duplicate_executions_total",
			"Two workers observed executing the same attempt, which is a scheduler bug", "{execution}"),
		SideEffectsSkipped: counter("side_effects_skipped_total",
			"Repeats suppressed by the idempotency ledger", "{effect}"),
		TaskLatency:  histogram("task_latency_seconds", "Handler execution time", taskBuckets),
		RecoveryTime: histogram("recovery_time_seconds", "First failure to run completion", recoveryBuckets),
		RunDuration:  histogram("run_duration_seconds", "Run creation to terminal state", runBuckets),
	}

	gauge, err := m.Int64Gauge("queue_depth",
		metric.WithDescription("Tasks that are runnable or waiting on a retry"),
		metric.WithUnit("{task}"))
	if err != nil {
		errs = append(errs, fmt.Errorf("create gauge queue_depth: %w", err))
	}
	inst.QueueDepth = gauge

	if len(errs) > 0 {
		return nil, fmt.Errorf("telemetry: build metrics: %w", errs[0])
	}
	return inst, nil
}

// Attribute helpers, so that label names are spelled once. A metric whose
// label is "status" in one place and "state" in another produces two time
// series that look like one.
func Status(v string) attribute.KeyValue   { return attribute.String("status", v) }
func Workflow(v string) attribute.KeyValue { return attribute.String("workflow", v) }
func TaskName(v string) attribute.KeyValue { return attribute.String("task", v) }

// The recording methods below are all safe on a nil *Metrics.
//
// A process whose telemetry setup failed still runs workflows — telemetry is
// diagnostic, the workflows are the job — and making every call site say so
// would put a nil check next to every measurement. Naming each measurement
// also keeps its label set in one place, so two call sites cannot disagree
// about what a series is keyed by.

// RecordTaskExecution counts one finished attempt and its duration.
func (m *Metrics) RecordTaskExecution(ctx context.Context, status, workflow, task string,
	seconds float64) {
	if m == nil {
		return
	}
	attrs := metric.WithAttributes(Status(status), Workflow(workflow), TaskName(task))
	if m.TaskExecutions != nil {
		m.TaskExecutions.Add(ctx, 1, attrs)
	}
	if m.TaskLatency != nil {
		m.TaskLatency.Record(ctx, seconds, attrs)
	}
}

// RecordRun counts a run reaching a terminal state, with how long it took.
func (m *Metrics) RecordRun(ctx context.Context, status, workflow string, seconds float64) {
	if m == nil {
		return
	}
	attrs := metric.WithAttributes(Status(status), Workflow(workflow))
	if m.WorkflowRuns != nil {
		m.WorkflowRuns.Add(ctx, 1, attrs)
	}
	if m.RunDuration != nil {
		m.RunDuration.Record(ctx, seconds, attrs)
	}
}

// RecordRetry counts a retry scheduled after a handler failure.
func (m *Metrics) RecordRetry(ctx context.Context, workflow, task string) {
	if m == nil || m.TaskRetries == nil {
		return
	}
	m.TaskRetries.Add(ctx, 1, metric.WithAttributes(Workflow(workflow), TaskName(task)))
}

// RecordLeaseExpiration counts a lease observed to expire, which is what
// triggers recovery.
func (m *Metrics) RecordLeaseExpiration(ctx context.Context, n int64) {
	if m == nil || m.LeaseExpirations == nil || n == 0 {
		return
	}
	m.LeaseExpirations.Add(ctx, n)
}

// RecordWorkersLost counts workers declared dead.
func (m *Metrics) RecordWorkersLost(ctx context.Context, n int64) {
	if m == nil || m.WorkersLost == nil || n == 0 {
		return
	}
	m.WorkersLost.Add(ctx, n)
}

// RecordDuplicateExecution counts two workers observed executing one attempt.
// It should always be zero; a non-zero value is a scheduler bug, not load.
func (m *Metrics) RecordDuplicateExecution(ctx context.Context, task string) {
	if m == nil || m.DuplicateExecutions == nil {
		return
	}
	m.DuplicateExecutions.Add(ctx, 1, metric.WithAttributes(TaskName(task)))
}

// RecordSideEffectSkipped counts a repeat suppressed by the idempotency ledger.
func (m *Metrics) RecordSideEffectSkipped(ctx context.Context, task string) {
	if m == nil || m.SideEffectsSkipped == nil {
		return
	}
	m.SideEffectsSkipped.Add(ctx, 1, metric.WithAttributes(TaskName(task)))
}

// RecordRecovery records how long a run took to recover from its first failure.
func (m *Metrics) RecordRecovery(ctx context.Context, workflow string, seconds float64) {
	if m == nil || m.RecoveryTime == nil {
		return
	}
	m.RecoveryTime.Record(ctx, seconds, metric.WithAttributes(Workflow(workflow)))
}

// RecordQueueDepth reports how many tasks are runnable or waiting on a retry.
func (m *Metrics) RecordQueueDepth(ctx context.Context, depth int64) {
	if m == nil || m.QueueDepth == nil {
		return
	}
	m.QueueDepth.Record(ctx, depth)
}
