# Deployment

How ReLab is deployed, why it is deployed that way, and what the deployed system
does not do.

`docs/reliability.md` remains the authority on guarantees. Nothing here adds a
guarantee; a few things here constrain one.

---

## 1. The shape of the system

```
                    ┌──────────────────────────────┐
                    │  Vercel                      │
   browser ────────►│  Next.js dashboard           │
                    │  server-rendered, read-only  │
                    └──────────────┬───────────────┘
                                   │
                     RELAB_API_URL │  set?
                          ┌────────┴────────┐
                       no │                 │ yes
                          ▼                 ▼  HTTPS, server-side only
              ┌───────────────────┐  ┌──────────────────────┐
              │ bundled recording │  │  ReLab control plane │
              │ 5 real runs, from │  │  relab server        │
              │ `relab export`    │  │  ├ reaper sweep      │
              └───────────────────┘  │  └ HTTP read API     │
                                     └──────────┬───────────┘
                                                │  SQL
                                     ┌──────────┴───────────┐
                                     │     PostgreSQL 16    │
                                     └──────────┬───────────┘
                                                │  SQL
                                        relab worker × N
                                          ├ claim loop
                                          ├ heartbeat loop
                                          └ lease renewal loop
```

Three things are worth saying about this diagram, because they are the decisions
rather than the drawing.

**The workers are not on Vercel and cannot be.** A ReLab worker is a process
that holds a lease, renews it on a timer, heartbeats, and executes a task that
may take minutes. A serverless function is invoked, runs, and is frozen or
discarded. Reshaping the worker into a request handler would not be a port; it
would delete the thing being tested — a process that can be killed mid-task
while holding a lease. So the workers stay on a host that runs long-lived
processes.

**The dashboard is not the product**, so putting it somewhere else is cheap. It
reads; it cannot write; it holds no state. Vercel serves it well and costs
nothing at this size.

**The default deployment has no backend at all.** With `RELAB_API_URL` unset the
dashboard serves a recording of five real runs, exported from a real database
(§4). That is the deployment a reviewer sees, and it is honest about being a
recording on every page.

---

## 2. Local development

Everything, including the dashboard reading a live control plane:

```bash
docker compose up --build -d          # PostgreSQL, control plane, 3 workers, dashboard
open http://localhost:3000
```

Then break something and watch it recover:

```bash
docker compose exec api relab run /examples/data-pipeline.yaml --detach
docker compose kill worker-2          # while it is in flight
docker compose exec api relab runs list
```

The compose stack deliberately compresses the recovery timings — a 6s lease
rather than 30s — so that a reviewer sees recovery happen rather than waiting
for it. **Those values are not production values.** §6 gives the defaults.

The dashboard alone, serving the recording, which is what Vercel runs:

```bash
cd web && npm ci && npm run dev       # RELAB_API_URL unset
```

Against a control plane you started yourself:

```bash
cd web && RELAB_API_URL=http://localhost:8080 npm run dev
```

Backend work needs PostgreSQL and nothing else:

```bash
export RELAB_TEST_DSN=postgres://relab:relab@localhost:5432/postgres?sslmode=disable
make check
```

---

## 3. The Vercel project

Deployed at **<https://relabca.vercel.app>**, serving the recording with no
backend behind it.

| Setting | Value | Why |
|---|---|---|
| Framework | Next.js | Detected; pinned in `web/vercel.json` |
| Root directory | `web` | The repository root is a Go module, not a Next app |
| Install command | `npm ci` | The lockfile is committed and is the input |
| Build command | `npm run typecheck && npm run build` | A type error should fail the deploy, not reach the page |
| Node | 22.x | Matches `web/Dockerfile` |
| Production branch | `main` | |

Everything except the root directory is in `web/vercel.json`, so importing the
repository and setting **Root Directory** to `web` is the whole configuration.
Preview deployments for pull requests are Vercel's default and are wanted here:
a preview builds the same recording the production deployment serves, so a
change to the dashboard can be reviewed by looking at it.

### Environment variables

| Variable | Scope | Value | Effect |
|---|---|---|---|
| `RELAB_API_URL` | Production, Preview | *(unset)* | The dashboard serves the bundled recording and labels every page as such |
| `RELAB_API_URL` | Production | `https://relab-api.example` | Every page reads that control plane, server-side |

