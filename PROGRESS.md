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
