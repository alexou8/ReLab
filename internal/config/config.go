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
	"net"
	"os"
	"strconv"
	"strings"
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
	EnvDBMaxConns        = "RELAB_DB_MAX_CONNS"
	EnvAPITokens         = "RELAB_API_TOKENS"
	EnvInsecureNoAuth    = "RELAB_INSECURE_NO_AUTH"
	EnvAPIMaxBodyBytes   = "RELAB_API_MAX_BODY_BYTES"
	EnvAPIMaxLimit       = "RELAB_API_MAX_LIMIT"
	EnvAPIRateLimit      = "RELAB_API_RATE_LIMIT"
	EnvAPIRateBurst      = "RELAB_API_RATE_BURST"
)

// APIRole controls which authenticated API operations a token may perform.
type APIRole string

const (
	// RoleViewer permits read-only API operations.
	RoleViewer APIRole = "viewer"
	// RoleOperator permits viewer operations and is reserved for future
	// state-changing API operations.
	RoleOperator APIRole = "operator"
)

// APIToken is one API bearer token and its authorization role.
type APIToken struct {
	Value string
	Role  APIRole
}

// APIConfig contains the security and request-limit settings for the read API.
type APIConfig struct {
	Tokens             []APIToken
	InsecureNoAuth     bool
	MaxBodyBytes       int64
	MaxLimit           int
	RateLimitPerSecond int
	RateLimitBurst     int
}

const (
	// DefaultAPIMaxBodyBytes bounds request bodies at one mebibyte.
	DefaultAPIMaxBodyBytes int64 = 1 << 20
	// DefaultAPIMaxLimit bounds list endpoint limit parameters.
	DefaultAPIMaxLimit = 500
	// DefaultAPIRateLimit is the default sustained requests-per-second limit per
	// token or client IP.
	DefaultAPIRateLimit = 10
	// DefaultAPIRateBurst is the default short-term request burst per token or
	// client IP.
	DefaultAPIRateBurst = 20
)

// DefaultAPIConfig returns the secure-by-default API settings.
func DefaultAPIConfig() APIConfig {
	return APIConfig{
		MaxBodyBytes:       DefaultAPIMaxBodyBytes,
		MaxLimit:           DefaultAPIMaxLimit,
		RateLimitPerSecond: DefaultAPIRateLimit,
		RateLimitBurst:     DefaultAPIRateBurst,
	}
}

// APIConfigFromEnv loads API settings from the environment and validates all values.
// Token values are deliberately omitted from every parse error.
func APIConfigFromEnv() (APIConfig, error) {
	api := DefaultAPIConfig()
	if raw := os.Getenv(EnvAPITokens); raw != "" {
		var err error
		api.Tokens, err = parseAPITokens(raw)
		if err != nil {
			return APIConfig{}, err
		}
	}
	var err error
	if api.InsecureNoAuth, err = Bool(EnvInsecureNoAuth, false); err != nil {
		return APIConfig{}, err
	}
	if api.MaxBodyBytes, err = Int64(EnvAPIMaxBodyBytes, DefaultAPIMaxBodyBytes); err != nil {
		return APIConfig{}, err
	}
	if api.MaxLimit, err = Int(EnvAPIMaxLimit, DefaultAPIMaxLimit); err != nil {
		return APIConfig{}, err
	}
	if api.RateLimitPerSecond, err = Int(EnvAPIRateLimit, DefaultAPIRateLimit); err != nil {
		return APIConfig{}, err
	}
	if api.RateLimitBurst, err = Int(EnvAPIRateBurst, DefaultAPIRateBurst); err != nil {
		return APIConfig{}, err
	}
	if err := api.Validate(); err != nil {
		return APIConfig{}, err
	}
	return api, nil
}

// Validate rejects invalid API limits.
func (a APIConfig) Validate() error {
	if a.MaxBodyBytes <= 0 {
		return fmt.Errorf("config: API max body bytes must be positive")
	}
	if a.MaxLimit <= 0 {
		return fmt.Errorf("config: API max limit must be positive")
	}
	if a.RateLimitPerSecond <= 0 {
		return fmt.Errorf("config: API rate limit must be positive")
	}
	if a.RateLimitBurst <= 0 {
		return fmt.Errorf("config: API rate burst must be positive")
	}
	seen := make(map[string]struct{}, len(a.Tokens))
	for _, token := range a.Tokens {
		if token.Value == "" {
			return fmt.Errorf("config: API token must not be empty")
		}
		if token.Role != RoleViewer && token.Role != RoleOperator {
			return fmt.Errorf("config: API token has an unknown role")
		}
		if _, ok := seen[token.Value]; ok {
			return fmt.Errorf("config: API tokens contain a duplicate")
		}
		seen[token.Value] = struct{}{}
	}
	return nil
}

// Bool returns a boolean environment variable, or a fallback. A present but
// malformed value is an error rather than a silent fallback.
func Bool(key string, fallback bool) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("config: %s must be a boolean", key)
	}
	return v, nil
}