There are no other variables, and there is no secret. That is not an oversight
to be corrected later: the ReLab API has no authentication in v1, so there is no
credential to hold. It also means **`RELAB_API_URL` must not point at a control
plane whose only protection is that nobody has guessed its address.** §7.

`RELAB_API_URL` is read in `web/src/lib/api.ts` on the server. It is never
prefixed `NEXT_PUBLIC_`, so it is not inlined into the client bundle and the
browser never learns it.

---

## 4. The recording

`web/src/demo/snapshot.json` is the output of `relab export`, produced by
`scripts/record-demo.sh`:

```bash
RELAB_DSN=postgres://... ./scripts/record-demo.sh
```

It runs five scenarios against a real database with real worker processes —
including two that SIGKILL a worker — and exports what the database then held,
in exactly the shapes the read API serves. Regenerating it is one command, which
is the only way "this is a recording of what happened" stays a checkable claim
rather than a promise.

The runs it contains, in the order the dashboard lists them:

| Run | What it shows |
|---|---|
| `worker-crash-after-effect` | A worker killed after performing a side effect but before acknowledging the task. The task recovers on another worker and the effect is **not** repeated |
| `worker-crash-during-analyze` | A worker killed mid-task. Lease expiry, requeue, completion elsewhere |
| `duplicate-delivery-on-charge` | The same task delivered twice. The ledger suppresses the second effect |
| `upstream-down-throughout` | An upstream that never recovers. Retries are spent, the task is dead-lettered, **the run fails** |
| *(no scenario)* | A clean run, for comparison |

The dashboard never mixes the recording with live data. A configured API that
cannot be reached renders an error state, not the recording: showing last week's
runs during an incident would be worse than showing nothing.

---

## 5. Deploying the backend

Only needed if you want the dashboard reading live data. The recording needs
none of this.

The control plane and the workers are the same image with different commands,
which is the whole reason there is one binary:

```bash
docker build -t relab .
docker run -e RELAB_DSN=... relab server --addr :8080
docker run -e RELAB_DSN=... relab worker --concurrency 4
```

Any host that runs long-lived containers with environment variables, logs and
health checks works — Fly.io, Render, Railway, a VM with `docker compose`, or
Kubernetes. ReLab needs nothing from the platform beyond that. PostgreSQL can be
managed (Neon, Supabase, RDS, Cloud SQL) or a container; it needs no extensions.

Order matters exactly once: **PostgreSQL must be reachable before the control
plane and the workers start.** They migrate on start-up (§8) and exit if they
cannot connect, which under an orchestrator means a crash loop until the
database is up. That is the intended behaviour — a process that started without
a database and pretended otherwise would be worse.

### Health checks

| Endpoint | Question | Fails when |
|---|---|---|
| `/healthz` | Is the process alive? | It is not answering |
| `/readyz` | Can it serve? | The database is unreachable, **or** its schema is not the version this build carries |

Use `/healthz` for liveness and `/readyz` for readiness, and do not swap them: a
schema mismatch is not fixed by restarting, so a liveness probe reading
`/readyz` would restart-loop a process forever over a bad deploy.

Neither endpoint returns a version number, a table name or a DSN. `/readyz`
says which of the two conditions failed; the numbers go to the log.

---

## 6. Production configuration

Every setting is an environment variable. There is no config file.

| Variable | Default | Notes |
|---|---|---|
| `RELAB_DSN` | — | Required. Redacted from every error and log line |
| `RELAB_LISTEN_ADDR` | `:8080` | Control plane only |
| `RELAB_LOG_LEVEL` | `info` | |
| `RELAB_LOG_FORMAT` | `text` | Use `json` in a deployment |
| `RELAB_DB_MAX_CONNS` | see `store.DefaultConfig` | Per process. Multiply by process count before comparing with the server's limit |
| `RELAB_WORKER_CONCURRENCY` | `4` | Tasks in flight per worker |
| `RELAB_OTLP_ENDPOINT` | *(unset)* | Unset disables export. A collector being down never stops a workflow |
| `RELAB_LEASE_DURATION` | `30s` | |
| `RELAB_LEASE_RENEW_INTERVAL` | `10s` | Must be ≤ half the lease; enforced at start-up |
| `RELAB_HEARTBEAT_INTERVAL` | `5s` | |
| `RELAB_REAPER_INTERVAL` | `1s` | |

