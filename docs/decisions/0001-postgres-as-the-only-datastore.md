# 0001. PostgreSQL is the only datastore

**Status:** accepted

## Context

ReLab needs durable run state, a work queue, and an append-only event journal.
The obvious architecture puts each in the system built for it: Postgres for
state, Redis or RabbitMQ or Kafka for the queue, and an append-only log
somewhere else again.

That architecture has a problem specific to this project. ReLab's claim is that
a run's recorded history is a faithful account of what happened. If the state
change and the event describing it live in different systems, there is a window
in which one has been written and the other has not — and no amount of care
closes it, because there is no transaction spanning both. Every replay would
carry a footnote saying "unless we crashed in that window". The whole product is
that footnote not existing.

## Decision

One PostgreSQL 16 database holds state, queue and journal. A state change and
the events describing it are written in one transaction or not at all.

The queue is a table claimed with `SELECT ... FOR UPDATE SKIP LOCKED`. A broker
sits behind the `queue.Queue` interface for a later version.

## Consequences

- Replay is trustworthy by construction, not by convention.
- Throughput is bounded by one Postgres instance. Measured numbers are in
  `docs/benchmarks.md`; they are adequate for the reliability-testing workloads
  ReLab targets and would not be adequate for a general-purpose task queue.
- The claim query is a hot spot. It is served by a partial index on the runnable
  set, and it is the first thing to look at when throughput disappoints.
- Operators need one thing running, not four. `docker compose up` is a real
  five-minute path rather than an aspiration.

## Rejected

**Kafka or Redis Streams for the journal.** Better at high-volume append. Both
would reintroduce the cross-system window the decision exists to close.

**A dedicated queue broker in v1.** Higher throughput, and the operational story
is well understood. Rejected for v1 because it splits the transaction, and
because a broker's own at-least-once semantics would be conflated with ReLab's
in every discussion of the guarantees.
