# Architecture

## What this system is

ReLab runs multi-step workflows across worker processes, records an append-only
event history, replays that history to reconstruct run state, injects
deterministic faults, and asserts that recovery behaves correctly.

It is a reliability testing and replay tool. It is not a Temporal replacement.

## High-level shape

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
                          ├ claim loop
                          ├ heartbeat loop
                          └ lease renewal loop
```

Every arrow is a SQL connection. There is no message bus, no service mesh, and
no RPC between ReLab processes. Processes coordinate only through the database,
which is what makes a crashed process indistinguishable from a slow one and
forces recovery down the path that also works when a machine loses power.

## Application boundaries

| Process | Responsibility | State held |
|---|---|---|
| `relab server` | Recovery sweep, HTTP read API | None |
| `relab worker` | Claim, execute, heartbeat, renew | The tasks it is executing |
| `relab <verb>` | One-shot operator commands | None |
| dashboard | Server-rendered read-only views | None |

The only process holding anything in memory is a worker, and only for the
duration of a task it is executing. That is why a coordinator restart needs no
resume logic: there is nothing to resume.

## Packages

```
cmd/relab              main, version
internal/
  api/                 HTTP handlers and routes
  assert/              scenario assertions, answered from the journal
  bench/               benchmark harness, percentiles, CSV
  cli/                 the cobra command tree
  config/              settings, timing, validation
  engine/              run and task state; the single writer
  event/               the append-only journal
  examples/            handlers for the shipped example workflows
  fault/               scenarios, injectors, trigger points
  faultengine/         wires fault to engine without a cycle
  idem/                the side-effect ledger
  replay/              the pure reducer and divergence reporting
  retry/               backoff arithmetic
  store/               the pool, transactions, typed errors, migrations
  telemetry/           traces, metrics, trace-correlated logging
  testsupport/         per-test databases
  worker/              the worker process
  workflow/            definition parsing and DAG validation
