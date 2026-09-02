# 0003. One task row per step, with an append-only attempt ledger

**Status:** accepted

## Context

A task that is retried needs to record every attempt. Two shapes are available:
a row per attempt, or a row per step whose attempt counter advances in place.

There is a second requirement pulling on the same design. ReLab has to be able
to prove that two workers never executed the same attempt of the same task at
the same time. A metric counting duplicates is not proof; it only counts what
the code that increments it noticed.

## Decision

`tasks` holds one row per workflow step, unique on `(run_id, task_name)`, with
`attempt` advancing in place. A separate append-only `task_attempts` table has
primary key `(task_id, attempt)`, and a worker inserts into it when it begins
executing.

## Consequences

- The scheduler's working set is proportional to the workflow, not to the number
  of retries. The claim query's partial index stays small.
- Concurrent execution of one attempt is impossible rather than merely counted:
  the second insert fails on the primary key. The M2 acceptance test asserts on
  exactly that.
- A legitimate at-least-once duplicate — a lease expiring while the original
  worker is still running — always carries a new attempt number, so it does not
  collide. That distinction is the point: the constraint catches scheduler bugs
  without catching the behaviour the system is documented to have.
- Per-attempt history lives in the event journal and in `task_attempts`, not in
  the tasks table. Anything wanting the history of an attempt reads the journal.

## Rejected

**A row per attempt.** Keeps attempt history in one place, but makes "the
current state of this step" a query with a subselect for the newest attempt,
which is the query the scheduler runs constantly.
