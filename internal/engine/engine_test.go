package engine_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/store"
	"github.com/alexou8/relab/internal/testsupport"
	"github.com/alexou8/relab/internal/workflow"
	"github.com/alexou8/relab/sdk"
)

const linearYAML = `
name: linear
version: 1
steps:
  - {name: first, handler: ok}
  - {name: second, handler: ok, depends_on: [first]}
`

const diamondYAML = `
name: diamond
version: 1
steps:
  - {name: split, handler: ok}
  - {name: left, handler: ok, depends_on: [split]}
  - {name: right, handler: ok, depends_on: [split]}
  - {name: join, handler: ok, depends_on: [left, right]}
`

// fixture wires an engine, a registry and a definition against a fresh database.
type fixture struct {
	t    *testing.T
	db   *store.DB
	eng  *engine.Engine
	reg  *sdk.Registry
	def  *workflow.Definition
	wf   engine.Workflow
	ctx  context.Context
	runs *engine.LocalRunner
}

func newFixture(t *testing.T, yaml string, handlers map[string]sdk.Handler) *fixture {
	t.Helper()
	db := testsupport.DB(t)
	ctx := context.Background()

	reg := sdk.NewRegistry()
	for name, h := range handlers {
		reg.MustHandle(name, h)
	}
	def, err := workflow.Parse([]byte(yaml), reg.Set())
	if err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	eng, err := engine.New(db, engine.Options{})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	wf, err := eng.RegisterWorkflow(ctx, def)
	if err != nil {
		t.Fatalf("register workflow: %v", err)
	}
	runner, err := engine.NewLocalRunner(ctx, eng, reg, nil)
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	return &fixture{t: t, db: db, eng: eng, reg: reg, def: def, wf: wf, ctx: ctx, runs: runner}
}

func (f *fixture) start(opts engine.CreateRunOptions) engine.Run {
	f.t.Helper()
	run, err := f.eng.CreateRun(f.ctx, f.wf, f.def, opts)
	if err != nil {
		f.t.Fatalf("create run: %v", err)
	}
	return run
}

func (f *fixture) drive(run engine.Run) engine.Run {
	f.t.Helper()
	final, err := f.runs.Run(f.ctx, run.ID)
	if err != nil {
		f.t.Fatalf("drive run: %v", err)
	}
	return final
}

func (f *fixture) types(runID interface{ String() string }) []event.Type {
	f.t.Helper()
	run, err := f.eng.RunByID(f.ctx, mustUUID(f.t, runID.String()))
	if err != nil {
		f.t.Fatalf("read run: %v", err)
	}
	events, err := f.eng.Events(f.ctx, run.ID)
	if err != nil {
		f.t.Fatalf("read events: %v", err)
	}
	out := make([]event.Type, 0, len(events))
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}

func okHandler(_ context.Context, _ *sdk.TaskContext) (any, error) {
	return map[string]int{"ok": 1}, nil
}

func TestRunSucceedsAndRecordsAnOrderedTimeline(t *testing.T) {
	f := newFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler})
	run := f.drive(f.start(engine.CreateRunOptions{}))

	if run.Status != engine.RunSucceeded {
		t.Fatalf("run ended %s, want SUCCEEDED", run.Status)
	}
	types := f.types(run.ID)
	if len(types) == 0 || types[0] != event.RunCreated {
		t.Fatalf("first event is %v, want RUN_CREATED", types)
	}
	if last := types[len(types)-1]; last != event.RunSucceeded {
		t.Fatalf("last event is %s, want RUN_SUCCEEDED", last)
	}
	assertOrder(t, types, event.RunCreated, event.RunQueued, event.TaskLeased,
		event.TaskStarted, event.TaskSucceeded, event.RunSucceeded)
}