sdk/                   the public Go interface for defining workflows
web/                   the read-only dashboard
test/process/          tests that spawn real binaries and SIGKILL them
test/scenarios/        the fault scenario corpus as a regression suite
```

### Dependency rules

- `internal/engine` is the **single writer** for run and task state. Nothing
  else writes to `runs`, `tasks`, `task_attempts` or `dead_letters`.
- `internal/replay` imports nothing that can do I/O. The reducer is pure, and
  the compiler enforces it by the package having no database dependency.
- `internal/fault` does not import `internal/engine`, and `internal/engine`
  does not import `internal/fault`. The engine declares the small interfaces it
  needs (`FaultSource`, `TriggerPoints`) and `internal/faultengine` satisfies
  them. This is the only reason that package exists.
- `sdk` imports nothing internal. It is what a user's own binary depends on.

## Data flow: one task, end to end

1. `engine.CreateRun` inserts the run and **every** task in one transaction,
   with `RUN_CREATED`, one `TASK_SCHEDULED` per root, and `RUN_QUEUED`.
2. A worker's claim loop runs `engine.ClaimTasks`, which takes runnable rows
   with `FOR UPDATE SKIP LOCKED`, sets them `LEASED`, increments `attempt`, and
   appends `TASK_LEASED` — all in one transaction.
3. `engine.StartTask` moves the task to `RUNNING` and inserts into
   `task_attempts`. That insert is what makes concurrent execution of one
   attempt impossible rather than merely unlikely.
4. The handler runs under the step's timeout. The worker's renewal loop extends
   the lease every `LeaseRenewInterval`.
5. `engine.CompleteTask` records the outcome, the artifacts, the events, the
   dependent tasks that just became ready, and the run's terminal state if this
   was the last one — in one transaction.

If the worker dies at any point after step 2, the coordinator's sweep observes
the lease expire, appends `TASK_LEASE_EXPIRED` and `TASK_REQUEUED`, and returns
the task to the queue under a new attempt number.

## Database architecture

One PostgreSQL 16 database holds state, queue and journal. The reasoning is in
`docs/decisions/0001`; the short version is that a state change and the event
describing it must be one transaction, and no transaction spans two systems.

The queue is the `tasks` table, claimed with `SELECT ... FOR UPDATE SKIP
LOCKED`. A broker sits behind a future `queue.Queue` interface; it is not in v1
because it would split that transaction.

Schema, constraints, indexes and their reasoning are in `DATA.md`.

### Transaction boundaries

Every state change is one transaction that includes its events. The transactions
are short and take row locks in a consistent order (run row, then task rows),
which is why the system does not deadlock under a pool of contending workers.

The one place an explicit lock is taken is closing a run: `SELECT status FROM
runs WHERE id = $1 FOR UPDATE`, so that two callers observing completion at once
produce exactly one terminal event.

## API architecture

`net/http` with `chi` for routing. The API is read-mostly: everything that
changes state goes through `internal/engine`, which the CLI also calls, so
there is one implementation of each state change rather than one per entry
point.

```
GET /healthz                    process is up
GET /readyz                     database is reachable
GET /api/v1/workflows
GET /api/v1/runs                ?status= &workflow= &limit=
GET /api/v1/runs/{id}
GET /api/v1/runs/{id}/tasks
GET /api/v1/runs/{id}/events
GET /api/v1/workers
GET /api/v1/stats
```

Error bodies carry a category, never an internal detail: a database error can
contain a table name, a constraint, or a fragment of a query, none of which
helps a legitimate caller. The full error is logged with the request id.

## Authentication and authorisation

**There is none in v1.** This is a deliberate scope decision, not an oversight,
and `SECURITY.md` states the threat model it implies. The intended deployment is
a developer's machine or a CI runner on a private network.

## Frontend architecture

Next.js 15 App Router, server components only. Every fetch happens on the server
during rendering, so the browser never talks to the API and there is no
credential in the page. There is no client-side state management because there
is no client-side state: the dashboard reads, and it does not write.

Pages are `force-dynamic`. A cached dashboard would be actively misleading
during the one thing it exists to observe.

One client component exists, `nav.tsx`, and it is one so the current section can
carry `aria-current`. Filters on the runs list and the timeline are links
carrying query parameters rather than controls: a filtered view is then a URL
that can be sent to whoever is being asked to look, and the page needs no
JavaScript to produce one.

The dashboard has two data sources, chosen once by whether `RELAB_API_URL` is
set: the live read API, or `web/src/demo/snapshot.json`, a `relab export` of
real runs that a deployment with no reachable control plane serves instead. They
never mix — a configured API that is down is an error state — and the recording
is in the API's own shapes, so nothing in the pages gets a second code path.

## Background work

| Loop | Where | Interval | Purpose |
|---|---|---|---|
| Reaper sweep | coordinator | 1s | Expire leases, sweep workers, promote retries |
| Claim | worker | 200ms when idle | Take runnable tasks |
| Heartbeat | worker | 5s | Report liveness |
| Lease renewal | worker | 10s | Extend held leases |

The heartbeat and renewal loops are separate goroutines from execution, so a
blocked handler cannot make a healthy worker look dead.

## Caching

None. The database is the only source of truth and the workloads are small
enough that a cache would add a consistency problem in exchange for latency
nobody has complained about.

## Storage

Artifacts are stored **by content hash only** — name, sha256, size, content
type. The bytes are not stored. Replay compares hashes, and keeping the bytes
would make the database the wrong size for the job it does.

## Error handling

- Errors are wrapped with `%w` and context, and matched with `errors.Is`/`As`.
- An error is either logged or returned, never both.
- Driver errors are translated into typed sentinels in `internal/store`, so no
  package outside it knows what database is underneath.
- An illegal state transition returns an error, never a panic: a bug in one run
  must not take down a process recovering others.
- A handler panic is recovered, recorded as a permanent failure of that task,
  and does not reach the worker's other tasks.

## Configuration

Environment variables, listed in `internal/config`. There is no config file: the
deployment surface is one binary with a handful of knobs, and a file format
would be one more thing to keep in sync with the documentation.

Timing values are overridable so that scenarios and tests can compress recovery
windows, which is also what an operator tuning recovery latency needs.

## Deployment

`docker compose up` brings up PostgreSQL, the control plane, three individually
killable workers, and the dashboard. One image serves the control plane and the
workers; the command decides which it is. Both run as non-root.

Migrations run on start-up under an advisory lock, so the processes cannot race
the same DDL. `/readyz` refuses traffic when the applied schema is not the
version the binary carries, so an older process against a newer database stays
out of rotation rather than failing one query at a time.

The dashboard deploys separately, to a static host — the repository is set up
for Vercel with `web` as the root directory. The workers do not go with it: a
worker holds a lease, renews it on a timer and may run for minutes, and a
request handler cannot be any of those things. `docs/deployment.md` has the
whole picture and the parts of it that are not reassuring.

## CI/CD

Three GitHub Actions jobs:

1. **check** — gofmt, `go mod tidy` cleanliness, `go vet`, golangci-lint, and
   `go test -race ./...` against a real PostgreSQL service.
2. **scenarios** — every file in `examples/scenarios/` as a regression suite,
   discovered from the directory so adding a file adds a CI case.
3. **process** — the suite that spawns real worker binaries and SIGKILLs them.

## Scalability

The bottleneck is one PostgreSQL instance, specifically the claim query. It is
served by a partial index on the runnable set (`WHERE status = 'READY'`), so it
is proportional to work outstanding rather than to work ever created.

Measured throughput is in `docs/benchmarks.md`. The numbers are adequate for
reliability testing and would not be adequate for a general-purpose task queue,
which the README says.

Workers are stateless and horizontally scalable. Coordinators are stateless and
may run in any number.

## Failure handling and disaster recovery

| Failure | Behaviour |
|---|---|
| Worker crash | Lease expires; task requeued under a new attempt |
| Worker unreachable but alive | Same, and the original attempt's result is refused |
| Coordinator crash | Another sweeps; a restarted one resumes from the database |
| Database unreachable | Workers log and back off; they do not exit, so a blip does not become a recovery event for every task they hold |
| Database lost | Everything is lost. There is no other store, and no backup strategy ships in v1 |

That last row is the honest one: ReLab's durability is exactly PostgreSQL's
durability, and operating it means operating a database.

## Tradeoffs

| Decision | Bought | Cost |
|---|---|---|
| One datastore | Transactional journal; trustworthy replay | Throughput ceiling |
| Postgres queue | No broker to operate | Claim query is the hot spot |
| At-least-once | Simple, honest recovery | Duplicate effects possible in one window |
| Pure reducer | Replay that cannot lie | Cannot reconstruct unrecorded behaviour |
| Real fault injection | Tests the actual recovery path | Crash scenarios need spawned processes |
| No auth in v1 | Smaller surface to get right | Cannot be exposed to a network |
