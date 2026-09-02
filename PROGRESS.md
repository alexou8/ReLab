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
