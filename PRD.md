# Product requirements

## Vision

A developer should be able to prove that their distributed workflows recover
when things break — not argue about it in a design review, and not find out in
production.

## Problem

Workflow engines are tested on the happy path. The failure paths — a worker
dying mid-task, a lease expiring under a live worker, a retry repeating a side
effect, a coordinator restarting with work in flight — are the ones that
actually cost money, and they are the hardest to exercise deliberately.

Three things make this hard today:

1. **Failures are hard to cause on purpose.** "Kill a worker at exactly the
   moment it has performed a side effect but not acknowledged it" is not
   something a test suite normally expresses.
2. **Recovery is hard to observe.** After the fact you have logs from several
   processes and no single ordered account of what happened.
3. **Reliability claims are unfalsifiable.** "We handle worker failure" is
   either backed by a test that kills a real process, or it is a hope.

## Target users

**Primary: the backend engineer who owns a workflow system.** They already have
a queue and workers. They want to know whether a specific failure mode is
handled, and they want the answer in CI rather than in an incident.

**Secondary: the engineer evaluating a design.** They want to compare "what
happens if we retry here" against "what happens if we don't", with numbers.

**Not a target:** teams wanting a production workflow engine. That is Temporal
and it is a different product, which the README says in its first paragraph.

## Goals

1. Define a multi-step workflow and run it across real worker processes.
2. Record a complete, ordered, gapless history of everything that happened.
3. Kill a worker mid-task and watch the task recover, automatically.
4. Prove no duplicate side effect resulted.
5. Replay a run's history and reconstruct its state.
6. Inject five named failure modes deterministically.
7. Assert on recovery from CI, with a non-zero exit on failure.
8. Publish benchmarks that were measured rather than claimed.

## Non-goals for v1

Authentication, multi-tenancy, rate limiting, quotas, billing, Kubernetes or
Helm packaging, a hosted service, sandboxing of arbitrary user code, a custom
broker, a custom storage engine, multi-region, mobile, a heavy dashboard,
differential replay across workflow versions, and production trace import.

Each is excluded because it would enlarge the surface without making the core
claim — *recovery works, and here is the proof* — any more credible.

## Core features (MVP — all shipped)

| Feature | Delivered by |
|---|---|
| Workflow definition as versioned, hashed YAML | `internal/workflow` |
| DAG scheduling: linear, branching, fan-out, fan-in | `internal/engine` |
| Worker pool with leases, heartbeats and renewal | `internal/worker` |
| Automatic recovery from worker death | the reaper, `internal/engine` |
| At-least-once delivery with an idempotency ledger | `internal/idem` |
| Retry with exponential backoff and deterministic jitter | `internal/retry` |
| Dead-letter queue | `internal/engine` |
| Append-only event journal, gapless and versioned | `internal/event` |
| Replay to logical state, with divergence reporting | `internal/replay` |
| Five deterministic fault types | `internal/fault` |
| Reliability assertions with CI exit codes | `internal/assert`, `relab test` |
| Traces, metrics and correlated logs | `internal/telemetry` |
| Benchmark harness with committed results | `internal/bench` |
| Read-only dashboard | `web/` |

## Secondary features (shipped, not core)

`relab bench`, the HTTP read API, `relab run cancel`, `--json` on every command,
`relab workers`.

## Post-MVP

- A broker behind the `queue.Queue` interface, for throughput beyond one
  Postgres instance.
- A fixture adapter so external API responses are reproducible under replay.
- Conditional edges (`when:`) in workflow definitions.
- Differential replay across workflow versions.
- Retention and archival for the journal.

## Future / optional

Network partition and clock-skew faults, message reordering, corrupted-output
faults, a hosted service, and Kubernetes packaging.

## User stories

- *As a backend engineer,* I define a four-step pipeline in YAML and run it with
  one command, so I can see the whole thing work before I involve a worker pool.
- *As a backend engineer,* I kill a worker mid-task and see the task picked up by
  another one, so I know recovery is real and not aspirational.
- *As a backend engineer,* I write a scenario file that crashes a worker at a
  named point and assert `duplicate_effects: 0`, so a regression in the
  idempotency ledger fails my build.
- *As an on-call engineer,* I open a run in the dashboard and read its timeline
  in order, so I can see what happened without correlating four processes' logs
  by timestamp.
- *As a reviewer,* I run `docker compose up` and see a recovery in under five
  minutes, so I can evaluate the project without reading it.

