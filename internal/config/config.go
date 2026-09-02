// Package config resolves runtime settings from flags and the environment.
//
// Every setting has a documented default that works for the compose stack, so
// that a reviewer can start the system without reading this file. Nothing here
// reads a config file: the deployment surface is a single binary with a handful
// of knobs, and a config file format would be one more thing to keep in sync
// with the documentation.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Defaults for the timing that governs recovery. They are stated once, here,
// because the reaper, the worker and the documentation must agree on them.
const (
	// DefaultLeaseDuration is how long a claim is valid without renewal. It has
	// to exceed the renewal interval by enough that a single slow renewal does
	// not lose the task.
	DefaultLeaseDuration = 30 * time.Second
	// DefaultLeaseRenewInterval is how often a worker renews the leases it
	// holds: one third of the lease, so two consecutive renewals can fail
	// before the lease is lost.
	DefaultLeaseRenewInterval = 10 * time.Second
	// DefaultHeartbeatInterval is how often a worker reports liveness.
	DefaultHeartbeatInterval = 5 * time.Second
	// DefaultSuspectAfterBeats is how many missed heartbeats make a worker
	// SUSPECT. One missed heartbeat is never enough — a single GC pause or
	// scheduling hiccup must not cost a worker its tasks.
	DefaultSuspectAfterBeats = 3
	// DefaultLostAfterBeats is how many missed heartbeats make a worker LOST,
	// at which point its leases are released.
	DefaultLostAfterBeats = 5
	// DefaultReaperInterval is how often expired leases and dead workers are
	// swept. It bounds recovery latency from below.
	DefaultReaperInterval = 1 * time.Second
	// DefaultTaskTimeout bounds a single handler execution.
	DefaultTaskTimeout = 5 * time.Minute
)

// Database describes how to reach PostgreSQL.
type Database struct {
	// DSN is a PostgreSQL connection string. It carries credentials, so it is
	// read from the environment by default rather than passed on a command
	// line, where it would be visible in the process table.
	DSN string
}

// Timing groups the durations that determine how quickly a failure is
// detected and recovered from. They are exposed so that the fault scenarios can
// compress them; the defaults are what the documentation quotes.
type Timing struct {
	LeaseDuration      time.Duration
	LeaseRenewInterval time.Duration
	HeartbeatInterval  time.Duration
	SuspectAfterBeats  int
	LostAfterBeats     int
	ReaperInterval     time.Duration
	TaskTimeout        time.Duration
}

// DefaultTiming returns the production defaults.
func DefaultTiming() Timing {
	return Timing{
		LeaseDuration:      DefaultLeaseDuration,
		LeaseRenewInterval: DefaultLeaseRenewInterval,
		HeartbeatInterval:  DefaultHeartbeatInterval,
		SuspectAfterBeats:  DefaultSuspectAfterBeats,
		LostAfterBeats:     DefaultLostAfterBeats,
		ReaperInterval:     DefaultReaperInterval,
		TaskTimeout:        DefaultTaskTimeout,
	}
}

// Validate rejects timing that cannot work. These are not style preferences:
// a renewal interval at or above the lease duration guarantees that every
// worker eventually loses tasks it is still running, which looks like a
// mysterious duplicate-execution bug rather than a misconfiguration.
func (t Timing) Validate() error {
	if t.LeaseDuration <= 0 {
		return fmt.Errorf("config: lease duration must be positive, got %s", t.LeaseDuration)
	}
	if t.LeaseRenewInterval <= 0 {
		return fmt.Errorf("config: lease renew interval must be positive, got %s", t.LeaseRenewInterval)
	}
	if t.LeaseRenewInterval*2 > t.LeaseDuration {
		return fmt.Errorf(
			"config: lease renew interval %s must be at most half the lease duration %s, "+
				"so that one failed renewal does not lose the task",
			t.LeaseRenewInterval, t.LeaseDuration)
	}
	if t.HeartbeatInterval <= 0 {
		return fmt.Errorf("config: heartbeat interval must be positive, got %s", t.HeartbeatInterval)
	}
	if t.SuspectAfterBeats < 2 {
		return fmt.Errorf(
			"config: suspect threshold is %d beats; it must be at least 2, because one missed "+
				"heartbeat never means failure", t.SuspectAfterBeats)
	}
	if t.LostAfterBeats <= t.SuspectAfterBeats {
		return fmt.Errorf("config: lost threshold %d must exceed suspect threshold %d",
			t.LostAfterBeats, t.SuspectAfterBeats)
	}
	if t.ReaperInterval <= 0 {
		return fmt.Errorf("config: reaper interval must be positive, got %s", t.ReaperInterval)
	}
	if t.TaskTimeout <= 0 {
		return fmt.Errorf("config: task timeout must be positive, got %s", t.TaskTimeout)
	}
	return nil
}

// Env names every environment variable ReLab reads.
const (
	EnvDSN               = "RELAB_DSN"
	EnvListenAddr        = "RELAB_LISTEN_ADDR"
	EnvLogLevel          = "RELAB_LOG_LEVEL"
	EnvLogFormat         = "RELAB_LOG_FORMAT"
	EnvOTLPEndpoint      = "RELAB_OTLP_ENDPOINT"
	EnvWorkerConcurrency = "RELAB_WORKER_CONCURRENCY"
)

// DSN returns the configured connection string. The flag wins over the
// environment so that a one-off command can target another database without
// exporting anything, but the environment is the documented path.
func DSN(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if dsn := os.Getenv(EnvDSN); dsn != "" {
		return dsn, nil
	}
	return "", fmt.Errorf("config: no database configured: set %s or pass --dsn", EnvDSN)
}

// String returns the value of an environment variable, or a fallback.
func String(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Int returns an integer environment variable, or a fallback. A value that is
// present but unparseable is an error rather than a silent fallback: silently
// ignoring RELAB_WORKER_CONCURRENCY=four would be a very hard bug to see.
func Int(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer, got %q", key, raw)
	}
	return v, nil
}
