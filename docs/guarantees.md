# Guarantee matrix

Every reliability claim ReLab makes in public, and the automated test that
proves it. If a row has no test, it says so rather than being left out — the
gaps at the bottom are the honest part of this document.

`docs/reliability.md` is the authority on what each guarantee *means*, including
its limits. This file answers a narrower question: what would fail if the
guarantee stopped holding.

Where a test needs a real PostgreSQL it is marked **[pg]**; where it spawns a
real binary and kills it, **[proc]**. Tests without a mark need neither.

## Delivery and recovery

| Claim | Proved by | Where | Run with |
|---|---|---|---|
| A task whose worker is `SIGKILL`ed is retried and the run still succeeds | `TestSIGKILLedWorkerLosesItsTaskAndTheRunStillSucceeds` **[pg, proc]** | `test/process/crash_test.go` | `make crash-tests` |
| An expired lease returns the task to the queue | `TestLeaseExpiryRequeuesTheTask` **[pg]** | `internal/engine/lease_test.go` | `make check` |
| A killed worker is declared `LOST` without reporting anything | `TestKilledWorkerIsDeclaredLost` **[pg, proc]** | `test/process/crash_test.go` | `make crash-tests` |
| A failing handler is retried, then dead-lettered, and the run fails | `TestFailingTaskRetriesThenDeadLettersAndFailsTheRun` **[pg]** | `internal/engine/engine_test.go` | `make check` |

## Side effects

| Claim | Proved by | Where | Run with |
|---|---|---|---|
| An effect recorded under a key survives a crash before acknowledgement, and the retry does not repeat it | `TestEffectSurvivesACrashBeforeAcknowledgement` **[pg, proc]** | `test/process/idempotency_test.go` | `make crash-tests` |
| Concurrent callers under one key leave one record and one result | `TestDoUnderConcurrencyPerformsTheEffectOnce` **[pg]** | `internal/idem/idem_test.go` | `make check` |
| A failed effect is not recorded, so it can be retried | `TestFailedEffectIsNotRecorded` **[pg]** | `internal/idem/idem_test.go` | `make check` |

Note what the second row does *not* say. It proves one surviving record and one
result, not that the effect body ran once: the window between performing an
effect and recording it is real, and `docs/reliability.md` describes it. This is
why the project says at-least-once and never exactly-once.

## Concurrent execution

| Claim | Proved by | Where | Run with |
|---|---|---|---|
| Two workers never execute the same *attempt* of a task | `TestConcurrentAttemptIsRefused`, `TestThreeWorkersProcessAFanOutWithoutDoubleExecution` **[pg]** | `internal/engine/lease_test.go` | `make check` |
| A recovered run's journal contains no duplicate attempt | `assertNoDuplicateAttempt`, in `TestSIGKILLedWorkerLosesItsTaskAndTheRunStillSucceeds` and `TestEffectSurvivesACrashBeforeAcknowledgement` **[pg, proc]** | `test/process/` | `make crash-tests` |

The enforcement is the primary key on `task_attempts (task_id, attempt)`, not
timing. Two workers *may* run the same task under different attempt numbers when
a lease expires while the first is still going; that is the at-least-once
duplicate the idempotency ledger exists for.

## The event journal

| Claim | Proved by | Where | Run with |
|---|---|---|---|
| Sequence numbers are gapless under concurrent appends | `TestAppendConcurrentSeqIsGapless` (50 concurrent appends to one run) **[pg]** | `internal/event/log_test.go` | `make check` |
| A rolled-back append leaves no gap | `TestAppendRollbackReleasesSeq` **[pg]** | `internal/event/log_test.go` | `make check` |
| A run's terminal event is its last | `TestAppendRefusesAClosedRun` **[pg]** | `internal/event/log_test.go` | `make check` |
| A corrupt or unknown-version event is an error, not a guess | `TestCorruptedEventProducesACategoryNotACrash` **[pg]** | `internal/replay/integration_test.go` | `make check` |

## Replay

| Claim | Proved by | Where | Run with |
|---|---|---|---|
| Replaying a real run's journal reconstructs its state | `TestReplayOfRecordedRunsMatches` (ten runs, three with recovery) **[pg]** | `internal/replay/integration_test.go` | `make check` |
| The reducer is deterministic | `TestReduceIsDeterministic` | `internal/replay/reduce_test.go` | `make test-unit` |

The reducer has no I/O dependency, which the compiler enforces: `internal/replay`
imports nothing that could reach the database.

## Recovery timing

| Claim | Proved by | Where | Run with |
|---|---|---|---|
| A worker is `SUSPECT` before it is `LOST` | `TestWorkerBecomesSuspectBeforeLost` **[pg]** | `internal/engine/lease_test.go` | `make check` |
| A `LOST` worker's leases are released immediately | `TestLostWorkerReleasesItsLeasesImmediately` **[pg]** | `internal/engine/lease_test.go` | `make check` |
| The lease does not bound how long a task may take | `TestLongTaskSurvivesManyLeasePeriods` **[pg, proc]** | `test/process/crash_test.go` | `make crash-tests` |
| One missed heartbeat never means failure; renewal is at most half the lease | `TestTimingValidate` | `internal/config/config_test.go` | `make test-unit` |
| A clean shutdown does not strand in-flight work | `TestGracefulShutdownDoesNotStrandWork` **[pg, proc]** | `test/process/crash_test.go` | `make crash-tests` |

## Cancellation and restart

| Claim | Proved by | Where | Run with |
|---|---|---|---|
| Cancelling a run ends every unstarted task | `TestCancelRunEndsEveryUnstartedTask` **[pg]** | `internal/engine/cancel_test.go` | `make check` |
| A cancelled run's in-flight task is not requeued | `TestCancelledRunsInFlightTaskIsNotRequeued` **[pg]** | `internal/engine/cancel_test.go` | `make check` |
| A restarted coordinator resumes from the database | `TestCoordinatorRestartResumesFromTheDatabase` **[pg]** | `internal/engine/cancel_test.go` | `make check` |

## Fault scenarios

Every file in `examples/scenarios/` is run by `make scenarios`, and CI discovers
them automatically: adding a scenario adds a test. A scenario asserts the run's
final state, lost tasks, duplicate effects, recovery time and that the fault was
actually injected — `faults_injected` exists so a scenario cannot pass because
nothing happened.

## What is not proved by a named test

Stated here because a matrix that only listed its wins would be an advertisement.

- **The published recovery-latency bound.** The worst case in
  `docs/reliability.md` — `LOST after` plus one reaper interval — is derived from
  the defaults and observed in the scenario corpus, not asserted by a test that
  fails if the bound is exceeded.
- **Cancellation of a *running* handler.** The cancellation tests do not execute
  a live handler, so what a handler already inside its work does with a
  cancelled run is not covered.
- **Two coordinators dividing work.** The restart test proves recovery from the
  database, not that concurrent coordinators partition claims correctly under
  `SKIP LOCKED`.
- **`queue-overload`.** Named in the code and deliberately not implemented; a
  scenario using it is rejected rather than run without it.
- **Anything under sustained load.** There is no soak test, and no long-running
  result to point at.

These are the next things worth testing, in roughly that order.