func TestFanInWaitsForEveryDependency(t *testing.T) {
	f := newFixture(t, diamondYAML, map[string]sdk.Handler{"ok": okHandler})
	run := f.drive(f.start(engine.CreateRunOptions{}))
	if run.Status != engine.RunSucceeded {
		t.Fatalf("run ended %s, want SUCCEEDED", run.Status)
	}

	events, err := f.eng.Events(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	// join must be scheduled exactly once, and only after both branches
	// succeeded. Scheduling it twice would mean the barrier released early.
	var joinScheduled, leftDone, rightDone int64
	scheduleCount := 0
	for _, e := range events {
		switch {
		case e.Type == event.TaskScheduled && e.TaskName == "join":
			joinScheduled = e.Seq
			scheduleCount++
		case e.Type == event.TaskSucceeded && e.TaskName == "left":
			leftDone = e.Seq
		case e.Type == event.TaskSucceeded && e.TaskName == "right":
			rightDone = e.Seq
		}
	}
	if scheduleCount != 1 {
		t.Fatalf("join was scheduled %d times, want exactly 1: the fan-in barrier released early", scheduleCount)
	}
	if joinScheduled < leftDone || joinScheduled < rightDone {
		t.Fatalf("join was scheduled at seq %d, before left (%d) or right (%d) succeeded",
			joinScheduled, leftDone, rightDone)
	}
}

func TestFailingTaskRetriesThenDeadLettersAndFailsTheRun(t *testing.T) {
	const yaml = `
name: flaky
version: 1
steps:
  - {name: boom, handler: fail, retry: {max_attempts: 2, initial_delay: "1ms", multiplier: 1, max_delay: "1ms", jitter: 0}}
  - {name: after, handler: fail, depends_on: [boom]}
`
	attempts := 0
	f := newFixture(t, yaml, map[string]sdk.Handler{
		"fail": func(_ context.Context, _ *sdk.TaskContext) (any, error) {
			attempts++
			return nil, errors.New("deliberate failure")
		},
	})
	run := f.drive(f.start(engine.CreateRunOptions{}))

	if run.Status != engine.RunFailed {
		t.Fatalf("run ended %s, want FAILED", run.Status)
	}
	if attempts != 2 {
		t.Fatalf("the handler ran %d times, want 2 (the first attempt plus one retry)", attempts)
	}

	tasks, err := f.eng.Tasks(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("read tasks: %v", err)
	}
	byName := map[string]engine.Task{}
	for _, task := range tasks {
		byName[task.Name] = task
	}
	if got := byName["boom"].Status; got != engine.TaskDead {
		t.Fatalf("boom ended %s, want DEAD", got)
	}
	// The downstream task can never run; leaving it PENDING would look
	// identical to a stuck scheduler.
	if got := byName["after"].Status; got != engine.TaskDead {
		t.Fatalf("after ended %s, want DEAD (its upstream never succeeded)", got)
	}

	types := f.types(run.ID)
	assertContains(t, types, event.TaskFailed, event.TaskRetryScheduled,
		event.TaskDeadLettered, event.RunFailed)
}

func TestPermanentFailureSkipsRetries(t *testing.T) {
	const yaml = `
name: permanent
version: 1
steps:
  - {name: boom, handler: fail, retry: {max_attempts: 5, initial_delay: "1ms", multiplier: 1, max_delay: "1ms", jitter: 0}}
`
	attempts := 0
	f := newFixture(t, yaml, map[string]sdk.Handler{
		"fail": func(_ context.Context, _ *sdk.TaskContext) (any, error) {
			attempts++
			return nil, sdk.Permanent(errors.New("malformed input"))
		},
	})
	run := f.drive(f.start(engine.CreateRunOptions{}))

	if run.Status != engine.RunFailed {
		t.Fatalf("run ended %s, want FAILED", run.Status)
	}
	if attempts != 1 {
		t.Fatalf("the handler ran %d times; a permanent failure must not be retried", attempts)
	}
	types := f.types(run.ID)
	for _, typ := range types {
		if typ == event.TaskRetryScheduled {
			t.Fatal("a retry was scheduled for a permanent failure")
		}
	}
}

func TestHandlerPanicIsRecordedNotPropagated(t *testing.T) {
	const yaml = `
name: panicky
version: 1
steps:
  - {name: boom, handler: panic}
`
	f := newFixture(t, yaml, map[string]sdk.Handler{
		"panic": func(_ context.Context, _ *sdk.TaskContext) (any, error) {
			panic("handler exploded")
		},
	})
	run := f.drive(f.start(engine.CreateRunOptions{}))

	if run.Status != engine.RunFailed {
		t.Fatalf("run ended %s, want FAILED", run.Status)
	}
	tasks, err := f.eng.Tasks(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("read tasks: %v", err)
	}
	if !strings.Contains(tasks[0].Error, "handler exploded") {
		t.Fatalf("recorded error is %q, want it to mention the panic", tasks[0].Error)
	}
}

func TestDependentReadsUpstreamOutput(t *testing.T) {
	const yaml = `
name: chained
version: 1
steps:
  - {name: produce, handler: produce}
  - {name: consume, handler: consume, depends_on: [produce]}
`
	var seen int
	f := newFixture(t, yaml, map[string]sdk.Handler{
		"produce": func(_ context.Context, _ *sdk.TaskContext) (any, error) {
			return map[string]int{"value": 41}, nil
		},
		"consume": func(_ context.Context, tc *sdk.TaskContext) (any, error) {
			var in map[string]int
			if err := tc.Input("produce", &in); err != nil {
				return nil, err
			}
			seen = in["value"] + 1
			return nil, nil
		},
	})
	run := f.drive(f.start(engine.CreateRunOptions{}))
	if run.Status != engine.RunSucceeded {
		t.Fatalf("run ended %s, want SUCCEEDED", run.Status)
	}
	if seen != 42 {
		t.Fatalf("the dependent read %d, want 42", seen)
	}
}

func TestRegisterIsIdempotentButRejectsAChangedDefinition(t *testing.T) {
	f := newFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler})

	again, err := f.eng.RegisterWorkflow(f.ctx, f.def)
	if err != nil {
		t.Fatalf("re-registering identical bytes failed: %v", err)
	}
	if again.ID != f.wf.ID {
		t.Fatalf("re-registration created a new row (%s vs %s)", again.ID, f.wf.ID)
	}

	changed, err := workflow.Parse([]byte(strings.Replace(linearYAML,
		"{name: second, handler: ok, depends_on: [first]}",
		"{name: third, handler: ok, depends_on: [first]}", 1)), f.reg.Set())
	if err != nil {
		t.Fatalf("parse changed definition: %v", err)
	}
	if _, err := f.eng.RegisterWorkflow(f.ctx, changed); !errors.Is(err, engine.ErrDefinitionChanged) {
		t.Fatalf("registering a changed definition returned %v, want engine.ErrDefinitionChanged", err)
	}
}

