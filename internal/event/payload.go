package event

import (
	"encoding/json"
	"fmt"
	"time"
)

// PayloadVersion is the schema version stamped into every payload written by
// this build. It is bumped when an existing field changes meaning or is
// removed. Adding an optional field does not require a bump, because a decoder
// that ignores an unknown optional field still reconstructs correct state.
const PayloadVersion = 1

// Payload is the body of an event. Implementations are plain structs with JSON
// tags; the "v" key is added by Encode rather than by each struct, so the
// invariant holds for every payload without relying on the author remembering.
type Payload interface {
	// Type reports the event type this payload belongs to. The pairing is
	// checked on write, so a payload can never be filed under the wrong type.
	Type() Type
}

// Encode marshals p and stamps the payload version.
func Encode(p Payload) (json.RawMessage, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("event: marshal %s payload: %w", p.Type(), err)
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("event: %s payload must marshal to an object: %w", p.Type(), err)
	}
	fields["v"] = json.RawMessage(fmt.Sprintf("%d", PayloadVersion))
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("event: re-marshal %s payload: %w", p.Type(), err)
	}
	return out, nil
}

// Decode unmarshals a stored payload into p, rejecting a version this build
// does not understand. A missing "v" is treated as unknown rather than as
// version 1: every payload this system has ever written carries one, so its
// absence means the row did not come from ReLab.
func Decode(raw json.RawMessage, p Payload) error {
	var envelope struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("event: read %s payload envelope: %w", p.Type(), err)
	}
	if envelope.V == nil {
		return fmt.Errorf("event: %s payload has no version field", p.Type())
	}
	if *envelope.V != PayloadVersion {
		return fmt.Errorf("event: %s payload version %d is not supported by this build (expected %d)",
			p.Type(), *envelope.V, PayloadVersion)
	}
	if err := json.Unmarshal(raw, p); err != nil {
		return fmt.Errorf("event: unmarshal %s payload: %w", p.Type(), err)
	}
	return nil
}

// --- Run lifecycle -----------------------------------------------------------

// RunCreatedPayload records the definition a run was started from. The
// definition hash is what makes a replay comparison meaningful: two runs of
// "the same workflow" are only comparable when the bytes matched.
type RunCreatedPayload struct {
	WorkflowName    string `json:"workflow_name"`
	WorkflowVersion int    `json:"workflow_version"`
	DefinitionHash  string `json:"definition_hash"`
	ScenarioName    string `json:"scenario_name,omitempty"`
	ScenarioHash    string `json:"scenario_hash,omitempty"`
	Seed            int64  `json:"seed"`
	CorrelationID   string `json:"correlation_id,omitempty"`
}

func (RunCreatedPayload) Type() Type { return RunCreated }

// RunQueuedPayload records the tasks admitted to the queue at run start.
type RunQueuedPayload struct {
	TaskCount int `json:"task_count"`
}

func (RunQueuedPayload) Type() Type { return RunQueued }

// RunStartedPayload marks the first task execution of a run.
type RunStartedPayload struct{}

func (RunStartedPayload) Type() Type { return RunStarted }

// RunSucceededPayload closes a run in which every task reached SUCCEEDED.
type RunSucceededPayload struct {
	TasksSucceeded int `json:"tasks_succeeded"`
}

func (RunSucceededPayload) Type() Type { return RunSucceeded }

