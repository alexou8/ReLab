# Security

This document describes ReLab's actual security posture, including what it does
not do. The read API supports role-bearing bearer tokens and refuses an
unauthenticated non-loopback bind by default. Anyone who can reach the database
can still do anything.

## Reporting a vulnerability

**Do not open a public issue.** Report privately through GitHub's
[private vulnerability reporting form](https://github.com/alexou8/relab/security/advisories/new),
which opens a draft advisory only the maintainers can see.

Useful to include: what an attacker can do, the version or commit, and the
smallest reproduction you have. Please do not include a credential, a token, or
a production connection string — a redacted DSN is enough, and this repository
is public the moment an advisory is published.

What to expect: ReLab is maintained by one person, so there is no response-time
commitment, but security reports take priority over everything else. You will
get an acknowledgement, an assessment of whether it is in scope, and — if it is
— a fix and a credited advisory unless you would rather not be named.

**In scope:** anything that lets someone bypass a stated boundary in this
document, corrupt or lose journal data, escalate from the dashboard to the
control plane, or execute code from workflow or scenario input.

**Out of scope, because they are documented below rather than accidents:** an
operator explicitly setting `RELAB_INSECURE_NO_AUTH=true`, and anything that
requires database access, which is already full control.

## Threat model

**Intended deployment:** a developer's machine, a CI runner, or a self-hosted
private network. ReLab is a tool you point at your own workflows to find out
whether they recover.

**Explicitly out of scope for v1:** the public internet, multi-tenancy, any
deployment where the operator does not trust everyone who can reach the port.

| Actor | Assumed | Consequence |
|---|---|---|
| Operator | Trusted | Full access by design |
| Workflow author | Trusted | Handlers run as the worker process, unsandboxed |
| Caller with a viewer token | Trusted to read | Full read access |
| Caller with an operator token | Trusted to operate ReLab | Currently the same read access; the role is reserved for future write endpoints |
| Unauthenticated caller | Untrusted | Probe access only; API requests receive the same 401 response |
| Anyone reaching the database | Trusted | Full read/write |
| Network between processes | Trusted | Postgres TLS is the DSN's business |

## Trust boundaries

ReLab enforces an HTTP authentication boundary and the **process boundary around
a worker**. A worker executes handler code and can do anything that code can do.
There is no sandbox, and none is planned — the workflows are the user's own, and
a sandbox that a workflow author can trivially step outside is worse than an
honest statement that there is none.

The coordinator and database remain inside one trusted deployment zone.

## Authentication

Set `RELAB_API_TOKENS` to a comma-separated list of `role:token` entries, where
the role is `viewer` or `operator`. API callers send `Authorization: Bearer
<token>`. Token digests are compared in constant time. Missing, malformed and
unknown credentials all receive the same status and body: 401 with
`{"error":"unauthorized"}`. Tokens are never returned or logged.

`/healthz` and `/readyz` deliberately remain unauthenticated so container and
orchestrator probes do not need a credential.

Without tokens, ReLab serves only on an explicit loopback address. A wildcard
or other non-loopback bind fails startup unless the operator explicitly sets
`RELAB_INSECURE_NO_AUTH=true`. That switch makes every API caller a viewer; its
name is intentionally hard to mistake for a safe production setting.

## Authorisation

Viewer tokens can use every current endpoint. Operator tokens inherit viewer
access and are reserved for future state-changing endpoints. There are no such
endpoints today.

## Session management

Not applicable — no sessions exist.

## Credential handling and secret management

The database connection string and configured bearer tokens are ReLab's
secrets.

- It is read from `RELAB_DSN` rather than a flag, so it does not appear in the
  process table.
- It is never logged.
- It is redacted from driver error messages. pgx echoes the connection string in
  several failure modes, and those messages reach logs and CLI output;
  `internal/store/redact.go` strips both the whole DSN and, for URL-form DSNs,
  the password alone, because a truncated DSN inside a driver message would slip
  past a whole-string match. `TestOpenDoesNotLeakPassword` asserts it.

Bearer tokens are read from `RELAB_API_TOKENS`, are never logged, and are
omitted from configuration errors. Supply both tokens and the DSN through the
deployment platform's secret mechanism, not a checked-in environment file.

## Input validation

| Input | Validation |
|---|---|
| Workflow YAML | Strict: unknown fields rejected, cycles detected, names restricted to `[A-Za-z0-9_-]` and 64 characters, retry policies checked. Every problem reported at once |
| Scenario YAML | Same strictness; unknown fault types and trigger points rejected |
| Run ids in URLs | Parsed as UUIDs before use |
| Query parameters | `limit` clamped to `RELAB_API_MAX_LIMIT` (500 by default); unparseable values fall back to endpoint defaults |
| Request bodies | Capped by `RELAB_API_MAX_BODY_BYTES` (1 MiB by default) |
| Environment durations | A present but unparseable value is an error, never a silent fallback |

Step and workflow names are restricted specifically because they end up in
idempotency keys, where a `:` would let one operation's key collide with
another's, and in metric labels.

## Injection prevention

**Every query is parameterised.** There is no string concatenation of user data
into SQL anywhere in the codebase, and no ORM or query builder to hide one.

The two places SQL is assembled as a string are:
1. Column lists, which are compile-time constants in the package that owns them.
2. Database names in `internal/testsupport`, which are generated from a counter
   and a timestamp — never from test input — and quoted with `quoteIdent`.
   Identifiers cannot be parameterised, so DDL that names a database has to
   build a string.

## Output encoding and XSS

The API serves JSON with `Content-Type: application/json; charset=utf-8` and
`X-Content-Type-Options: nosniff`, which closes the one XSS route a JSON API
has.

The dashboard is React with server components. All interpolation goes through
JSX, which escapes by default. There is no `dangerouslySetInnerHTML` anywhere in
`web/`.

## CSRF

Not applicable: the API has no state-changing endpoints and no cookies.

## SSRF

ReLab makes no outbound HTTP requests of its own. A *handler* may, and a handler
is user code — a workflow that fetches an attacker-supplied URL is the workflow
author's problem, and one ReLab cannot see.

## Rate limiting and abuse prevention

The API uses an in-memory token bucket per authenticated token, or per direct
client IP when authentication is disabled or fails. The default is 10 requests
per second with a burst of 20; `RELAB_API_RATE_LIMIT` and
`RELAB_API_RATE_BURST` change it. Exhaustion returns 429 with the category
`rate limited`. Proxy forwarding headers are not trusted.

The limiter is local to a process, so deployments with several control planes
multiply the effective allowance. It does not replace an edge limiter for a
service exposed to an untrusted network. Per-request defenses additionally
include a 30-second handler timeout, the body and row caps, and
`ReadHeaderTimeout` on the HTTP server.

## File upload

There is none. Artifacts are recorded by content hash; ReLab never receives or
stores file bytes.

## API security

- Timeouts on read headers, read, write and idle, so a stalled client cannot
  hold a connection and a goroutine indefinitely.
- `Recoverer` middleware, so a panic in one handler does not take the server
  down.
- Bearer authentication on `/api/v1`; probes stay unauthenticated.
- Per-client rate limiting and request body and query-result caps.
- **Error bodies carry a category, never an internal detail.** A database error
  can contain a table name, a constraint, or a fragment of a query, none of
  which helps a legitimate caller. The full error is logged with the request id.
- No `Server` header (`poweredByHeader: false` on the dashboard; Go's default
  is already minimal).

## Database security

- Least privilege is the operator's job. ReLab needs `CREATE`/`SELECT`/`INSERT`/
  `UPDATE`/`DELETE` on its own schema and nothing else. The **test** DSN needs
  `CREATEDB`, which is why the tests use a separate connection string.
- The connection pool is bounded (10 by default), so ReLab cannot exhaust the
  server's connection slots on its own.
- Migrations run under an advisory lock so concurrent processes cannot race DDL,
  and a released migration cannot be edited without failing the checksum check.

## Encryption

- **In transit:** whatever the DSN's `sslmode` specifies. The compose stack uses
  `sslmode=disable` because everything is on one Docker network on one host. A
  deployment where the database is elsewhere must set `sslmode=verify-full`.
- **At rest:** whatever the PostgreSQL deployment provides. ReLab adds nothing.
- **OTLP:** the exporter connects insecurely, on the assumption of a collector on
  the same host. It is not configurable in v1.

ReLab claims no encryption of its own, because it implements none.

## Logging and auditability

The event journal is a complete, append-only, tamper-evident-by-gap record of
everything that happened to a run. It is not a security audit log — it records
what the system did, not who asked — but for the question "what happened to this
run" it is authoritative.

**Logs never contain:** passwords, tokens, the DSN, or anything ReLab knows to
be a secret. They *do* contain handler error messages, which are user-controlled
and could contain anything a handler puts in them.

## Privacy

ReLab stores no personal data of its own. It stores whatever handlers return, in
`tasks.output_ref` and in `TASK_SUCCEEDED` payloads. A handler that returns
personal data will have it persisted and replayed. ReLab cannot detect this;
handler authors must not return what they do not want stored.

## Dependency and supply-chain security

Direct dependencies, chosen to be few and boring:

| Dependency | Why |
|---|---|
| `jackc/pgx/v5` | The PostgreSQL driver. No ORM |
| `spf13/cobra` | Command tree |
| `go-chi/chi/v5` | Router. Stdlib `net/http` underneath |
| `google/uuid` | Identifiers |
| `gopkg.in/yaml.v3` | Workflow and scenario parsing |
| `go.opentelemetry.io/otel` | Traces and metrics |
| `golang.org/x/sync` | `errgroup` |

- `go.mod` and `go.sum` are committed; CI fails if `go mod tidy` changes them.
- Builds are reproducible: `-trimpath`, `CGO_ENABLED=0`, a pinned base image.
- The container runs as a non-root user (uid 10001) and contains only the
  binary and the example files.

**Not done in v1:** dependency vulnerability scanning in CI (`govulncheck`),
SBOM generation, image signing. These are named here rather than omitted,
because a security document that lists only what was done is a marketing
document.

## Security headers

The API sets `X-Content-Type-Options: nosniff`. It does not set CSP, HSTS or
frame options, because it serves JSON to programs rather than HTML to browsers.

The dashboard does set them, in `web/vercel.json`: `nosniff`,
`Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, and a CSP naming no
external origin, which it can afford because it loads nothing from one. HSTS is
Vercel's, and applies to every response it serves.

## The public dashboard deployment

The deployed dashboard is read-only in three independent ways, which is the
reason it is safe to expose and a promise in one place would not be:

1. **The API has no mutating route.** There is no POST, PUT, PATCH or DELETE
   handler to reach. `TestTheAPIRefusesEveryWrite` fails if one is added.
2. **The dashboard has no write path.** It fetches during server rendering and
   emits HTML; there is no client-side mutation to disable.
3. **With `RELAB_API_URL` unset it has no backend at all**, and serves a
   recorded export instead. That is the default and the intended public
   deployment.

A visitor therefore cannot start a run, cancel one, kill a worker, inject a
fault, register a workflow, or reach the database. Everything that changes
ReLab's state is a CLI verb that needs a DSN.

`RELAB_API_URL` is read on the server and is never prefixed `NEXT_PUBLIC_`, so
it is not inlined into the client bundle. The browser never learns the API's
address and never issues a request to it. A live dashboard integration must hold
a viewer token on its server side and send it as a bearer credential. The
browser must never receive that token. See `docs/deployment.md` §7.

The recording is an export of runs from a scratch database created by
`scripts/record-demo.sh`. It contains workflow definitions, event payloads and
handler output from the shipped examples, and nothing else. Regenerate it
against a database that holds anything you would not publish, and you will
publish it.

## Environment and deployment security

- Non-root containers.
- No secrets in the image or in `docker-compose.yml` beyond the local
  development password `relab`, which is a development credential and is
  documented as one. **Change it for anything that is not a laptop.**
- The compose stack publishes 5432 to the host for convenience. Remove that
  mapping for anything shared.

## Incident response

There is no on-call runbook, because there is no hosted service. For a
deployment of your own: the event journal is the record of what happened; the
`relab run inspect` and `relab replay` commands read it; and `docs/reliability.md`
says what each metric means.

## Backup and recovery

**None ships with ReLab.** Durability is exactly PostgreSQL's durability, and
operating ReLab means operating a database — `pg_dump`, WAL archiving, or a
managed service's snapshots. If the database is lost, everything is lost: there
is no second store.

## Security testing

- Every query parameterised, enforced by review and by there being no
  string-building helper to misuse.
- `TestOpenDoesNotLeakPassword` asserts the redaction.
- API tests assert indistinguishable authentication failures, unauthenticated
  probes, request caps and rate limiting.
- `golangci-lint` runs `gosec`-adjacent checks (`bodyclose`, `noctx`,
  `sqlclosecheck`, `rowserrcheck`) on every push.
- The process-level suite kills real binaries, which is a robustness test rather
  than a security one, but it is the reason the system fails safe rather than
  silently.

**Not done:** fuzzing of the YAML parsers, dependency scanning, penetration
testing.

## Residual risks

Stated plainly, in the order they would matter:

1. **Bearer tokens are long-lived shared secrets.** There is no expiry,
   revocation endpoint, individual identity, or audit attribution. Mitigation:
   rotate the environment value and restart control planes; use an
   authenticating proxy when individual identities matter.
2. **The rate limiter is per process and in memory.** Restarts refill it and
   replicas multiply it. Mitigation: enforce a deployment-wide limit at the
   ingress when the network is untrusted.
3. **Handlers are unsandboxed.** A workflow author has the worker's full
   privileges. Mitigation: only run workflows you trust, and run workers with
   least privilege.
4. **Handler output is stored verbatim.** A handler returning a secret persists
   it. Mitigation: do not return secrets.
5. **No retention policy.** The journal grows without bound, so a long-running
   deployment accumulates whatever handlers put in it. Mitigation: delete old
   runs.
6. **No dependency scanning in CI.** A vulnerable dependency would not be caught
   automatically. Mitigation: run `govulncheck` and `npm audit` before
   releasing.
7. **The dashboard carries a build-time `postcss` advisory** that cannot be
   resolved without moving `next` outside its supported range. It is reachable
   only by a source map in CSS this repository writes itself, not by anything a
   visitor sends. Mitigation: it clears when Next ships a release that bumps it.
8. **The recording is whatever the export contained.** It is checked into the
   repository and served publicly, so anything in the database it was made from
   is public. Mitigation: record against a scratch database, which is what
   `scripts/record-demo.sh` documents.
9. **OTLP is insecure by default.** Traces could be read on a hostile network.
   Mitigation: keep the collector local.

None of these is a bug. All of them are consequences of v1's scope, and every
one has a stated mitigation.
