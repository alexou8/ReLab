# 0002. Gapless per-run event sequence

**Status:** accepted

## Context

Replay reduces a run's events into state. A reader has to be able to tell "this
is the whole history" from "some of the history is missing", because reducing a
history with a hole in it produces a state that never existed and reports it as
fact.

A PostgreSQL sequence cannot provide this. Sequences are explicitly non-
transactional: a rolled back transaction consumes its number permanently, so
gaps are routine and carry no information.

## Decision

Each run row carries an `event_seq` counter. An append allocates its sequence
number with

```sql
UPDATE runs SET event_seq = event_seq + 1 WHERE id = $1 RETURNING event_seq
```

inside the transaction that writes the event. Reading a run verifies the numbers
are contiguous and returns `event.ErrGap` if they are not.

## Consequences

- A gap means data loss. That is a strong enough statement to build replay on.
- The `UPDATE` takes the run row's lock, so appends to one run serialise. This
  is the intended cost: events within a run are ordered anyway, and runs do not
  contend with each other. Under fifty concurrent appenders to one run the
  serialisation is measurable and correct.
- Appending outside a transaction still yields correct numbers but decouples the
  journal from the state it describes. The `event.Append` documentation says so;
  it is the one misuse the type system cannot prevent.

## Rejected

**A PostgreSQL sequence per run.** Non-transactional, so gaps are meaningless.
Also unbounded DDL: one sequence object per run.

**`SELECT max(seq) + 1` with an explicit lock.** Two statements instead of one,
and it needs its own `SELECT ... FOR UPDATE` on the run row to be safe — which
is the lock the `UPDATE` already takes.
