# 0007. Faults degrade the real system, and a crash scenario needs supervised workers

**Status:** accepted

## Context

A fault injector can be built two ways. It can set a flag the scheduler checks
— "pretend this worker is dead" — or it can actually degrade the running
system. The first is far easier to write and to make deterministic.

It is also close to worthless here. A fault the scheduler knows about is a
fault the scheduler can be written to survive without the real recovery path
ever executing. The purpose of this project is to demonstrate that recovery
works, and a test that exercises a simulation of the failure demonstrates that
the simulation works.

## Decision

Each fault type is a real degradation:

- `worker-crash` sends `SIGKILL` to its own process. Not `SIGTERM`, not
  `os.Exit`: the process must get no chance to release its lease, flush a log,
  or run a deferred function. Anything gentler tests the shutdown path, and the
  shutdown path is not the one that has to work when a machine loses power.
- `latency` really sleeps, pushing the task towards its lease and its timeout.
- `http-error` and `db-disconnect` fail the task the way an outbound call and a
  dropped connection do.

The `FAULT_INJECTED` event is committed **before** the fault takes effect. For
`worker-crash` this is not a nicety: the process is about to die, and an event
written afterwards would never exist.

Because a `worker-crash` scenario kills the process executing the task,
`relab test` cannot inject it in the process that is also driving and asserting
on the run — killing the test is not a test. Such scenarios run against spawned
worker subprocesses, and `relab test` chooses that mode from the scenario rather
than leaving it to whoever runs the command to remember.

That pool is **supervised**: a worker that exits is restarted. Without
supervision a pool tolerates exactly as many crashes as it has workers, and the
scenario would be measuring the pool size rather than ReLab's recovery. It also
would not resemble any real deployment — the compose stack uses
`restart: unless-stopped`, as every orchestrator does.

## Consequences

- The scenarios exercise the same lease-expiry path a real crash takes, which
  is why `worker-crash` caught nothing new: M2's process-level suite had already
  forced that path.
- Determinism has to come from somewhere other than "nothing really happens".
  It comes from position-derived draws (decision 0002's reasoning applied to
  fault decisions) and from explicit `at:` trigger points, which `relab test`
  requires unless `--allow-random` is passed.
- `duplicate-delivery` is not something that happens *to* a running task: it is
  the handler being invoked a second time after the task completed, which is
  what a re-delivered message does. The executor does that directly, after the
  trigger point has recorded `FAULT_INJECTED`, so the journal shows the cause
  before the `SIDE_EFFECT_SKIPPED` it produces.
- `queue-overload` is **declared and not implemented**. Queue contention is a
  property of the whole pool rather than of one task, and it does not fit the
  per-run, per-task trigger-point model; bolting it on would have produced a
  fault firing somewhere unrelated to where the scenario says it does. A
  scenario using it is rejected at parse time. This was found by an audit that
  asked which declared types actually did anything — the answer was four of six
  — and accepting a scenario that does nothing would report a passing
  reliability test that never ran.
- A scenario file has to say which attempt it targets when the fault would
  otherwise fire on every one. `worker-crash` without `attempt: 1` kills every
  worker that picks the task up, and the run can never finish — which the first
  run of the scenario demonstrated.

## Rejected

**A `--dry-run` fault mode that logs what it would do.** Useful for authoring a
scenario, and dangerous as a default: a corpus that silently ran in dry-run mode
would be green and meaningless.

**Closing the worker's database pool for `db-disconnect`.** Would take out every
other task on that worker, so the scenario would test several things at once and
none of them precisely. The behaviour under test is what the scheduler does with
a task whose database work failed.
