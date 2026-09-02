package process

import (
	"fmt"
	"time"

	"github.com/alexou8/relab/internal/config"
)

// timingFromMap parses the same environment values the spawned workers get, so
// the test's own engine and the worker processes agree on the recovery windows.
// They would silently disagree otherwise, and the test would be asserting on a
// system that does not exist in production.
func timingFromMap(env map[string]string) (config.Timing, error) {
	timing := config.DefaultTiming()
	fields := map[string]*time.Duration{
		config.EnvLeaseDuration:      &timing.LeaseDuration,
		config.EnvLeaseRenewInterval: &timing.LeaseRenewInterval,
		config.EnvHeartbeatInterval:  &timing.HeartbeatInterval,
		config.EnvReaperInterval:     &timing.ReaperInterval,
		config.EnvTaskTimeout:        &timing.TaskTimeout,
	}
	for key, target := range fields {
		raw, ok := env[key]
		if !ok {
			continue
		}
		v, err := time.ParseDuration(raw)
		if err != nil {
			return config.Timing{}, fmt.Errorf("process: %s=%q: %w", key, raw, err)
		}
		*target = v
	}
	if err := timing.Validate(); err != nil {
		return config.Timing{}, err
	}
	return timing, nil
}
