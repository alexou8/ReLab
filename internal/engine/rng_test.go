package engine_test

import (
	"testing"

	"github.com/alexou8/relab/internal/engine"
)

func TestDerivedRandIsReproducible(t *testing.T) {
	a := engine.DerivedRand(42, "run-1", "retry-jitter", "analyze", "2")
	b := engine.DerivedRand(42, "run-1", "retry-jitter", "analyze", "2")
	for i := 0; i < 8; i++ {
		if x, y := a.Float64(), b.Float64(); x != y {
			t.Fatalf("draw %d differs: %v vs %v; the same seed and position must give the same value", i, x, y)
		}
	}
}

func TestDerivedRandSeparatesPositions(t *testing.T) {
	base := engine.DerivedRand(42, "run-1", "retry-jitter", "analyze", "1").Float64()
	cases := map[string]float64{
		"a different attempt": engine.DerivedRand(42, "run-1", "retry-jitter", "analyze", "2").Float64(),
		"a different task":    engine.DerivedRand(42, "run-1", "retry-jitter", "import", "1").Float64(),
		"a different run":     engine.DerivedRand(42, "run-2", "retry-jitter", "analyze", "1").Float64(),
		"a different seed":    engine.DerivedRand(43, "run-1", "retry-jitter", "analyze", "1").Float64(),
		"a different purpose": engine.DerivedRand(42, "run-1", "fault", "analyze", "1").Float64(),
	}
	for name, got := range cases {
		if got == base {
			t.Errorf("%s produced the same draw as the base position (%v); positions must not share a stream", name, got)
		}
	}
}

func TestDerivedRandPartBoundariesAreUnambiguous(t *testing.T) {
	// Without a length prefix, ("ab","c") and ("a","bc") would hash identically
	// and two different draws would silently share a stream.
	x := engine.DerivedRand(1, "ab", "c").Float64()
	y := engine.DerivedRand(1, "a", "bc").Float64()
	if x == y {
		t.Fatalf(`("ab","c") and ("a","bc") produced the same draw %v`, x)
	}
}

func TestNewSeedIsNonNegative(t *testing.T) {
	for i := 0; i < 64; i++ {
		seed, err := engine.NewSeed()
		if err != nil {
			t.Fatalf("NewSeed: %v", err)
		}
		if seed < 0 {
			t.Fatalf("NewSeed returned %d; seeds are printed in scenario files and must not be negative", seed)
		}
	}
}
