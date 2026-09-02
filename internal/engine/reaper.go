package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/store"
)

// ReapResult counts what one sweep did.
type ReapResult struct {
	LeasesExpired   int
	TasksRequeued   int
	TasksDead       int
	WorkersSuspect  int
	WorkersLost     int
	RetriesPromoted int
}

// Empty reports whether the sweep found nothing to do, which is the normal
// case and is worth not logging.
func (r ReapResult) Empty() bool {
	return r == ReapResult{}
}

// Reap performs one recovery sweep: expire leases, requeue or dead-letter the
// tasks they held, move silent workers towards LOST, and promote due retries.
//
// This is the only thing that turns a crashed worker back into progress. It is
// deliberately a sweep over database state rather than a reaction to an event:
// a worker that is gone sends no notification, and a coordinator that restarts
// must recover the same way whether it was running when the worker died or
// started afterwards.
//
// The order matters. Leases are expired first, so that a task held by a worker
// that is about to be declared LOST is already back in the queue by the time
// the worker row changes; doing it the other way round leaves a window in which
// the worker is LOST and its tasks are still marked as held by it.
func (e *Engine) Reap(ctx context.Context) (ReapResult, error) {
	var result ReapResult

	expired, requeued, dead, err := e.expireLeases(ctx)
	if err != nil {
		return result, err
	}
	result.LeasesExpired, result.TasksRequeued, result.TasksDead = expired, requeued, dead

	suspect, lost, err := e.sweepWorkers(ctx)
	if err != nil {
		return result, err
	}
	result.WorkersSuspect, result.WorkersLost = suspect, lost

	promoted, err := e.PromoteDueRetries(ctx, 200)
	if err != nil {
		return result, err
	}
	result.RetriesPromoted = promoted
	return result, nil
}

// expireLeases hands back every task whose lease has run out.
//
// A task is requeued if it has attempts left and dead-lettered if it does not.
// The attempt was already consumed at claim time, so a worker that dies while
// holding a task does not get to retry it indefinitely.
func (e *Engine) expireLeases(ctx context.Context) (expired, requeued, dead int, err error) {
	now := e.now()
	err = e.db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
		const selectExpired = `
			SELECT ` + taskColumns + `
			FROM tasks
			WHERE status IN ('LEASED', 'RUNNING') AND lease_expires_at <= $1
			ORDER BY lease_expires_at
			LIMIT 200
			FOR UPDATE SKIP LOCKED`
		rows, queryErr := tx.Query(ctx, selectExpired, now)
		if queryErr != nil {
			return fmt.Errorf("select expired leases: %w", queryErr)
		}
		tasks, collectErr := collectTasks(rows)
		if collectErr != nil {
			return collectErr
		}

		for _, task := range tasks {
			expired++
			if err := e.recordLeaseExpiry(ctx, tx, task, now); err != nil {
				return err
			}
			def, err := e.definitionForRun(ctx, tx, task.RunID)
			if err != nil {
				return err
			}
			policy := retryPolicyFor(def, task.Name)
			if task.Attempt < policy.MaxAttempts {
				if err := e.requeue(ctx, tx, task, now); err != nil {
					return err
				}
				requeued++
				continue
			}
			if err := e.deadLetterExpired(ctx, tx, task, now); err != nil {
				return err
			}
			if err := e.abandonDependents(ctx, tx, task.RunID, task.Name, def, now); err != nil {
				return err
			}
			if err := e.settleRun(ctx, tx, task.RunID, now); err != nil {
				return err
			}
			dead++
		}
		return nil
	})
	if err != nil {
		return 0, 0, 0, fmt.Errorf("engine: expire leases: %w", err)
	}
	return expired, requeued, dead, nil
}

func (e *Engine) recordLeaseExpiry(ctx context.Context, tx store.Conn, task Task, now time.Time) error {
	expiredAt := now
	if task.LeaseExpiresAt != nil {
		expiredAt = *task.LeaseExpiresAt
	}
	_, err := event.Append(ctx, tx, task.RunID, event.TaskLeaseExpiredPayload{
		Attempt:        task.Attempt,
		LeaseExpiredAt: expiredAt,
	}, event.Meta{TaskName: task.Name, WorkerID: task.WorkerID, OccurredAt: now})
	return err
}

