package sdk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// TaskContext is what a handler is given: the identity of the task, the outputs
// of the steps it depends on, and the two facilities a handler cannot safely
// implement itself — an idempotent effect wrapper and an artifact recorder.
type TaskContext struct {
	// RunID and TaskName identify the work. They appear in every log line and
	// every event the handler causes.
	RunID    uuid.UUID
	TaskName string
	// Attempt counts from 1. A handler that behaves differently on a retry is
	// usually a handler that should have used Do instead.
	Attempt int
	// WorkerID identifies the process executing this attempt.
	WorkerID uuid.UUID

	// inputs holds the outputs of the steps this one depends on, by step name.
	inputs map[string]json.RawMessage
	// effects performs and records idempotent side effects.
	effects EffectLedger
	// artifacts collects what the handler produced.
	artifacts []Artifact
}

// EffectLedger records that an effect happened, so that a repeat of the same
// attempt-or-later does not perform it twice. The worker supplies the
// implementation; the interface is here so that handler tests can substitute a
// trivial one.
type EffectLedger interface {
	// Do performs fn exactly once for a given key across every attempt of a
	// task, and returns the recorded result on any later call. The
	// skipped return value reports whether fn was skipped, so the caller can
	// emit SIDE_EFFECT_SKIPPED.
	Do(ctx context.Context, key string, fn func(context.Context) (any, error)) (result json.RawMessage, skipped bool, err error)
}

// NewTaskContext builds a TaskContext. It is called by the worker; handler
// tests can call it directly to build a context with fixed inputs.
func NewTaskContext(runID uuid.UUID, taskName string, attempt int, workerID uuid.UUID,
	inputs map[string]json.RawMessage, effects EffectLedger) *TaskContext {
	return &TaskContext{
		RunID:    runID,
		TaskName: taskName,
		Attempt:  attempt,
		WorkerID: workerID,
		inputs:   inputs,
		effects:  effects,
	}
}

// Input decodes the output of a step this one depends on into v.
//
// Asking for a step that is not a declared dependency is an error rather than
// an empty result: the dependency graph is what guarantees the step has already
// run, and reading around it would read a value that may not exist yet.
func (tc *TaskContext) Input(step string, v any) error {
	raw, ok := tc.inputs[step]
	if !ok {
		return fmt.Errorf(
			"sdk: task %q has no input from step %q; is %q listed in its depends_on?",
			tc.TaskName, step, step)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return fmt.Errorf("sdk: step %q produced no output for task %q to read", step, tc.TaskName)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("sdk: decode output of step %q: %w", step, err)
	}
	return nil
}

// InputNames lists the steps whose output is available.
func (tc *TaskContext) InputNames() []string {
	names := make([]string, 0, len(tc.inputs))
	for name := range tc.inputs {
		names = append(names, name)
	}
	return names
}

// Do performs an external side effect at most once across every attempt of this
// task.
//
// op names the effect within the task — "charge", "send-email", "publish". The
// full idempotency key is run_id:task_name:op, so the same logical operation in
// two different tasks, or in two different runs, is a different effect.
//
// On a repeat, fn is not called and the recorded result is returned instead.
// This is the mechanism that turns at-least-once delivery into
// at-most-once external effects, and it is the only such mechanism: a handler
// that calls an external API outside Do is not protected.
func (tc *TaskContext) Do(ctx context.Context, op string, fn func(context.Context) (any, error)) (json.RawMessage, error) {
	if tc.effects == nil {
		return nil, fmt.Errorf("sdk: task %q has no effect ledger; Do is not usable here", tc.TaskName)
	}
	if op == "" {
		return nil, fmt.Errorf("sdk: task %q called Do with an empty operation name", tc.TaskName)
	}
	result, _, err := tc.effects.Do(ctx, IdempotencyKey(tc.RunID, tc.TaskName, op), fn)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// IdempotencyKey derives the ledger key for one logical operation.
//
// The parts are joined with ':' and step names are validated to exclude ':',
// so two different operations cannot produce the same key by concatenation.
func IdempotencyKey(runID uuid.UUID, taskName, op string) string {
	return runID.String() + ":" + taskName + ":" + op
}

// Artifact is a named output whose content hash is recorded and compared on
// replay. Artifacts are how a handler says "this is the part of my output that
// should be identical if this run is reproduced".
type Artifact struct {
	Name        string
	SHA256      string
	Size        int64
	ContentType string
}

// Emit records an artifact by hashing its content. The content itself is not
// stored: ReLab compares hashes, and keeping the bytes would make the database
// the wrong size for the job it does.
func (tc *TaskContext) Emit(name, contentType string, content []byte) Artifact {
	sum := sha256.Sum256(content)
	a := Artifact{
		Name:        name,
		SHA256:      hex.EncodeToString(sum[:]),
		Size:        int64(len(content)),
		ContentType: contentType,
	}
	tc.artifacts = append(tc.artifacts, a)
	return a
}

// EmitHashed records an artifact whose hash the handler computed itself, for
// content that was streamed somewhere else rather than held in memory.
func (tc *TaskContext) EmitHashed(name, contentType, sha256Hex string, size int64) Artifact {
	a := Artifact{Name: name, SHA256: sha256Hex, Size: size, ContentType: contentType}
	tc.artifacts = append(tc.artifacts, a)
	return a
}

// Artifacts returns what the handler emitted. The worker calls it after the
// handler returns.
func (tc *TaskContext) Artifacts() []Artifact { return tc.artifacts }
