# Reliability: what ReLab guarantees, and what it does not

This document is the authority on ReLab's guarantees. Where the README, the
package documentation and this file disagree, this file is correct and the
others are bugs.

Every claim here is backed by a test named in the text. A claim without a test
does not belong in this document.

---

## Delivery

**ReLab delivers at least once.** A task may execute more than once. There is no
configuration that makes it exactly-once, and there is no plan to add one,
because for external side effects it is not achievable.

A task executes a second time when:

- its handler failed and the retry policy has attempts remaining, or
- its lease expired while a worker still held it — because the worker crashed,
  or because it was alive but the coordinator could not tell.

The second case is the interesting one. A worker that is alive but unreachable
is indistinguishable from one that is dead, and the coordinator must choose
between leaving work stranded and running it twice. ReLab chooses to run it
twice, and bounds the damage with the idempotency ledger.

*Tested by:* `TestSIGKILLedWorkerLosesItsTaskAndTheRunStillSucceeds`,
`TestLeaseExpiryRequeuesTheTask`.

---

## Side effects

**An effect recorded under an idempotency key is performed at most once after it
has been recorded.**

`sdk.TaskContext.Do` performs an effect and records it in the `side_effects`
table under `run_id:task_name:operation`. A later attempt that finds a record
skips the effect, returns the recorded result, and emits `SIDE_EFFECT_SKIPPED`.

**The window this does not close.** The effect is external and the record is in
PostgreSQL. No transaction spans both, so a crash between performing the effect
and recording it produces a duplicate. The window is one `INSERT` wide. It is
real, and no amount of care closes it.

**Effects performed outside `Do` are not protected at all.** A handler that
calls an HTTP API directly gets no idempotency from ReLab.

**A failed effect is not recorded**, so the retry performs it again. This is
required for a retry to mean anything. It has a corollary: an effect that
succeeded externally but *reported* failure will be repeated. The external
API's own idempotency key is the only real answer, and a handler that has one
should pass it.

*Tested by:* `TestEffectSurvivesACrashBeforeAcknowledgement` (which SIGKILLs a
process between the effect and the acknowledgement),
`TestDoUnderConcurrencyPerformsTheEffectOnce`, `TestFailedEffectIsNotRecorded`.

---

## Concurrent execution

**Two workers never execute the same attempt of the same task at the same time.**

This is enforced by the primary key on `task_attempts (task_id, attempt)`, not
by timing. A worker inserts a row when it begins executing; a second worker on
the same attempt gets a constraint violation and stops.

Note what this does *not* say. Two workers may execute the same *task* at once,
under different attempt numbers, when a lease expires while the first worker is
still running. That is the at-least-once duplicate described above, and it is
the case the idempotency ledger exists for.

*Tested by:* `TestThreeWorkersProcessAFanOutWithoutDoubleExecution`,
`TestConcurrentAttemptIsRefused`, and the `assertNoDuplicateAttempt` check in
every process-level crash test.

---

## The event journal

**A run's history is gapless, ordered, and closed.**

- Sequence numbers are allocated by incrementing a counter on the run row inside
  the appending transaction, so a rolled back append rolls back its number. A
  gap therefore means data loss, not a routine abort.
- No event may be appended to a run whose `completed_at` is set. A run's
  terminal event is its last one, so a finished run's story cannot change.
- Every payload carries a schema version. A payload the reading build does not
  understand is an error, not something to skip.

Reading a run verifies all three. `relab replay` refuses a journal that violates
any of them rather than reconstructing a plausible-looking wrong answer.

*Tested by:* `TestAppendConcurrentSeqIsGapless` (50 concurrent appends to one
run), `TestAppendRollbackReleasesSeq`, `TestAppendRefusesAClosedRun`,
`TestCorruptedEventProducesACategoryNotACrash`.

---

## Replay

**Replay reconstructs logical state. It does not re-execute handlers.**

What it reconstructs: which tasks ran, how many attempts each took, what they
produced, which artifacts they emitted (by content hash), which faults fired,
which effects were suppressed, and how the run ended.

What it does not reconstruct:

- Anything a handler did that was not recorded. If a handler wrote to a file and
  did not emit an artifact, replay does not know.
- External API responses. They are only reproducible when recorded through a
  fixture adapter, which ReLab does not ship in v1.
