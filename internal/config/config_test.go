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

func TestTimingFromEnvAppliesOverrides(t *testing.T) {
	t.Setenv(config.EnvLeaseDuration, "400ms")
	t.Setenv(config.EnvLeaseRenewInterval, "100ms")
	t.Setenv(config.EnvHeartbeatInterval, "50ms")

	timing, err := config.TimingFromEnv()
	if err != nil {
		t.Fatalf("TimingFromEnv: %v", err)
	}
	if timing.LeaseDuration != 400*time.Millisecond {
		t.Fatalf("lease duration is %s, want 400ms", timing.LeaseDuration)
	}
	if timing.HeartbeatInterval != 50*time.Millisecond {
		t.Fatalf("heartbeat interval is %s, want 50ms", timing.HeartbeatInterval)
	}
	// Untouched values keep their defaults.
	if timing.ReaperInterval != config.DefaultReaperInterval {
		t.Fatalf("reaper interval is %s, want the default %s", timing.ReaperInterval, config.DefaultReaperInterval)
	}
}

func TestTimingFromEnvValidatesTheWholeSet(t *testing.T) {
	// Shortening the lease without shortening the renewal interval is the
	// mistake that produces "random" duplicate executions.
	t.Setenv(config.EnvLeaseDuration, "400ms")
	_, err := config.TimingFromEnv()
	if err == nil || !strings.Contains(err.Error(), "at most half the lease duration") {
		t.Fatalf("TimingFromEnv returned %v, want a lease/renewal mismatch error", err)
	}
}

func TestDurationRejectsAUnitlessValue(t *testing.T) {
	t.Setenv(config.EnvLeaseDuration, "500")
	_, err := config.Duration(config.EnvLeaseDuration, time.Second)
	if err == nil || !strings.Contains(err.Error(), "must be a duration with a unit") {
		t.Fatalf("Duration returned %v; running with a 30s lease because someone wrote 500 "+
			"instead of 500ms would look like a scheduler bug", err)
	}
}

func clearAPIEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		config.EnvAPITokens, config.EnvInsecureNoAuth, config.EnvAPIMaxBodyBytes,
		config.EnvAPIMaxLimit, config.EnvAPIRateLimit, config.EnvAPIRateBurst,
	} {
		t.Setenv(key, "")
	}
}

func TestAPIConfigFromEnvDefaultsAndTokens(t *testing.T) {
	clearAPIEnv(t)
	t.Setenv(config.EnvAPITokens, "viewer:alpha-secret, operator:beta:secret")
	got, err := config.APIConfigFromEnv()
	if err != nil {
		t.Fatalf("APIConfigFromEnv: %v", err)
	}
	if len(got.Tokens) != 2 || got.Tokens[0] != (config.APIToken{Value: "alpha-secret", Role: config.RoleViewer}) ||
		got.Tokens[1] != (config.APIToken{Value: "beta:secret", Role: config.RoleOperator}) {
		t.Fatalf("APIConfigFromEnv parsed tokens as %#v; token roles and values must be preserved", got.Tokens)
	}
	if got.MaxBodyBytes != config.DefaultAPIMaxBodyBytes || got.MaxLimit != config.DefaultAPIMaxLimit ||
		got.RateLimitPerSecond != config.DefaultAPIRateLimit || got.RateLimitBurst != config.DefaultAPIRateBurst {
		t.Fatalf("APIConfigFromEnv defaults are %#v; request protections must have documented defaults", got)
	}
}

func TestAPIConfigFromEnvRejectsMalformedSecretsAndValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{"empty role", config.EnvAPITokens, ":secret-value", "invalid token entry"},
		{"empty token", config.EnvAPITokens, "viewer:", "invalid token entry"},
		{"unknown role", config.EnvAPITokens, "admin:secret-value", "unknown role"},
		{"duplicate token", config.EnvAPITokens, "viewer:secret-value,operator:secret-value", "duplicate token"},
		{"malformed boolean", config.EnvInsecureNoAuth, "sometimes", "must be a boolean"},
		{"nonpositive body size", config.EnvAPIMaxBodyBytes, "0", "max body bytes must be positive"},
		{"nonpositive list limit", config.EnvAPIMaxLimit, "-1", "max limit must be positive"},
		{"nonpositive rate", config.EnvAPIRateLimit, "0", "rate limit must be positive"},
		{"nonpositive burst", config.EnvAPIRateBurst, "0", "rate burst must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAPIEnv(t)
			t.Setenv(tt.key, tt.value)
			_, err := config.APIConfigFromEnv()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("APIConfigFromEnv returned %v; must reject %s to protect the API boundary", err, tt.name)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("APIConfigFromEnv error %q leaks a configured token", err)
			}
		})
	}
}

func TestAPIConfigValidateBindProtectsUnauthenticatedExposure(t *testing.T) {
	tests := []struct {
		name    string
		api     config.APIConfig
		addr    string
		wantErr bool
	}{
		{"empty host is non-loopback", config.DefaultAPIConfig(), ":8080", true},
		{"wildcard IPv4 is non-loopback", config.DefaultAPIConfig(), "0.0.0.0:8080", true},
		{"wildcard IPv6 is non-loopback", config.DefaultAPIConfig(), "[::]:8080", true},
		{"loopback IPv4 is allowed", config.DefaultAPIConfig(), "127.0.0.1:8080", false},
		{"loopback IPv6 is allowed", config.DefaultAPIConfig(), "[::1]:8080", false},
		{"localhost is allowed", config.DefaultAPIConfig(), "localhost:8080", false},
		{"token allows public bind", config.APIConfig{Tokens: []config.APIToken{{Value: "x", Role: config.RoleViewer}}}, ":8080", false},
		{"explicit opt out allows public bind", config.APIConfig{InsecureNoAuth: true}, ":8080", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.api.ValidateBind(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateBind(%q) returned %v; must protect the unauthenticated bind boundary", tt.addr, err)
			}
			if err != nil && (!strings.Contains(err.Error(), config.EnvAPITokens) || !strings.Contains(err.Error(), config.EnvInsecureNoAuth)) {
				t.Fatalf("ValidateBind error %q must name both safe configuration fixes", err)
			}
		})
	}
}

func TestAPIConfigValidateRejectsProgrammaticInvalidTokensWithoutLeakingThem(t *testing.T) {
	tests := []struct {
		name   string
		tokens []config.APIToken
	}{
		{name: "empty token", tokens: []config.APIToken{{Role: config.RoleViewer}}},
		{name: "unknown role", tokens: []config.APIToken{{Value: "secret-value", Role: "admin"}}},
		{name: "duplicate token", tokens: []config.APIToken{
			{Value: "secret-value", Role: config.RoleViewer},
			{Value: "secret-value", Role: config.RoleOperator},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := config.DefaultAPIConfig()
			api.Tokens = tt.tokens
			err := api.Validate()
			if err == nil {
				t.Fatalf("APIConfig.Validate accepted %s; programmatic configuration must enforce the authentication boundary", tt.name)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("APIConfig.Validate error %q leaks a bearer token", err)
			}
		})
	}
}
