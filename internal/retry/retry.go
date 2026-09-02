// Package retry computes backoff delays.
//
// The policy is exponential growth with a cap and proportional jitter. Jitter
// is drawn from a caller-supplied source rather than a package-level RNG, so a
// run replays to the same delays: a scenario that is only reproducible when
// nothing random happens is not much of a reliability test.
package retry

import (
	"fmt"
	"math"
	"time"

	"github.com/alexou8/relab/internal/workflow"
)

// Policy is a validated retry policy.
type Policy struct {
	MaxAttempts  int
	InitialDelay time.Duration
	Multiplier   float64
	MaxDelay     time.Duration
	Jitter       float64
}

// FromWorkflow converts a definition's policy.
func FromWorkflow(p workflow.RetryPolicy) Policy {
	return Policy{
		MaxAttempts:  p.MaxAttempts,
		InitialDelay: p.InitialDelay.Duration(),
		Multiplier:   p.Multiplier,
		MaxDelay:     p.MaxDelay.Duration(),
		Jitter:       p.Jitter,
	}
}

// Rand supplies jitter as a float in [0, 1). math/rand's Float64 satisfies it,
// as does the run's seeded source.
type Rand interface {
	Float64() float64
}

// ShouldRetry reports whether an attempt may be retried.
//
// attempt counts completed attempts, so after the first failure attempt is 1.
// permanent short-circuits regardless of the count.
func (p Policy) ShouldRetry(attempt int, permanent bool) bool {
	if permanent {
		return false
	}
	return attempt < p.MaxAttempts
}

// Delay returns the wait before the attempt after the given one.
//
// The base delay is InitialDelay * Multiplier^(attempt-1), capped at MaxDelay.
// Jitter then scales it by a factor drawn uniformly from
// [1-Jitter, 1+Jitter] — full jitter in both directions rather than only
// downwards, so that a fleet retrying together spreads out instead of all
// arriving early.
//
// The cap is applied before jitter, so a jittered delay can exceed MaxDelay by
// up to Jitter. That is intentional: clamping after jitter would pile every
// capped retry onto exactly MaxDelay, which is the synchronisation that jitter
// exists to break.
func (p Policy) Delay(attempt int, rng Rand) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := float64(p.InitialDelay)
	if p.Multiplier > 1 {
		growth := math.Pow(p.Multiplier, float64(attempt-1))
		// math.Pow overflows to +Inf for large attempt counts. MaxAttempts is
		// bounded at 100 by validation, but the guard is cheap and the
		// alternative is a negative duration after conversion.
		if math.IsInf(growth, 1) || growth > math.MaxFloat64/math.Max(base, 1) {
			base = float64(p.MaxDelay)
		} else {
			base *= growth
		}
	}
	if p.MaxDelay > 0 && base > float64(p.MaxDelay) {
		base = float64(p.MaxDelay)
	}
	if p.Jitter > 0 && rng != nil {
		// rng.Float64() is in [0, 1); 2*f-1 maps it to [-1, 1).
		base *= 1 + p.Jitter*(2*rng.Float64()-1)
	}
	if base < 0 {
		base = 0
	}
	return time.Duration(base)
}

// Validate rejects a policy that cannot work. Definitions are validated at
// parse time; this catches a policy assembled in code.
func (p Policy) Validate() error {
	if p.MaxAttempts < 1 {
		return fmt.Errorf("retry: max attempts must be at least 1, got %d", p.MaxAttempts)
	}
	if p.InitialDelay < 0 {
		return fmt.Errorf("retry: initial delay must not be negative, got %s", p.InitialDelay)
	}
	if p.MaxAttempts > 1 && p.Multiplier < 1 {
		return fmt.Errorf("retry: multiplier must be at least 1, got %v", p.Multiplier)
	}
	if p.Jitter < 0 || p.Jitter > 1 {
		return fmt.Errorf("retry: jitter must be between 0 and 1, got %v", p.Jitter)
	}
	return nil
}
