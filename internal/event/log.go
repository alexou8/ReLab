package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/store"
)

// Event is one journal row.
type Event struct {
	RunID      uuid.UUID
	Seq        int64
	Type       Type
	TaskName   string
	WorkerID   *uuid.UUID
	Payload    json.RawMessage
	OccurredAt time.Time
}

// Meta carries the optional columns of an append. Every field is optional; a
// zero Meta is valid for run-scoped events that belong to no task or worker.
type Meta struct {
	TaskName string
	WorkerID *uuid.UUID
	// OccurredAt overrides the append time. It exists for tests that need a
	// deterministic clock; production callers leave it zero and let the
	// database stamp it.
	OccurredAt time.Time
}

// Appender is the subset of store.Conn this package needs. Append requires a
// transaction in practice — see the documentation on Append — but the type is
// stated in terms of the interface so tests can substitute.
type Appender = store.Conn

// Append writes one event and returns it with the sequence number it was
// given.
//
// It MUST be called inside a transaction, together with the state change the
// event describes. The sequence number is allocated by incrementing the run
// row's counter, which takes that row's lock: concurrent appends to one run
// serialise behind it, and an append that rolls back releases its number. That
// is what makes the sequence gapless. Appending outside a transaction still
// produces correct numbers but decouples the journal from the state it
// describes, which is the one failure mode replay cannot detect.
//
// Appending to a run that does not exist returns store.ErrNotFound.
func Append(ctx context.Context, tx Appender, runID uuid.UUID, p Payload, meta Meta) (Event, error) {
	if p == nil {
		return Event{}, fmt.Errorf("event: append to run %s: nil payload", runID)
	}
	typ := p.Type()
	if !typ.Known() {
		return Event{}, &ErrUnknownType{Type: typ, RunID: runID.String()}
	}
	raw, err := Encode(p)
	if err != nil {
		return Event{}, err
	}

	// Allocating the sequence number as an UPDATE ... RETURNING keeps it to one
	// round trip and takes the run row's lock as a side effect. A separate
	// SELECT max(seq) would need its own explicit lock and would still be a
	// second statement.
	// The guard on completed_at is what enforces "a run's terminal event is its
	// last one". Replay relies on that invariant to know a history is complete,
	// and an event arriving after the run closed would make a finished run's
	// story keep changing. Callers that close a run append the terminal event
	// first and set completed_at afterwards, within the same transaction.
	var seq int64
	err = tx.QueryRow(ctx,
		`UPDATE runs SET event_seq = event_seq + 1
		 WHERE id = $1 AND completed_at IS NULL
		 RETURNING event_seq`,
		runID).Scan(&seq)
	if err != nil {
		if errors.Is(store.Classify(err), store.ErrNotFound) {
			// Either the run does not exist or it is already closed. Tell the
			// two apart, because they need different fixes.
			var closed bool
			if probeErr := tx.QueryRow(ctx,
				`SELECT completed_at IS NOT NULL FROM runs WHERE id = $1`, runID).Scan(&closed); probeErr == nil && closed {
				return Event{}, fmt.Errorf("event: append %s to run %s: %w", typ, runID, ErrRunClosed)
			}
		}
		return Event{}, fmt.Errorf("event: allocate seq for run %s: %w", runID, store.Classify(err))
	}

	evt := Event{
		RunID:      runID,
		Seq:        seq,
		Type:       typ,
		TaskName:   meta.TaskName,
		WorkerID:   meta.WorkerID,
		Payload:    raw,
		OccurredAt: meta.OccurredAt,
	}

	const insert = `
		INSERT INTO events (run_id, seq, type, task_name, worker_id, payload, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, coalesce($7, now()))
		RETURNING occurred_at`
	var occurredAt *time.Time
	if !meta.OccurredAt.IsZero() {
		t := meta.OccurredAt.UTC()
		occurredAt = &t
	}
	if err := tx.QueryRow(ctx, insert,
		runID, seq, string(typ), nullString(meta.TaskName), meta.WorkerID, raw, occurredAt,
	).Scan(&evt.OccurredAt); err != nil {
		return Event{}, fmt.Errorf("event: append %s to run %s: %w", typ, runID, store.Classify(err))
	}
	return evt, nil
}

