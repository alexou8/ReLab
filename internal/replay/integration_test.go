package replay_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/config"
	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/replay"
	"github.com/alexou8/relab/internal/store"
	"github.com/alexou8/relab/internal/testsupport"
	"github.com/alexou8/relab/internal/workflow"
	"github.com/alexou8/relab/sdk"
)

const artifactWorkflow = `
name: replayable
version: 1
steps:
  - name: produce
    handler: produce
    retry: {max_attempts: 3, initial_delay: "1ms", multiplier: 1, max_delay: "1ms", jitter: 0}
  - name: consume
    handler: consume
    depends_on: [produce]
`

// TestReplayOfRecordedRunsMatches is the M4 acceptance test: ten recorded runs,
// three of which recovered from a lost worker, all replay to MATCH on state and
// artifact hash.
func TestReplayOfRecordedRunsMatches(t *testing.T) {
	const total, withRecovery = 10, 3

	h := newHarness(t)
	runs := make([]uuid.UUID, 0, total)

	for i := 0; i < total; i++ {
		run := h.start()
		if i < withRecovery {
			h.runWithLostWorker(run)
		} else {
			h.drive(run)
		}
		runs = append(runs, run)
	}

	recovered := 0
	for i, runID := range runs {
		state, err := replay.Load(h.ctx, h.db.Conn(), runID)
		if err != nil {
			t.Fatalf("run %d: replay: %v", i, err)
		}
		if state.Status != replay.StatusSucceeded {
			t.Fatalf("run %d ended %s, want SUCCEEDED", i, state.Status)
		}

		// Re-reducing the same journal must produce the same state.
		again, err := replay.Load(h.ctx, h.db.Conn(), runID)
		if err != nil {
			t.Fatalf("run %d: second replay: %v", i, err)
		}
		if report := replay.Compare(state, again); !report.Match() {
			t.Fatalf("run %d does not replay to itself: %v", i, report.Divergences)
		}

		// Artifact hashes in the journal must agree with the artifacts table.
		stored, err := replay.StoredArtifacts(h.ctx, h.db.Conn(), runID)
		if err != nil {
			t.Fatalf("run %d: read artifacts: %v", i, err)
		}
		if divergences := replay.VerifyArtifacts(state, stored); len(divergences) > 0 {
			t.Fatalf("run %d: journal and artifacts table disagree: %v", i, divergences)
		}

		if state.Tasks["produce"].Requeues > 0 {
			recovered++
		}
	}

	if recovered < withRecovery {
		t.Fatalf("only %d of the %d runs actually recovered from a lost worker; the test is not "+
			"exercising what it claims to", recovered, withRecovery)
	}

	// Two runs of the same workflow must reconstruct to the same artifact
	// hashes: the handlers are deterministic, so identical inputs give
	// identical bytes.
	first, err := replay.Load(h.ctx, h.db.Conn(), runs[0])
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	last, err := replay.Load(h.ctx, h.db.Conn(), runs[total-1])
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range first.TaskNames() {
		a, b := first.Tasks[name].Artifacts, last.Tasks[name].Artifacts
		if len(a) != len(b) {
			t.Fatalf("task %s produced %d artifacts in one run and %d in another", name, len(a), len(b))
		}
		for i := range a {
			if a[i].SHA256 != b[i].SHA256 {
				t.Fatalf("task %s produced %s in one run and %s in another; the handler is not "+
					"deterministic", name, a[i].SHA256[:12], b[i].SHA256[:12])
			}
		}
	}
}

