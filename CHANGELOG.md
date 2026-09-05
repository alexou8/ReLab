# Changelog

All notable changes to ReLab are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and ReLab aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html) from its first
tagged release onward.

Until `v1.0.0`, the public surfaces below may change in a minor release, and
each such change is listed here:

- the HTTP read API,
- the CLI's commands, flags and `--json` output,
- the workflow and scenario file formats,
- the `sdk` package,
- the database schema, which is migrated forward and never edited in place.

## [Unreleased]

### Added

- **API authentication.** `RELAB_API_TOKENS` configures shared bearer tokens in
  two roles (`viewer`, `operator`); every `/api/v1` endpoint then requires one,
  while `/healthz` and `/readyz` stay open for probes. Tokens are compared as
  SHA-256 digests in constant time, and missing, malformed and unknown tokens
  are answered identically so a caller cannot learn which tokens exist.
- **Fail-closed exposure default.** A server asked to bind a non-loopback
  address with no tokens configured refuses to start, naming both ways out.
  `RELAB_INSECURE_NO_AUTH=true` is the deliberate opt-out, and the compose stack
  sets it because it is a development stack.
- **Request limits.** A body-size cap, a cap on the `limit` query parameter, and
  a token-bucket rate limit per token — or per source address where no tokens
  are configured — answering 429 with `Retry-After`.

- A glossary in the dashboard: every event type in plain language beside the
  type itself, the terms the overview uses, and what each status means.
- A recovery flow on the dashboard overview — the six stages from lease to
  `RUN_SUCCEEDED`, each naming the event that records it.
- "Who this is for" and "ReLab is not" on the overview, so the boundary appears
  in the first screen rather than in the README alone.
- `LICENSE` (Apache-2.0), `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `SUPPORT.md`,
  issue and pull-request templates, and a vulnerability reporting policy in
  `SECURITY.md`.
- `relab demo`: one command that registers a workflow, starts two worker
  processes, kills the one holding the charge step after it has charged and
  before it acknowledged, and narrates what the journal recorded. The workflow
  and scenario are embedded in the binary — the same two files the corpus runs,
  with a test that fails if the copies drift — so a first run needs a database
  and nothing else.
- A release workflow: pushing a `vX.Y.Z` tag re-runs the full gate against the
  tagged commit, then publishes binaries for linux and darwin on amd64 and
  arm64, a SHA-256 checksum file, an SPDX SBOM, and a multi-architecture
  container image at `ghcr.io/alexou8/relab`.
- `docs/operations.md`: what each exported instrument means, alerts worth having
  with the reasoning behind each threshold, backup and restore, and an explicit
  list of what is not measured.
- `docs/guarantees.md`: every public reliability claim beside the test that
  proves it, how to run it, and an explicit list of what no test proves yet.
  `test/docs` fails if the matrix cites a test that does not exist.
- `docs/openapi.yaml`: the read API described, including the health and
  readiness endpoints. `internal/api/openapi_test.go` compares it against the
  structs the handlers return, so a renamed field fails the build.
- `.codex/` orchestration configuration for the agents developing this
  repository, described in `docs/orchestration.md`.

### Fixed

- A malformed reply from a configured control plane took every dashboard page
  down with a 500. Response shapes are now checked at the API boundary, and a
  body the dashboard cannot read renders the error state instead — distinguished
  from a control plane that could not be reached at all.

### Changed

- The dashboard sends `RELAB_API_TOKEN` as a bearer token when reading a live
  control plane. It is read on the server during rendering and never reaches the
  browser. A 401 is reported as a configuration problem here rather than as a
  broken control plane.
- Every document that said "v1 has no authentication" now describes what v1
  actually has, and what it still does not: no accounts, no sessions, no expiry,
  no per-token audit identity.

- `docs/reliability.md` said the duplicate-attempt check runs in every
  process-level crash test. Two of the five call it; corrected, and the retry
  path now cites its own test.
- Corrected claims the implementation does not support: `WORKER_REGISTERED`,
  `WORKER_HEARTBEAT` and `WORKER_SUSPECT` are defined types that are never
  written to a journal; `WORKER_LOST` also covers a deliberate shutdown holding
  a lease; a lease is per attempt, so two workers may run one task under
  different attempt numbers; `http-error` and `db-disconnect` report the failure
  a dependency would have produced rather than degrading a real dependency; and
  `duplicate_effects` is the one assertion that cannot be answered from the
  journal alone.

## Releases

No version has been tagged yet. The first entry below this line will be
`v0.1.0`, and `v1.0.0` waits on the self-hosted boundary described in the
README's roadmap — authentication, bounded queries, retention, and tested
upgrade and rollback.