// requeue returns an expired task to READY so another worker can claim it.
//
// The row is cleared of its lease and its worker, and the status guard on
// worker_id and attempt means a worker that comes back to life and completes
// the task a moment later finds its own UPDATE affecting no rows: it gets
// ErrLeaseLost and discards its result, rather than overwriting the attempt
// that has already been handed to someone else.
func (e *Engine) requeue(ctx context.Context, tx store.Conn, task Task, now time.Time) error {
	if err := task.Status.Transition(TaskReady); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE tasks
		SET status = 'READY', worker_id = NULL, lease_expires_at = NULL,
		    scheduled_at = $1, updated_at = $1, started_at = NULL
		WHERE id = $2 AND status IN ('LEASED', 'RUNNING') AND attempt = $3`,
		now, task.ID, task.Attempt)
	if err != nil {
		return fmt.Errorf("requeue task %s: %w", task.Name, err)
	}
	if tag.RowsAffected() == 0 {
		// The worker completed the task between the select and the update.
		// That is a legitimate outcome, not an error: the work is done.
		return nil
	}
	_, err = event.Append(ctx, tx, task.RunID, event.TaskRequeuedPayload{
		Attempt:     task.Attempt,
		NextAttempt: task.Attempt + 1,
		Reason:      "lease expired",
	}, event.Meta{TaskName: task.Name, OccurredAt: now})
	return err
}

func (e *Engine) deadLetterExpired(ctx context.Context, tx store.Conn, task Task, now time.Time) error {
	if _, err := tx.Exec(ctx, `
		UPDATE tasks
		SET status = 'DEAD', worker_id = NULL, lease_expires_at = NULL,
		    completed_at = $1, updated_at = $1,
		    error = coalesce(error, 'lease expired with no attempts remaining')
		WHERE id = $2 AND status IN ('LEASED', 'RUNNING') AND attempt = $3`,
		now, task.ID, task.Attempt); err != nil {
		return fmt.Errorf("dead-letter expired task %s: %w", task.Name, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO dead_letters (task_id, run_id, reason, attempts, last_worker, created_at)
		VALUES ($1, $2, 'lease expired with no attempts remaining', $3, $4, $5)
		ON CONFLICT (task_id) DO NOTHING`,
		task.ID, task.RunID, max(task.Attempt, 1), task.WorkerID, now); err != nil {
		return fmt.Errorf("record dead letter for %s: %w", task.Name, err)
	}
	_, err := event.Append(ctx, tx, task.RunID, event.TaskDeadLetteredPayload{
		Attempts: task.Attempt,
		Reason:   "lease expired with no attempts remaining",
	}, event.Meta{TaskName: task.Name, OccurredAt: now})
	return err
}

