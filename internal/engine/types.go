package engine

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Workflow is a registered definition.
type Workflow struct {
	ID        uuid.UUID
	Name      string
	Version   int
	YAML      string
	Hash      string
	CreatedAt time.Time
}

// Run is one execution of a workflow.
type Run struct {
	ID            uuid.UUID
	WorkflowID    uuid.UUID
	WorkflowName  string
	WorkflowVer   int
	Status        RunStatus
	ScenarioName  string
	Seed          int64
	EventSeq      int64
	CorrelationID string
	FailureReason string
	CreatedAt     time.Time
	StartedAt     *time.Time
	CompletedAt   *time.Time
}

// Task is one step of one run.
type Task struct {
	ID             uuid.UUID
	RunID          uuid.UUID
	Name           string
	Attempt        int
	MaxAttempts    int
	Status         TaskStatus
	WorkerID       *uuid.UUID
	LeaseExpiresAt *time.Time
	ScheduledAt    time.Time
	StartedAt      *time.Time
	CompletedAt    *time.Time
	Output         json.RawMessage
	Error          string
	IdempotencyKey string
}

// Attempts returns how many attempts have completed, which is what the
// dead-letter record and the assertions report. Attempt counts from 0 before
// the first claim, so a task that has run once has Attempt == 1.
func (t *Task) Attempts() int { return t.Attempt }

// CreateRunOptions configures a new run.
type CreateRunOptions struct {
	// ScenarioName and ScenarioHash record which fault scenario, if any, the
	// run executed under. A run with no scenario still has a seed.
	ScenarioName string
	ScenarioHash string
	// Seed drives the run's deterministic RNG. Zero means "choose one", which
	// is then recorded, so every run is reproducible after the fact.
	Seed int64
	// CorrelationID ties a run to something outside ReLab.
	CorrelationID string
	// Now overrides the clock, for tests. Zero means time.Now.
	Now time.Time
}

// ClaimedTask is what a worker receives when it wins a claim: the task, the
// handler to call, and the outputs of the steps it depends on.
type ClaimedTask struct {
	Task           Task
	RunID          uuid.UUID
	WorkflowName   string
	Handler        string
	Inputs         map[string]json.RawMessage
	Timeout        time.Duration
	LeaseExpiresAt time.Time
}

// Outcome is the result of one attempt, as reported by whatever executed it.
type Outcome struct {
	Output    json.RawMessage
	Artifacts []ArtifactRecord
	Err       error
	// Permanent suppresses retries regardless of remaining attempts.
	Permanent bool
	Duration  time.Duration
}

// ArtifactRecord is a content-addressed task output.
type ArtifactRecord struct {
	Name        string
	SHA256      string
	Size        int64
	ContentType string
}
