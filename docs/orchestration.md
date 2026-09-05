# Multi-agent orchestration

How this repository is developed by more than one agent, how the second
orchestrator authenticates, and what the baseline was when the arrangement was
set up. The coordination rules themselves live in `AGENTS.md`; this file is the
setup and evidence record.

## The arrangement

Claude Opus is the primary orchestrator: scope, product direction, frontend
information architecture, UX, integration, and the final call on any
disagreement. GPT-5.6 Sol, reached through the official OpenAI Codex plugin for
Claude Code, is the engineering co-orchestrator: it challenges plans, coordinates
subagents, and implements cross-cutting backend work. Four project-scoped
GPT-5.6 Luna agents do narrow work under Sol — `luna-explorer` (read-only
investigation), `luna-implementer` (one bounded change), `luna-tester`
(verification), and `luna-risk-reviewer` (read-only security and concurrency
review).

The rule that keeps it from producing conflicting edits: **one writer per file
or subsystem at a time.** Parallel agents may read the same code. They may not
edit overlapping paths.

## Configuration in this repository

| File | Contains |
|---|---|
| `.codex/config.toml` | Parent model, reasoning effort, subagent defaults, concurrency cap |
| `.codex/agents/*.toml` | The four Luna agent definitions |
| `AGENTS.md` | Responsibility split, task-packet rules, secret handling |

Neither file carries a credential, and neither is intended to. Nothing in the Go
module or in any build or test depends on them.

## Authentication

Codex signs in with **Sign in with ChatGPT**, so usage is charged to the ChatGPT
plan. Do not configure `OPENAI_API_KEY`, `CODEX_API_KEY`, `--with-api-key`, or a
custom provider for this project.

Install and sign in:

```bash
npm install -g @openai/codex
codex login              # or: codex login --device-auth, in a headless environment
codex login status
```

For a headless or cloud environment, put this in the **user-level**
`~/.codex/config.toml` — not in this repository:

```toml
cli_auth_credentials_store = "file"
```

The login is then cached in `~/.codex/auth.json` and refreshed in place. That
file is a credential:

- It is never committed here, to any fork, or to any other repository.
- It is never pasted into a prompt, an issue, a log, CI output, a build
  artifact, or a shared cache.
- `.gitignore` excludes `**/auth.json` so an accidental write into the working
  tree cannot be staged.

A fully ephemeral cloud session — a fresh machine with no persistent private
home directory — cannot reuse a previous login. The supported answer there is to
run `codex login --device-auth` again in that session. Copying `auth.json` into
the repository or into a setup script is not an alternative; a refreshed token
would still need somewhere secure to live.

## Baseline recorded at setup

Run on the repository state at the time this file was added, before any product
change:

| Check | Result |
|---|---|
| `gofmt -l .` | no output |
| `go build ./...` | passes |
| `go vet ./...` | passes |
| `go test -short ./...` | passes; PostgreSQL-backed suites skip without `RELAB_TEST_DSN` |

Not run in this environment, because they need a live PostgreSQL, a process
supervisor, or a Node toolchain: `make check`, `make scenarios`,
`make crash-tests`, and the dashboard build. CI covers all four
(`.github/workflows/ci.yml`); its result on this branch is the baseline for
those, not a local claim.

No benchmark, coverage, or recovery number is recorded here. The published
numbers stay in `docs/benchmarks.md`, produced by `relab bench` on stated
hardware, and nothing in this file changes them.

## Working method

1. Opus writes a compact task brief: user problem, in and out of scope, relevant
   files, constraints, acceptance criteria, required success and failure tests,
   subagent budget, and whether Sol may write.
2. Sol challenges and decomposes it before editing anything.
3. Opus resolves design and scope, then Sol implements the accepted slice in the
   same thread (`--resume`) until the milestone is done.
4. Focused tests during implementation; the full gate once before merge.
5. `/codex:review` for ordinary changes; `/codex:adversarial-review` only for
   high-risk milestones. The automatic review gate stays disabled.
