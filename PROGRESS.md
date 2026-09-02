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
M5.2 five injectable fault types                      | orchestrator | done | internal/fault
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

## M6 — Observability, benchmarks, dashboard, documentation

```
M6.1 OTel traces, metrics, trace-correlated logs      | orchestrator | done | internal/telemetry
M6.2 executor spans and the reliability metric set    | orchestrator | done | internal/engine
M6.3 benchmark harness: percentiles, CSV, environment | orchestrator | done | internal/bench
M6.4 `relab bench` and the matrix                     | orchestrator | done | internal/cli
M6.5 connection pool sizing (RELAB_DB_MAX_CONNS)      | orchestrator | done | internal/store, internal/cli
M6.6 read-only Next.js dashboard, 4 views             | orchestrator | done | web/
M6.7 measured benchmark matrix, committed CSV         | orchestrator | done | docs/data
M6.8 README, ARCHITECTURE, DATA, SECURITY, PRD        | orchestrator | done | (root)
M6.9 CLAUDE.md, AGENTS.md, SKILLS.md                  | orchestrator | done | (root)
M6.A docs/reliability.md, docs/benchmarks.md          | orchestrator | done | docs/
```

**Acceptance:** `docker compose up` brings up PostgreSQL, the control plane,
three individually killable workers and the dashboard; `docker compose kill
worker-2` during a run produces a visible recovery in the timeline and in the
dashboard. The benchmark matrix (4 worker counts × 3 fault rates × 25 runs) was
measured and committed at `docs/data/benchmarks.csv`, with the hardware and
versions on every row. Every reliability claim in `docs/reliability.md` names a
test, and every named test was verified to exist.

## Final audit

An audit of the finished repository, per the master prompt's requirements. What
it found and what was done:

```
A.1 dead exports removed (ConstraintName, AppendAll,     | orchestrator | done | internal/store, internal/event,
    Item, sdk.Output, config.Database)                   |              |      | sdk, internal/config
A.2 duplicate-delivery was declared and never invoked;   | orchestrator | done | internal/engine, examples
    now implemented, with a corpus scenario proving      |              |      |
    the ledger suppresses the repeat                     |              |      |
A.3 queue-overload was declared and did nothing; now     | orchestrator | done | internal/fault
    rejected at parse time and documented as unshipped   |              |      |
A.4 FAULT_INJECTED now precedes the SIDE_EFFECT_SKIPPED  | orchestrator | done | internal/engine
    it causes; the journal had the effect before its     |              |      |
    cause                                                |              |      |
A.5 scenarios may name their workflow, so the corpus     | orchestrator | done | internal/fault, test/scenarios
    stays directory-discovered                           |              |      |
A.6 every test named in docs/reliability.md verified to  | orchestrator | done | (docs)
    exist; timing defaults, event types and API routes   |              |      |
    reconciled against the code                          |              |      |
```

**No TODOs, commented-out code, debug prints or unused module dependencies
remain in first-party code.**

---

## Public deployment

Making the dashboard deployable on its own, and fixing what that exposed.

```
D.1 `relab run --scenario` recorded the scenario's name    | orchestrator | done | internal/cli
    and injected none of its faults; runs were labelled    |              |      |
    with a scenario that never ran                         |              |      |
D.2 a worker that shut down cleanly was declared LOST      | orchestrator | done | internal/store, internal/engine,
    five beats later, so a redeploy read as a crash;       |              |      | internal/worker
    migration 002 adds STOPPED, written by the process     |              |      |
D.3 `relab test`'s pool and LocalRunner abandoned their    | orchestrator | done | internal/cli, internal/engine
    workers; the demo went from 7 lost workers to the 2    |              |      |
    the scenarios actually kill                            |              |      |
D.4 `/readyz` passed on a schema the binary did not carry  | orchestrator | done | internal/api, internal/store
D.5 internal/api had no tests; now covers read-only-ness,  | orchestrator | done | internal/api
    id validation, error-body leakage, limit capping and   |              |      |
    readiness                                              |              |      |
D.6 two scenarios added: a crash after a recorded side     | orchestrator | done | examples/scenarios
    effect, and an upstream that never recovers (the       |              |      |
    first corpus entry asserting a FAILED run)             |              |      |
D.7 `relab export` and scripts/record-demo.sh; the         | orchestrator | done | internal/cli, scripts, web
    dashboard serves that recording where no control       |              |      |
    plane is reachable, labelled as one                    |              |      |
D.8 run detail rewritten around a verdict, event-counted   | orchestrator | done | web
    evidence and a filterable timeline; accessibility      |              |      |
    and responsive work alongside                          |              |      |
D.9 docs/deployment.md written; SECURITY.md, ARCHITECTURE  | orchestrator | done | (docs)
    .md, DATA.md, README.md reconciled with it             |              |      |
```

**Not done:** the Vercel project itself. The connected account cannot create
projects, so the import is a manual step; every setting it needs is pinned in
`web/vercel.json` and written out in `docs/deployment.md` §3.
