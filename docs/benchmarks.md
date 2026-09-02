# Benchmarks

Every number here was produced by `relab bench`. Nothing is estimated, and no
figure appears anywhere in this repository that was not measured. The raw
results are committed at [`docs/data/benchmarks.csv`](data/benchmarks.csv),
including the hardware and versions each row was measured on.

## Hardware and versions

| | |
|---|---|
| Platform | linux/amd64 |
| CPUs | 4 |
| Go | 1.25.0 |
| PostgreSQL | 16.13 |
| ReLab | development build |
| Measured | 2026-09-02 |

This is a modest 4-core container with PostgreSQL on the same host. Treat these
numbers as a shape, not a ceiling: the interesting result is how throughput and
recovery latency respond to worker count and fault rate, not the absolute
figures.

## Methodology

- Workflow: `examples/data-pipeline.yaml` — four steps, strictly linear, so
  every task waits on the one before it. Four task executions per run.
- 25 runs per matrix point, all created **before** the workers start, so the
  measurement is of how fast the pool drains a saturated queue rather than how
  fast runs are created.
- Faults are injected by failing a fraction of task attempts, drawn from a
  position-derived source (seed, run, task, attempt), so the same fault rate
  produces the same pattern of failures on every machine.
- Retry policy as shipped: 3 attempts, 1s initial delay, ×2, 30s cap, 0.2 jitter.
- The coordinator sweeps throughout, so a fault that costs a lease is recovered
  rather than stalling the measurement.
- Latency is reported as percentiles. A mean would hide exactly the behaviour a
  reliability benchmark is about.
- Recovery percentiles **exclude runs that never went wrong**. Including their
  zeros would drag the median to zero and make recovery look instantaneous.

Reproduce with:

```bash
relab bench examples/data-pipeline.yaml \
  --workers 1,5,10,25 --fault-rate 0,0.01,0.05 --runs 25 \
  --csv docs/data/benchmarks.csv
```

## Results

| Workers | Fault rate | Runs/s | Tasks/s | Run p50 | Run p99 | Recovery p50 | Retries | Lost tasks |
|--------:|-----------:|-------:|--------:|--------:|--------:|-------------:|--------:|-----------:|
| 1  | 0%   | 26.3 | 105 | 556ms | 950ms  | —      | 0 | 0 |
| 1  | 1%   | 13.5 | 54  | 500ms | 1.86s  | 1.02s  | 1 | 0 |
| 1  | 5%   | 5.1  | 21  | 3.51s | 4.88s  | 961ms  | 4 | 0 |
| 5  | 0%   | 78.4 | 314 | 259ms | 325ms  | —      | 0 | 0 |
| 5  | 1%   | 18.9 | 77  | 178ms | 1.36s  | 1.03s  | 2 | 0 |
| 5  | 5%   | 18.3 | 76  | 211ms | 1.41s  | 963ms  | 4 | 0 |
| 10 | 0%   | 95.4 | 381 | 231ms | 272ms  | —      | 0 | 0 |
| 10 | 1%   | 97.8 | 391 | 226ms | 262ms  | —      | 0 | 0 |
| 10 | 5%   | 18.8 | 80  | 227ms | 1.38s  | 1.13s  | 7 | 0 |
| 25 | 0%   | 75.3 | 301 | 341ms | 397ms  | —      | 0 | 0 |
| 25 | 1%   | 86.1 | 344 | 316ms | 386ms  | —      | 0 | 0 |
| 25 | 5%   | 7.6  | 32  | 314ms | 3.36s  | 1.01s  | 7 | 0 |

A dash in the recovery column means no run at that point had anything go wrong,
so there is no recovery time to report.

## What the numbers say

**Zero tasks were lost, at every point in the matrix.** That is the column that
matters most: at no worker count and no fault rate did the system accept work
and fail to complete it.

**Throughput scales to about 10 workers and then flattens.** 1 → 5 workers gives
roughly 3×; 5 → 10 gives another 20%; 25 workers is *slower* than 10 (75 vs 95
runs/s at 0% faults). Past that point the workers are contending for the claim
query and for connections rather than doing work. On a 4-core host with
PostgreSQL sharing it, that is the expected shape.

**Faults cost throughput, and the cost is the backoff, not the failure.** At 5%
faults the p50 run latency barely moves (227ms at 10 workers, against 231ms
clean) while the p99 jumps to 1.38s. The runs that hit a fault wait out a
1-second initial retry delay; the ones that do not are unaffected. A deployment
that cares more about tail latency than about hammering a struggling dependency
should shorten `initial_delay`.

**Recovery is dominated by the retry delay, as designed.** Every recovery p50
sits near 1 second, which is `initial_delay`. The scheduler is not the slow
part; the deliberate wait is.

**The 25-worker, 5% row is the interesting outlier.** 7.6 runs/s with a 3.2s
worst-case recovery. With 25 workers contending and a fault rate high enough
that several runs retry at once, retries queue behind the contention. This is
the shape of a system past its useful concurrency, and it is why the guidance is
to size the pool near the point where throughput flattens rather than as high as
possible.

## A measurement bug worth recording

The first attempt at this matrix timed out, and the cause was not the system
under test. The connection pool defaults to 10 connections; each in-process
runner holds one while it works. At 25 workers the runners were queueing on the
*pool*, which reads exactly like a slow database.

`relab bench` now sizes the pool from the worker count, and `RELAB_DB_MAX_CONNS`
exposes the same knob to operators. The general lesson is in the numbers above:
a pool smaller than your concurrency is a throughput ceiling that has nothing to
do with your database.

## What is not measured here

- **Worker-crash recovery latency under load.** The process-level suite proves
  crash recovery works, and the `worker-crash` scenario passes 20 consecutive
  runs, but neither is a throughput measurement.
- **Behaviour on real hardware.** 4 cores with a co-located database is a
  laptop, not a deployment.
- **Long runs.** Every run here takes well under a second. A workflow with
  minute-long steps exercises lease renewal far harder.
- **Large journals.** No point in this matrix produced enough events to make
  `events` interesting to the query planner.

Each is a gap, and naming them is the point: a benchmark section that lists only
what was measured invites the reader to assume the rest.
