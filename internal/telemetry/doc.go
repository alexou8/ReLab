// Package telemetry sets up traces, metrics and structured logging.
//
// The three share identifiers deliberately. Every log line a run produces
// carries run_id, task_id and worker_id; every span carries the same
// attributes; and every log line emitted inside a span also carries the
// trace_id. That is what makes it possible to see a failure in a log, jump to
// the trace, and find the task that caused it — which is the whole reason to
// instrument a system whose interesting behaviour is distributed.
//
// The default exporter is stdout, so `docker compose up` produces usable
// telemetry with nothing else running. OTLP is enabled by setting
// RELAB_OTLP_ENDPOINT.
package telemetry