// AppendAll writes several events in order within one transaction. It exists so
// that a state change producing more than one event (a lease expiring and the
// task being requeued, say) cannot be interrupted between them.
func AppendAll(ctx context.Context, tx Appender, runID uuid.UUID, items ...Item) ([]Event, error) {
	out := make([]Event, 0, len(items))
	for i, item := range items {
		evt, err := Append(ctx, tx, runID, item.Payload, item.Meta)
		if err != nil {
			return out, fmt.Errorf("event: append item %d of %d: %w", i+1, len(items), err)
		}
		out = append(out, evt)
	}
	return out, nil
}

// Item pairs a payload with its metadata for AppendAll.
type Item struct {
	Payload Payload
	Meta    Meta
}

// Read returns a run's complete history in sequence order.
//
// It verifies gaplessness as it reads: the journal is the input to replay, and
// replaying a history with a hole in it silently produces a state that never
// existed. A gap is reported as ErrGap rather than repaired.
func Read(ctx context.Context, conn store.Conn, runID uuid.UUID) ([]Event, error) {
	return ReadFrom(ctx, conn, runID, 0)
}

// ReadFrom returns events with seq strictly greater than afterSeq. Passing 0
// reads the whole history. Contiguity is checked relative to afterSeq, so
// tailing a live run does not report a false gap.
func ReadFrom(ctx context.Context, conn store.Conn, runID uuid.UUID, afterSeq int64) ([]Event, error) {
	const query = `
		SELECT seq, type, coalesce(task_name, ''), worker_id, payload, occurred_at
		FROM events
		WHERE run_id = $1 AND seq > $2
		ORDER BY seq`
	rows, err := conn.Query(ctx, query, runID, afterSeq)
	if err != nil {
		return nil, fmt.Errorf("event: read run %s: %w", runID, store.Classify(err))
	}
	defer rows.Close()

	var events []Event
	expected := afterSeq + 1
	for rows.Next() {
		evt := Event{RunID: runID}
		var typ string
		if err := rows.Scan(&evt.Seq, &typ, &evt.TaskName, &evt.WorkerID, &evt.Payload, &evt.OccurredAt); err != nil {
			return nil, fmt.Errorf("event: scan run %s: %w", runID, store.Classify(err))
		}
		if evt.Seq != expected {
			return nil, &ErrGap{RunID: runID, Expected: expected, Found: evt.Seq}
		}
		expected++
		evt.Type = Type(typ)
		if !evt.Type.Known() {
			return nil, &ErrUnknownType{Type: evt.Type, RunID: runID.String(), Seq: evt.Seq}
		}
		events = append(events, evt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("event: iterate run %s: %w", runID, store.Classify(err))
	}
	return events, nil
}

// ErrRunClosed reports an append to a run that has already reached a terminal
// state. It is a bug in the caller: a finished run's history is finished, and a
// tool whose job is to say what happened cannot have that change afterwards.
var ErrRunClosed = errors.New("event: the run is closed and its history cannot be added to")

// ErrGap reports a hole in a run's sequence. It is a data integrity failure,
// not a transient condition, and callers should surface it rather than retry.
type ErrGap struct {
	RunID    uuid.UUID
	Expected int64
	Found    int64
}

func (e *ErrGap) Error() string {
	return fmt.Sprintf("event: gap in run %s: expected seq %d, found %d", e.RunID, e.Expected, e.Found)
}

// LastSeq returns the highest sequence number written for a run, or 0 when the
// run has no events. It reads the run counter rather than the log, so it also
// reports numbers allocated by an in-flight transaction that has not committed
// — which is what a caller polling for new events wants to know.
func LastSeq(ctx context.Context, conn store.Conn, runID uuid.UUID) (int64, error) {
	var seq int64
	if err := conn.QueryRow(ctx, `SELECT event_seq FROM runs WHERE id = $1`, runID).Scan(&seq); err != nil {
		return 0, fmt.Errorf("event: last seq for run %s: %w", runID, store.Classify(err))
	}
	return seq, nil
}

// nullString maps the empty string to SQL NULL, so that "no task" is NULL in
// the column rather than an empty string that would sort and group as a task.
func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
