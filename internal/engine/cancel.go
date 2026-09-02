package engine

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/store"
)

// ErrRunFinished reports an attempt to cancel a run that has already ended.
var ErrRunFinished = fmt.Errorf("run has already reached a terminal state")

// CancelRun stops a run and every task in it that has not finished.
//
// What cancellation can and cannot do is worth being precise about. Tasks that
// have not started are marked DEAD immediately and will never run. A task that
// is currently executing on a worker is *not* interrupted from here: the
// coordinator has no channel to a worker's goroutine, and inventing one would
// mean the guarantee only holds while that worker is reachable — which is
// exactly when it is least likely to be. Instead the task's lease is expired,
// so the worker discovers the cancellation the same way it discovers any other
// loss of ownership, and the run is closed regardless.
//
// The consequence, stated in docs/reliability.md: a cancelled run may still
// have one in-flight handler running for up to its remaining timeout. Its
// result is discarded.
func (e *Engine) CancelRun(ctx context.Context, runID uuid.UUID, reason string) error {
	now := e.now()
	err := e.db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
		// Lock the run first so two concurrent cancellations, or a cancellation
		// racing a task completion, serialise rather than interleaving.
		var status RunStatus
		if err := tx.QueryRow(ctx,
			`SELECT status FROM runs WHERE id = $1 FOR UPDATE`, runID).Scan(&status); err != nil {
			return fmt.Errorf("read run: %w", store.Classify(err))
		}
		if status.Terminal() {
			return fmt.Errorf("%w: %s", ErrRunFinished, status)
		}
		if err := status.Transition(RunCancelled); err != nil {
			return err
		}

		// Unstarted tasks end here and now.
		if _, err := tx.Exec(ctx, `
			UPDATE tasks
			SET status = 'DEAD', completed_at = $1, updated_at = $1, worker_id = NULL,
			    lease_expires_at = NULL,
			    error = coalesce(error, 'run cancelled')
			WHERE run_id = $2 AND status IN ('PENDING', 'READY', 'RETRYING')`,
			now, runID); err != nil {
			return fmt.Errorf("cancel pending tasks: %w", err)
		}

		// Tasks in flight have their leases expired instead. The worker finds
		// out when it tries to record its outcome and gets ErrLeaseLost.
		if _, err := tx.Exec(ctx, `
			UPDATE tasks SET lease_expires_at = $1, updated_at = $1
			WHERE run_id = $2 AND status IN ('LEASED', 'RUNNING')`,
			now, runID); err != nil {
			return fmt.Errorf("expire leases of cancelled run: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE runs SET status = 'CANCELLED', completed_at = $1, failure_reason = $2
			WHERE id = $3`, now, nullString(reason), runID); err != nil {
			return fmt.Errorf("cancel run: %w", err)
		}
		_, err := event.Append(ctx, tx, runID, event.RunCancelledPayload{Reason: reason},
			event.Meta{OccurredAt: now})
		return err
	})
	if err != nil {
		return fmt.Errorf("engine: cancel run %s: %w", runID, err)
	}
	return nil
}
