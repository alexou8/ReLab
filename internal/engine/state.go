package engine

import "fmt"

// RunStatus is the lifecycle of a run.
type RunStatus string

// The run states. CREATED exists so that a run and its tasks can be written in
// one transaction before anything is allowed to claim them.
const (
	RunCreated   RunStatus = "CREATED"
	RunQueued    RunStatus = "QUEUED"
	RunRunning   RunStatus = "RUNNING"
	RunSucceeded RunStatus = "SUCCEEDED"
	RunFailed    RunStatus = "FAILED"
	RunCancelled RunStatus = "CANCELLED"
)

// TaskStatus is the lifecycle of one workflow step within a run.
type TaskStatus string

// The task states.
//
// PENDING means "waiting for a dependency"; READY means "eligible to be
// claimed". The distinction is what keeps the claim query's index small: only
// READY rows are in it.
//
// LEASED and RUNNING are separate because they fail differently. A task that is
// LEASED was claimed but the handler has not been entered, so losing the worker
// costs nothing. A task that is RUNNING may have already performed a side
// effect, which is why recovery from RUNNING depends on the idempotency ledger.
const (
	TaskPending   TaskStatus = "PENDING"
	TaskReady     TaskStatus = "READY"
	TaskLeased    TaskStatus = "LEASED"
	TaskRunning   TaskStatus = "RUNNING"
	TaskSucceeded TaskStatus = "SUCCEEDED"
	TaskFailed    TaskStatus = "FAILED"
	TaskRetrying  TaskStatus = "RETRYING"
	TaskDead      TaskStatus = "DEAD"
)

// Terminal reports whether a run has finished.
func (s RunStatus) Terminal() bool {
	switch s {
	case RunSucceeded, RunFailed, RunCancelled:
		return true
	default:
		return false
	}
}

// Terminal reports whether a task has finished.
//
// FAILED is not terminal. A failed attempt is always resolved, in the same
// transaction that records it, into either RETRYING (attempts remain) or DEAD
// (they do not) — so a task observed in FAILED is mid-decision, not finished.
// Treating FAILED as terminal would make a run look finished while a retry was
// still pending.
func (s TaskStatus) Terminal() bool {
	switch s {
	case TaskSucceeded, TaskDead:
		return true
	default:
		return false
	}
}

// Holds reports whether a task in this state is held by a worker under a lease.
// These are the states the reaper examines.
func (s TaskStatus) Holds() bool {
	return s == TaskLeased || s == TaskRunning
}

func (s RunStatus) String() string  { return string(s) }
func (s TaskStatus) String() string { return string(s) }

// runTransitions is the complete run state machine. Anything not listed is
// illegal.
var runTransitions = map[RunStatus][]RunStatus{
	RunCreated: {RunQueued, RunCancelled},
	RunQueued:  {RunRunning, RunSucceeded, RunFailed, RunCancelled},
	// A run can succeed straight from QUEUED only in the degenerate case of a
	// workflow whose tasks all dead-letter before any of them starts; RUNNING
	// is the normal path.
	RunRunning: {RunSucceeded, RunFailed, RunCancelled},

	RunSucceeded: nil,
	RunFailed:    nil,
	RunCancelled: nil,
}

// taskTransitions is the complete task state machine.
var taskTransitions = map[TaskStatus][]TaskStatus{
	// A dependency succeeded, or the run was cancelled before this step ran.
	TaskPending: {TaskReady, TaskFailed, TaskDead},
	// Claimed by a worker, or cancelled while waiting.
	TaskReady: {TaskLeased, TaskDead},
	// The handler was entered, the claim was lost, or the run was cancelled.
	// LEASED -> READY is lease expiry: the reaper hands the task back.
	TaskLeased: {TaskRunning, TaskReady, TaskFailed, TaskDead},
	// The handler returned, or the lease expired under a live worker.
	TaskRunning: {TaskSucceeded, TaskFailed, TaskReady, TaskDead},
	// A failure that has attempts left waits in RETRYING until its backoff
	// elapses, then returns to READY.
	TaskFailed:   {TaskRetrying, TaskDead},
	TaskRetrying: {TaskReady, TaskDead},

	TaskSucceeded: nil,
	TaskDead:      nil,
}

// IllegalTransitionError reports a transition the state machine does not allow.
// It is returned, never panicked: an illegal transition reaching the engine
// means a bug, and a bug in one run must not take down a process that is
// recovering others.
type IllegalTransitionError struct {
	Kind    string
	From    string
	To      string
	Allowed []string
}

func (e *IllegalTransitionError) Error() string {
	if len(e.Allowed) == 0 {
		return fmt.Sprintf("engine: %s cannot leave terminal state %s (attempted %s)",
			e.Kind, e.From, e.To)
	}
	return fmt.Sprintf("engine: illegal %s transition %s -> %s (allowed from %s: %v)",
		e.Kind, e.From, e.To, e.From, e.Allowed)
}

// CanTransition reports whether a run may move from -> to.
func (s RunStatus) CanTransition(to RunStatus) bool {
	for _, allowed := range runTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// Transition validates a run state change.
func (s RunStatus) Transition(to RunStatus) error {
	if !s.CanTransition(to) {
		return &IllegalTransitionError{Kind: "run", From: string(s), To: string(to),
			Allowed: stringsOfRun(runTransitions[s])}
	}
	return nil
}

// CanTransition reports whether a task may move from -> to.
func (s TaskStatus) CanTransition(to TaskStatus) bool {
	for _, allowed := range taskTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// Transition validates a task state change.
func (s TaskStatus) Transition(to TaskStatus) error {
	if !s.CanTransition(to) {
		return &IllegalTransitionError{Kind: "task", From: string(s), To: string(to),
			Allowed: stringsOfTask(taskTransitions[s])}
	}
	return nil
}

// Valid reports whether the value is one of the declared states, which is how a
// status read back from the database is checked before it is trusted.
func (s RunStatus) Valid() bool {
	_, ok := runTransitions[s]
	return ok
}

// Valid reports whether the value is one of the declared task states.
func (s TaskStatus) Valid() bool {
	_, ok := taskTransitions[s]
	return ok
}

func stringsOfRun(in []RunStatus) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}

func stringsOfTask(in []TaskStatus) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}
