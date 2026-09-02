package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/retry"
	"github.com/alexou8/relab/internal/store"
	"github.com/alexou8/relab/internal/telemetry"
	"github.com/alexou8/relab/internal/workflow"
)

// CompleteTask records the outcome of one attempt and advances the run.
//
// Everything it does happens in one transaction: the task's new state, the
// events describing it, any artifacts, the dependent tasks that just became
// ready, and the run's terminal state if this was the last outstanding task.
// Splitting those would leave windows in which the recorded history and the
// state disagree — a run marked SUCCEEDED with no RUN_SUCCEEDED event, or a
// dependent that is READY in the table but was never announced.
//
// A worker whose lease was revoked while it was running gets ErrLeaseLost and
// discards its result. That is not a failure of the task: the task has already
// been handed to someone else, and recording a stale outcome would overwrite
// the newer attempt.
func (e *Engine) CompleteTask(ctx context.Context, workerID uuid.UUID, task Task, outcome Outcome) error {
	now := e.now()
	err := e.db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
		def, err := e.definitionForRun(ctx, tx, task.RunID)
		if err != nil {
			return err
		}
		if outcome.Err != nil {
			return e.recordFailure(ctx, tx, workerID, task, outcome, def, now)
		}
		return e.recordSuccess(ctx, tx, workerID, task, outcome, def, now)
	})
	if err != nil {
		if errors.Is(err, ErrLeaseLost) {
			return err
		}
		return fmt.Errorf("engine: complete task %s of run %s: %w", task.Name, task.RunID, err)
	}
	return nil
}

func (e *Engine) recordSuccess(ctx context.Context, tx store.Conn, workerID uuid.UUID,
	task Task, outcome Outcome, def *workflow.Definition, now time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE tasks
		SET status = 'SUCCEEDED', output_ref = $1, completed_at = $2, updated_at = $2,
		    lease_expires_at = NULL, error = NULL
		WHERE id = $3 AND status = 'RUNNING' AND worker_id = $4 AND attempt = $5`,
		nullJSON(outcome.Output), now, task.ID, workerID, task.Attempt)
	if err != nil {
		return fmt.Errorf("mark task succeeded: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}

	refs, err := e.recordArtifacts(ctx, tx, task, outcome.Artifacts, now)
	if err != nil {
		return err
	}
	if _, err := event.Append(ctx, tx, task.RunID, event.TaskSucceededPayload{
		Attempt:    task.Attempt,
		DurationMS: outcome.Duration.Milliseconds(),
		Output:     outcome.Output,
		Artifacts:  refs,
	}, event.Meta{TaskName: task.Name, WorkerID: &workerID, OccurredAt: now}); err != nil {
		return err
	}

	if err := e.unlockDependents(ctx, tx, task.RunID, task.Name, def, now); err != nil {
		return err
	}
	return e.settleRun(ctx, tx, task.RunID, now)
}

func (e *Engine) recordFailure(ctx context.Context, tx store.Conn, workerID uuid.UUID,
	task Task, outcome Outcome, def *workflow.Definition, now time.Time) error {
	policy := retryPolicyFor(def, task.Name)
	willRetry := policy.ShouldRetry(task.Attempt, outcome.Permanent)

	// FAILED is written first even when a retry follows, so that the journal
	// shows the failure and the decision as two facts rather than one
	// conclusion. Replay needs both to explain why a run took the shape it did.
	// LEASED as well as RUNNING: a task can fail before its handler is entered —
	// the handler is not registered on this worker, or a fault fired between
	// the claim and the start — and such a task would otherwise sit LEASED
	// until its lease expired, with nothing able to record why.
	tag, err := tx.Exec(ctx, `
		UPDATE tasks
		SET status = 'FAILED', error = $1, completed_at = $2, updated_at = $2, lease_expires_at = NULL
		WHERE id = $3 AND status IN ('RUNNING', 'LEASED') AND worker_id = $4 AND attempt = $5`,
		truncateError(outcome.Err.Error()), now, task.ID, workerID, task.Attempt)
	if err != nil {
		return fmt.Errorf("mark task failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	if _, err := event.Append(ctx, tx, task.RunID, event.TaskFailedPayload{
		Attempt:    task.Attempt,
		DurationMS: outcome.Duration.Milliseconds(),
		Error:      truncateError(outcome.Err.Error()),
		Retryable:  willRetry,
	}, event.Meta{TaskName: task.Name, WorkerID: &workerID, OccurredAt: now}); err != nil {
		return err
	}

	if willRetry {
		return e.scheduleRetry(ctx, tx, task, policy, def.Name, now)
	}
	reason := "attempts exhausted"
	if outcome.Permanent {
		reason = "permanent failure"
	}
	if err := e.deadLetter(ctx, tx, task, reason, outcome.Err.Error(), now); err != nil {
		return err
	}
	if err := e.abandonDependents(ctx, tx, task.RunID, task.Name, def, now); err != nil {
		return err
	}
	return e.settleRun(ctx, tx, task.RunID, now)
}

// scheduleRetry moves a failed task to RETRYING with the time it becomes
// eligible again. Promotion to READY is the reaper's job, so the backoff is
// enforced by the same sweep that enforces lease expiry rather than by a timer
// in a process that might not survive the wait.
func (e *Engine) scheduleRetry(ctx context.Context, tx store.Conn, task Task,
	policy retry.Policy, workflowName string, now time.Time) error {
	rng, err := e.runRNG(ctx, tx, task.RunID, "retry-jitter", task.Name, strconv.Itoa(task.Attempt))
	if err != nil {
		return err
	}
	delay := policy.Delay(task.Attempt, rng)
	runAt := now.Add(delay)

	if err := TaskFailed.Transition(TaskRetrying); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE tasks
		SET status = 'RETRYING', scheduled_at = $1, completed_at = NULL, updated_at = $2,
		    worker_id = NULL
		WHERE id = $3 AND status = 'FAILED'`, runAt, now, task.ID); err != nil {
		return fmt.Errorf("schedule retry: %w", err)
	}
	metrics, _ := telemetry.Meter()
	metrics.RecordRetry(ctx, workflowName, task.Name)

	_, err = event.Append(ctx, tx, task.RunID, event.TaskRetryScheduledPayload{
		Attempt:     task.Attempt,
		NextAttempt: task.Attempt + 1,
		DelayMS:     delay.Milliseconds(),
		RunAt:       runAt,
	}, event.Meta{TaskName: task.Name, OccurredAt: now})
	return err
}

