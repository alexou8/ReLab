package retry_test

import (
	"testing"
	"time"

	"github.com/alexou8/relab/internal/retry"
)

// fixedRand returns the same value every draw, so delay arithmetic can be
// asserted exactly.
type fixedRand float64

func (f fixedRand) Float64() float64 { return float64(f) }

func policy() retry.Policy {
	return retry.Policy{
		MaxAttempts:  5,
		InitialDelay: time.Second,
		Multiplier:   2,
		MaxDelay:     30 * time.Second,
		Jitter:       0,
	}
}

func TestDelayGrowsExponentially(t *testing.T) {
	p := policy()
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, w := range want {
		attempt := i + 1
		if got := p.Delay(attempt, nil); got != w {
			t.Errorf("Delay(%d) = %s, want %s", attempt, got, w)
		}
	}
}

func TestDelayIsCapped(t *testing.T) {
	p := policy()
	if got := p.Delay(20, nil); got != 30*time.Second {
		t.Fatalf("Delay(20) = %s, want the 30s cap", got)
	}
}

func TestDelayDoesNotOverflow(t *testing.T) {
	p := retry.Policy{MaxAttempts: 100, InitialDelay: time.Hour, Multiplier: 10, MaxDelay: time.Minute}
	got := p.Delay(100, nil)
	if got < 0 {
		t.Fatalf("Delay(100) = %s; exponentiation overflowed into a negative duration", got)
	}
	if got > time.Minute {
		t.Fatalf("Delay(100) = %s, want at most the 1m cap", got)
	}
}

func TestJitterSpreadsBothWays(t *testing.T) {
	p := policy()
	p.Jitter = 0.5

	// A draw of 0 is the bottom of the range, 0.5 the middle, ~1 the top.
	if got, want := p.Delay(1, fixedRand(0)), 500*time.Millisecond; got != want {
		t.Errorf("with the lowest draw, Delay(1) = %s, want %s", got, want)
	}
	if got, want := p.Delay(1, fixedRand(0.5)), time.Second; got != want {
		t.Errorf("with a middle draw, Delay(1) = %s, want %s", got, want)
	}
	high := p.Delay(1, fixedRand(0.999999))
	if high <= time.Second {
		t.Errorf("with the highest draw, Delay(1) = %s, want more than 1s: jitter must be able "+
			"to delay as well as advance, or a fleet retrying together never spreads out", high)
	}
	if high > 1500*time.Millisecond {
		t.Errorf("with the highest draw, Delay(1) = %s, want at most 1.5s", high)
	}
}

func TestJitterIsDeterministicForASeed(t *testing.T) {
	// The same draws must produce the same delays; this is what makes a
	// scenario reproducible.
	p := policy()
	p.Jitter = 0.3
	for attempt := 1; attempt <= 4; attempt++ {
		a := p.Delay(attempt, fixedRand(0.42))
		b := p.Delay(attempt, fixedRand(0.42))
		if a != b {
			t.Fatalf("Delay(%d) returned %s then %s for the same draw", attempt, a, b)
		}
	}
}

func TestDelayIsNeverNegative(t *testing.T) {
	p := policy()
	p.Jitter = 1
	for draw := 0.0; draw < 1; draw += 0.05 {
		if got := p.Delay(1, fixedRand(draw)); got < 0 {
			t.Fatalf("Delay with draw %v = %s", draw, got)
		}
	}
}

func TestShouldRetry(t *testing.T) {
	p := policy() // 5 attempts
	for attempt := 1; attempt < 5; attempt++ {
		if !p.ShouldRetry(attempt, false) {
			t.Errorf("after attempt %d of 5, ShouldRetry said no", attempt)
		}
	}
	if p.ShouldRetry(5, false) {
		t.Error("after the final attempt, ShouldRetry said yes")
	}
	if p.ShouldRetry(1, true) {
		t.Error("a permanent failure was reported as retryable")
	}

	noRetry := retry.Policy{MaxAttempts: 1, InitialDelay: time.Second, Multiplier: 2}
	if noRetry.ShouldRetry(1, false) {
		t.Error("max_attempts: 1 means no retry, but ShouldRetry said yes")
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]retry.Policy{
		"zero attempts":     {MaxAttempts: 0, Multiplier: 2},
		"negative delay":    {MaxAttempts: 3, InitialDelay: -time.Second, Multiplier: 2},
		"shrinking backoff": {MaxAttempts: 3, Multiplier: 0.5},
		"jitter above one":  {MaxAttempts: 3, Multiplier: 2, Jitter: 1.5},
		"jitter below zero": {MaxAttempts: 3, Multiplier: 2, Jitter: -0.1},
	}
	for name, p := range cases {
		if err := p.Validate(); err == nil {
			t.Errorf("Validate accepted %s: %+v", name, p)
		}
	}
	if err := policy().Validate(); err != nil {
		t.Errorf("Validate rejected a sound policy: %v", err)
	}
}

func TestZeroInitialDelayMeansImmediateRetry(t *testing.T) {
	p := retry.Policy{MaxAttempts: 3, InitialDelay: 0, Multiplier: 2}
	if got := p.Delay(1, nil); got != 0 {
		t.Fatalf("Delay(1) = %s, want 0", got)
	}
	if got := p.Delay(2, nil); got != 0 {
		t.Fatalf("Delay(2) = %s; a zero initial delay must stay zero however it is multiplied", got)
	}
}
