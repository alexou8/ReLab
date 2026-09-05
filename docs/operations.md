# Operating a ReLab deployment

What to watch, what to alert on, and what each number does and does not mean.
Everything here is keyed to instruments that exist in `internal/telemetry`; no
metric is invented for the sake of a nice dashboard.

## How metrics leave the process

ReLab exports OpenTelemetry metrics over OTLP/gRPC. There is **no Prometheus
endpoint on the process**: point the standard OpenTelemetry Collector at it and
scrape the collector.

```bash
export RELAB_OTLP_ENDPOINT=collector:4317   # unset disables export entirely
```

A collector with `otlp` in and `prometheus` out is the whole configuration
needed. The instrument names below are what arrive; a Prometheus exporter turns
`task_lease_expirations_total` into `task_lease_expirations_total` with the
resource attributes as labels.

Adding a Prometheus endpoint to the binary would mean a new direct dependency
and a second serving surface to secure, and the collector already does it. If
that trade stops making sense for a real deployment, it is a good issue to file.

## The instruments

| Instrument | Type | What it counts |
|---|---|---|
| `workflow_runs_total` | counter | Runs that reached a terminal state, by status |
| `task_executions_total` | counter | Task attempts that finished, by status |
| `task_retries_total` | counter | Retries scheduled after a handler failure |
| `task_lease_expirations_total` | counter | Leases observed to expire — the recovery trigger |
| `worker_lost_total` | counter | Workers declared dead and their leases released |
| `duplicate_executions_total` | counter | Two workers observed on the same attempt |
| `side_effects_skipped_total` | counter | Repeats suppressed by the idempotency ledger |
| `task_latency_seconds` | histogram | Handler execution time |
| `recovery_time_seconds` | histogram | First failure to run completion |
| `run_duration_seconds` | histogram | Run creation to terminal state |
| `queue_depth` | gauge | Tasks runnable or waiting on a retry |

## Alerts worth having

The thresholds are starting points, not measurements. Each one says what it
means, because an alert nobody can interpret gets silenced.

### `duplicate_executions_total` increases at all

**Page.** Two workers executed the same *attempt* of a task, which the primary
key on `task_attempts` is supposed to make impossible. This is a scheduler bug
or a database that is not enforcing the constraint, and it invalidates the
project's central concurrency claim. Any non-zero rate is worth waking someone.

```
increase(duplicate_executions_total[15m]) > 0
```

### Workers lost with no deploy in progress

**Page** if sustained. Losing workers is normal during a rolling restart and is
how recovery is supposed to work; a steady rate outside a deploy means processes
are dying, or heartbeats are not reaching the database.

```
rate(worker_lost_total[10m]) > 0 and on() absent(deploy_in_progress)
```

### Lease expirations climbing without worker loss

**Warn.** Leases expiring while workers stay alive means renewal is not keeping
up: an overloaded database, a saturated connection pool, or handlers blocking
the renewal loop. This is the condition that produces duplicate *task* execution
under a healthy-looking fleet.

```
rate(task_lease_expirations_total[10m]) > 3 * rate(worker_lost_total[10m])
```

### Recovery time above its budget

**Warn.** The worst case with the shipped defaults is `LOST after` (25s) plus
one reaper interval. A p99 well above that means the reaper is not sweeping, or
the database is slow enough to matter.

```
histogram_quantile(0.99, rate(recovery_time_seconds_bucket[30m])) > 40
```

### Queue depth rising monotonically

**Warn.** Tasks are arriving faster than the pool drains them, or nothing is
claiming: no healthy workers, or a claim query that is failing. Cross-check
against the worker table before adding capacity.

```
deriv(queue_depth[30m]) > 0 and queue_depth > 100
```

### Dead letters appearing

**Warn.** A task exhausted its attempts, so a run failed and its work stopped.
It is a normal outcome of a genuinely broken handler and an abnormal one
otherwise. The count is in `/api/v1/stats`; `workflow_runs_total{status="FAILED"}`
is the metric side of the same event.

### Replay divergence

**Page.** `relab replay <run-id> --diff` exits non-zero when the journal and the
database disagree, which means the recorded history is not a faithful account of
the run — the one claim everything else here rests on. Run it over a sample of
finished runs on a schedule rather than waiting for someone to notice.

## What is not measured

Stated so nobody builds a dashboard panel expecting it:

- **No database saturation metric of ReLab's own.** Connection pool pressure is
  visible from PostgreSQL (`pg_stat_activity`) and from `task_latency_seconds`
  widening; ReLab does not export pool statistics.
- **No stuck-run detector.** A run that stops progressing without failing shows
  up as `queue_depth` not falling and no terminal event, not as its own signal.
- **No alert has been exercised against a real incident.** These are derived
  from what the instruments mean, not from a postmortem. Treat the thresholds as
  a starting point and tune them against your own traffic.
- **No load or soak testing has been done.** `docs/benchmarks.md` reports
  `relab bench` on stated hardware, which is a benchmark and not a soak.

## Backup and restore

Everything ReLab knows is in PostgreSQL, in one schema, with no second datastore
to keep consistent. An ordinary `pg_dump` is a complete backup, and restoring it
restores every run's history.

```bash
pg_dump "$RELAB_DSN" > relab-$(date -I).sql
psql "$RELAB_DSN" < relab-2026-01-01.sql
```

Migrations are forward-only and checksummed: a binary refuses to start against a
schema whose applied migrations do not match the ones it was built with, which
is deliberate — it fails one deploy loudly rather than corrupting a journal
quietly. Rolling back a release therefore means rolling back to a binary whose
expected schema version matches what is applied; there is no down-migration, and
`docs/deployment.md` covers the sequence.
