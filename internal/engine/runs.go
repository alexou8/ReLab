package engine

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/store"
	"github.com/alexou8/relab/internal/workflow"
)

// CreateRun creates a run, its tasks, and the events describing both, in one
// transaction.
//
// Every task is created up front rather than as its dependencies complete. The
// full shape of the run is therefore visible from the moment it exists, which
// is what lets `run inspect` show a plan before anything has executed and what
// lets a restarted coordinator resume without re-deriving the graph.
func (e *Engine) CreateRun(ctx context.Context, wf Workflow, def *workflow.Definition, opts CreateRunOptions) (Run, error) {
	if def.Hash != wf.Hash {
		return Run{}, fmt.Errorf(
			"engine: definition %s does not match registered workflow %s v%d (%s)",
			def.Hash[:12], wf.Name, wf.Version, wf.Hash[:12])
	}

	seed := opts.Seed
	if seed == 0 {
		var err error
		if seed, err = NewSeed(); err != nil {
			return Run{}, err
		}
	}
	now := opts.Now
	if now.IsZero() {
		now = e.now()
	}
	now = now.UTC()

	run := Run{
		ID:            uuid.New(),
		WorkflowID:    wf.ID,
		WorkflowName:  wf.Name,
		WorkflowVer:   wf.Version,
		Status:        RunQueued,
		ScenarioName:  opts.ScenarioName,
		Seed:          seed,
		CorrelationID: opts.CorrelationID,
		CreatedAt:     now,
	}

	err := e.db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
		// The run is inserted as CREATED and moved to QUEUED at the end of the
		// same transaction, so the recorded history shows the transition the
		// state machine describes rather than a run that sprang into existence
		// already queued.
		if _, err := tx.Exec(ctx, `
			INSERT INTO runs (id, workflow_id, status, scenario_name, seed, correlation_id, created_at)
			VALUES ($1, $2, 'CREATED', $3, $4, $5, $6)`,
			run.ID, run.WorkflowID, nullString(run.ScenarioName), run.Seed,
			nullString(run.CorrelationID), now); err != nil {
			return fmt.Errorf("insert run: %w", err)
		}

		if _, err := event.Append(ctx, tx, run.ID, event.RunCreatedPayload{
			WorkflowName:    wf.Name,
			WorkflowVersion: wf.Version,
			DefinitionHash:  wf.Hash,
			ScenarioName:    opts.ScenarioName,
			ScenarioHash:    opts.ScenarioHash,
			Seed:            seed,
			CorrelationID:   opts.CorrelationID,
		}, event.Meta{OccurredAt: now}); err != nil {
			return err
		}

		for _, step := range def.Steps {
			status := TaskPending
			if len(step.DependsOn) == 0 {
				status = TaskReady
			}
			retry := def.RetryFor(step.Name)
			taskID := uuid.New()
			if _, err := tx.Exec(ctx, `
				INSERT INTO tasks (id, run_id, task_name, attempt, max_attempts, status,
				                   scheduled_at, idempotency_key, created_at, updated_at)
				VALUES ($1, $2, $3, 0, $4, $5, $6, $7, $8, $8)`,
				taskID, run.ID, step.Name, retry.MaxAttempts, string(status),
				now, idempotencyPrefix(run.ID, step.Name), now); err != nil {
				return fmt.Errorf("insert task %q: %w", step.Name, err)
			}
			// Only tasks that can actually be claimed are announced as
			// scheduled. A PENDING task is announced when its dependencies
			// complete, which is where the interesting ordering information is.
			if status == TaskReady {
				if _, err := event.Append(ctx, tx, run.ID, event.TaskScheduledPayload{
					Attempt:     0,
					ScheduledAt: now,
					DependsOn:   step.DependsOn,
				}, event.Meta{TaskName: step.Name, OccurredAt: now}); err != nil {
					return err
				}
			}
		}

		if err := RunCreated.Transition(RunQueued); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE runs SET status = 'QUEUED' WHERE id = $1 AND status = 'CREATED'`,
			run.ID); err != nil {
			return fmt.Errorf("queue run: %w", err)
		}
		_, err := event.Append(ctx, tx, run.ID, event.RunQueuedPayload{TaskCount: len(def.Steps)},
			event.Meta{OccurredAt: now})
		return err
	})
	if err != nil {
		return Run{}, fmt.Errorf("engine: create run of %s v%d: %w", wf.Name, wf.Version, err)
	}
	return run, nil
}

const runColumns = `
	r.id, r.workflow_id, w.name, w.version, r.status, coalesce(r.scenario_name, ''),
	r.seed, r.event_seq, coalesce(r.correlation_id, ''), coalesce(r.failure_reason, ''),
	r.created_at, r.started_at, r.completed_at`

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var run Run
	err := row.Scan(&run.ID, &run.WorkflowID, &run.WorkflowName, &run.WorkflowVer,
		&run.Status, &run.ScenarioName, &run.Seed, &run.EventSeq, &run.CorrelationID,
		&run.FailureReason, &run.CreatedAt, &run.StartedAt, &run.CompletedAt)
	return run, err
}

// RunByID returns one run.
func (e *Engine) RunByID(ctx context.Context, id uuid.UUID) (Run, error) {
	row := e.db.Conn().QueryRow(ctx, `
		SELECT `+runColumns+`
		FROM runs r JOIN workflows w ON w.id = r.workflow_id
		WHERE r.id = $1`, id)
	run, err := scanRun(row)
	if err != nil {
		return Run{}, fmt.Errorf("engine: look up run %s: %w", id, store.Classify(err))
	}
	return run, nil
}

// ListRunsOptions filters a run listing.
type ListRunsOptions struct {
	Status   RunStatus
	Workflow string
	Limit    int
}

// ListRuns returns runs newest first.
func (e *Engine) ListRuns(ctx context.Context, opts ListRunsOptions) ([]Run, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	// The filters are optional, so each is written as "the filter is empty OR
	// it matches". That keeps the query one statement with fixed parameters
	// instead of a string built at run time.
	rows, err := e.db.Conn().Query(ctx, `
		SELECT `+runColumns+`
		FROM runs r JOIN workflows w ON w.id = r.workflow_id
		WHERE ($1 = '' OR r.status = $1)
		  AND ($2 = '' OR w.name = $2)
		ORDER BY r.created_at DESC
		LIMIT $3`, string(opts.Status), opts.Workflow, limit)
	if err != nil {
		return nil, fmt.Errorf("engine: list runs: %w", store.Classify(err))
	}
	defer rows.Close()

	runs := make([]Run, 0, limit)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("engine: scan run: %w", store.Classify(err))
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("engine: list runs: %w", store.Classify(err))
	}
	return runs, nil
}

const taskColumns = `
	id, run_id, task_name, attempt, max_attempts, status, worker_id, lease_expires_at,
	scheduled_at, started_at, completed_at, output_ref, coalesce(error, ''), idempotency_key`

// taskColumnsQualified is the same list qualified by the alias t, for the
// RETURNING clause of the claim's UPDATE ... FROM. It is spelled out rather
// than derived from taskColumns by string surgery: the derivation was harder to
// read than the duplication, and scanTask fails loudly if the two ever drift.
const taskColumnsQualified = `
	t.id, t.run_id, t.task_name, t.attempt, t.max_attempts, t.status, t.worker_id,
	t.lease_expires_at, t.scheduled_at, t.started_at, t.completed_at, t.output_ref,
	coalesce(t.error, ''), t.idempotency_key`

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.RunID, &t.Name, &t.Attempt, &t.MaxAttempts, &t.Status,
		&t.WorkerID, &t.LeaseExpiresAt, &t.ScheduledAt, &t.StartedAt, &t.CompletedAt,
		&t.Output, &t.Error, &t.IdempotencyKey)
	return t, err
}

// Tasks returns a run's tasks in creation order.
func (e *Engine) Tasks(ctx context.Context, runID uuid.UUID) ([]Task, error) {
	return tasksOf(ctx, e.db.Conn(), runID)
}

func tasksOf(ctx context.Context, conn store.Conn, runID uuid.UUID) ([]Task, error) {
	rows, err := conn.Query(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE run_id = $1 ORDER BY created_at, task_name`, runID)
	if err != nil {
		return nil, fmt.Errorf("engine: list tasks of run %s: %w", runID, store.Classify(err))
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("engine: scan task: %w", store.Classify(err))
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("engine: list tasks of run %s: %w", runID, store.Classify(err))
	}
	return tasks, nil
}

