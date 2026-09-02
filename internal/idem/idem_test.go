package idem_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/idem"
	"github.com/alexou8/relab/internal/testsupport"
)

func TestNewKeyIsScopedToRunTaskAndOperation(t *testing.T) {
	runA, runB := uuid.New(), uuid.New()
	base := idem.NewKey(runA, "charge", "capture")

	for name, other := range map[string]idem.Key{
		"another run":       idem.NewKey(runB, "charge", "capture"),
		"another task":      idem.NewKey(runA, "refund", "capture"),
		"another operation": idem.NewKey(runA, "charge", "void"),
	} {
		if base == other {
			t.Errorf("%s produced the same key %q; one effect would suppress another", name, base)
		}
	}
}

func TestDoPerformsAnEffectOnceAcrossAttempts(t *testing.T) {
	db := testsupport.DB(t)
	ctx := context.Background()
	runID := testsupport.SeedRun(t, db)
	ledger := idem.New(db)
	key := idem.NewKey(runID, "charge", "capture")

	calls := 0
	effect := func(context.Context) (any, error) {
		calls++
		return map[string]int{"amount": 100}, nil
	}

	first, skipped, err := ledger.Do(ctx, key, runID, "charge", effect)
	if err != nil {
		t.Fatalf("first Do: %v", err)
	}
	if skipped {
		t.Fatal("the first call reported the effect as already performed")
	}

	second, skipped, err := ledger.Do(ctx, key, runID, "charge", effect)
	if err != nil {
		t.Fatalf("second Do: %v", err)
	}
	if !skipped {
		t.Fatal("the second call did not report the effect as already performed")
	}
	if calls != 1 {
		t.Fatalf("the effect ran %d times, want 1", calls)
	}
	if string(first) != string(second) {
		t.Fatalf("the repeat returned %s, want the recorded %s", second, first)
	}
}

// TestDoUnderConcurrencyPerformsTheEffectOnce forces the race the ledger
// exists to close: two attempts of the same task arriving at the same key at
// once. Exactly one insert may win, and both callers must agree on the result.
func TestDoUnderConcurrencyPerformsTheEffectOnce(t *testing.T) {
	const racers = 16

	db := testsupport.DB(t)
	ctx := context.Background()
	runID := testsupport.SeedRun(t, db)
	ledger := idem.New(db)
	key := idem.NewKey(runID, "charge", "capture")

	var mu sync.Mutex
	calls := 0
	results := make([]string, 0, racers)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	errs := make(chan error, racers)

	for i := 0; i < racers; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			raw, _, err := ledger.Do(ctx, key, runID, "charge", func(context.Context) (any, error) {
				mu.Lock()
				calls++
				mu.Unlock()
				// The result names the caller, so a disagreement is visible.
				return map[string]int{"by": i}, nil
			})
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			results = append(results, string(raw))
			mu.Unlock()
		}(i)
	}
	start.Done()
	done.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Do: %v", err)
	}

	// The effect itself may run more than once — this is at-least-once
	// delivery, and the ledger cannot prevent two callers entering fn before
	// either has recorded. What it does guarantee is that exactly one record
	// survives and every caller sees it.
	count, err := ledger.CountForRun(ctx, runID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("the ledger holds %d records for one key, want exactly 1", count)
	}
	if len(results) != racers {
		t.Fatalf("%d of %d callers got a result", len(results), racers)
	}
	for i, got := range results {
		if got != results[0] {
			t.Fatalf("caller %d saw %s but caller 0 saw %s; every caller must agree on the "+
				"recorded result", i, got, results[0])
		}
	}
	t.Logf("the effect body ran %d times under %d racers; exactly one record survived",
		calls, racers)
}

// TestFailedEffectIsNotRecorded is the property that keeps a retry meaningful:
// an effect that failed did not happen, and recording it would suppress the
// retry that is supposed to fix it.
func TestFailedEffectIsNotRecorded(t *testing.T) {
	db := testsupport.DB(t)
	ctx := context.Background()
	runID := testsupport.SeedRun(t, db)
	ledger := idem.New(db)
	key := idem.NewKey(runID, "charge", "capture")

	boom := errors.New("the payment gateway said no")
	if _, _, err := ledger.Do(ctx, key, runID, "charge", func(context.Context) (any, error) {
		return nil, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("Do returned %v, want the effect's own error", err)
	}

	if _, err := ledger.Lookup(ctx, key); !errors.Is(err, idem.ErrNotRecorded) {
		t.Fatalf("a failed effect was recorded: %v", err)
	}

	// The retry runs it again, as it must.
	ran := false
	if _, skipped, err := ledger.Do(ctx, key, runID, "charge", func(context.Context) (any, error) {
		ran = true
		return map[string]bool{"ok": true}, nil
	}); err != nil || skipped {
		t.Fatalf("retry returned err=%v skipped=%v, want a fresh attempt", err, skipped)
	}
	if !ran {
		t.Fatal("the retry did not run the effect")
	}
}

func TestLookupReportsErrNotRecorded(t *testing.T) {
	db := testsupport.DB(t)
	ledger := idem.New(db)
	_, err := ledger.Lookup(context.Background(), idem.NewKey(uuid.New(), "t", "op"))
	if !errors.Is(err, idem.ErrNotRecorded) {
		t.Fatalf("Lookup returned %v, want idem.ErrNotRecorded", err)
	}
}

func TestDoRejectsANonSerialisableResult(t *testing.T) {
	db := testsupport.DB(t)
	ctx := context.Background()
	runID := testsupport.SeedRun(t, db)
	ledger := idem.New(db)

	_, _, err := ledger.Do(ctx, idem.NewKey(runID, "t", "op"), runID, "t",
		func(context.Context) (any, error) {
			return make(chan int), nil // channels do not marshal
		})
	if err == nil {
		t.Fatal("a result that cannot be recorded was accepted")
	}
	var syntax *json.UnsupportedTypeError
	if !errors.As(err, &syntax) {
		t.Logf("error was %v", err)
	}
}