- Wall-clock timings. Two runs of the same workflow take different amounts of
  time, and `relab replay --diff` deliberately does not compare durations.

*Tested by:* `TestReplayOfRecordedRunsMatches` (ten runs, three with recovery),
`TestReduceIsDeterministic`.

---

## Recovery timing

These are the defaults. They are stated once, in `internal/config`, and every
process reads them from there.

| Setting | Default | What it controls |
|---|---|---|
| Lease duration | 30s | How long a claim is valid without renewal |
| Lease renewal | 10s | How often a worker extends its leases |
| Heartbeat | 5s | How often a worker reports liveness |
| SUSPECT after | 3 missed beats | When a worker is doubted |
| LOST after | 5 missed beats | When its leases are released |
| Reaper interval | 1s | How often expired leases are swept |
| Task timeout | 5m | How long one handler execution may take |

**Worst-case recovery latency** from a worker's death to its task being
claimable again is bounded by `LOST after` (25s) plus one reaper interval, or by
the lease duration plus one reaper interval, whichever comes first. In practice
the worker-lost path is faster, because a lost worker's leases are released
immediately rather than serving out their remainder.

Three constraints are enforced by `config.Timing.Validate` rather than left to
documentation:

- The renewal interval must be at most half the lease duration. A single failed
  renewal must not cost a worker its task.
- The SUSPECT threshold must be at least 2 beats. **One missed heartbeat never
  means failure** — a GC pause or a scheduling hiccup is common, and reclaiming
  work from a healthy worker turns a hiccup into duplicate execution.
- The LOST threshold must exceed the SUSPECT threshold.

**The lease does not bound how long a task may take.** It is a claim the
renewal loop keeps alive; the step's timeout bounds the work. Conflating the two
was a real bug, found by the process-level crash suite and recorded in
`docs/decisions/0004`.

*Tested by:* `TestWorkerBecomesSuspectBeforeLost`,
`TestLostWorkerReleasesItsLeasesImmediately`,
`TestLongTaskSurvivesManyLeasePeriods`, `TestTimingValidate`.

---

## Cancellation

**Cancelling a run stops everything that has not started. A task already
executing is not interrupted.**

The coordinator has no channel to a worker's goroutine, and inventing one would
give a guarantee that only holds while the worker is reachable — which is when
it is least likely to be. Instead the task's lease is expired, the worker
discovers the loss when it tries to record its outcome, and its result is
discarded.

**Consequence:** a cancelled run may still have one handler running, for up to
its remaining timeout. Its side effects still happen.

*Tested by:* `TestCancelRunEndsEveryUnstartedTask`,
`TestCancelledRunsInFlightTaskIsNotRequeued`.

---

## Coordinator restart

**A coordinator holds no state in memory, so there is nothing to resume.**

Recovery is a sweep over database rows. Any number of coordinators may run at
once — the sweep's queries take row locks with `SKIP LOCKED`, so they divide the
work rather than duplicating it — and one that restarts simply sweeps again.

*Tested by:* `TestCoordinatorRestartResumesFromTheDatabase`, which discards the
engine mid-run and builds a new one against the same database.

---

## Metrics, and what to conclude from them

| Metric | Reading |
|---|---|
| `duplicate_executions_total` | Should always be 0. Non-zero is a scheduler bug, not load. |
| `task_lease_expirations_total` | Rising means workers are dying or overloaded. |
| `worker_lost_total` | Compare against deploys; a spike without one is a real problem. |
| `side_effects_skipped_total` | Non-zero is the ledger working, not a fault. |
| `task_retries_total` | Rising with no lease expirations means handlers are failing. |
| `queue_depth` | Rising means the pool is not keeping up. |
| `recovery_time_seconds` | Read the p95, not the mean. |

---

## What ReLab is not

- **Not a Temporal replacement.** No durable timers, no signals, no queries, no
  child workflows, no versioned workflow code with deterministic replay of
  handler logic.
- **Not exactly-once.** See above.
- **Not multi-region.** One PostgreSQL database is the whole system.
- **Not authenticated.** v1 has no authentication or authorisation. See
  `SECURITY.md`.
- **`queue-overload` is not implemented.** It is named in the code and rejected
  at scenario-parse time, so no scenario can silently run without it.
- **Not a general-purpose task queue.** Throughput is bounded by one Postgres
  instance; see `docs/benchmarks.md` for measured numbers.