// Events returns a run's journal.
func (e *Engine) Events(ctx context.Context, runID uuid.UUID) ([]event.Event, error) {
	return event.Read(ctx, e.db.Conn(), runID)
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Stats is the overview the dashboard and `/api/v1/stats` show.
type Stats struct {
	RunsByStatus  map[string]int `json:"runs_by_status"`
	TasksByStatus map[string]int `json:"tasks_by_status"`
	Workers       map[string]int `json:"workers_by_status"`
	QueueDepth    int            `json:"queue_depth"`
	DeadLetters   int            `json:"dead_letters"`
}

// Stats returns the counts behind the overview.
//
// It is four grouped counts rather than one query per number: the dashboard
// polls this, and a page that issues a dozen round trips to render a header is
// how a debugging tool becomes the thing that needs debugging.
func (e *Engine) Stats(ctx context.Context) (Stats, error) {
	stats := Stats{
		RunsByStatus:  map[string]int{},
		TasksByStatus: map[string]int{},
		Workers:       map[string]int{},
	}
	if err := countByStatus(ctx, e.db.Conn(),
		`SELECT status, count(*) FROM runs GROUP BY status`, stats.RunsByStatus); err != nil {
		return Stats{}, err
	}
	if err := countByStatus(ctx, e.db.Conn(),
		`SELECT status, count(*) FROM tasks GROUP BY status`, stats.TasksByStatus); err != nil {
		return Stats{}, err
	}
	if err := countByStatus(ctx, e.db.Conn(),
		`SELECT status, count(*) FROM workers GROUP BY status`, stats.Workers); err != nil {
		return Stats{}, err
	}
	if err := e.db.Conn().QueryRow(ctx,
		`SELECT count(*) FROM dead_letters`).Scan(&stats.DeadLetters); err != nil {
		return Stats{}, fmt.Errorf("engine: count dead letters: %w", store.Classify(err))
	}
	stats.QueueDepth = stats.TasksByStatus[string(TaskReady)] + stats.TasksByStatus[string(TaskRetrying)]
	return stats, nil
}

func countByStatus(ctx context.Context, conn store.Conn, query string, into map[string]int) error {
	rows, err := conn.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("engine: count by status: %w", store.Classify(err))
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return fmt.Errorf("engine: scan status count: %w", store.Classify(err))
		}
		into[status] = count
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("engine: count by status: %w", store.Classify(err))
	}
	return nil
}
