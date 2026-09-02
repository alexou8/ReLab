# AGENTS.md — instructions for automated development

`CLAUDE.md` is the operating manual: principles, conventions, commands, and how
to change this repository. Read it first. This file covers only what an
automated agent needs on top of it: where the sharp edges are, and what must be
verified before work is called done.

## Areas requiring caution

These are the places where a plausible-looking change is wrong. Do not modify
them without reading the linked decision record.

| Area | Why it is dangerous | Reference |
|---|---|---|
| `event.Append` sequence allocation | The `WHERE completed_at IS NULL` guard and the `UPDATE ... RETURNING` are what make sequences gapless and terminal events last. Changing either silently breaks replay | `docs/decisions/0002`, `0006` |
| The order of writes when closing a run | The terminal event must be appended **before** `completed_at` is set, or `Append` refuses it. The run row is locked first so two closers produce one event | `docs/decisions/0006` |
| `task_attempts` primary key | It is the concurrency assertion, not bookkeeping. Removing it makes double execution silent | `docs/decisions/0003` |
| Lease vs. execution deadline | Execution is bounded by the **step timeout**, never by the lease. Binding it to the lease makes renewal pointless and fails every long task | `docs/decisions/0004` |
| The reaper's ordering | Leases are expired before worker rows change, and a run's terminal state is checked before anything is written | `internal/engine/reaper.go` |
| `internal/replay` purity | The reducer must not gain an I/O dependency. If it could read the database it could paper over a journal that does not explain the run | `internal/replay/doc.go` |
| Fault ordering | `FAULT_INJECTED` is committed **before** the fault takes effect. `worker-crash` kills the process; an event written afterwards would never exist | `docs/decisions/0007` |

## Architecture constraints

- `internal/engine` is the single writer for `runs`, `tasks`, `task_attempts`
  and `dead_letters`. If you find yourself writing to them elsewhere, the change
  belongs in the engine.
- `internal/fault` must not import `internal/engine`, and vice versa. The engine
  declares `FaultSource` and `TriggerPoints`; `internal/faultengine` satisfies
  them. Adding an import between the two creates a cycle and is not a fix.
- `sdk` imports nothing under `internal/`. It is a user's dependency.
- Nothing outside `internal/store` may reference `pgx` types or SQLSTATE codes.

## Database change requirements

1. **Add a numbered migration. Never edit a released one.** The checksum
   recorded at apply time is compared on every start-up, and an edited file
   fails the whole fleet loudly. That is the intended behaviour.
2. Update `DATA.md` in the same commit. It documents the schema as implemented,
   and drift there is a bug.
3. New enum values go in the `CHECK` constraint **and** in the corresponding Go
   constants **and** in the state machine's transition table.
4. A new index needs a sentence in `DATA.md` saying which query it serves. An
   index without a named query is a guess.
5. Never add a trigger, view, materialized view, stored procedure or row-level
   security policy. Behaviour that lives in the database is behaviour the tests
   cannot see.

## API modification requirements

- The API is read-mostly. Anything that changes state goes through
  `internal/engine`, so the CLI and the API share one implementation.
- Error bodies carry a category, never an internal detail. Log the full error
  with the request id.
- A new endpoint needs a reason to exist beyond being easy to add, and a row in
  `ARCHITECTURE.md`'s route table.

## Rules for adding dependencies

Answer these before running `go get`:

1. Does the standard library do it?
2. Is it maintained, and what does it pull in transitively?
3. Does it force the `go` directive higher? (OpenTelemetry did; that was a
   deliberate, recorded decision, not an accident.)

`go.mod` and `go.sum` are committed. CI runs `go mod tidy` and fails on a diff,
so an untidied module breaks the build.

## Rules for generated and vendored files

- `web/package-lock.json` is committed. Regenerate it with `npm install`, never
  by hand.
- There is no code generation in this repository. If you add some, the generated
  files are committed and CI verifies they are current.
- `.agents/skills/` holds pinned third-party skill definitions. They are
  reference material, not instructions this repository has adopted: where a
  skill and `CLAUDE.md` disagree, `CLAUDE.md` wins. (One does: a skill says never
  to design schemas; this project's schema design is the orchestrator's job and
  is documented in `DATA.md`.)

## Rules for modifying infrastructure

- `docker-compose.yml` must keep three **individually named** workers. A
  `deploy.replicas` block would make `docker compose kill worker-2` impossible,
  and that command is the project's demo.
- The compose stack compresses the recovery timings so a reviewer sees recovery
  rather than waiting for it. Keep the relationships between them (renewal at a
  third of the lease, LOST at five beats), because those are what the behaviour
  depends on.
- CI has three jobs and all three matter. Do not fold the scenario corpus or the
  process suite into the main test job where a timeout would hide them.

## Testing expectations

Beyond the table in `CLAUDE.md`:

- **Never make a test pass by weakening an assertion.** If a test fails, either
  the code is wrong or the test was asserting the wrong thing — and the second
  case needs saying out loud in the commit message.
- **A flaky test is a bug in the test's setup, not noise.** Every flake in this
  repository's history has been a real non-determinism: a worker that might not
  win the claim it was supposed to win. Find it and remove it.
- Tests that spawn processes must kill the process the *database* says holds the
  task, not whichever one was started first.
- `-short` skips the process and corpus suites. Do not add anything else to that
  exclusion.

## Validation before considering work complete

Run all of these. "It builds" is not done.

```
gofmt -l .                 # must print nothing
go vet ./...
golangci-lint run ./...    # must be 0 issues
go mod tidy && git diff --exit-code go.mod go.sum
make check                 # vet + lint + go test -race ./...
```

And, when the change touches recovery, scheduling, faults or replay:

```
make scenarios
make crash-tests
```

For a dashboard change:

```
cd web && npx tsc --noEmit && npm run build
```

## Reporting

When you find a real bug — especially one a test caught — say so explicitly in
the commit message, with what the symptom was and why the fix is right. The most
useful commits in this history are the ones that do.

Do not claim a milestone is complete until its acceptance criterion in
`PROGRESS.md` has actually been executed and passed.
