# Support

ReLab is maintained by one person in their own time. That sets what support
can honestly mean here, so this file says it plainly rather than implying a
service level that does not exist.

## Where to go

| You want to | Go to |
|---|---|
| Understand what ReLab does | The [README](README.md), or the [live dashboard](https://relabca.vercel.app) |
| Understand what it guarantees | [`docs/reliability.md`](docs/reliability.md) — the authority, including the limitations |
| Understand a word the dashboard used | The [glossary](https://relabca.vercel.app/glossary) |
| Report a bug | [A bug issue](https://github.com/alexou8/relab/issues/new/choose), with the run's journal attached |
| Report a security issue | **Not a public issue.** [SECURITY.md](SECURITY.md) |
| Propose a feature or a change in scope | An issue first, before the pull request |
| Contribute a change | [CONTRIBUTING.md](CONTRIBUTING.md) |

## What to expect

- Issues are read. They are not guaranteed a fix, and there is no response-time
  commitment.
- Security reports are the exception, and take priority over everything else.
- A bug report carrying the run's event history (`relab run inspect <run-id>`,
  or `relab export <run-id>`) is far more likely to be actionable than a
  description, because the journal usually says what happened without a
  reproduction.
- Questions that turn out to be documentation failures are treated as bugs in
  the documentation.

## What is out of scope

ReLab is positioned as a self-hosted reliability-testing tool for development,
staging and CI. Help running it as a production workflow engine is out of scope,
because it is not one: no durable timers, no signals, no queries, no versioned
workflow code, and no API authentication in v1.
