package event_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/store"
	"github.com/alexou8/relab/internal/testsupport"
)

// TestAppendConcurrentSeqIsGapless is the M0 acceptance test. Fifty goroutines
// append to the same run at once; the resulting sequence must be exactly
// 1..50 with no gap, no duplicate and no reordering relative to seq.
func TestAppendConcurrentSeqIsGapless(t *testing.T) {
	const appenders = 50

	db := testsupport.DB(t)
	ctx := context.Background()
	runID := testsupport.SeedRun(t, db)

	// Release every goroutine at once so the appends genuinely contend for the
	// run row rather than trickling in.
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	errs := make(chan error, appenders)
	seqs := make(chan int64, appenders)

	for i := 0; i < appenders; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			err := db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
				evt, err := event.Append(ctx, tx, runID,
					event.TaskStartedPayload{Attempt: i, Handler: "noop"},
					event.Meta{TaskName: "concurrent"})
				if err != nil {
					return err
				}
				seqs <- evt.Seq
				return nil
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	start.Done()
	done.Wait()
	close(errs)
	close(seqs)

	for err := range errs {
		t.Fatalf("concurrent append: %v", err)
	}

	seen := make(map[int64]bool, appenders)
	for seq := range seqs {
		if seen[seq] {
			t.Fatalf("sequence %d was handed out twice", seq)
		}
		seen[seq] = true
	}
	for want := int64(1); want <= appenders; want++ {
		if !seen[want] {
			t.Fatalf("sequence %d was never allocated: the sequence has a gap", want)
		}
	}
	if len(seen) != appenders {
		t.Fatalf("got %d distinct sequence numbers, want %d", len(seen), appenders)
	}

	// Read verifies contiguity itself, so a hole in the stored rows fails here
	// even if the allocated numbers looked right.
	events, err := event.Read(ctx, db.Conn(), runID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != appenders {
		t.Fatalf("read %d events, want %d", len(events), appenders)
	}
	for i, evt := range events {
		if evt.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d, want %d", i, evt.Seq, i+1)
		}
	}

	last, err := event.LastSeq(ctx, db.Conn(), runID)
	if err != nil {
		t.Fatalf("last seq: %v", err)
	}
	if last != appenders {
		t.Fatalf("last seq is %d, want %d", last, appenders)
	}
}

// TestAppendRollbackReleasesSeq proves the sequence stays gapless when an
// appending transaction fails. This is the property that makes a gap on read
// mean data loss rather than a routine abort.
func TestAppendRollbackReleasesSeq(t *testing.T) {
	db := testsupport.DB(t)
	ctx := context.Background()
	runID := testsupport.SeedRun(t, db)

	mustAppend(t, db, runID, event.RunStartedPayload{})

	sentinel := errors.New("deliberate rollback")
	err := db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
		if _, err := event.Append(ctx, tx, runID, event.RunStartedPayload{}, event.Meta{}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx returned %v, want the sentinel", err)
	}

	mustAppend(t, db, runID, event.RunStartedPayload{})

	events, err := event.Read(ctx, db.Conn(), runID)
	if err != nil {
		t.Fatalf("read after rollback: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("read %d events, want 2", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("sequences are %d and %d, want 1 and 2", events[0].Seq, events[1].Seq)
	}
}

func TestAppendToUnknownRun(t *testing.T) {
	db := testsupport.DB(t)
	ctx := context.Background()

	err := db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
		_, err := event.Append(ctx, tx, uuid.New(), event.RunStartedPayload{}, event.Meta{})
		return err
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("append to unknown run returned %v, want store.ErrNotFound", err)
	}
}

func TestReadDetectsGap(t *testing.T) {
	db := testsupport.DB(t)
	ctx := context.Background()
	runID := testsupport.SeedRun(t, db)

	for i := 0; i < 3; i++ {
		mustAppend(t, db, runID, event.RunStartedPayload{})
	}
	// Deleting a row is something the application never does; the test does it
	// to simulate the corruption Read is meant to catch.
	if _, err := db.Conn().Exec(ctx, `DELETE FROM events WHERE run_id = $1 AND seq = 2`, runID); err != nil {
		t.Fatalf("delete event: %v", err)
	}

	_, err := event.Read(ctx, db.Conn(), runID)
	var gap *event.ErrGap
	if !errors.As(err, &gap) {
		t.Fatalf("read returned %v, want *event.ErrGap", err)
	}
	if gap.Expected != 2 || gap.Found != 3 {
		t.Fatalf("gap reported expected=%d found=%d, want 2 and 3", gap.Expected, gap.Found)
	}
}

