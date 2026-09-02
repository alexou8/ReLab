# Progress

One line per completed task, in the format
`<milestone>.<task> <what> | <who> | <status> | <package>`.

A fresh session can resume from this file without replaying the conversation
that produced it.

## M0 — Foundation

```
M0.1 go module, Makefile, golangci-lint, .gitignore   | orchestrator | done | (root)
M0.2 docker-compose (postgres) and CI workflow        | orchestrator | done | (root)
M0.3 schema migration 001, all tables and indexes     | orchestrator | done | internal/store/migrations
M0.4 migration runner, checksums, advisory lock       | orchestrator | done | internal/store
M0.5 connection pool, InTx, typed errors, DSN redact  | orchestrator | done | internal/store
M0.6 event types, versioned payloads, Append, Read    | orchestrator | done | internal/event
M0.7 per-test database fixture                        | orchestrator | done | internal/testsupport
M0.8 config: timings, validation, env resolution      | orchestrator | done | internal/config
M0.9 relab binary skeleton, `relab migrate`           | orchestrator | done | cmd/relab, internal/cli
M0.A decision records 0001-0003                       | orchestrator | done | docs/decisions
```

**Acceptance:** `make check` green. `go test ./internal/event` proves sequence
gaplessness under 50 concurrent appends to one run
(`TestAppendConcurrentSeqIsGapless`), and proves a rolled back append does not
leave a gap (`TestAppendRollbackReleasesSeq`).

## M1 — Single-node engine

```
M1.1 workflow YAML parse, canonical hash, Duration type | orchestrator | done | internal/workflow
M1.2 DAG validation: cycles, dups, unknown handlers     | orchestrator | done | internal/workflow
M1.3 SDK: Registry, Handler, TaskContext, Permanent     | orchestrator | done | sdk
M1.4 run/task state machines, legal + illegal           | orchestrator | done | internal/engine
M1.5 workflow registration, idempotent + hash-guarded   | orchestrator | done | internal/engine
M1.6 CreateRun: run, tasks and events in one txn        | orchestrator | done | internal/engine
M1.7 claim (SKIP LOCKED), start, complete, settle       | orchestrator | done | internal/engine
M1.8 retry backoff with positional jitter               | orchestrator | done | internal/retry
M1.9 dependency unlock, fan-in barrier, abandon         | orchestrator | done | internal/engine
M1.A executor: timeouts, panics, artifacts, ledger      | orchestrator | done | internal/engine
M1.B local runner with stall detection                  | orchestrator | done | internal/engine
M1.C CLI: workflow validate|register|list, run, runs,   | orchestrator | done | internal/cli
     run inspect
M1.D scenario loader, fault types and trigger points    | orchestrator | done | internal/fault
M1.E example workflows and handlers                     | orchestrator | done | examples, internal/examples
```

**Acceptance:** `relab run examples/data-pipeline.yaml` completes, and
`relab run inspect <id>` shows an ordered 20-event timeline ending in
`RUN_SUCCEEDED`. `examples/fan-out.yaml` exercises fan-out and the fan-in
barrier; `TestFanInWaitsForEveryDependency` asserts the barrier releases
exactly once and only after both branches succeed.

## M2 — Worker pool and leases

```
M2.1 reaper: lease expiry, requeue, dead-letter      | orchestrator | done | internal/engine
M2.2 worker liveness sweep, SUSPECT then LOST        | orchestrator | done | internal/engine
M2.3 lease renewal returning the ids it kept         | orchestrator | done | internal/engine
M2.4 worker process: claim, heartbeat, renew loops   | orchestrator | done | internal/worker
M2.5 coordinator: stateless recovery sweep on a timer| orchestrator | done | internal/engine
M2.6 HTTP API: runs, tasks, events, workers, stats   | orchestrator | done | internal/api
M2.7 CLI: server, worker, workers                    | orchestrator | done | internal/cli
M2.8 timing overridable from the environment         | orchestrator | done | internal/config
M2.9 process-level SIGKILL tests with real binaries  | orchestrator | done | test/process
M2.A Dockerfile and the three-worker compose stack   | orchestrator | done | (root)
M2.B decision 0004: lease renewal vs execution bound | orchestrator | done | docs/decisions
```