**Do not copy the compose timings into production.** `docker-compose.yml` uses a
6s lease and a 500ms reaper so a reviewer sees a recovery within seconds. In
production that trades a real margin for a demo: a 6s lease means a worker whose
renewal is delayed by a GC pause and a slow query can lose a task it is still
running. The defaults above leave room for both.

`docs/reliability.md` gives the recovery latency each choice buys and the
constraints `config.Timing.Validate` enforces.

### Shutdown

`SIGTERM` or `SIGINT` cancels the root context, and every long-lived goroutine
takes that context and returns on it.

- The API stops accepting connections and drains in flight requests, with a 10s
  cap.
- A worker finishes the tasks it has already started — an ordinary redeploy
  should not produce the same events a crash does — then records itself
  `STOPPED` and releases any lease it still holds.
- Telemetry flushes on the way out.

None of this is load-bearing. A worker that is killed says nothing, and its
tasks come back through lease expiry, which is the path that has to work when a
machine loses power. Retiring is best-effort with a 5s deadline; a failure is
logged and changes nothing about recovery.

---

## 7. What the public deployment may and may not do

The deployed dashboard is read-only, and that is enforced in three places rather
than promised in one:

1. **The API has no mutating route.** No POST, PUT, PATCH or DELETE handler
   exists to expose. `TestTheAPIRefusesEveryWrite` fails if one appears.
2. **The dashboard has no write path.** It fetches during server rendering and
   renders HTML. There is no client-side mutation to disable.
3. **The public deployment reads a recording.** With `RELAB_API_URL` unset there
   is no backend for a visitor to reach at all.

A visitor therefore cannot start a run, kill a worker, inject a fault, register
a workflow or reach the database. Everything that can change ReLab's state is a
CLI verb requiring a DSN.

If you do point the public dashboard at a live control plane, understand what
you are exposing: **the ReLab API has no authentication.** Anyone who can reach
it can read every run, every event payload and every workflow definition in that
database. Reachability is the whole access control. Put the control plane on a
private network, or accept that its contents are public.

`SECURITY.md` has the threat model and the residual risks.

---

## 8. Migrations

Migrations are embedded in the binary, applied in version order, checksummed,
and **append-only**. Editing a released migration fails the checksum check on
every database that already applied it, which is the intent.

The control plane and every worker call `Migrate` on start-up under a PostgreSQL
advisory lock, so several starting at once cannot race the same DDL: one runs
them, the others wait and find nothing to do.

That is right for this system, and it is worth saying why, because automatic
migration on start-up is often wrong. It is safe here because the migrations are
additive and the lock makes concurrency a non-issue. It would stop being safe
the first time a migration rewrote a large table or dropped a column an older
running process still selects. If that day comes, split it: run `relab migrate`
as a deploy step and stop calling it from `server` and `worker`.

**Rollback.** Redeploy the previous image. There are no down migrations, on
purpose: an automated rollback that drops a column drops the data in it. An
older binary against a newer schema fails `/readyz` and stays out of rotation
rather than running against a schema it does not understand. If a migration
itself must be undone, that is a new forward migration, written deliberately.

---

## 9. Logs and telemetry

Logs are structured (`slog`); set `RELAB_LOG_FORMAT=json` in a deployment. Every
line logged inside a span carries its `trace_id`, so a log line and a trace can
be moved between. The DSN is redacted from driver errors, and no credential is
ever logged.

With `RELAB_OTLP_ENDPOINT` set, traces and metrics are exported over OTLP:
recovery timing, task retries, lease expirations and duplicate-execution
detection among them. Unset, export is off and the instruments are no-ops. A
collector that is down never stops a workflow — the workflows are the job and
telemetry is diagnostic.

---

## 10. Limitations

Stated plainly, because a deployment guide that only lists what works is not
one.

- **The dashboard on Vercel shows a recording.** It is real and it is labelled,
  but it is not live, and <https://relabca.vercel.app> has no backend behind it.
- **The API has no authentication.** v1 non-goal. It bounds where a control
  plane can be exposed.
- **Workers cannot run on Vercel**, and no amount of configuration changes that.
- **`/readyz` reports schema equality, not correctness.** A database whose
  schema matches but whose data was hand-edited passes.
- **Multiple control planes are supported and untested at scale.** The sweep
  divides work with `SKIP LOCKED`, so several are correct; the benchmarks in
  `docs/benchmarks.md` were measured with one.
- **The recording ages.** It is regenerated by running the script, not
  automatically, so its timestamps are the date it was last made.
