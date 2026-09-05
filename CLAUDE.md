# CLAUDE.md — operating manual for this repository

## What this project is

ReLab runs multi-step workflows across worker processes, records an append-only
event history, replays that history to reconstruct run state, injects
deterministic faults, and asserts that recovery behaves correctly.

**It is a reliability testing and replay tool. It is not a Temporal
replacement.** Say so anywhere the distinction could be missed.

## The one idea everything follows from

The project's claim is that a run's recorded history is a faithful account of
what happened. Every design decision follows from protecting that claim:

- A state change and the events describing it are written in **one transaction**,
  which is why there is one datastore and not two.
- Sequence numbers are **gapless**, so a gap means data loss rather than a
  routine abort.
- A run's **terminal event is its last**, so a finished run's story cannot
  change.
- The reducer is **pure**, so replay cannot consult something other than the
  journal to make its answer look right.
- Faults are **real degradations**, so tests exercise the recovery path rather
  than a simulation of it.

If a change would weaken any of these, it needs a decision record explaining
why, not a comment.

## Engineering principles

1. **Prefer being caught to being careful.** A constraint the database enforces
   beats a rule everyone remembers. The primary key on `task_attempts` is the
   model.
2. **Fail loudly on what you do not understand.** An unknown event type, an
   unknown payload version, a gap: all errors. A permissive reader produces a
   plausible wrong answer, which is worse than no answer.
3. **State guarantees precisely.** "At-least-once, and an effect recorded under
   a key is performed at most once *after it has been recorded*" — not
   "idempotent".
4. **No mock-only reliability claims.** If it is about recovery, the test uses a
   real PostgreSQL and, where it is about crashes, a real process and a real
   `SIGKILL`.
5. **Measure, do not claim.** Every number in the README came from `relab bench`
   on stated hardware.
6. **Simplicity over cleverness.** A duplicated column list that `scanTask` will
   catch beats a string-surgery helper that derives it.

## Repository conventions

### Layout

See `ARCHITECTURE.md`. The rules that matter:

- `internal/engine` is the **single writer** for run and task state.
- `internal/replay` has **no I/O dependency**. Keep it that way; the compiler is
  the enforcement.
- `internal/fault` and `internal/engine` must not import each other.
  `internal/faultengine` bridges them; that is the only reason it exists.
- `sdk` imports nothing internal.

### Comments

Comments explain **why**, never what. A comment restating the code is noise; a
comment explaining why a lock is taken, why a guard exists, or what breaks
without it is the reason the next person does not remove it.

Do not add a comment on every function. Add one where a reader would otherwise
ask a question.

### Errors

- Wrap with `%w` and context: `fmt.Errorf("engine: claim tasks for worker %s: %w", id, err)`.
- Lowercase, no trailing punctuation.
- Match with `errors.Is` / `errors.As`, never a type assertion or string compare.
- **Either log an error or return it, never both.**
- A deliberate best-effort call is written `_ = f()` so the decision is visible
  in review. `errcheck`'s `check-blank` is off for that reason.
- An illegal state transition returns an error. It never panics: a bug in one
  run must not take down a process recovering others.

### SQL

- Every query parameterised. No exceptions, no query builder, no ORM.
- Hand-written, in the package that owns the table.
- `defer rows.Close()` immediately after `Query`, and always check `rows.Err()`.
- Use `Exec` for statements that return no rows; `Query` leaks a connection if
  the caller forgets to close.
- Driver errors are translated by `store.Classify` into typed sentinels. No
  package outside `internal/store` should know what driver is underneath.

### Tests

- Table-driven where the cases are uniform.
- **Failure messages state what the test is protecting**, not just what differed.
  `"the fan-in barrier released early"` beats `"got 2, want 1"`.
- Tests that need a database call `testsupport.DB(t)`, which creates a fresh one
  per test. They skip when `RELAB_TEST_DSN` is unset.
- A test that relies on timing must not rely on *luck*. If two workers could
  both win a claim, start one, wait for the state that proves it won, then start
  the other. Two tests have already had to be fixed for exactly this.

## Development workflow

