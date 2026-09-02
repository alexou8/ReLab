# 0005. At-least-once delivery, and what the ledger actually guarantees

**Status:** accepted

## Context

Every workflow engine is asked whether it does exactly-once. The honest answer
for any system whose side effects are external is no, and the useful question is
what it does instead and how large the remaining window is.

## Decision

ReLab delivers at least once. `internal/idem` records each performed effect
under a key derived from `run_id:task_name:operation`, and skips the effect on
any later attempt that finds a record.

The guarantee is stated precisely, in the package documentation and in
`docs/reliability.md`: **an effect recorded under a key is performed at most
once after it has been recorded.** The window between performing an effect and
recording it cannot be closed, because the effect is external and the record is
in PostgreSQL, and no transaction spans both. A crash inside that window
produces a duplicate.

Nothing in ReLab claims exactly-once for external side effects, and the README
says so above the benchmarks rather than below them.

## Consequences

- The window is small — one insert — but real. `SIDE_EFFECT_SKIPPED` events make
  the mechanism observable, and the `duplicate_effects` assertion counts what
  actually happened rather than what was intended.
- An effect that fails is not recorded, so the retry runs it again. That is
  required for a retry to mean anything, and it has a corollary: an effect that
  succeeded externally but *reported* failure will be repeated. The external
  API's own idempotency key is the only real answer to that, and a handler that
  has one should pass it.
- The acceptance test does not simulate the crash. `effect_then_die` performs
  the effect and then SIGKILLs its own process, so the window is exercised for
  real and the assertion is on the ledger's actual contents afterwards.

## Rejected

**Two-phase commit with the external service.** Correct, and unavailable: the
services handlers call do not offer a prepare phase.

**Recording the effect before performing it.** Moves the window rather than
closing it, and moves it to the worse side: a crash after the record and before
the effect means the effect never happens and the retry skips it.
