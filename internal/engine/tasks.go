package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/retry"
	"github.com/alexou8/relab/internal/store"
	"github.com/alexou8/relab/internal/workflow"
)

// ErrLeaseLost reports that a task was no longer held by the caller when it
// tried to record an outcome. It is an ordinary condition, not a failure: it
// means the reaper decided the worker was gone and handed the task to someone
// else. The worker that lost the lease discards its result.
var ErrLeaseLost = errors.New("lease lost")

// ErrConcurrentAttempt reports that another worker was already executing this
// exact attempt. It is a scheduler bug, surfaced by the primary key on
// task_attempts, and it is what the M2 acceptance test asserts never happens.
var ErrConcurrentAttempt = errors.New("this attempt is already being executed by another worker")

// ClaimTasks atomically takes up to limit runnable tasks for a worker.
//
// The claim is `FOR UPDATE SKIP LOCKED`: concurrent claimers step over rows
// each other has locked rather than blocking on them, which is what lets a
// worker pool scale without a coordinator handing out work. Losing the race
// returns fewer tasks, never an error.
//
// The attempt counter is incremented here, at claim time rather than at start
// time. A worker that claims a task and then dies before entering the handler
// has still consumed an attempt, and the alternative — incrementing on start —
// lets a worker that reliably crashes between claim and start retry forever.
func (e *Engine) ClaimTasks(ctx context.Context, workerID uuid.UUID, limit int) ([]ClaimedTask, error) {
	return e.claim(ctx, workerID, nil, limit)
}

// claim is the shared implementation. runID, when non-nil, restricts the claim
// to one run; the filter is a parameter of the same query rather than a second
// query, so both paths take the same locks in the same order.
func (e *Engine) claim(ctx context.Context, workerID uuid.UUID, runID *uuid.UUID, limit int) ([]ClaimedTask, error) {
	if limit <= 0 {
		return nil, nil
	}
	now := e.now()
	leaseUntil := now.Add(e.timing.LeaseDuration)

	var claimed []ClaimedTask
	err := e.db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
		const claimSQL = `
			WITH runnable AS (
				SELECT id
				FROM tasks
				WHERE status = 'READY'
				  AND scheduled_at <= $1
				  AND ($5::uuid IS NULL OR run_id = $5)
				ORDER BY scheduled_at
				LIMIT $2
				FOR UPDATE SKIP LOCKED
			)
			UPDATE tasks t
			SET status = 'LEASED',
			    worker_id = $3,
			    lease_expires_at = $4,
			    attempt = t.attempt + 1,
			    updated_at = $1
			FROM runnable
			WHERE t.id = runnable.id
			RETURNING ` + taskColumnsQualified
		rows, err := tx.Query(ctx, claimSQL, now, limit, workerID, leaseUntil, runID)
		if err != nil {
			return fmt.Errorf("claim: %w", err)
		}
		tasks, err := collectTasks(rows)
		if err != nil {
			return err
		}

		for _, task := range tasks {
			if _, err := event.Append(ctx, tx, task.RunID, event.TaskLeasedPayload{
				Attempt:        task.Attempt,
				LeaseExpiresAt: leaseUntil,
			}, event.Meta{TaskName: task.Name, WorkerID: &workerID, OccurredAt: now}); err != nil {
				return err
			}
			ct, err := e.hydrate(ctx, tx, task, leaseUntil)
			if err != nil {
				return err
			}
			claimed = append(claimed, ct)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("engine: claim tasks for worker %s: %w", workerID, err)
	}
	return claimed, nil
}

// ClaimTasksForRun is ClaimTasks restricted to a single run. `relab run` uses
// it so that driving one workflow locally does not consume work a deployed
// worker pool is meant to handle.
func (e *Engine) ClaimTasksForRun(ctx context.Context, workerID, runID uuid.UUID, limit int) ([]ClaimedTask, error) {
	return e.claim(ctx, workerID, &runID, limit)
}

