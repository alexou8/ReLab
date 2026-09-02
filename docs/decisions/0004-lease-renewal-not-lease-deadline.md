# 0004. Execution is bounded by the step timeout, not by the lease deadline

**Status:** accepted

## Context

A worker claims a task under a lease that expires at a fixed time, and a
renewal loop extends that lease while the task runs. The first implementation
also used the lease deadline as the deadline for executing the handler, on the
reasoning that a worker should not keep working past the point where its claim
was valid.

That reasoning is wrong, and the process-level crash test found it. Binding
execution to the deadline the task was *claimed* under makes renewal
pointless: the handler is cancelled at the original expiry no matter how many
times the lease has since been extended. Every task taking longer than one
lease duration fails, retries, and fails again until its attempts run out. The
symptom in the journal is a run that dead-letters after three identical
`TASK_LEASE_EXPIRED` events with nothing in between to explain them.

## Decision

Execution is bounded by the step's timeout (`Step.Timeout`, defaulting to
`Timing.TaskTimeout`) and by nothing else.

The lease is a claim the renewal loop keeps alive, not a budget for the work.
When the renewal loop finds it did not renew a task — because the reaper
declared this worker lost and handed the task on — it cancels that task's
execution, freeing the slot instead of spending the rest of the timeout on a
result that will be discarded.

`RenewLease` returns the ids it renewed rather than a count, because the worker
needs to know *which* tasks it lost, not how many.

## Consequences

- A long task runs to completion across arbitrarily many lease periods, which
  is the behaviour the lease/renewal design was for in the first place.
- The lease duration is now purely a recovery-latency knob: it decides how long
  a task waits after a worker dies, and nothing about how long work may take.
- A worker that cannot reach the database does not cancel its work on the first
  failed renewal. A failed *call* is not proof the lease is gone, and
  cancelling on it would throw away tasks the worker still owns. If the leases
  really were lost, recording the outcome fails with `ErrLeaseLost` anyway.
- The step timeout is now the only thing bounding a runaway handler, so it has
  to be set. It defaults to five minutes.

## Rejected

**Recomputing the deadline on every renewal.** Would work, but it makes the
handler's deadline a moving target that depends on database latency, and it
still conflates two independent questions: how long may this work take, and who
owns it right now.
