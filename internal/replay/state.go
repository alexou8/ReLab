package replay

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/google/uuid"
)

// RunState is what the reducer produces: everything the journal knows about a
// run, and nothing it does not.
type RunState struct {
	RunID    uuid.UUID `json:"run_id"`
	Workflow string    `json:"workflow"`
	Version  int       `json:"workflow_version"`
	// DefinitionHash ties the state to the exact definition the run used. Two
	// runs of "the same workflow" are only comparable when this matches.
	DefinitionHash string `json:"definition_hash"`
	Scenario       string `json:"scenario,omitempty"`
	Seed           int64  `json:"seed"`

	Status        string `json:"status"`
	FailureReason string `json:"failure_reason,omitempty"`

	Tasks map[string]*TaskState `json:"tasks"`

	// Faults is every fault the run injected, in order.
	Faults []FaultState `json:"faults,omitempty"`
	// SkippedEffects records each time the idempotency ledger suppressed a
	// repeat. Its length is the evidence that at-least-once retries did not
	// become duplicate effects.
	SkippedEffects []SkippedEffect `json:"skipped_effects,omitempty"`
	// WorkersLost counts workers declared dead during the run.
	WorkersLost int `json:"workers_lost"`

	// EventCount and LastSeq describe the journal this state came from, so a
	// comparison can say "the same events" as well as "the same state".
	EventCount int   `json:"event_count"`
	LastSeq    int64 `json:"last_seq"`
}

// TaskState is one step's reconstructed state.
type TaskState struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Attempts is how many attempts were started. A task that succeeded on its
	// third try has Attempts 3.
	Attempts int `json:"attempts"`
	// Requeues counts how many times the task was handed back after a lease
	// expired. It separates "retried because the handler failed" from "retried
	// because the worker died", which are different reliability stories.
	Requeues int `json:"requeues"`
	// Retries counts scheduled retries after a handler failure.
	Retries       int             `json:"retries"`
	LeaseExpiries int             `json:"lease_expiries"`
	Error         string          `json:"error,omitempty"`
	Output        json.RawMessage `json:"output,omitempty"`
	Artifacts     []Artifact      `json:"artifacts,omitempty"`
	// Handler is the handler name the task was executed with, from the journal
	// rather than from the definition.
	Handler string `json:"handler,omitempty"`
	// FirstStarted and Completed are wall-clock times. They are recorded for
	// human inspection and are deliberately excluded from comparison: replay
	// does not claim to reproduce timings.
	FirstStarted *time.Time `json:"first_started,omitempty"`
	Completed    *time.Time `json:"completed,omitempty"`
}

// Artifact is a content-addressed output.
type Artifact struct {
	Name        string `json:"name"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
}

// FaultState is one injected fault.
type FaultState struct {
	Type  string `json:"type"`
	Point string `json:"point"`
	Task  string `json:"task,omitempty"`
	Draw  int64  `json:"draw"`
}

// SkippedEffect is one suppressed repeat.
type SkippedEffect struct {
	Key          string `json:"key"`
	Task         string `json:"task"`
	Attempt      int    `json:"attempt"`
	FirstAttempt int    `json:"first_attempt"`
}

// TaskNames returns the task names in sorted order, so that anything iterating
// the map produces stable output. Map iteration order in Go is deliberately
// random, and a diff that changes between runs is a diff nobody trusts.
func (s *RunState) TaskNames() []string {
	names := make([]string, 0, len(s.Tasks))
	for name := range s.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Task returns a task's state, creating it if this is the first event that
// mentions it.
func (s *RunState) task(name string) *TaskState {
	if s.Tasks == nil {
		s.Tasks = map[string]*TaskState{}
	}
	t, ok := s.Tasks[name]
	if !ok {
		t = &TaskState{Name: name, Status: TaskPending}
		s.Tasks[name] = t
	}
	return t
}

// TotalAttempts sums attempts across every task, which is what the benchmark
// and assertion layers report as work done.
func (s *RunState) TotalAttempts() int {
	total := 0
	for _, t := range s.Tasks {
		total += t.Attempts
	}
	return total
}

// MaxRetries returns the highest retry count of any task, for the
// max_retries_per_task assertion.
func (s *RunState) MaxRetries() int {
	most := 0
	for _, t := range s.Tasks {
		if t.Retries > most {
			most = t.Retries
		}
	}
	return most
}

// LostTasks counts tasks that ended without succeeding. The assertion of the
// same name is the blunt question "did the system lose any work".
func (s *RunState) LostTasks() int {
	lost := 0
	for _, t := range s.Tasks {
		if t.Status == TaskDead {
			lost++
		}
	}
	return lost
}

// Terminal reports whether the journal shows the run finishing.
func (s *RunState) Terminal() bool {
	switch s.Status {
	case StatusSucceeded, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// The run statuses the reducer produces. They mirror engine.RunStatus but are
// declared here as plain strings: the reducer must not import the engine, or it
// would acquire the ability to consult something other than the journal.
const (
	StatusCreated   = "CREATED"
	StatusQueued    = "QUEUED"
	StatusRunning   = "RUNNING"
	StatusSucceeded = "SUCCEEDED"
	StatusFailed    = "FAILED"
	StatusCancelled = "CANCELLED"
)

// The task statuses the reducer produces.
const (
	TaskPending   = "PENDING"
	TaskReady     = "READY"
	TaskLeased    = "LEASED"
	TaskRunning   = "RUNNING"
	TaskSucceeded = "SUCCEEDED"
	TaskFailed    = "FAILED"
	TaskRetrying  = "RETRYING"
	TaskDead      = "DEAD"
)
