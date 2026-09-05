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

- A glossary in the dashboard: every event type in plain language beside the
  type itself, the terms the overview uses, and what each status means.
- A recovery flow on the dashboard overview — the six stages from lease to
  `RUN_SUCCEEDED`, each naming the event that records it.
- "Who this is for" and "ReLab is not" on the overview, so the boundary appears
  in the first screen rather than in the README alone.
- `LICENSE` (Apache-2.0), `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `SUPPORT.md`,
  issue and pull-request templates, and a vulnerability reporting policy in
  `SECURITY.md`.
- `.codex/` orchestration configuration for the agents developing this
  repository, described in `docs/orchestration.md`.

### Fixed

- A malformed reply from a configured control plane took every dashboard page
  down with a 500. Response shapes are now checked at the API boundary, and a
  body the dashboard cannot read renders the error state instead — distinguished
  from a control plane that could not be reached at all.

### Changed

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
