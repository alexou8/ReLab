# Security

This document describes ReLab's actual security posture, including what it does
not do. ReLab v1 has **no authentication and no authorisation**. Anyone who can
reach the API can read every run, and anyone who can reach the database can do
anything.

That is a scope decision, not an oversight, and this document exists so nobody
has to discover it by reading the code.

## Threat model

**Intended deployment:** a developer's machine, or a CI runner, on a private
network. ReLab is a tool you point at your own workflows to find out whether
they recover.

**Explicitly out of scope for v1:** the public internet, multi-tenancy, any
deployment where the operator does not trust everyone who can reach the port.

| Actor | Assumed | Consequence |
|---|---|---|
| Operator | Trusted | Full access by design |
| Workflow author | Trusted | Handlers run as the worker process, unsandboxed |
| Anyone reaching the API | **Trusted, because nothing stops them** | Full read access |
| Anyone reaching the database | Trusted | Full read/write |
| Network between processes | Trusted | Postgres TLS is the DSN's business |

## Trust boundaries

There is one boundary that ReLab enforces: **the process boundary around a
worker**. A worker executes handler code and can do anything that code can do.
There is no sandbox, and none is planned — the workflows are the user's own, and
a sandbox that a workflow author can trivially step outside is worse than an
honest statement that there is none.

Everything else — API, coordinator, database — is inside one trust zone.

## Authentication

None. There is no login, no API key, no token.

**If you expose the API**, put it behind something that authenticates:
a reverse proxy with mTLS or OIDC, a service mesh, or an SSH tunnel. Do not put
ReLab on a public address and hope.

## Authorisation

None. Every caller can read every run.

## Session management

Not applicable — no sessions exist.

## Credential handling and secret management

The **database connection string is the only secret ReLab has.**

- It is read from `RELAB_DSN` rather than a flag, so it does not appear in the
  process table.
- It is never logged.
- It is redacted from driver error messages. pgx echoes the connection string in
  several failure modes, and those messages reach logs and CLI output;
  `internal/store/redact.go` strips both the whole DSN and, for URL-form DSNs,
  the password alone, because a truncated DSN inside a driver message would slip
  past a whole-string match. `TestOpenDoesNotLeakPassword` asserts it.

ReLab has no secret store and needs none.

## Input validation

| Input | Validation |
|---|---|
| Workflow YAML | Strict: unknown fields rejected, cycles detected, names restricted to `[A-Za-z0-9_-]` and 64 characters, retry policies checked. Every problem reported at once |
| Scenario YAML | Same strictness; unknown fault types and trigger points rejected |
| Run ids in URLs | Parsed as UUIDs before use |
| Query parameters | `limit` clamped to 500; unparseable values fall back to defaults |
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

None. A caller that can reach the API can hammer it. In the intended deployment
that caller is the operator.

The API does bound resource use per request: a 30-second handler timeout, a
`limit` clamped to 500, and `ReadHeaderTimeout` on the HTTP server (which is
what closes the slowloris hole).

## File upload

There is none. Artifacts are recorded by content hash; ReLab never receives or
stores file bytes.

## API security

- Timeouts on read headers, read, write and idle, so a stalled client cannot
  hold a connection and a goroutine indefinitely.
- `Recoverer` middleware, so a panic in one handler does not take the server
  down.
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
The dashboard, if exposed, should be behind a proxy that sets them.

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
- `golangci-lint` runs `gosec`-adjacent checks (`bodyclose`, `noctx`,
  `sqlclosecheck`, `rowserrcheck`) on every push.
- The process-level suite kills real binaries, which is a robustness test rather
  than a security one, but it is the reason the system fails safe rather than
  silently.

**Not done:** fuzzing of the YAML parsers, dependency scanning, penetration
testing.

## Residual risks

Stated plainly, in the order they would matter:

1. **No authentication.** An exposed API is fully readable. Mitigation: do not
   expose it; put a proxy in front if you must.
2. **Handlers are unsandboxed.** A workflow author has the worker's full
   privileges. Mitigation: only run workflows you trust, and run workers with
   least privilege.
3. **Handler output is stored verbatim.** A handler returning a secret persists
   it. Mitigation: do not return secrets.
4. **No retention policy.** The journal grows without bound, so a long-running
   deployment accumulates whatever handlers put in it. Mitigation: delete old
   runs.
5. **No dependency scanning in CI.** A vulnerable dependency would not be caught
   automatically. Mitigation: run `govulncheck` before releasing.
6. **OTLP is insecure by default.** Traces could be read on a hostile network.
   Mitigation: keep the collector local.

None of these is a bug. All of them are consequences of v1's scope, and every
one has a stated mitigation.
