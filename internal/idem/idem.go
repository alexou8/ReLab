// Package idem implements the side-effect ledger.
//
// ReLab delivers at least once. A task may therefore run more than once, and a
// handler that calls an external API will call it again on a retry unless
// something stops it. This package is that something, and it is the only one:
// an effect performed outside idem.Do is not protected.
//
// What it guarantees, precisely: an effect recorded under a key is performed at
// most once *after it has been recorded*. The window between performing an
// effect and recording it is real and cannot be closed, because the effect is
// external and the record is in PostgreSQL — there is no transaction spanning
// both. A crash inside that window produces a duplicate. The ledger bounds the
// damage to that window rather than eliminating it, and docs/reliability.md
// says so in those words rather than claiming exactly-once.
package idem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/store"
)

// Key is a ledger key. It is opaque to callers; build one with NewKey.
type Key string

// NewKey derives the key for one logical operation of one task of one run.
//
// The parts are joined with ':'. Run ids are UUIDs and task names are validated
// to exclude ':' at workflow-parse time, so no two distinct triples can produce
// the same key by concatenation — which would silently suppress an unrelated
// effect.
func NewKey(runID uuid.UUID, taskName, op string) Key {
	return Key(runID.String() + ":" + taskName + ":" + op)
}

func (k Key) String() string { return string(k) }

// Record is one performed effect.
type Record struct {
	Key       Key
	RunID     uuid.UUID
	TaskName  string
	Result    json.RawMessage
	FirstSeen bool
}

// Ledger records effects in PostgreSQL.
type Ledger struct {
	db *store.DB
}

// New returns a Ledger.
func New(db *store.DB) *Ledger { return &Ledger{db: db} }

// ErrNotRecorded reports that a key has no recorded effect.
var ErrNotRecorded = errors.New("idem: no effect recorded under this key")

// Do performs fn at most once per key, and returns the recorded result.
//
// skipped reports that fn was not called because the effect had already
// happened — the caller emits SIDE_EFFECT_SKIPPED on the strength of it, which
// is the observable evidence that a retry did not become a duplicate.
//
// If fn returns an error, nothing is recorded and the next attempt will call it
// again. That is deliberate: an effect that failed did not happen, and
// recording it would suppress the retry that is supposed to fix it. It also
// means an effect that succeeded externally but *reported* failure will be
// repeated, which is the ambiguity every at-least-once system has and which the
// external API's own idempotency key, if it has one, is the only real answer to.
func (l *Ledger) Do(ctx context.Context, key Key, runID uuid.UUID, taskName string,
	fn func(context.Context) (any, error)) (result json.RawMessage, skipped bool, err error) {
	recorded, err := l.Lookup(ctx, key)
	switch {
	case err == nil:
		return recorded.Result, true, nil
	case !errors.Is(err, ErrNotRecorded):
		return nil, false, err
	}

	value, err := fn(ctx)
	if err != nil {
		return nil, false, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, false, fmt.Errorf("idem: effect %q returned a result that is not JSON: %w", key, err)
	}

	stored, inserted, err := l.record(ctx, key, runID, taskName, raw)
	if err != nil {
		return nil, false, err
	}
	if inserted {
		return stored, false, nil
	}

	// Another attempt recorded this effect while ours was in flight. Return
	// what it recorded, so both attempts agree on the result rather than each
	// believing its own.
	recorded, err = l.Lookup(ctx, key)
	if err != nil {
		return nil, false, err
	}
	return recorded.Result, true, nil
}

// Lookup returns a recorded effect, or ErrNotRecorded.
func (l *Ledger) Lookup(ctx context.Context, key Key) (Record, error) {
	rec := Record{Key: key}
	err := l.db.Conn().QueryRow(ctx, `
		SELECT run_id, task_name, result FROM side_effects WHERE idempotency_key = $1`,
		string(key)).Scan(&rec.RunID, &rec.TaskName, &rec.Result)
	if err != nil {
		if errors.Is(store.Classify(err), store.ErrNotFound) {
			return Record{}, fmt.Errorf("%w: %s", ErrNotRecorded, key)
		}
		return Record{}, fmt.Errorf("idem: read effect %q: %w", key, store.Classify(err))
	}
	return rec, nil
}

// record inserts an effect and returns what was stored, along with whether this
// call was the one that wrote it.
//
// ON CONFLICT DO NOTHING rather than a read-then-write: the read-then-write has
// a race, and closing it is the whole point of the ledger. RETURNING gives back
// the stored bytes in the same round trip, so the caller that inserted and the
// callers that lost the race all see the same representation — the column is
// jsonb, which reorders keys and normalises spacing, and returning the caller's
// own bytes here would make two callers of one key disagree byte for byte on a
// value they agree on semantically.
func (l *Ledger) record(ctx context.Context, key Key, runID uuid.UUID, taskName string,
	raw json.RawMessage) (json.RawMessage, bool, error) {
	var stored json.RawMessage
	err := l.db.Conn().QueryRow(ctx, `
		INSERT INTO side_effects (idempotency_key, run_id, task_name, result)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING result`,
		string(key), runID, taskName, raw).Scan(&stored)
	if err != nil {
		// No row returned means the conflict clause fired: someone else wrote
		// this key first.
		if errors.Is(store.Classify(err), store.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("idem: record effect %q: %w", key, store.Classify(err))
	}
	return stored, true, nil
}

// CountForRun returns how many effects a run recorded, which is what the
// duplicate_effects assertion compares against.
func (l *Ledger) CountForRun(ctx context.Context, runID uuid.UUID) (int, error) {
	var count int
	if err := l.db.Conn().QueryRow(ctx,
		`SELECT count(*) FROM side_effects WHERE run_id = $1`, runID).Scan(&count); err != nil {
		return 0, fmt.Errorf("idem: count effects of run %s: %w", runID, store.Classify(err))
	}
	return count, nil
}