```
make build        # bin/relab
make check        # vet + lint + the full test suite. The gate.
make test         # go test -race ./...
make test-unit    # only the tests that need no database
make scenarios    # the fault scenario corpus
make crash-tests  # the process-level SIGKILL suite
make up / down    # the full compose stack
```

`make check` must be green before anything is committed.

Set `RELAB_TEST_DSN` to a PostgreSQL instance the tests may create and drop
databases on. Without it, the database tests skip rather than fail.

## Testing requirements

| Change | Requires |
|---|---|
| A state transition | Unit tests for the legal and the illegal cases |
| Anything touching leases or the reaper | An integration test against real PostgreSQL |
| Anything about crash recovery | A test in `test/process/` that kills a real binary |
| A new event type | A reducer case, and an entry in the exhaustiveness test |
| A new fault type | A scenario in `examples/scenarios/`, which is auto-discovered by CI |
| A schema change | A new migration. **Never edit a released one** — the checksum check will fail |

## Security requirements

- Never log a credential. The DSN is redacted from driver errors; keep it that
  way.
- Every query parameterised.
- API error bodies carry a category, never an internal detail.
- The API takes bearer tokens (`RELAB_API_TOKENS`, roles `viewer` and
  `operator`) and refuses to serve unauthenticated on a non-loopback address
  unless `RELAB_INSECURE_NO_AUTH=true` says so out loud. There are no user
  accounts, no sessions, and no per-token audit trail; do not add a feature that
  assumes any of those exist.
- **This repository is public.** No credential, token, or `auth.json` belongs in
  a tracked file, a prompt, an issue, a log, or CI output. Agent tooling signs
  in through the user's private `~/.codex`; see AGENTS.md, "Secret handling".

See `SECURITY.md` for the threat model and the residual risks.

## Coding standards

- `gofmt` and `goimports` with `-local github.com/alexou8/relab`.
- Exported identifiers have doc comments; the `revive` `exported` rule is
  deliberately off, because a mandatory comment on each of twenty-one trivial
  `Type()` methods trains readers to skip doc comments.
- Contexts are the first parameter and are actually propagated.
- Long-lived goroutines take a context and return when it ends.

## Naming

- Packages: one word, no `util`, no `common`, no `helpers`.
- Sentinel errors: `ErrLeaseLost`, `ErrRunClosed`.
- Error types: `ConstraintError`, `IllegalTransitionError`.
- Test helpers on the fixture, not free functions, where there is state.

## Dependency rules

Before adding one, answer: does the standard library do this? Is it maintained?
What does it pull in? The current direct set is seven and is listed in
`SECURITY.md`.

`go.mod` and `go.sum` are committed and CI fails if `go mod tidy` changes them.

## Git and commits

- Commit messages explain **why**, in prose, in the body. State what was
  decided and what was rejected.
- When a test finds a real bug, say so in the message. Several commits here do,
  and they are the most useful ones in the history.
- Update `PROGRESS.md` when a milestone completes.

## Documentation

`docs/reliability.md` is the authority on guarantees. If it and any other
document disagree, it is right and the other is a bug.

When implementation changes, check whether `DATA.md`, `ARCHITECTURE.md`,
`SECURITY.md` or `docs/reliability.md` is affected, and fix it in the same
commit. Documentation drift here is worse than in most projects, because the
documents are the product's argument.

## Multi-agent development

Claude Opus owns scope, product, and frontend; GPT-5.6 Sol co-orchestrates
backend engineering through the Codex plugin; bounded Luna subagents do the
narrow work. One writer per file or subsystem at a time. The full split, the
task-packet rules, and the secret-handling rules are in AGENTS.md; the agent
definitions are in `.codex/`.

## When modifying this repository

1. Read `docs/reliability.md` first. Most changes touch something it promises.
2. Do not weaken an invariant to make a test pass. The invariants are the
   product.
3. If a test is flaky, find the race. Every flake found so far has been a real
   non-determinism in the test's setup, not noise.
4. Prefer adding a constraint over adding a check.
5. Run `make check` before claiming anything works.
