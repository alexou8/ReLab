# SKILLS.md — engineering capabilities this project needs

The capabilities below are the ones ReLab actually exercises. Each says when it
applies, what standard to hold, what to prefer, and the mistake that has already
been made or nearly made here.

Skills that would pad this list — AI/ML, mobile — are absent because the
project does not use them. Interface design is on the list because the
dashboard is how most people will meet this project, and a page that does not
communicate is a page the engine's argument never reaches.

---

## Distributed systems reasoning

**When:** any change to leases, the reaper, claiming, or worker liveness. This
is the core of the product.

**Standard:** reason about the failure case first. For every mechanism, answer:
what happens if the process dies here? What if it is alive but unreachable? What
if two of them do this at once?

**Prefer:** making a failure impossible (a constraint) over detecting it (a
check) over counting it (a metric). Prefer the database's locks over
application-level coordination.

**Avoid:** assuming a process that stopped responding has stopped working. That
assumption is what produces duplicate execution, and it is why a worker becomes
SUSPECT before LOST.

**Mistake made here:** binding a task's execution deadline to the lease it was
claimed under. It looks careful — do not work past your claim — and it makes
lease renewal pointless, guaranteeing failure for any task longer than one
lease. Found by the process-level suite; see `docs/decisions/0004`.

---

## Database engineering (PostgreSQL)

**When:** any schema change, any new query, anything about concurrency.

**Standard:** every query parameterised. Every index tied to a named query.
Every constraint justified in `DATA.md`. Transactions short, with locks taken in
a consistent order.

**Prefer:** `pgx` directly, hand-written SQL, partial indexes for hot queries
(`WHERE status = 'READY'`), `FOR UPDATE SKIP LOCKED` for queue semantics,
`ON CONFLICT DO NOTHING ... RETURNING` over read-then-write.

**Avoid:** an ORM. Avoid triggers, views and stored procedures — behaviour that
lives in the database is behaviour the tests cannot see. Avoid adding an index
to fix a slow query before knowing which query is slow.

**Mistake nearly made here:** returning the caller's own JSON bytes from the
idempotency ledger while other callers got the jsonb round-trip. The values
agreed semantically and differed byte for byte, which would have surfaced as a
mysterious comparison failure much later.

---

## Go backend development

**When:** all of it.

**Standard:** contexts propagated and respected; goroutines that take a context
and return when it ends; errors wrapped with `%w` and matched with
`errors.Is`/`As`; an error either logged or returned, never both.

**Prefer:** small interfaces declared by the consumer (`store.Conn`,
`engine.TriggerPoints`), plain structs, standard-library solutions.

**Avoid:** `panic` for anything recoverable. Avoid package-level mutable state —
the one singleton here (the metric set) is documented as to why. Avoid
goroutines without a clear owner and a clear exit.

---

## API design

**When:** any change under `internal/api`.

**Standard:** each endpoint has a reason to exist beyond being easy to write.
Predictable shapes, correct status codes, timeouts on everything, no internal
detail in an error body.

**Prefer:** `net/http` with a router and nothing else. Read endpoints that map
to one question a human actually asks.

**Avoid:** an endpoint added because a page needed a field. Add the field.

---

## Testing

**When:** always. This project's credibility is its test suite.

**Standard:**
- Real PostgreSQL for anything about concurrency or recovery. No mocks.
- Real processes and real `SIGKILL` for anything about crashes.
- Failure messages that say what is being protected, not just what differed.
- Both the legal and the illegal cases for every state transition.

**Prefer:** a test that would fail if the feature were removed. Deliberately
break the mechanism and confirm the test goes red — that was done for the reaper
and is recorded in `PROGRESS.md`.

**Avoid:** a test that can pass for the wrong reason. Two tests here started two
workers and relied on a particular one winning the claim; both passed whenever
the other won, proving nothing. Start one, wait for the state that proves it
won, then start the second.

---

## Security engineering

**When:** anything touching credentials, input parsing, or the API surface.

**Standard:** parameterised queries without exception; no credential in a log or
an error; input validated at the boundary; error responses that say a category
and not a cause.

**Prefer:** stating what is *not* protected. `SECURITY.md` lists six residual
risks, each with a mitigation, because a security document listing only
successes is a marketing document.

**Avoid:** implying protection that does not exist. v1's authentication is
shared bearer tokens with no accounts, no expiry and no audit identity, and the
documents say exactly that rather than "authenticated".

---

## Observability

**When:** adding a metric, a span, or a log line.

**Standard:** traces, metrics and logs share identifiers. A metric has a
documented reading — what does a non-zero value mean? Label names spelled once,
in helpers, so two call sites cannot disagree about what a series is keyed by.