// deadLetter ends a task permanently and records why.
func (e *Engine) deadLetter(ctx context.Context, tx store.Conn, task Task,
	reason, detail string, now time.Time) error {
	if err := TaskFailed.Transition(TaskDead); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE tasks SET status = 'DEAD', completed_at = $1, updated_at = $1, worker_id = NULL
		WHERE id = $2 AND status IN ('FAILED', 'PENDING', 'READY', 'RETRYING')`,
		now, task.ID); err != nil {
		return fmt.Errorf("mark task dead: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO dead_letters (task_id, run_id, reason, attempts, last_worker, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (task_id) DO NOTHING`,
		task.ID, task.RunID, reason, max(task.Attempt, 1), task.WorkerID, now); err != nil {
		return fmt.Errorf("record dead letter: %w", err)
	}
	_, err := event.Append(ctx, tx, task.RunID, event.TaskDeadLetteredPayload{
		Attempts: task.Attempt,
		Reason:   reason + ": " + truncateError(detail),
	}, event.Meta{TaskName: task.Name, OccurredAt: now})
	return err
}

// unlockDependents promotes every PENDING task whose dependencies have all
// succeeded. It is called after a success, and it is where fan-in is enforced:
// a task with three dependencies is promoted by the third one to complete, and
// by neither of the first two.
func (e *Engine) unlockDependents(ctx context.Context, tx store.Conn, runID uuid.UUID,
	completed string, def *workflow.Definition, now time.Time) error {
	dependents := def.Dependents(completed)
	if len(dependents) == 0 {
		return nil
	}
	succeeded, err := succeededTasks(ctx, tx, runID)
	if err != nil {
		return err
	}

	for _, name := range dependents {
		step, ok := def.Step(name)
		if !ok {
			return fmt.Errorf("engine: dependent %q is not a step of %s", name, def.Name)
		}
		ready := true
		for _, dep := range step.DependsOn {
			if !succeeded[dep] {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		// The status guard makes this safe against two dependencies completing
		// concurrently: the second UPDATE affects no rows and emits no event.
		tag, err := tx.Exec(ctx, `
			UPDATE tasks SET status = 'READY', scheduled_at = $1, updated_at = $1
			WHERE run_id = $2 AND task_name = $3 AND status = 'PENDING'`, now, runID, name)
		if err != nil {
			return fmt.Errorf("promote dependent %q: %w", name, err)
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		if _, err := event.Append(ctx, tx, runID, event.TaskScheduledPayload{
			Attempt:     0,
			ScheduledAt: now,
			DependsOn:   step.DependsOn,
		}, event.Meta{TaskName: name, OccurredAt: now}); err != nil {
			return err
		}
	}
	return nil
}

// abandonDependents dead-letters everything downstream of a task that will
// never succeed. Without it a run with a dead task would sit with PENDING
// tasks forever, which looks identical to a stuck scheduler.
func (e *Engine) abandonDependents(ctx context.Context, tx store.Conn, runID uuid.UUID,
	failed string, def *workflow.Definition, now time.Time) error {
	unreachable := descendants(def, failed)
	for _, name := range unreachable {
		var t Task
		row := tx.QueryRow(ctx,
			`SELECT `+taskColumns+` FROM tasks WHERE run_id = $1 AND task_name = $2`, runID, name)
		t, err := scanTask(row)
		if err != nil {
			return fmt.Errorf("read dependent %q: %w", name, store.Classify(err))
		}
		if t.Status.Terminal() {
			continue
		}
		if err := e.deadLetter(ctx, tx, t, "upstream task "+failed+" did not succeed", "", now); err != nil {
			return err
		}
	}
	return nil
}

// descendants returns every step reachable from name by following dependency
// edges forwards. The definition is acyclic by validation, so this terminates.
func descendants(def *workflow.Definition, name string) []string {
	seen := map[string]bool{name: true}
	var out []string
	queue := []string{name}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range def.Dependents(current) {
			if seen[next] {
				continue
			}
			seen[next] = true
			out = append(out, next)
			queue = append(queue, next)
		}
	}
	return out
}

// settleRun closes a run once no task can still make progress.
func (e *Engine) settleRun(ctx context.Context, tx store.Conn, runID uuid.UUID, now time.Time) error {
	var total, succeeded, dead int
	if err := tx.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE status = 'SUCCEEDED'),
		       count(*) FILTER (WHERE status = 'DEAD')
		FROM tasks WHERE run_id = $1`, runID).Scan(&total, &succeeded, &dead); err != nil {
		return fmt.Errorf("count tasks of run %s: %w", runID, store.Classify(err))
	}
	if succeeded+dead < total {
		return nil // work outstanding
	}

	status, payload := RunSucceeded, event.Payload(event.RunSucceededPayload{TasksSucceeded: succeeded})
	reason := ""
	if dead > 0 {
		status = RunFailed
		reason = fmt.Sprintf("%d of %d tasks did not succeed", dead, total)
		payload = event.RunFailedPayload{Reason: "tasks dead-lettered", Detail: reason}
	}

	// The run row is locked before anything is written, so two callers
	// observing completion at once serialise here and only the first closes it.
	// The lock replaces what used to be an optimistic status guard on the
	// UPDATE, which could not work any more: the terminal event has to be
	// appended *before* completed_at is set, because event.Append refuses to
	// write to a closed run.
	var current RunStatus
	if err := tx.QueryRow(ctx,
		`SELECT status FROM runs WHERE id = $1 FOR UPDATE`, runID).Scan(&current); err != nil {
		return fmt.Errorf("lock run %s: %w", runID, store.Classify(err))
	}
	if current != RunQueued && current != RunRunning {
		return nil // already closed, or cancelled by an operator
	}
	if err := current.Transition(status); err != nil {
		return err
	}

	if _, err := event.Append(ctx, tx, runID, payload, event.Meta{OccurredAt: now}); err != nil {
		return err
	}
	e.recordRunFinished(ctx, tx, runID, status, now)
	if _, err := tx.Exec(ctx, `
		UPDATE runs SET status = $1, completed_at = $2, failure_reason = $3 WHERE id = $4`,
		string(status), now, nullString(reason), runID); err != nil {
		return fmt.Errorf("close run %s: %w", runID, err)
	}
	return nil
}

func succeededTasks(ctx context.Context, conn store.Conn, runID uuid.UUID) (map[string]bool, error) {
	rows, err := conn.Query(ctx,
		`SELECT task_name FROM tasks WHERE run_id = $1 AND status = 'SUCCEEDED'`, runID)
	if err != nil {
		return nil, fmt.Errorf("read succeeded tasks of run %s: %w", runID, store.Classify(err))
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan succeeded task: %w", store.Classify(err))
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read succeeded tasks of run %s: %w", runID, store.Classify(err))
	}
	return out, nil
}

func (e *Engine) recordArtifacts(ctx context.Context, tx store.Conn, task Task,
	artifacts []ArtifactRecord, now time.Time) ([]event.ArtifactRef, error) {
	if len(artifacts) == 0 {
		return nil, nil
	}
	refs := make([]event.ArtifactRef, 0, len(artifacts))
	for _, a := range artifacts {
		if _, err := tx.Exec(ctx, `
			INSERT INTO artifacts (id, run_id, task_name, name, sha256, size, content_type, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (run_id, task_name, name) DO UPDATE
			SET sha256 = excluded.sha256, size = excluded.size, content_type = excluded.content_type`,
			uuid.New(), task.RunID, task.Name, a.Name, a.SHA256, a.Size, a.ContentType, now); err != nil {
			return nil, fmt.Errorf("record artifact %q: %w", a.Name, err)
		}
		refs = append(refs, event.ArtifactRef{
			Name: a.Name, SHA256: a.SHA256, Size: a.Size, ContentType: a.ContentType,
		})
	}
	return refs, nil
}

func (e *Engine) definitionForRun(ctx context.Context, tx store.Conn, runID uuid.UUID) (*workflow.Definition, error) {
	var workflowID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT workflow_id FROM runs WHERE id = $1`, runID).Scan(&workflowID); err != nil {
		return nil, fmt.Errorf("read run %s: %w", runID, store.Classify(err))
	}
	return e.resolveDefinition(ctx, workflowID)
}

// errorMaxLen bounds what is stored in tasks.error and in a TASK_FAILED
// payload. An unbounded error — a stack trace, a dumped response body — would
// make the journal grow without limit and is not more useful than its first
// two kilobytes.
const errorMaxLen = 2048

func truncateError(s string) string {
	if len(s) <= errorMaxLen {
		return s
	}
	return s[:errorMaxLen] + "... (truncated)"
}

func nullJSON(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// appendSideEffectSkipped records that the idempotency ledger suppressed a
// repeat of an effect that had already happened.
func (e *Engine) appendSideEffectSkipped(ctx context.Context, task Task, key string) error {
	now := e.now()
	err := e.db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
		// The attempt that first performed the effect is recorded alongside it,
		// so the event can say which one this is a repeat of rather than only
		// that a repeat happened.
		var firstAttempt int
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(min(attempt), 0) FROM task_attempts WHERE task_id = $1`,
			task.ID).Scan(&firstAttempt); err != nil {
			return fmt.Errorf("read first attempt: %w", store.Classify(err))
		}
		_, err := event.Append(ctx, tx, task.RunID, event.SideEffectSkippedPayload{
			IdempotencyKey: key,
			Attempt:        task.Attempt,
			FirstAttempt:   firstAttempt,
		}, event.Meta{TaskName: task.Name, OccurredAt: now})
		return err
	})
	if err != nil {
		return fmt.Errorf("engine: record skipped side effect for task %s: %w", task.Name, err)
	}
	return nil
}

// recordRunFinished emits the run-level metrics.
//
// It reads the run's own timestamps rather than measuring elapsed time in this
// process: the run may have been created by one process and finished by
// another, and a duration measured here would be the settling transaction's,
// not the run's.
func (e *Engine) recordRunFinished(ctx context.Context, tx store.Conn, runID uuid.UUID,
	status RunStatus, now time.Time) {
	metrics, err := telemetry.Meter()
	if err != nil || metrics == nil {
		return
	}
	var createdAt time.Time
	var workflowName string
	if err := tx.QueryRow(ctx, `
		SELECT r.created_at, w.name
		FROM runs r JOIN workflows w ON w.id = r.workflow_id
		WHERE r.id = $1`, runID).Scan(&createdAt, &workflowName); err != nil {
		// A metric is not worth failing the transaction that closes a run.
		return
	}
	metrics.RecordRun(ctx, string(status), workflowName, now.Sub(createdAt).Seconds())
}