**Acceptance:** `TestThreeWorkersProcessAFanOutWithoutDoubleExecution` runs a
21-task fan-out across three workers and asserts every task recorded exactly one
attempt, with no `(task, attempt)` pair started twice.
`TestSIGKILLedWorkerLosesItsTaskAndTheRunStillSucceeds` spawns a real worker
binary, waits until the database says it holds the running task, `SIGKILL`s that
exact process, and asserts the run still succeeds via `TASK_LEASE_EXPIRED` and
`TASK_REQUEUED`.

## M3 — Reliability core

```
M3.1 idempotency ledger extracted, jsonb-consistent  | orchestrator | done | internal/idem
M3.2 SIDE_EFFECT_SKIPPED emitted on suppression      | orchestrator | done | internal/engine
M3.3 cancellation: unstarted DEAD, in-flight revoked | orchestrator | done | internal/engine
M3.4 reaper does not restart a finished run's tasks  | orchestrator | done | internal/engine
M3.5 step timeouts fail the attempt                  | orchestrator | done | internal/engine
M3.6 coordinator restart resumes from the database   | orchestrator | done | internal/engine
M3.7 `relab run cancel`                              | orchestrator | done | internal/cli
M3.8 crash-before-ack acceptance test                | orchestrator | done | test/process
M3.9 decision 0005: what the ledger guarantees       | orchestrator | done | docs/decisions
```

Retry backoff (`internal/retry`) and the dead-letter queue landed in M1, where
recording a failure first needed them.

**Acceptance:** `TestEffectSurvivesACrashBeforeAcknowledgement` runs a handler
that performs a recorded side effect and then `SIGKILL`s its own process before
the outcome can be acknowledged. The task is recovered by lease expiry and
retried; the ledger holds exactly one effect, the journal carries
`SIDE_EFFECT_SKIPPED`, and the recorded result is still the first attempt's.

## M4 — Replay

```
M4.1 RunState and TaskState, comparison-relevant only | orchestrator | done | internal/replay
M4.2 pure reducer, exhaustive over every event type   | orchestrator | done | internal/replay
M4.3 divergence categories and comparison             | orchestrator | done | internal/replay
M4.4 artifact verification against the artifacts table| orchestrator | done | internal/replay
M4.5 `relab replay [--diff]` with a non-zero exit     | orchestrator | done | internal/cli
M4.6 terminal-event-is-last enforced in event.Append  | orchestrator | done | internal/event
M4.7 decision 0006                                    | orchestrator | done | docs/decisions
```

**Acceptance:** `TestReplayOfRecordedRunsMatches` records ten runs, three of
which recover from a lost worker, and replays every one to a state that matches
its re-reduction and to artifact hashes that agree with the artifacts table.
`TestCorruptedEventProducesACategoryNotACrash` corrupts a journal four different
ways — a deleted event, an unknown type, a stripped payload version, a future
payload version — and asserts each produces a specific, named failure rather
than a panic or a plausible-looking wrong answer.

## M5 — Fault lab and test runner

```
M5.1 injector: trigger points, targets, seeded draws  | orchestrator | done | internal/fault
M5.2 all six v1 fault types                           | orchestrator | done | internal/fault
M5.3 FAULT_INJECTED recorded before the fault fires   | orchestrator | done | internal/faultengine
M5.4 trigger points wired into the executor           | orchestrator | done | internal/engine
M5.5 assert package, answered from the journal        | orchestrator | done | internal/assert
M5.6 `relab test`, exit codes, --json, --repeat       | orchestrator | done | internal/cli
M5.7 supervised worker pool for crash scenarios       | orchestrator | done | internal/cli
M5.8 `relab worker --scenario`                        | orchestrator | done | internal/cli
M5.9 scenario corpus, discovered from the directory   | orchestrator | done | examples/scenarios
M5.A CI jobs for the corpus and the crash suite       | orchestrator | done | .github/workflows
M5.B decision 0007                                    | orchestrator | done | docs/decisions
```

**Acceptance:** `relab test examples/data-pipeline.yaml --scenario
examples/scenarios/worker-crash.yaml --repeat 20` passed 20 of 20 with no
failures. Deliberately breaking the reaper — making `expireLeases` look for
leases that expired 24 hours ago — makes the same command exit 1 with the run
never completing, because a SIGKILLed worker's task has no other way back.
`TestScenarioCorpus` runs all five scenarios and checks that the command's exit
code agrees with its report, so a CI step cannot pass on a failing scenario.