**Prefer:** percentiles over means for anything latency-shaped. Instrumentation
that is nil-safe, so a process whose telemetry setup failed still does its job.

**Avoid:** counting something because it is countable. Avoid deriving a
reliability claim from a counter: a counter records what the code that
increments it noticed, and the journal records what happened. Assertions read
the journal.

**Mistake made here:** telemetry setup failing took the whole process down. An
observability problem became an outage.

---

## CLI and developer experience

**When:** adding a command or a flag.

**Standard:** a machine-readable `--json` and a human-readable default. A
non-zero exit when something failed, without anyone parsing output. Help text
that says why, not only what.

**Prefer:** making the right thing automatic. `relab test` decides from the
scenario whether it needs spawned workers, rather than leaving the caller to
remember.

**Avoid:** a flag whose wrong value fails silently.

---

## Documentation

**When:** any change that affects behaviour a document describes.

**Standard:** one canonical statement of each fact. `docs/reliability.md` is the
authority on guarantees; when it and another document disagree, the other is a
bug. Every reliability claim names the test that backs it.

**Prefer:** stating limits before benefits. The README puts Limitations above
Benchmarks deliberately.

**Avoid:** documentation written from the plan rather than from the code. Two
things in `DATA.md` deviate from the original design sketch, and both say so.

---

## Frontend and UI

**When:** changes under `web/`. It is a debugging surface, not the product, and
should stay small.

**Standard:** server components, no client-side fetching, no credential in the
page. Every user-facing state considered: loading, empty, error, and the state
where the API is simply not running — which is the common one.

**Prefer:** semantic HTML, real tables, plain visual design that does not compete
with the data.

**Avoid:** conveying status by colour alone. Avoid animation. Avoid adding
client-side state to a page that has none.

---

## Accessibility

**When:** any dashboard change.

**Standard:** status as a word as well as a colour; visible focus rings; one
`<h1>`; labelled navigation; `role="alert"` on errors; wide tables scrolling in
their own container rather than the page.

**Avoid:** removing the focus outline. Avoid a colour-only signal — someone will
read this page while an incident is happening, and that is the worst possible
time to lose information to a hue.

---

## Performance and benchmarking

**When:** publishing any number.

**Standard:** measured, with the hardware, the versions and the parameters
recorded next to it. Percentiles, not means.

**Prefer:** committing the raw CSV alongside the summary, so a reader can check
the arithmetic.

**Avoid:** including runs that never went wrong in a recovery percentile — their
zeros drag the median to zero and make recovery look instantaneous.

---

## CI/CD and DevOps

**When:** touching the workflows, the Dockerfiles or compose.

**Standard:** the corpus is discovered from the directory, so adding a scenario
adds a CI case. Non-root containers. Reproducible builds.

**Avoid:** folding the slow suites into the main test job, where a timeout would
hide them. Avoid `deploy.replicas` for the workers — the demo depends on being
able to kill one by name.


---

## Interface design (the dashboard)

**When:** any change under `web/`.

**Standard:** every element answers "does this help someone understand,
investigate, or reason about a failure?" The dashboard is a read-only debugging
surface, and its job is to make one run's history legible to someone who has
never seen ReLab, without making it less true.

**Prefer:** plain language over the event type, with the event type still
there. A reader checking the page against `relab replay` has to be able to find
the same row by name, and a reader who cannot tell good news from bad from an
identifier is not served by hiding the identifier either. Prefer a `<details>`
disclosure over a modal: no JavaScript, it prints, it is keyboard-operable, and
several can be open at once. Prefer a link over a control, so a filtered view
is a URL that can be sent to whoever is being asked to look.

**Avoid:** decoration with no job. No gradients, no glow, no floating cards, no
KPI tiles that count nothing anyone asked about. Avoid conveying a state by
colour alone: every status here carries a word, a hue and a glyph, so it
survives a colour-blind reader and a monochrome print.

**Decided here:** monospace is what the machine recorded, sans is what a person
says about it. Event types, ids, sequence numbers, timestamps and payloads are
always mono; labels, explanations and verdicts are always sans. The distinction
between evidence and interpretation is the project's whole claim, so the type
carries it on every page.

**Mistake avoided here:** deriving a friendly name for an unknown event type
from the shape of its identifier. That is the permissive reading `internal/event`
refuses to do, and it would let a future type be announced as a recovery on the
strength of its name. An unknown type renders as itself.

**Skills used:** `frontend-design` (anthropics), `web-design-guidelines`
(vercel-labs) and `ui-ux-pro-max` (nextlevelbuilder) are installed and pinned in
`skills-lock.json`. Their recommendations are input, not instruction: the
terminal palette, the IBM Plex pairing and the character-grid layout were chosen
against their defaults, and the reasons are in `web/src/app/globals.css`.