func TestReadRejectsUnknownType(t *testing.T) {
	db := testsupport.DB(t)
	ctx := context.Background()
	runID := testsupport.SeedRun(t, db)

	// Written directly, because Append refuses to write an unknown type.
	_, err := db.Conn().Exec(ctx, `
		UPDATE runs SET event_seq = 1 WHERE id = $1`, runID)
	if err != nil {
		t.Fatalf("bump seq: %v", err)
	}
	if _, err := db.Conn().Exec(ctx, `
		INSERT INTO events (run_id, seq, type, payload)
		VALUES ($1, 1, 'TASK_TELEPORTED', '{"v":1}')`, runID); err != nil {
		t.Fatalf("insert unknown event: %v", err)
	}

	_, err = event.Read(ctx, db.Conn(), runID)
	var unknown *event.ErrUnknownType
	if !errors.As(err, &unknown) {
		t.Fatalf("read returned %v, want *event.ErrUnknownType", err)
	}
	if unknown.Type != "TASK_TELEPORTED" {
		t.Fatalf("reported type %q, want TASK_TELEPORTED", unknown.Type)
	}
}

func TestReadFromTailsWithoutFalseGap(t *testing.T) {
	db := testsupport.DB(t)
	ctx := context.Background()
	runID := testsupport.SeedRun(t, db)

	for i := 0; i < 5; i++ {
		mustAppend(t, db, runID, event.RunStartedPayload{})
	}
	events, err := event.ReadFrom(ctx, db.Conn(), runID, 3)
	if err != nil {
		t.Fatalf("read from seq 3: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("read %d events, want 2", len(events))
	}
	if events[0].Seq != 4 {
		t.Fatalf("first event has seq %d, want 4", events[0].Seq)
	}
}

func mustAppend(t *testing.T, db *store.DB, runID uuid.UUID, p event.Payload) event.Event {
	t.Helper()
	var evt event.Event
	err := db.InTx(context.Background(), func(ctx context.Context, tx store.Conn) error {
		var err error
		evt, err = event.Append(ctx, tx, runID, p, event.Meta{})
		return err
	})
	if err != nil {
		t.Fatalf("append %s: %v", p.Type(), err)
	}
	return evt
}

// TestAppendRefusesAClosedRun covers the invariant replay depends on: a run's
// terminal event is its last one, so a finished run's story cannot change
// afterwards. It is enforced on the write path rather than trusted to callers.
func TestAppendRefusesAClosedRun(t *testing.T) {
	db := testsupport.DB(t)
	ctx := context.Background()
	runID := testsupport.SeedRun(t, db)

	mustAppend(t, db, runID, event.RunStartedPayload{})

	// Close the run the way the engine does: the terminal event is appended
	// first, then completed_at is set in the same transaction.
	err := db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
		if _, err := event.Append(ctx, tx, runID,
			event.RunSucceededPayload{TasksSucceeded: 1}, event.Meta{}); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`UPDATE runs SET status = 'SUCCEEDED', completed_at = now() WHERE id = $1`, runID)
		return err
	})
	if err != nil {
		t.Fatalf("close run: %v", err)
	}

	err = db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
		_, err := event.Append(ctx, tx, runID, event.WorkerLostPayload{MissedBeats: 5}, event.Meta{})
		return err
	})
	if !errors.Is(err, event.ErrRunClosed) {
		t.Fatalf("appending to a closed run returned %v, want event.ErrRunClosed", err)
	}

	events, err := event.Read(ctx, db.Conn(), runID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if last := events[len(events)-1]; last.Type != event.RunSucceeded {
		t.Fatalf("the last event is %s, want the terminal RUN_SUCCEEDED", last.Type)
	}
}
