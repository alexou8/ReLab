package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/alexou8/relab/sdk"
)

func noop(_ context.Context, _ *sdk.TaskContext) (any, error) { return nil, nil }

func TestRegistryRejectsDuplicateNames(t *testing.T) {
	reg := sdk.NewRegistry()
	if err := reg.Handle("a", noop); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	err := reg.Handle("a", noop)
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("second registration returned %v; two packages each claiming a handler name "+
			"must not silently replace one another", err)
	}
}

func TestRegistryRejectsEmptyNameAndNilHandler(t *testing.T) {
	reg := sdk.NewRegistry()
	if err := reg.Handle("", noop); err == nil {
		t.Error("an empty handler name was accepted")
	}
	if err := reg.Handle("a", nil); err == nil {
		t.Error("a nil handler was accepted")
	}
}

func TestRegistryNamesAreSorted(t *testing.T) {
	reg := sdk.NewRegistry()
	for _, name := range []string{"charlie", "alpha", "bravo"} {
		reg.MustHandle(name, noop)
	}
	got := reg.Names()
	want := []string{"alpha", "bravo", "charlie"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}
}

func TestPermanentErrorWraps(t *testing.T) {
	base := errors.New("malformed input")
	wrapped := sdk.Permanent(base)
	if !sdk.IsPermanent(wrapped) {
		t.Fatal("a wrapped error was not reported as permanent")
	}
	if !errors.Is(wrapped, base) {
		t.Fatal("Permanent lost the underlying error")
	}
	if sdk.IsPermanent(base) {
		t.Fatal("an ordinary error was reported as permanent")
	}
	if sdk.Permanent(nil) != nil {
		t.Fatal("Permanent(nil) should stay nil")
	}
}

func TestPermanentSurvivesFurtherWrapping(t *testing.T) {
	// A handler that wraps its own errors on the way out must not lose the
	// permanence marker, or the task would be retried to no purpose.
	err := sdk.Permanent(errors.New("bad row"))
	wrapped := errors.Join(errors.New("context"), err)
	if !sdk.IsPermanent(wrapped) {
		t.Fatal("permanence was lost through errors.Join")
	}
}

func TestInputRequiresADeclaredDependency(t *testing.T) {
	tc := sdk.NewTaskContext(uuid.New(), "consume", 1, uuid.New(),
		map[string]json.RawMessage{"produce": json.RawMessage(`{"value":1}`)}, nil)

	var out map[string]int
	if err := tc.Input("produce", &out); err != nil {
		t.Fatalf("reading a declared dependency: %v", err)
	}
	if out["value"] != 1 {
		t.Fatalf("read %v, want value 1", out)
	}

	err := tc.Input("somewhere-else", &out)
	if err == nil || !strings.Contains(err.Error(), "depends_on") {
		t.Fatalf("reading an undeclared dependency returned %v; it must point at depends_on, "+
			"because the graph is what guarantees the step has already run", err)
	}
}

func TestInputRejectsAnEmptyUpstreamOutput(t *testing.T) {
	tc := sdk.NewTaskContext(uuid.New(), "consume", 1, uuid.New(),
		map[string]json.RawMessage{"produce": json.RawMessage(`null`)}, nil)
	var out map[string]int
	if err := tc.Input("produce", &out); err == nil {
		t.Fatal("a null upstream output was silently accepted as an empty value")
	}
}

func TestEmitHashesContent(t *testing.T) {
	tc := sdk.NewTaskContext(uuid.New(), "t", 1, uuid.New(), nil, nil)
	a := tc.Emit("report.txt", "text/plain", []byte("hello"))

	// sha256("hello")
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if a.SHA256 != want {
		t.Fatalf("hash is %s, want %s", a.SHA256, want)
	}
	if a.Size != 5 {
		t.Fatalf("size is %d, want 5", a.Size)
	}
	if got := tc.Artifacts(); len(got) != 1 || got[0].Name != "report.txt" {
		t.Fatalf("Artifacts() = %v, want one report.txt", got)
	}
}

func TestIdempotencyKeyIsScopedToRunAndTask(t *testing.T) {
	runA, runB := uuid.New(), uuid.New()
	base := sdk.IdempotencyKey(runA, "charge", "capture")

	if base == sdk.IdempotencyKey(runB, "charge", "capture") {
		t.Error("two runs share an idempotency key; one run's effect would suppress another's")
	}
	if base == sdk.IdempotencyKey(runA, "refund", "capture") {
		t.Error("two tasks in one run share an idempotency key")
	}
	if base == sdk.IdempotencyKey(runA, "charge", "void") {
		t.Error("two operations in one task share an idempotency key")
	}
	if !strings.HasPrefix(base, runA.String()+":charge:") {
		t.Errorf("key %q does not have the documented run:task:op shape", base)
	}
}

func TestDoWithoutALedgerIsAnError(t *testing.T) {
	tc := sdk.NewTaskContext(uuid.New(), "t", 1, uuid.New(), nil, nil)
	_, err := tc.Do(context.Background(), "charge", func(context.Context) (any, error) {
		t.Fatal("the effect ran even though there was no ledger to record it")
		return nil, nil
	})
	if err == nil {
		t.Fatal("Do without a ledger silently performed an unrecorded effect")
	}
}

func TestDoRejectsAnEmptyOperationName(t *testing.T) {
	tc := sdk.NewTaskContext(uuid.New(), "t", 1, uuid.New(), nil, stubLedger{})
	if _, err := tc.Do(context.Background(), "", func(context.Context) (any, error) {
		return nil, nil
	}); err == nil {
		t.Fatal("an empty operation name was accepted; it would collide with every other effect")
	}
}

type stubLedger struct{}

func (stubLedger) Do(ctx context.Context, _ string,
	fn func(context.Context) (any, error)) (json.RawMessage, bool, error) {
	v, err := fn(ctx)
	if err != nil {
		return nil, false, err
	}
	raw, err := json.Marshal(v)
	return raw, false, err
}