// Int64 returns an int64 environment variable, or a fallback. A present but
// malformed value is an error rather than a silent fallback.
func Int64(key string, fallback int64) (int64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer", key)
	}
	return v, nil
}

func parseAPITokens(raw string) ([]APIToken, error) {
	entries := strings.Split(raw, ",")
	tokens := make([]APIToken, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		parts := strings.SplitN(strings.TrimSpace(entry), ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return nil, fmt.Errorf("config: %s contains an invalid token entry", EnvAPITokens)
		}
		role, ok := parseRole(strings.TrimSpace(parts[0]))
		if !ok {
			return nil, fmt.Errorf("config: %s contains an unknown role", EnvAPITokens)
		}
		value := strings.TrimSpace(parts[1])
		if _, ok := seen[value]; ok {
			return nil, fmt.Errorf("config: %s contains a duplicate token", EnvAPITokens)
		}
		seen[value] = struct{}{}
		tokens = append(tokens, APIToken{Value: value, Role: role})
	}
	return tokens, nil
}

func parseRole(raw string) (APIRole, bool) {
	switch strings.ToLower(raw) {
	case "viewer":
		return RoleViewer, true
	case "operator":
		return RoleOperator, true
	default:
		return "", false
	}
}

// ValidateBind rejects an unauthenticated non-loopback TCP bind unless the
// operator explicitly opts into insecure exposure.
func (a APIConfig) ValidateBind(addr string) error {
	if len(a.Tokens) != 0 || a.InsecureNoAuth {
		return nil
	}
	host := bindHost(addr)
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("config: refusing unauthenticated non-loopback bind %q; set %s or explicitly set %s=true", addr, EnvAPITokens, EnvInsecureNoAuth)
}

func bindHost(addr string) string {
	addr = strings.TrimSpace(addr)
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return strings.Trim(host, "[]")
	}
	if strings.HasPrefix(addr, "[") && strings.HasSuffix(addr, "]") {
		return strings.Trim(addr, "[]")
	}
	return addr
}

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

// Environment variables that compress the recovery windows.
//
// They exist because the fault scenarios and the process-level tests have to
// observe real lease expiry and real heartbeat thresholds, and waiting the
// production 30 seconds for each would make the suite unusable. They are
// documented rather than hidden: an operator tuning recovery latency needs the
// same knobs.
const (
	EnvLeaseDuration      = "RELAB_LEASE_DURATION"
	EnvLeaseRenewInterval = "RELAB_LEASE_RENEW_INTERVAL"
	EnvHeartbeatInterval  = "RELAB_HEARTBEAT_INTERVAL"
	EnvReaperInterval     = "RELAB_REAPER_INTERVAL"
	EnvTaskTimeout        = "RELAB_TASK_TIMEOUT"
	EnvSuspectAfterBeats  = "RELAB_SUSPECT_AFTER_BEATS"
	EnvLostAfterBeats     = "RELAB_LOST_AFTER_BEATS"
)

// TimingFromEnv returns the defaults with any environment overrides applied,
// validated as a whole.
//
// It validates after applying every override rather than after each one, so
// that setting a shorter lease and a shorter renewal interval together is
// accepted while setting only the lease is rejected — the constraint is between
// the two values, not on either alone.
func TimingFromEnv() (Timing, error) {
	timing := DefaultTiming()
	durations := []struct {
		key    string
		target *time.Duration
	}{
		{EnvLeaseDuration, &timing.LeaseDuration},
		{EnvLeaseRenewInterval, &timing.LeaseRenewInterval},
		{EnvHeartbeatInterval, &timing.HeartbeatInterval},
		{EnvReaperInterval, &timing.ReaperInterval},
		{EnvTaskTimeout, &timing.TaskTimeout},
	}
	for _, d := range durations {
		v, err := Duration(d.key, *d.target)
		if err != nil {
			return Timing{}, err
		}
		*d.target = v
	}
	counts := []struct {
		key    string
		target *int
	}{
		{EnvSuspectAfterBeats, &timing.SuspectAfterBeats},
		{EnvLostAfterBeats, &timing.LostAfterBeats},
	}
	for _, c := range counts {
		v, err := Int(c.key, *c.target)
		if err != nil {
			return Timing{}, err
		}
		*c.target = v
	}
	if err := timing.Validate(); err != nil {
		return Timing{}, err
	}
	return timing, nil
}

// Duration returns a duration environment variable, or a fallback. As with Int,
// a present but unparseable value is an error: silently running with a 30
// second lease because someone wrote "500" instead of "500ms" would look like a
// scheduler bug.
func Duration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be a duration with a unit, such as 30s or 500ms, got %q", key, raw)
	}
	if v <= 0 {
		return 0, fmt.Errorf("config: %s must be positive, got %q", key, raw)
	}
	return v, nil
}