// RunFailedPayload closes a run that could not complete. Reason is a short
// stable phrase for grouping; Detail carries the specifics.
type RunFailedPayload struct {
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

func (RunFailedPayload) Type() Type { return RunFailed }

// RunCancelledPayload closes a run cancelled by an operator.
type RunCancelledPayload struct {
	Reason string `json:"reason,omitempty"`
}

func (RunCancelledPayload) Type() Type { return RunCancelled }

// --- Task lifecycle ----------------------------------------------------------

// TaskScheduledPayload records a task becoming eligible to run. Attempt is
// included because a retry schedules the same task again under a new attempt.
type TaskScheduledPayload struct {
	Attempt     int       `json:"attempt"`
	ScheduledAt time.Time `json:"scheduled_at"`
	DependsOn   []string  `json:"depends_on,omitempty"`
}

func (TaskScheduledPayload) Type() Type { return TaskScheduled }

// TaskLeasedPayload records a worker winning the claim race.
type TaskLeasedPayload struct {
	Attempt        int       `json:"attempt"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

func (TaskLeasedPayload) Type() Type { return TaskLeased }

// TaskStartedPayload records the handler being entered.
type TaskStartedPayload struct {
	Attempt int    `json:"attempt"`
	Handler string `json:"handler"`
}

func (TaskStartedPayload) Type() Type { return TaskStarted }

// TaskSucceededPayload records a handler returning without error.
// DurationMS is wall clock and is explicitly NOT compared during replay.
type TaskSucceededPayload struct {
	Attempt    int             `json:"attempt"`
	DurationMS int64           `json:"duration_ms"`
	Output     json.RawMessage `json:"output,omitempty"`
	Artifacts  []ArtifactRef   `json:"artifacts,omitempty"`
}

func (TaskSucceededPayload) Type() Type { return TaskSucceeded }

// ArtifactRef identifies a task output by content hash. Replay compares
// hashes, never bytes: the bytes may be large and live outside the database.
type ArtifactRef struct {
	Name        string `json:"name"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
}

// TaskFailedPayload records a handler returning an error, or the runtime
// failing the attempt on its behalf (timeout, panic, cancellation).
type TaskFailedPayload struct {
	Attempt    int    `json:"attempt"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error"`
	// Retryable is the scheduler's decision, recorded so that replay can tell a
	// terminal failure apart from one that simply ran out of attempts.
	Retryable bool `json:"retryable"`
}

func (TaskFailedPayload) Type() Type { return TaskFailed }

// TaskRetryScheduledPayload records the backoff decision for the next attempt.
type TaskRetryScheduledPayload struct {
	Attempt     int       `json:"attempt"`
	NextAttempt int       `json:"next_attempt"`
	DelayMS     int64     `json:"delay_ms"`
	RunAt       time.Time `json:"run_at"`
}

func (TaskRetryScheduledPayload) Type() Type { return TaskRetryScheduled }

// TaskLeaseExpiredPayload records the reaper observing an expired lease. This
// is the recovery trigger: it is emitted whether or not the worker is alive.
type TaskLeaseExpiredPayload struct {
	Attempt        int       `json:"attempt"`
	LeaseExpiredAt time.Time `json:"lease_expired_at"`
}

func (TaskLeaseExpiredPayload) Type() Type { return TaskLeaseExpired }

// TaskRequeuedPayload records a task returning to READY after lease loss.
type TaskRequeuedPayload struct {
	Attempt     int    `json:"attempt"`
	NextAttempt int    `json:"next_attempt"`
	Reason      string `json:"reason"`
}

func (TaskRequeuedPayload) Type() Type { return TaskRequeued }

// TaskDeadLetteredPayload records a task exhausting its attempts.
type TaskDeadLetteredPayload struct {
	Attempts int    `json:"attempts"`
	Reason   string `json:"reason"`
}

func (TaskDeadLetteredPayload) Type() Type { return TaskDeadLettered }

// --- Worker lifecycle --------------------------------------------------------
//
// Worker events are recorded against the run whose tasks the worker holds. A
// worker that holds nothing produces no run-scoped events; its liveness lives
// in the workers table, which is current state rather than history.

// WorkerRegisteredPayload records a worker joining the pool.
type WorkerRegisteredPayload struct {
	Hostname string `json:"hostname"`
	Version  string `json:"version"`
	Capacity int    `json:"capacity"`
}

func (WorkerRegisteredPayload) Type() Type { return WorkerRegistered }

// WorkerHeartbeatPayload records a liveness ping. Heartbeats are not written to
// the run journal on every beat — only when they change a worker's state — so
// that a long run's history stays proportional to the work it did.
type WorkerHeartbeatPayload struct {
	ActiveTasks int `json:"active_tasks"`
}

func (WorkerHeartbeatPayload) Type() Type { return WorkerHeartbeat }

// WorkerSuspectPayload records a worker missing enough heartbeats to be
// doubted. One missed heartbeat is never enough; see MissedBeats.
type WorkerSuspectPayload struct {
	MissedBeats int `json:"missed_beats"`
}

func (WorkerSuspectPayload) Type() Type { return WorkerSuspect }

// WorkerLostPayload records a worker being declared dead and its leases
// released.
type WorkerLostPayload struct {
	MissedBeats    int `json:"missed_beats"`
	LeasesReleased int `json:"leases_released"`
}

func (WorkerLostPayload) Type() Type { return WorkerLost }

// --- Fault lab ---------------------------------------------------------------

// FaultInjectedPayload is written before the fault takes effect, so that a
// fault which kills the process is still in the log afterwards.
type FaultInjectedPayload struct {
	FaultType  string `json:"fault_type"`
	FaultPoint string `json:"fault_point"`
	Scenario   string `json:"scenario"`
	Seed       int64  `json:"seed"`
	// Sequence within the run's deterministic RNG stream. Two runs with the
	// same seed and scenario must produce the same values here.
	Draw   int64           `json:"draw"`
	Params json.RawMessage `json:"params,omitempty"`
}

func (FaultInjectedPayload) Type() Type { return FaultInjected }

// SideEffectSkippedPayload records the idempotency ledger suppressing a repeat
// of an effect that already happened. Its presence is the proof that an
// at-least-once retry did not become a duplicate effect.
type SideEffectSkippedPayload struct {
	IdempotencyKey string `json:"idempotency_key"`
	Attempt        int    `json:"attempt"`
	// FirstAttempt is the attempt that actually performed the effect.
	FirstAttempt int `json:"first_attempt"`
}

func (SideEffectSkippedPayload) Type() Type { return SideEffectSkipped }