// sweepWorkers moves workers along HEALTHY -> SUSPECT -> LOST by heartbeat age,
// and releases the leases of the ones it declares lost.
//
// Two thresholds rather than one: a single missed heartbeat is a GC pause or a
// scheduling hiccup, and reclaiming work from a worker that is merely busy
// turns a hiccup into duplicate execution. SUSPECT is visible in `relab
// workers` without costing the worker anything.
func (e *Engine) sweepWorkers(ctx context.Context) (suspect, lost int, err error) {
	now := e.now()
	beat := e.timing.HeartbeatInterval
	suspectAfter := now.Add(-beat * time.Duration(e.timing.SuspectAfterBeats))
	lostAfter := now.Add(-beat * time.Duration(e.timing.LostAfterBeats))

	err = e.db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
		tag, err := tx.Exec(ctx, `
			UPDATE workers SET status = 'SUSPECT'
			WHERE status = 'HEALTHY' AND last_heartbeat <= $1`, suspectAfter)
		if err != nil {
			return fmt.Errorf("mark workers suspect: %w", err)
		}
		suspect = int(tag.RowsAffected())

		rows, err := tx.Query(ctx, `
			UPDATE workers SET status = 'LOST'
			WHERE status IN ('HEALTHY', 'SUSPECT') AND last_heartbeat <= $1
			RETURNING id`, lostAfter)
		if err != nil {
			return fmt.Errorf("mark workers lost: %w", err)
		}
		var lostIDs []uuid.UUID
		func() {
			defer rows.Close()
			for rows.Next() {
				var id uuid.UUID
				if err = rows.Scan(&id); err != nil {
					return
				}
				lostIDs = append(lostIDs, id)
			}
			err = rows.Err()
		}()
		if err != nil {
			return fmt.Errorf("collect lost workers: %w", store.Classify(err))
		}
		lost = len(lostIDs)

		// A lost worker's leases are released immediately rather than waiting
		// for each to expire on its own. The worker is gone; making its tasks
		// serve out the remainder of their leases adds latency for no benefit.
		for _, id := range lostIDs {
			released, err := e.releaseLeasesOf(ctx, tx, id, now)
			if err != nil {
				return err
			}
			if err := e.announceWorkerLost(ctx, tx, id, released, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("engine: sweep workers: %w", err)
	}
	return suspect, lost, nil
}

// releaseLeasesOf expires every lease held by a worker by moving the deadline
// into the past. The next expireLeases sweep — or the rest of this one on a
// later tick — requeues them through the normal path, so a released lease and
// an expired one produce exactly the same events.
func (e *Engine) releaseLeasesOf(ctx context.Context, tx store.Conn, workerID uuid.UUID, now time.Time) (int, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE tasks SET lease_expires_at = $1, updated_at = $1
		WHERE worker_id = $2 AND status IN ('LEASED', 'RUNNING')`, now, workerID)
	if err != nil {
		return 0, fmt.Errorf("release leases of worker %s: %w", workerID, err)
	}
	return int(tag.RowsAffected()), nil
}

// announceWorkerLost records WORKER_LOST against each run the worker was
// holding work for. Worker liveness is process-scoped, but a run's journal has
// to be able to explain why its task was taken away, so the event is written
// where the explanation is needed.
func (e *Engine) announceWorkerLost(ctx context.Context, tx store.Conn, workerID uuid.UUID,
	released int, now time.Time) error {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT run_id FROM tasks WHERE worker_id = $1`, workerID)
	if err != nil {
		return fmt.Errorf("find runs of lost worker %s: %w", workerID, store.Classify(err))
	}
	var runIDs []uuid.UUID
	func() {
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err = rows.Scan(&id); err != nil {
				return
			}
			runIDs = append(runIDs, id)
		}
		err = rows.Err()
	}()
	if err != nil {
		return fmt.Errorf("find runs of lost worker %s: %w", workerID, store.Classify(err))
	}

	for _, runID := range runIDs {
		if _, err := event.Append(ctx, tx, runID, event.WorkerLostPayload{
			MissedBeats:    e.timing.LostAfterBeats,
			LeasesReleased: released,
		}, event.Meta{WorkerID: &workerID, OccurredAt: now}); err != nil {
			return err
		}
	}
	return nil
}

// RenewLease extends the deadline on the tasks a worker is holding, and returns
// the ids it actually renewed.
//
// It renews only tasks the worker still owns, so a worker that was declared
// lost and whose task was handed on does not steal it back. The renewed ids are
// returned rather than a count because the caller needs to know *which* tasks
// it lost: continuing to execute work that has already been given to someone
// else burns capacity on a result that will be discarded.
func (e *Engine) RenewLease(ctx context.Context, workerID uuid.UUID, taskIDs []uuid.UUID) ([]uuid.UUID, error) {
	if len(taskIDs) == 0 {
		return nil, nil
	}
	now := e.now()
	rows, err := e.db.Conn().Query(ctx, `
		UPDATE tasks
		SET lease_expires_at = $1, updated_at = $2
		WHERE id = ANY($3) AND worker_id = $4 AND status IN ('LEASED', 'RUNNING')
		RETURNING id`,
		now.Add(e.timing.LeaseDuration), now, taskIDs, workerID)
	if err != nil {
		return nil, fmt.Errorf("engine: renew leases for worker %s: %w", workerID, store.Classify(err))
	}
	defer rows.Close()

	renewed := make([]uuid.UUID, 0, len(taskIDs))
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("engine: scan renewed lease: %w", store.Classify(err))
		}
		renewed = append(renewed, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("engine: renew leases for worker %s: %w", workerID, store.Classify(err))
	}
	return renewed, nil
}