func TestSeedIsRecordedWhenNotSupplied(t *testing.T) {
	f := newFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler})
	run := f.start(engine.CreateRunOptions{})
	if run.Seed == 0 {
		t.Fatal("a run started without a seed recorded seed 0; it would not be reproducible")
	}
	stored, err := f.eng.RunByID(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if stored.Seed != run.Seed {
		t.Fatalf("stored seed %d differs from the returned one %d", stored.Seed, run.Seed)
	}
}

func TestUnknownHandlerDeadLettersRatherThanRetrying(t *testing.T) {
	f := newFixture(t, linearYAML, map[string]sdk.Handler{"ok": okHandler})
	run := f.start(engine.CreateRunOptions{})

	// A runner whose registry lacks the handler: the situation a worker running
	// an older build is in.
	empty := sdk.NewRegistry()
	runner, err := engine.NewLocalRunner(f.ctx, f.eng, empty, nil)
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	final, err := runner.Run(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("drive run: %v", err)
	}
	if final.Status != engine.RunFailed {
		t.Fatalf("run ended %s, want FAILED", final.Status)
	}
	tasks, err := f.eng.Tasks(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("read tasks: %v", err)
	}
	if !strings.Contains(tasks[0].Error, "not registered") {
		t.Fatalf("recorded error is %q, want it to name the missing handler", tasks[0].Error)
	}
}

func assertOrder(t *testing.T, got []event.Type, want ...event.Type) {
	t.Helper()
	i := 0
	for _, typ := range got {
		if i < len(want) && typ == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("timeline %v does not contain %v in order (matched %d of %d)", got, want, i, len(want))
	}
}

func assertContains(t *testing.T, got []event.Type, want ...event.Type) {
	t.Helper()
	present := map[event.Type]bool{}
	for _, typ := range got {
		present[typ] = true
	}
	for _, w := range want {
		if !present[w] {
			t.Errorf("timeline %v is missing %s", got, w)
		}
	}
}
