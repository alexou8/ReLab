# ReLab

**Prove that your distributed workflows actually recover when things break.**

ReLab runs multi-step workflows across worker processes, records an append-only
event history, replays that history to reconstruct run state, injects
deterministic faults, and asserts that recovery behaves correctly.

**ReLab is a reliability testing and replay tool. It is not a Temporal
replacement.** It has no durable timers, no signals, no queries, and no
versioned workflow code. What it has is a way to kill a worker mid-task and get
a machine-checkable answer to "did that actually recover, and did it cost a
duplicate side effect?"

**[See it without installing anything → relabca.vercel.app](https://relabca.vercel.app)**

The dashboard runs there on its own, serving a recording of five real runs — two
of them with a worker killed by `SIGKILL` mid-task — labelled as a recording on
every page. It is an export of what actually happened, made by
`scripts/record-demo.sh`, not a fixture written to look plausible.

The run worth opening first is
[worker-crash-after-effect](https://relabca.vercel.app/runs/96ea5bfd-1536-42c2-ac1b-72b7101efeb7?events=recovery):
a worker killed after it had already charged a customer but before it could
acknowledge the task. Four events tell the whole story — the worker lost, the
lease expired, the task requeued, the charge suppressed — and the run succeeded
without charging twice.

---

## ReLab in one picture

```text
        your workflow
              |
              v
      +-------------------+
      |    ReLab engine   |     break it on purpose
      |                   |     ---------------------
      |  runs the tasks   |     worker crash (SIGKILL)
      |  across real      |     duplicate delivery
      |  worker processes |     latency spike
      |                   |     HTTP error
      +-------------------+     database disconnect
              |
              v
      the system recovers, or does not
              |
              v
      every state change and the event
      describing it, one transaction,
      gapless sequence
              |
              v
      replay: a pure reducer rebuilds the
      run from the journal alone
              |
              v
      assertions check the recovery
              |
              v
        RECOVERED / FAILED
```

Each stage is a real component: `internal/engine` writes the state and the
events together, `internal/fault` degrades the real system rather than
pretending to, `internal/replay` has no I/O dependency at all, and
`internal/assert` is what turns a journal into a verdict.

---

## The problem

Workflow engines get tested on the happy path. The failure paths are the ones
that cost money, and they are the hardest to exercise on purpose:

- A worker dies holding a task. Does the task come back?
- A lease expires while the worker is *still alive*. Do two workers now run it?
- A retry repeats a step that already charged a customer. Does it charge twice?
- The coordinator restarts with work in flight. Does the work resume?

"We handle worker failure" is either backed by a test that kills a real process,
or it is a hope. ReLab is the test.

---

## 30 seconds

```bash
docker compose up --build -d

# Register a workflow and start a run against the worker pool.
docker compose exec api relab run /examples/data-pipeline.yaml --detach

# While it is running, kill a worker outright. No graceful shutdown.
docker compose kill worker-2

# The task comes back and the run finishes.
docker compose exec api relab runs list
docker compose exec api relab run inspect <run-id>
```

The timeline shows exactly what happened:

```
SEQ   TIME           TYPE                   TASK       DETAIL
...
13    18:22:04.940   TASK_LEASED            analyze    attempt=1
14    18:22:04.943   TASK_STARTED           analyze    attempt=1
15    18:22:07.318   TASK_LEASE_EXPIRED     analyze    attempt=1
16    18:22:07.319   TASK_REQUEUED          analyze    attempt=1 next_attempt=2
17    18:22:07.402   TASK_LEASED            analyze    attempt=2
18    18:22:07.410   TASK_STARTED           analyze    attempt=2
19    18:22:07.418   TASK_SUCCEEDED         analyze    attempt=2
20    18:22:07.421   RUN_SUCCEEDED                     tasks_succeeded=4
```

Nothing was released when the worker died — it received `SIGKILL` and got no
chance to. The recovery came from another process observing the lease expire,
which is the only mechanism that also works when a machine loses power.

---

## Architecture

```
                        ┌───────────────────────────────┐
   relab CLI ─────────► │                               │
                        │        PostgreSQL 16          │
   relab server ──────► │                               │
     ├ coordinator      │  workflows  runs  tasks       │
     │  (reaper sweep)  │  task_attempts  events        │
     └ HTTP API ──────► │  workers  side_effects        │
          ▲             │  artifacts  dead_letters      │
          │             │                               │
   dashboard            └───────────────────────────────┘
   (read-only)                    ▲        ▲
                                  │        │
                        relab worker × N ──┘
                          ├ claim loop        (FOR UPDATE SKIP LOCKED)
                          ├ heartbeat loop
                          └ lease renewal loop
```

Every arrow is a SQL connection. There is no message bus and no RPC between
ReLab processes — they coordinate only through the database. That is what makes
a crashed process indistinguishable from a slow one, and forces recovery down
the path that has to work in production.

One datastore is a deliberate choice: a state change and the event describing it
are written in **one transaction**, so the journal can be trusted as a record of
what happened rather than as a log of what someone remembered to write down.
See [`docs/decisions/0001`](docs/decisions/0001-postgres-as-the-only-datastore.md).

Full detail: [`ARCHITECTURE.md`](ARCHITECTURE.md).

---

## A failure, in full

Kill a worker while it holds a task and ReLab records the whole story:

| Event | What it means |
|---|---|
| `TASK_LEASED attempt=1` | A worker won the claim |
| `TASK_STARTED attempt=1` | The handler was entered |
| *(the process dies — nothing is written)* | |
| `TASK_LEASE_EXPIRED attempt=1` | The coordinator observed the lease run out |
| `WORKER_LOST leases_released=1` | The worker missed five heartbeats |
| `TASK_REQUEUED next_attempt=2` | The task went back to the queue |
| `TASK_LEASED attempt=2` | A different worker took it |
| `SIDE_EFFECT_SKIPPED` | The retry found the effect already done and did not repeat it |
| `RUN_SUCCEEDED` | |

That last-but-one line is the important one. The task ran twice — that is
at-least-once delivery working as designed — and the external effect happened
once.

---

## Replay

```bash
relab replay <run-id> --diff
```

```
MATCH  run e6f9fa45-8903-4af9-b8ea-2ae54c34241f
  16 events reduce to the same state, and every artifact hash agrees with
  the artifacts table
```

The reducer is a **pure function**: events in, state out, no I/O, no clock, no
randomness. It refuses a journal it does not understand — an unknown event type,
an unrecognised payload version, a gap, an event after the terminal one — rather
than reconstructing a plausible-looking wrong answer. Corrupt one event row and
you get a named divergence category, not a crash and not a lie.

---

## Reliability guarantees

Stated precisely. Each is backed by a named test; see
[`docs/reliability.md`](docs/reliability.md) for which.

- **At-least-once delivery.** A task may execute more than once.
- **Two workers never execute the same attempt of the same task concurrently.**
  Enforced by a primary key on `task_attempts (task_id, attempt)`, not by
  timing.
- **An effect recorded under an idempotency key is performed at most once after
  it has been recorded.**
- **A run's event sequence is gapless**, so a gap means data loss rather than a
  routine abort.
- **A run's terminal event is its last.** A finished run's story cannot change.
- **One missed heartbeat never means failure.** A worker is doubted after three
  and declared dead after five.
- **A coordinator holds nothing in memory**, so a restart resumes by sweeping.

---

## Limitations

Read these before the benchmarks.

- **Not exactly-once.** The window between performing an external effect and
  recording it cannot be closed — the effect is external and the record is in
  PostgreSQL, and no transaction spans both. A crash inside that window produces
  a duplicate. The window is one `INSERT` wide.
- **Effects performed outside `TaskContext.Do` are not protected at all.**
- **Replay reconstructs logical state and does not re-execute handlers.**
  Anything a handler did that was not recorded is not reconstructed.
- **External API responses are not reproducible.** A fixture adapter would make
  them so; ReLab does not ship one in v1.
- **Wall-clock timings are not reproduced**, and `--diff` deliberately does not
  compare them.
- **Single region, single database.** Durability is exactly PostgreSQL's, and
  there is no second store. If the database is lost, everything is lost.
- **No authentication in v1.** See [`SECURITY.md`](SECURITY.md), which lists six
  residual risks and their mitigations.
- **No retention policy.** The journal grows without bound.
- **Cancellation does not interrupt a running handler.** It expires the lease;
  the handler runs to its timeout and its result is discarded.

---

## Benchmarks

See [`docs/benchmarks.md`](docs/benchmarks.md) for the full matrix, the
methodology, and the raw CSV.

Every number there was produced by `relab bench` on stated hardware. Nothing in
this README is estimated, and no figure appears anywhere in this repository that
was not measured.

---

## Quick start

**With Docker** (nothing else needed):

```bash
docker compose up --build -d
docker compose exec api relab run /examples/data-pipeline.yaml
open http://localhost:3000        # the dashboard
```

**Without Docker** (Go 1.25+ and PostgreSQL 16+):

```bash
export RELAB_DSN='postgres://relab:relab@localhost:5432/relab?sslmode=disable'
make build
./bin/relab migrate
./bin/relab run examples/data-pipeline.yaml
```

---

## CLI

```
relab server                                 # control plane: recovery sweep + HTTP API
relab worker --concurrency 4                 # a worker process
relab worker --scenario s.yaml               # a worker that injects a scenario's faults

relab workflow validate <file>               # no database needed
relab workflow register <file>
relab workflow list

relab run <workflow> [--scenario f.yaml]     # run it, in this process
relab run <workflow> --detach                # create it for a worker pool
relab run inspect <run-id>                   # the event timeline
relab run cancel <run-id>
relab runs list [--status] [--workflow]

relab replay <run-id> [--diff]               # reconstruct state; --diff exits non-zero on divergence
relab test <workflow> --scenario <file>      # exit 0/1, for CI
relab bench <workflow> --workers N --fault-rate P --runs M

relab workers                                # liveness
relab migrate
relab export [run-id...]                     # JSON snapshot in the read API's shapes
```

Every command takes `--json`.

`relab test` output:

```
PASS worker-crash-during-analyze
  recovery time      1.85s
  retries            0
  lost tasks         0
  duplicate effects  0
  faults injected    1
  final state        SUCCEEDED
```

---

## SDK

A workflow is YAML plus Go handlers. They are separate on purpose: the
definition is data that can be hashed, versioned, diffed and replayed, and a
handler is code that cannot.

```yaml
name: data-pipeline
version: 1
steps:
  - name: import
    handler: import_csv
    retry: {max_attempts: 3, initial_delay: 1s, multiplier: 2, max_delay: 30s, jitter: 0.2}
  - name: validate
    handler: validate_rows
    depends_on: [import]
```

```go
reg := sdk.NewRegistry()

reg.MustHandle("import_csv", func(ctx context.Context, tc *sdk.TaskContext) (any, error) {
    // Anything with an external effect goes through Do, which records it and
    // skips it on a retry. This is the only mechanism that does so.
    result, err := tc.Do(ctx, "charge", func(ctx context.Context) (any, error) {
        return billing.Charge(ctx, amount)
    })
    if err != nil {
        return nil, err
    }

    // Emit records an output by content hash, so replay can compare it.
    tc.Emit("imported.csv", "text/csv", data)

    return map[string]int{"rows": len(rows)}, nil
})
```

A failure that retrying cannot fix is marked, so the scheduler stops
immediately instead of spending its remaining attempts:

```go
return nil, sdk.Permanent(fmt.Errorf("malformed row %d: %w", i, err))
```

---

## Fault injection

Five fault types, none of them a flag the scheduler consults — a fault the
scheduler knows about is one it can be written to survive without the recovery
path ever running. `worker-crash` and `latency` degrade the real system:
a real `SIGKILL`, and a real delay that pushes the task towards its lease.
`http-error`, `db-disconnect` and `duplicate-delivery` produce the failure the
dependency would have produced, at the point the task would have seen it.

| Type | What it does |
|---|---|
| `worker-crash` | `SIGKILL`s the worker process. No cleanup, no lease release |
| `duplicate-delivery` | Invokes the handler again after the task completed, as a re-delivered message would |
| `latency` | Really sleeps, pushing the task towards its lease and timeout |
| `http-error` | Fails the task as an outbound call would |
| `db-disconnect` | Fails the task as a dropped connection would. It reports the failure rather than closing the worker's shared pool, which would take out every other task on that worker and make the scenario test several things at once |

A sixth, `queue-overload`, is named in the code and **not implemented**. Queue
contention is a property of the whole pool rather than of one task, so it does
not fit the per-task trigger-point model the others use. A scenario using it is
**rejected** rather than run without it: silently accepting one would report a
passing reliability test that never ran.

```yaml
name: worker-crash-during-analyze
seed: 42
faults:
  - type: worker-crash
    target: {task: analyze, attempt: 1}
    at: after-task-start
assert:
  run_status: SUCCEEDED
  lost_tasks: 0
  duplicate_effects: 0
  max_recovery_time_ms: 5000
  faults_injected: 1
```

Assertions are answered from the event journal, never from counters the runtime
kept as it went: a counter records what the code that increments it noticed.
`faults_injected` exists so a scenario cannot pass because nothing happened.

`duplicate_effects` is the one that cannot be answered from the journal alone,
and it is worth saying why. A duplicate effect is by definition one the ledger
did *not* suppress, so it leaves no event behind; it is found by comparing the
attempts that performed effects against the rows the ledger holds and the
suppressions the journal recorded. That comparison reads two tables
(`internal/cli/test.go`). Everything else — run status, lost tasks, recovery
time, faults injected — comes from replaying the events.

`relab test` refuses probability-driven scenarios unless `--allow-random` is
passed, because a corpus entry that passes or fails by luck is not a regression
test.

---

## Design decisions

Recorded in [`docs/decisions/`](docs/decisions/), with what was rejected and why:

- [0001](docs/decisions/0001-postgres-as-the-only-datastore.md) — PostgreSQL is the only datastore
- [0002](docs/decisions/0002-gapless-event-sequence.md) — gapless per-run event sequence
- [0003](docs/decisions/0003-one-task-row-per-step.md) — one task row per step, with an append-only attempt ledger
- [0004](docs/decisions/0004-lease-renewal-not-lease-deadline.md) — execution is bounded by the step timeout, not the lease
- [0005](docs/decisions/0005-at-least-once-not-exactly-once.md) — at-least-once, and what the ledger actually guarantees
- [0006](docs/decisions/0006-terminal-event-is-last.md) — a run's terminal event is its last
- [0007](docs/decisions/0007-faults-degrade-the-real-system.md) — faults degrade the real system

---

## Testing

```bash
make check          # vet + lint + go test -race ./...   (the gate)
make scenarios      # every file in examples/scenarios/
make crash-tests    # spawns real binaries and SIGKILLs them
```

There are no mock-only reliability claims in this repository.

- **Unit** — state transitions (legal *and* illegal), backoff arithmetic, the
  reducer, DAG validation, idempotency key derivation.
- **Integration** — a fresh real PostgreSQL per test, because the behaviour
  under test is PostgreSQL's own and a mock would only assert that the mock
  matches the test's assumptions.
- **Process-level** — spawns real worker binaries and `SIGKILL`s the one the
  database says holds the running task. This is the suite that distinguishes
  recovery that works from recovery that is claimed.
- **Scenario corpus** — every file in `examples/scenarios/`, discovered from the
  directory so adding a file adds a CI case.

Two bugs in this repository were found by these tests rather than by review, and
the commits say so.

---

## Deployment

One binary, `relab`, serves both the control plane and the workers; the
subcommand decides which. Migrations run on start-up under an advisory lock, so
several processes cannot race the same DDL. Containers run as non-root.

The stack is PostgreSQL, one control plane, N workers, and optionally the
dashboard. Configuration is environment variables; there is no config file.

The dashboard is a Next.js app that renders on the server and deploys to Vercel
from `web/`. The workers do not and cannot go there: a ReLab worker holds a
lease, renews it on a timer and executes a task that may take minutes, and
reshaping that into a request handler would delete the thing being tested. They
run wherever long-lived containers run.

With `RELAB_API_URL` unset the dashboard needs no backend at all — it serves the
recording. With it set, every page reads that control plane, on the server, so
the browser never talks to the API and never sees the URL.

**The API has no authentication.** Reachability is the entire access control, so
a publicly reachable control plane is a publicly readable one. Deploy it on a
private network or accept that.

Full detail, including health checks, migration and rollback, production timings
and the residual risks: [`docs/deployment.md`](docs/deployment.md).

---

## Roadmap

- A broker behind the `queue.Queue` interface, for throughput beyond one
  PostgreSQL instance.
- A fixture adapter, so external API responses are reproducible under replay.
- Conditional edges (`when:`) in workflow definitions.
- Differential replay across workflow versions.
- Retention and archival for the journal.

---

## Documentation

| Document | Contents |
|---|---|
| [`docs/reliability.md`](docs/reliability.md) | **The authority on guarantees.** Every claim names its test |
| [`ARCHITECTURE.md`](ARCHITECTURE.md) | System design, boundaries, tradeoffs |
| [`DATA.md`](DATA.md) | The schema as implemented, with the reasoning |
| [`SECURITY.md`](SECURITY.md) | Threat model and residual risks |
| [`PRD.md`](PRD.md) | Requirements, scope, non-goals |
| [`CLAUDE.md`](CLAUDE.md) | Conventions for changing this repository |
| [`AGENTS.md`](AGENTS.md) | Sharp edges, and what to verify before calling work done |
| [`SKILLS.md`](SKILLS.md) | Engineering practices this project holds itself to |
| [`docs/benchmarks.md`](docs/benchmarks.md) | Measured numbers, with methodology |
| [`docs/deployment.md`](docs/deployment.md) | How it is deployed, and what the deployment does not do |
| [`docs/orchestration.md`](docs/orchestration.md) | How the agents developing this repository are set up and coordinated |

---

## License

[Apache License 2.0](LICENSE). Permissive, with an explicit patent grant, which
is the thing that matters to anyone adopting a reliability tool inside a
company.