// TestCorruptedEventProducesACategoryNotACrash is the other half of the M4
// criterion.
func TestCorruptedEventProducesACategoryNotACrash(t *testing.T) {
	h := newHarness(t)
	runID := h.start()
	h.drive(runID)

	cases := []struct {
		name    string
		corrupt string
		args    []any
		wantErr string
	}{
		{
			name:    "an event is deleted",
			corrupt: `DELETE FROM events WHERE run_id = $1 AND seq = 4`,
			args:    []any{runID},
			wantErr: "gap in run",
		},
		{
			name:    "an event type is rewritten to something unknown",
			corrupt: `UPDATE events SET type = 'TASK_TELEPORTED' WHERE run_id = $1 AND seq = 4`,
			args:    []any{runID},
			wantErr: "unknown type",
		},
		{
			name:    "a payload loses its version",
			corrupt: `UPDATE events SET payload = '{"attempt":1}' WHERE run_id = $1 AND seq = 4`,
			args:    []any{runID},
			wantErr: "no version field",
		},
		{
			name:    "a payload claims a future version",
			corrupt: `UPDATE events SET payload = '{"v":99,"attempt":1}' WHERE run_id = $1 AND seq = 4`,
			args:    []any{runID},
			wantErr: "not supported by this build",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each case works on its own copy of the run, so the corruptions do
			// not compound.
			copyID := h.start()
			h.drive(copyID)
			args := append([]any{copyID}, tc.args[1:]...)
			if _, err := h.db.Conn().Exec(h.ctx, tc.corrupt, args...); err != nil {
				t.Fatalf("corrupt: %v", err)
			}

			// The specific requirement: a category, not a crash.
			state, err := func() (state *replay.RunState, err error) {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("replay panicked on a corrupt journal: %v", r)
					}
				}()
				return replay.Load(h.ctx, h.db.Conn(), copyID)
			}()
			if err == nil {
				t.Fatalf("replay accepted a corrupt journal and produced %+v", state)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("replay said %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestArtifactTamperingIsDetected proves --diff catches a row changed outside
// ReLab, which is the integrity check the artifacts comparison exists for.
func TestArtifactTamperingIsDetected(t *testing.T) {
	h := newHarness(t)
	runID := h.start()
	h.drive(runID)

	if _, err := h.db.Conn().Exec(h.ctx, `
		UPDATE artifacts SET sha256 = repeat('b', 64) WHERE run_id = $1 AND task_name = 'produce'`,
		runID); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	state, err := replay.Load(h.ctx, h.db.Conn(), runID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	stored, err := replay.StoredArtifacts(h.ctx, h.db.Conn(), runID)
	if err != nil {
		t.Fatalf("read artifacts: %v", err)
	}
	divergences := replay.VerifyArtifacts(state, stored)
	if len(divergences) == 0 {
		t.Fatal("a tampered artifact hash was not detected")
	}
	if divergences[0].Category != replay.CategoryArtifactHash {
		t.Fatalf("reported category %s, want %s", divergences[0].Category, replay.CategoryArtifactHash)
	}
}

func TestCompareReportsTerminalStateDivergenceFirst(t *testing.T) {
	succeeded := successfulRun(t).mustReduce()

	failed := newJournal(t).
		addRunCreated("wf", 1, "abc123", 42).
		add(event.TaskScheduledPayload{}, "first").
		add(event.RunFailedPayload{Reason: "boom"}, "").
		mustReduce()

	report := replay.Compare(succeeded, failed)
	if report.Match() {
		t.Fatal("two runs with different terminal states compared equal")
	}
	if report.Divergences[0].Category != replay.CategoryTerminalState {
		t.Fatalf("first divergence is %s, want terminal-state: it is the most serious category "+
			"and belongs at the top", report.Divergences[0].Category)
	}
}

func TestReplayLoadOfAnUnknownRun(t *testing.T) {
	h := newHarness(t)
	_, err := replay.Load(h.ctx, h.db.Conn(), uuid.New())
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("loading an unknown run returned %v, want store.ErrNotFound", err)
	}
}

// --- harness -----------------------------------------------------------------

type harness struct {
	t      *testing.T
	ctx    context.Context
	db     *store.DB
	eng    *engine.Engine
	reg    *sdk.Registry
	def    *workflow.Definition
	wf     engine.Workflow
	runner *engine.LocalRunner
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := testsupport.DB(t)
	ctx := context.Background()

	reg := sdk.NewRegistry()
	reg.MustHandle("produce", func(_ context.Context, tc *sdk.TaskContext) (any, error) {
		// Deterministic: derived only from the workflow, never from the clock
		// or a random source, so two runs produce the same hash.
		tc.Emit("produced.json", "application/json", []byte(`{"records":128}`))
		return map[string]int{"records": 128}, nil
	})
	reg.MustHandle("consume", func(_ context.Context, tc *sdk.TaskContext) (any, error) {
		var in map[string]int
		if err := tc.Input("produce", &in); err != nil {
			return nil, sdk.Permanent(err)
		}
		tc.Emit("consumed.txt", "text/plain", []byte(fmt.Sprintf("saw %d", in["records"])))
		return map[string]int{"seen": in["records"]}, nil
	})

	def, err := workflow.Parse([]byte(artifactWorkflow), reg.Set())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	timing := config.Timing{
		LeaseDuration: 300 * time.Millisecond, LeaseRenewInterval: 100 * time.Millisecond,
		HeartbeatInterval: 100 * time.Millisecond, SuspectAfterBeats: 3, LostAfterBeats: 5,
		ReaperInterval: 50 * time.Millisecond, TaskTimeout: 10 * time.Second,
	}
	eng, err := engine.New(db, engine.Options{Timing: timing, Logger: quiet()})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	wf, err := eng.RegisterWorkflow(ctx, def)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	runner, err := engine.NewLocalRunner(ctx, eng, reg, quiet())
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	return &harness{t: t, ctx: ctx, db: db, eng: eng, reg: reg, def: def, wf: wf, runner: runner}
}

func (h *harness) start() uuid.UUID {
	h.t.Helper()
	run, err := h.eng.CreateRun(h.ctx, h.wf, h.def, engine.CreateRunOptions{Seed: 99})
	if err != nil {
		h.t.Fatalf("create run: %v", err)
	}
	return run.ID
}

func (h *harness) drive(runID uuid.UUID) {
	h.t.Helper()
	run, err := h.runner.Run(h.ctx, runID)
	if err != nil {
		h.t.Fatalf("drive: %v", err)
	}
	if run.Status != engine.RunSucceeded {
		h.t.Fatalf("run ended %s, want SUCCEEDED", run.Status)
	}
}

// runWithLostWorker makes a run recover: a worker claims and starts the first
// task, then vanishes, and the reaper hands the task to the live runner.
func (h *harness) runWithLostWorker(runID uuid.UUID) {
	h.t.Helper()
	ghost, err := h.eng.RegisterWorker(h.ctx, engine.WorkerRegistration{
		Hostname: "ghost", Version: "test", Capacity: 1,
	})
	if err != nil {
		h.t.Fatalf("register ghost: %v", err)
	}
	claimed, err := h.eng.ClaimTasksForRun(h.ctx, ghost, runID, 1)
	if err != nil {
		h.t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 {
		h.t.Fatalf("claimed %d tasks, want 1", len(claimed))
	}
	if err := h.eng.StartTask(h.ctx, ghost, claimed[0].Task, "produce"); err != nil {
		h.t.Fatalf("start: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		result, err := h.eng.Reap(h.ctx)
		if err != nil {
			h.t.Fatalf("reap: %v", err)
		}
		if result.TasksRequeued > 0 {
			h.drive(runID)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatal("the abandoned task was never requeued")
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
