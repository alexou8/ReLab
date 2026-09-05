# Contributing to ReLab

Thank you for looking. This document is what you need to make a change land:
how to get the tests running, what the review will ask about, and the few rules
that exist because breaking them would break the project's central claim.

## What ReLab is, in one paragraph

ReLab runs multi-step workflows across worker processes, records an append-only
event history, replays that history to reconstruct run state, injects
deterministic faults, and asserts that recovery behaved correctly. It is a
reliability testing and replay tool for development, staging and CI — **not** a
production workflow engine, and not a Temporal replacement. A change that moves
it towards being one needs that conversation first, in an issue.

## Getting a working checkout

Go 1.25+ and PostgreSQL 16+, or Docker.

```bash
git clone https://github.com/alexou8/relab
cd relab
make build

# The tests that touch the database need one they may create and drop
# databases on. Without this they skip rather than fail.
export RELAB_TEST_DSN='postgres://relab:relab@localhost:5432/postgres?sslmode=disable'
make check
```

`docker compose up --build -d` brings up PostgreSQL, the API and three named
workers if you would rather not install anything. The workers are named
individually on purpose: `docker compose kill worker-2` is the project's demo.

The dashboard lives in `web/` and is a separate Node project:

```bash
cd web && npm install && npm run dev
```

With no `RELAB_API_URL` set it serves a recording of real runs, so you can work
on it without a control plane running.

## The gate

Run all of this before opening a pull request. "It builds" is not done.

```bash
gofmt -l .                  # must print nothing
go vet ./...
golangci-lint run ./...     # must be 0 issues
go mod tidy && git diff --exit-code go.mod go.sum
make check                  # vet + lint + go test -race ./...
```

If the change touches recovery, scheduling, faults or replay:

```bash
make scenarios              # the fault scenario corpus
make crash-tests            # the process-level SIGKILL suite
```

For a dashboard change:

```bash
cd web && npx tsc --noEmit && npm run build
```

CI runs the same three groups as separate jobs. They are separate so that a
timeout in one cannot hide the others.

## What review will ask about

The project has one claim — that a run's recorded history is a faithful account
of what happened — and every rule below protects it. `CLAUDE.md` is the full
operating manual; these are the parts that most often come up.

- **A state change and the events describing it go in one transaction.** That is
  why there is one datastore.
- **Sequence numbers are gapless, and a run's terminal event is its last.** Both
  are enforced in `event.Append`. Changing either silently breaks replay.
- **`internal/replay` has no I/O dependency.** If the reducer could read the
  database, it could paper over a journal that does not explain the run.
- **`internal/engine` is the single writer** for runs, tasks, attempts and dead
  letters.
- **Faults are real degradations where they can be.** A fault the scheduler
  knows about is one the code can be written to survive without the recovery
  path ever running.
- **Guarantees are stated precisely.** At-least-once, and an effect recorded
  under a key is performed at most once *after it has been recorded*. Not
  "idempotent", and never "exactly-once".
- **Never make a test pass by weakening an assertion.** If a test fails, either
  the code is wrong or the test asserted the wrong thing — and the second case
  needs saying out loud in the commit message.
- **A flaky test is a bug in the test.** Every flake in this repository's history
  has been a real non-determinism, usually a worker that might not win the claim
  it was supposed to win.

New behaviour comes with tests that match what it claims: a state transition
needs its legal and illegal cases, anything touching leases or the reaper needs
a test against real PostgreSQL, and anything about crash recovery needs a test
in `test/process/` that kills a real binary. A new event type needs a reducer
case and an entry in the exhaustiveness test; a new fault type needs a scenario
in `examples/scenarios/`, which CI discovers automatically.

Database changes add a numbered migration and never edit a released one — the
checksum recorded at apply time is compared on every start-up, and an edited
file fails the whole fleet loudly. `DATA.md` is updated in the same commit.

## Commits and pull requests

Commit messages explain **why**, in prose, in the body: what was decided, and
what was rejected. When a test finds a real bug, say so, with the symptom and
why the fix is right. The most useful commits in this history are the ones that
do.

A pull request should be one cohesive change. Update the documentation in the
same change as the behaviour — `DATA.md`, `ARCHITECTURE.md`, `SECURITY.md` and
`docs/reliability.md` are the product's argument, and drift in them is worse
here than in most projects.

## Reporting a security issue

Not in a public issue. See [SECURITY.md](SECURITY.md).

## Filing an issue

Bug reports are most useful with the run's journal: `relab run inspect <run-id>`
for the timeline, or `relab export <run-id>` for the whole run as JSON. The event history is usually enough to
say what happened without a reproduction. The issue templates ask for it.

## Licence

Contributions are accepted under the [Apache License 2.0](LICENSE), the same
licence as the project.
