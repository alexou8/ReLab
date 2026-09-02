package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/alexou8/relab/internal/config"
)

func TestDefaultTimingIsValid(t *testing.T) {
	if err := config.DefaultTiming().Validate(); err != nil {
		t.Fatalf("the shipped defaults do not validate: %v", err)
	}
}

func TestTimingValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*config.Timing)
		wantErr string
	}{
		{
			name:    "renewal at the lease duration",
			mutate:  func(c *config.Timing) { c.LeaseRenewInterval = c.LeaseDuration },
			wantErr: "at most half the lease duration",
		},
		{
			name:    "renewal just over half the lease",
			mutate:  func(c *config.Timing) { c.LeaseRenewInterval = c.LeaseDuration/2 + time.Millisecond },
			wantErr: "at most half the lease duration",
		},
		{
			name:    "one missed heartbeat means failure",
			mutate:  func(c *config.Timing) { c.SuspectAfterBeats = 1 },
			wantErr: "one missed heartbeat never means failure",
		},
		{
			name:    "lost before suspect",
			mutate:  func(c *config.Timing) { c.LostAfterBeats = c.SuspectAfterBeats },
			wantErr: "must exceed suspect threshold",
		},
		{
			name:    "zero lease",
			mutate:  func(c *config.Timing) { c.LeaseDuration = 0 },
			wantErr: "lease duration must be positive",
		},
		{
			name:    "negative reaper interval",
			mutate:  func(c *config.Timing) { c.ReaperInterval = -time.Second },
			wantErr: "reaper interval must be positive",
		},
		{
			name:    "zero task timeout",
			mutate:  func(c *config.Timing) { c.TaskTimeout = 0 },
			wantErr: "task timeout must be positive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			timing := config.DefaultTiming()
			tt.mutate(&timing)
			err := timing.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %+v", timing)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate said %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestDSNPrefersFlagOverEnvironment(t *testing.T) {
	t.Setenv(config.EnvDSN, "postgres://from-env")
	got, err := config.DSN("postgres://from-flag")
	if err != nil {
		t.Fatalf("DSN: %v", err)
	}
	if got != "postgres://from-flag" {
		t.Fatalf("DSN returned %q, want the flag value", got)
	}
}

func TestDSNFallsBackToEnvironment(t *testing.T) {
	t.Setenv(config.EnvDSN, "postgres://from-env")
	got, err := config.DSN("")
	if err != nil {
		t.Fatalf("DSN: %v", err)
	}
	if got != "postgres://from-env" {
		t.Fatalf("DSN returned %q, want the environment value", got)
	}
}

func TestDSNRequiresConfiguration(t *testing.T) {
	t.Setenv(config.EnvDSN, "")
	_, err := config.DSN("")
	if err == nil || !strings.Contains(err.Error(), "no database configured") {
		t.Fatalf("DSN returned %v, want a not-configured error", err)
	}
}

func TestIntRejectsUnparseableValue(t *testing.T) {
	t.Setenv(config.EnvWorkerConcurrency, "four")
	_, err := config.Int(config.EnvWorkerConcurrency, 4)
	if err == nil || !strings.Contains(err.Error(), "must be an integer") {
		t.Fatalf("Int returned %v, want a parse error rather than a silent fallback", err)
	}
}