## Primary workflows

**Prove recovery**
```
relab workflow register examples/data-pipeline.yaml
relab run examples/data-pipeline.yaml --detach
docker compose kill worker-2         # while it is running
relab run inspect <run-id>           # TASK_LEASE_EXPIRED, TASK_REQUEUED, RUN_SUCCEEDED
```

**Assert it in CI**
```
relab test examples/data-pipeline.yaml --scenario examples/scenarios/worker-crash.yaml
```

**Replay it**
```
relab replay <run-id> --diff
```

## Functional requirements

1. A workflow definition is validated at registration, reporting every problem
   at once.
2. Registering a different definition under an existing `(name, version)` is
   refused.
3. Every state change and the events describing it are written in one
   transaction.
4. A run's event sequence is gapless; its terminal event is its last.
5. Two workers never execute the same attempt of the same task concurrently.
6. A task whose lease expires is requeued if attempts remain, dead-lettered if
   not.
7. A worker is SUSPECT after 3 missed heartbeats, LOST after 5; **one missed
   heartbeat never means failure.**
8. An effect recorded under an idempotency key is not performed again.
9. Replay refuses a corrupt journal rather than reconstructing a plausible
   wrong answer.
10. `relab test` exits non-zero when any assertion fails.

## Non-functional requirements

| Requirement | Target | Status |
|---|---|---|
| Reviewer running time | `docker compose up` to a visible recovery in < 5 min | Met |
| Recovery latency | Bounded and documented, not "fast" | Met (`docs/reliability.md`) |
| Test suite | Runs in CI on every push, including process kills | Met |
| Throughput | Measured and published, not claimed | Met (`docs/benchmarks.md`) |
| Single binary | One artifact for server and workers | Met |

## Accessibility requirements

The dashboard is a debugging tool that someone reads under stress, so:

- Status is always a **word**, never only a colour. A status a reader can only
  get from a hue is one colour-blind readers cannot get at all.
- Visible focus rings on every interactive element.
- Semantic HTML: real `<table>` markup with `<th>` headers, one `<h1>`, `<nav>`
  labelled, errors in `role="alert"`.
- Wide tables scroll inside their own container rather than the page.
- Light and dark are both honoured via `prefers-color-scheme`.
- No animation, so nothing needs a reduced-motion escape.

## Security requirements

v1 has no authentication and is intended for a private network. The full threat
model, the residual risks and their mitigations are in `SECURITY.md`. The
requirement that *is* met: no credential is ever logged, and the DSN is redacted
from driver errors.

## Performance requirements

The requirement is that numbers are measured and published with the hardware
they were measured on — not that they hit a particular figure. See
`docs/benchmarks.md`.

## Success metrics

1. A reviewer reaches a visible recovery within five minutes of `docker compose
   up`.
2. The scenario corpus passes on every push, and deliberately breaking recovery
   makes it fail.
3. Every reliability claim in the README names the test that backs it.
4. Every benchmark number in the README was produced by `relab bench` on stated
   hardware.

## Constraints

- Go and PostgreSQL only. No broker, no second datastore.
- One binary.
- Fault injection must be deterministic enough for CI.
- The reducer must be pure.

## Assumptions

- Handlers are written by people who can be told to use `Do` for side effects.
- Operators run PostgreSQL 16 or later.
- Runs are finite and bounded in size; the journal fits in one database.
- Wall-clock time is roughly monotonic across the fleet. ReLab does not depend
  on synchronised clocks for correctness — leases are evaluated by the database
  — but a wildly skewed worker will report confusing timings.

## Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Postgres becomes the throughput ceiling | Limits scale | Documented with numbers; `queue.Queue` interface for a broker later |
| "Deterministic" faults are not, under load | Corpus becomes flaky | Position-derived draws; explicit trigger points required in CI |
| Handlers do effects outside `Do` | Duplicate effects | Documented in the SDK; `SIDE_EFFECT_SKIPPED` makes the working path visible |
| The journal grows unbounded | Operational | Named as a v1 gap in `DATA.md` |
| Confused with a production engine | Wrong expectations | Stated in the README's first paragraph and in `docs/reliability.md` |

## Future considerations

The `queue.Queue` interface exists so a broker can be added without changing the
engine. The payload version field exists so event schemas can evolve. Neither is
used in v1, and both are cheap enough to be worth having.