// hydrate loads what a worker needs to execute a claimed task: which handler to
// call, what its dependencies produced, and how long it may take.
func (e *Engine) hydrate(ctx context.Context, tx store.Conn, task Task, leaseUntil time.Time) (ClaimedTask, error) {
	var workflowID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT workflow_id FROM runs WHERE id = $1`, task.RunID).
		Scan(&workflowID); err != nil {
		return ClaimedTask{}, fmt.Errorf("read run %s: %w", task.RunID, store.Classify(err))
	}
	def, err := e.resolveDefinition(ctx, workflowID)
	if err != nil {
		return ClaimedTask{}, err
	}
	step, ok := def.Step(task.Name)
	if !ok {
		return ClaimedTask{}, fmt.Errorf(
			"engine: run %s has a task %q that its workflow definition does not declare",
			task.RunID, task.Name)
	}

	inputs, err := dependencyOutputs(ctx, tx, task.RunID, step.DependsOn)
	if err != nil {
		return ClaimedTask{}, err
	}
	timeout := step.Timeout.Duration()
	if timeout <= 0 {
		timeout = e.timing.TaskTimeout
	}
	return ClaimedTask{
		Task:           task,
		RunID:          task.RunID,
		WorkflowName:   def.Name,
		Handler:        step.Handler,
		Inputs:         inputs,
		Timeout:        timeout,
		LeaseExpiresAt: leaseUntil,
	}, nil
}

// dependencyOutputs reads the recorded outputs of the steps a task depends on.
func dependencyOutputs(ctx context.Context, conn store.Conn, runID uuid.UUID, deps []string) (map[string]json.RawMessage, error) {
	if len(deps) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	rows, err := conn.Query(ctx,
		`SELECT task_name, output_ref FROM tasks WHERE run_id = $1 AND task_name = ANY($2)`,
		runID, deps)
	if err != nil {
		return nil, fmt.Errorf("read dependency outputs of run %s: %w", runID, store.Classify(err))
	}
	defer rows.Close()

	out := make(map[string]json.RawMessage, len(deps))
	for rows.Next() {
		var name string
		var output json.RawMessage
		if err := rows.Scan(&name, &output); err != nil {
			return nil, fmt.Errorf("scan dependency output: %w", store.Classify(err))
		}
		out[name] = output
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read dependency outputs of run %s: %w", runID, store.Classify(err))
	}
	return out, nil
}

// StartTask moves a leased task to RUNNING and records the attempt.
//
// The insert into task_attempts is what makes concurrent execution of one
// attempt impossible rather than merely unlikely: the second worker's insert
// violates the primary key and it gets ErrConcurrentAttempt instead of running
// the handler.
func (e *Engine) StartTask(ctx context.Context, workerID uuid.UUID, task Task, handler string) error {
	now := e.now()
	err := e.db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
		tag, err := tx.Exec(ctx, `
			UPDATE tasks
			SET status = 'RUNNING', started_at = coalesce(started_at, $1), updated_at = $1
			WHERE id = $2 AND status = 'LEASED' AND worker_id = $3 AND attempt = $4`,
			now, task.ID, workerID, task.Attempt)
		if err != nil {
			return fmt.Errorf("start task: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrLeaseLost
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO task_attempts (task_id, attempt, worker_id, started_at) VALUES ($1, $2, $3, $4)`,
			task.ID, task.Attempt, workerID, now); err != nil {
			if errors.Is(store.Classify(err), store.ErrConflict) {
				return fmt.Errorf("%w: task %s attempt %d", ErrConcurrentAttempt, task.Name, task.Attempt)
			}
			return fmt.Errorf("record attempt: %w", err)
		}

		if _, err := event.Append(ctx, tx, task.RunID, event.TaskStartedPayload{
			Attempt: task.Attempt, Handler: handler,
		}, event.Meta{TaskName: task.Name, WorkerID: &workerID, OccurredAt: now}); err != nil {
			return err
		}
		return e.markRunStarted(ctx, tx, task.RunID, now)
	})
	if err != nil {
		if errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrConcurrentAttempt) {
			return err
		}
		return fmt.Errorf("engine: start task %s of run %s: %w", task.Name, task.RunID, err)
	}
	return nil
}

// markRunStarted moves a run from QUEUED to RUNNING the first time any of its
// tasks starts. The guard on status makes it a no-op afterwards, so it is safe
// to call on every task start.
func (e *Engine) markRunStarted(ctx context.Context, tx store.Conn, runID uuid.UUID, now time.Time) error {
	tag, err := tx.Exec(ctx,
		`UPDATE runs SET status = 'RUNNING', started_at = $1 WHERE id = $2 AND status = 'QUEUED'`,
		now, runID)
	if err != nil {
		return fmt.Errorf("mark run running: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	_, err = event.Append(ctx, tx, runID, event.RunStartedPayload{}, event.Meta{OccurredAt: now})
	return err
}

func collectTasks(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}) ([]Task, error) {
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", store.Classify(err))
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read tasks: %w", store.Classify(err))
	}
	return tasks, nil
}

// retryPolicyFor returns the effective policy for a step.
func retryPolicyFor(def *workflow.Definition, taskName string) retry.Policy {
	return retry.FromWorkflow(def.RetryFor(taskName))
}
