package engine

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/store"
)

// PromoteDueRetries moves RETRYING tasks whose backoff has elapsed back to
// READY, and returns how many it promoted.
//
// The backoff is enforced by this sweep rather than by a timer in the process
// that saw the failure. A timer would be lost when that process restarts, and
// "the retry never happened because the coordinator was redeployed" is exactly
// the class of bug ReLab exists to make visible.
func (e *Engine) PromoteDueRetries(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	now := e.now()
	promoted := 0
	err := e.db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
		const promote = `
			WITH due AS (
				SELECT id FROM tasks
				WHERE status = 'RETRYING' AND scheduled_at <= $1
				ORDER BY scheduled_at
				LIMIT $2
				FOR UPDATE SKIP LOCKED
			)
			UPDATE tasks t
			SET status = 'READY', updated_at = $1
			FROM due
			WHERE t.id = due.id
			RETURNING t.run_id, t.task_name, t.attempt`
		rows, err := tx.Query(ctx, promote, now, limit)
		if err != nil {
			return fmt.Errorf("promote retries: %w", err)
		}
		type promotion struct {
			runID   [16]byte
			name    string
			attempt int
		}
		var promotions []promotion
		func() {
			defer rows.Close()
			for rows.Next() {
				var p promotion
				if err = rows.Scan(&p.runID, &p.name, &p.attempt); err != nil {
					return
				}
				promotions = append(promotions, p)
			}
			err = rows.Err()
		}()
		if err != nil {
			return fmt.Errorf("promote retries: %w", store.Classify(err))
		}

		for _, p := range promotions {
			if _, err := event.Append(ctx, tx, p.runID, event.TaskScheduledPayload{
				Attempt:     p.attempt,
				ScheduledAt: now,
			}, event.Meta{TaskName: p.name, OccurredAt: now}); err != nil {
				return err
			}
		}
		promoted = len(promotions)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("engine: promote due retries: %w", err)
	}
	return promoted, nil
}

// Progress summarises whether a run can still make progress on its own.
type Progress struct {
	Total    int
	Terminal int
	Runnable int // READY and due
	Waiting  int // RETRYING, or READY but not yet due
	InFlight int // LEASED or RUNNING
	Blocked  int // PENDING, waiting on a dependency
}

// Stalled reports a run that is not finished and yet has nothing that will ever
// move it: no runnable task, nothing in flight, nothing waiting on a timer.
// Only a bug produces this state, so callers should surface it rather than
// wait it out — a stalled run that is polled forever looks exactly like a slow
// one, which is the hardest kind of failure to diagnose.
func (p Progress) Stalled() bool {
	return p.Terminal < p.Total && p.Runnable == 0 && p.InFlight == 0 && p.Waiting == 0
}

// RunProgress counts a run's tasks by what they are waiting for.
func (e *Engine) RunProgress(ctx context.Context, runID uuid.UUID) (Progress, error) {
	now := e.now()
	var p Progress
	err := e.db.Conn().QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE status IN ('SUCCEEDED', 'DEAD')),
		       count(*) FILTER (WHERE status = 'READY' AND scheduled_at <= $2),
		       count(*) FILTER (WHERE status = 'RETRYING'
		                           OR (status = 'READY' AND scheduled_at > $2)),
		       count(*) FILTER (WHERE status IN ('LEASED', 'RUNNING')),
		       count(*) FILTER (WHERE status = 'PENDING')
		FROM tasks WHERE run_id = $1`, runID, now).
		Scan(&p.Total, &p.Terminal, &p.Runnable, &p.Waiting, &p.InFlight, &p.Blocked)
	if err != nil {
		return Progress{}, fmt.Errorf("engine: read progress of run %s: %w", runID, store.Classify(err))
	}
	return p, nil
}
