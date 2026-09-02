# 0006. A run's terminal event is its last, enforced at the write path

**Status:** accepted

## Context

Replay reduces a run's events into state. For the result to mean anything, a
reader has to be able to tell that it has the *whole* history — that no further
event will arrive to change the conclusion.

Decision 0002 gave gapless sequence numbers, which answers "is anything
missing". It does not answer "is anything still coming". A run that has
succeeded and then acquires a `WORKER_LOST` event has a history that keeps
changing after the conclusion was drawn, and a replay run before and after that
append reconstructs two different stories, both truthfully.

The M4 acceptance test found exactly this: the reaper announced a lost worker
against every run that worker had touched, including runs that had already
finished.

## Decision

An event may not be appended to a run whose `completed_at` is set.
`event.Append` enforces it in the same statement that allocates the sequence
number:

```sql
UPDATE runs SET event_seq = event_seq + 1
WHERE id = $1 AND completed_at IS NULL
RETURNING event_seq
```

An append to a closed run returns `event.ErrRunClosed`.

Callers that close a run therefore append the terminal event **first** and set
`completed_at` afterwards, within the same transaction. Because the terminal
event is no longer written after an optimistic `UPDATE ... WHERE status IN (...)`
that could report zero rows, those callers take the run row's lock with
`SELECT status ... FOR UPDATE` before writing anything, so two callers observing
completion at once still produce exactly one terminal event.

## Consequences

- Replay can treat a terminal event as proof the history is complete, and
  `replay.Reduce` reports `ErrEventAfterTerminal` for a journal that violates it.
- The reaper no longer narrates runs that are over. A task left holding a lease
  by a cancelled or completed run is released quietly: it did not fail, its run
  ended around it, and there is nothing about it that belongs in a history that
  is already finished.
- A caller with a genuine bug gets a loud error instead of a silently corrupted
  journal, which is the trade this project exists to make.

## Rejected

**Enforcing it by convention and code review.** It is a one-line guard on a
statement that already runs on every append, and the failure mode it prevents is
invisible until someone replays a run months later.

**Letting the reducer tolerate post-terminal events.** Moves the problem to
every consumer of replay output, and makes "what did this run do" a question
with more than one right answer.
