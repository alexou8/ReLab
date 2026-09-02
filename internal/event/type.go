package event

import "fmt"

// Type identifies what happened. Types are stored as text so that the log
// stays readable in psql and so that adding a type is not a schema migration.
type Type string

// The complete set of event types. The reducer in package replay must handle
// every one of these; a type absent from this list is rejected on read.
const (
	RunCreated   Type = "RUN_CREATED"
	RunQueued    Type = "RUN_QUEUED"
	RunStarted   Type = "RUN_STARTED"
	RunSucceeded Type = "RUN_SUCCEEDED"
	RunFailed    Type = "RUN_FAILED"
	RunCancelled Type = "RUN_CANCELLED"

	TaskScheduled      Type = "TASK_SCHEDULED"
	TaskLeased         Type = "TASK_LEASED"
	TaskStarted        Type = "TASK_STARTED"
	TaskSucceeded      Type = "TASK_SUCCEEDED"
	TaskFailed         Type = "TASK_FAILED"
	TaskRetryScheduled Type = "TASK_RETRY_SCHEDULED"
	TaskLeaseExpired   Type = "TASK_LEASE_EXPIRED"
	TaskRequeued       Type = "TASK_REQUEUED"
	TaskDeadLettered   Type = "TASK_DEAD_LETTERED"

	WorkerRegistered Type = "WORKER_REGISTERED"
	WorkerHeartbeat  Type = "WORKER_HEARTBEAT"
	WorkerSuspect    Type = "WORKER_SUSPECT"
	WorkerLost       Type = "WORKER_LOST"

	FaultInjected     Type = "FAULT_INJECTED"
	SideEffectSkipped Type = "SIDE_EFFECT_SKIPPED"
)

// knownTypes is the authority for what may appear in the log. It is also what
// makes an unknown type a loud failure on read instead of a silent skip.
var knownTypes = map[Type]struct{}{
	RunCreated: {}, RunQueued: {}, RunStarted: {},
	RunSucceeded: {}, RunFailed: {}, RunCancelled: {},

	TaskScheduled: {}, TaskLeased: {}, TaskStarted: {},
	TaskSucceeded: {}, TaskFailed: {}, TaskRetryScheduled: {},
	TaskLeaseExpired: {}, TaskRequeued: {}, TaskDeadLettered: {},

	WorkerRegistered: {}, WorkerHeartbeat: {}, WorkerSuspect: {}, WorkerLost: {},

	FaultInjected: {}, SideEffectSkipped: {},
}

// Known reports whether t is a type this build understands.
func (t Type) Known() bool {
	_, ok := knownTypes[t]
	return ok
}

func (t Type) String() string { return string(t) }

// Terminal reports whether t ends a run. Exactly one terminal event may appear
// in a run's log, and it is always the last one.
func (t Type) Terminal() bool {
	switch t {
	case RunSucceeded, RunFailed, RunCancelled:
		return true
	default:
		return false
	}
}

// ErrUnknownType is returned when the log contains a type this build does not
// know. It carries the offending value so the operator can tell whether the
// database was written by a newer binary or corrupted.
type ErrUnknownType struct {
	Type  Type
	RunID string
	Seq   int64
}

func (e *ErrUnknownType) Error() string {
	return fmt.Sprintf("event: unknown type %q at run %s seq %d", e.Type, e.RunID, e.Seq)
}
