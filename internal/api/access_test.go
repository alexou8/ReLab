package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alexou8/relab/internal/config"
)

func testAccessServer(cfg config.APIConfig) *Server {
	return &Server{config: cfg, access: newAccessControl(cfg)}
}

func TestBearerAuthentication(t *testing.T) {
	tests := []struct {
		name  string
		role  config.APIRole
		token string
	}{
		{name: "viewer reads", role: config.RoleViewer, token: "viewer-secret"},
		{name: "operator inherits reads", role: config.RoleOperator, token: "operator-secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultAPIConfig()
			cfg.Tokens = []config.APIToken{{Value: tt.token, Role: tt.role}}
			server := testAccessServer(cfg)
			leaf := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := roleFromContext(r.Context()); got != tt.role {
					t.Fatalf("authenticated role is %q, want %q: operator must inherit viewer access without losing its role", got, tt.role)
				}
				w.WriteHeader(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
			req.Header.Set("Authorization", "Bearer "+tt.token)
			rec := httptest.NewRecorder()
			server.authorizeAndRateLimit(leaf).ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s token could not read a viewer endpoint: status %d body %s",
					tt.role, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAuthenticationFailuresAreIndistinguishable(t *testing.T) {
	cfg := config.DefaultAPIConfig()
	cfg.Tokens = []config.APIToken{{Value: "configured-secret", Role: config.RoleViewer}}
	h := testAccessServer(cfg).authorizeAndRateLimit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	requests := []struct {
		name   string
		header string
	}{
		{name: "missing token"},
		{name: "wrong token", header: "Bearer wrong-secret"},
	}
	var firstBody string
	for _, tt := range requests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("an unauthenticated request returned %d, want 401 without revealing whether the token exists", rec.Code)
			}
			if firstBody == "" {
				firstBody = rec.Body.String()
			}
			if rec.Body.String() != firstBody {
				t.Fatalf("authentication failures have distinguishable bodies: first %q, this %q", firstBody, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("authentication failure echoed credential material: %q", rec.Body.String())
			}
		})
	}
}

func TestRequestBodyLimitRejectsKnownOversizeBody(t *testing.T) {
	cfg := config.DefaultAPIConfig()
	cfg.MaxBodyBytes = 4
	server := testAccessServer(cfg)
	h := server.limitRequestBody(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", bytes.NewBufferString("12345"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body larger than the configured maximum returned %d, want 413 to bound request memory", rec.Code)
	}
}

func TestRateLimitRejectsAfterConfiguredBurst(t *testing.T) {
	cfg := config.DefaultAPIConfig()
	cfg.RateLimitPerSecond = 1
	cfg.RateLimitBurst = 2
	server := testAccessServer(cfg)
	h := server.authorizeAndRateLimit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for requestNumber, want := range []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil))
		if rec.Code != want {
			t.Fatalf("request %d returned %d, want %d: the configured burst must be a hard immediate-request bound",
				requestNumber+1, rec.Code, want)
		}
	}
}

func TestIntParamCapsTheValue(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		fallback int
		want     int
	}{
		{name: "absent uses fallback", fallback: 17, want: 17},
		{name: "fallback is capped", fallback: 100, want: 25},
		{name: "valid value", query: "limit=9", fallback: 17, want: 9},
		{name: "oversize is capped", query: "limit=101", fallback: 17, want: 25},
		{name: "invalid uses fallback", query: "limit=invalid", fallback: 17, want: 17},
	}
	server := &Server{config: config.APIConfig{MaxLimit: 25}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/?"+tt.query, nil)
			if got := server.intParam(r, "limit", tt.fallback); got != tt.want {
				t.Fatalf("limit parsing returned %d, want %d: the server-side row cap must not be bypassable", got, tt.want)
			}
		})
	}
}

// A bounded client map is what stops a flood of source addresses from growing
// the limiter without limit. Refusing every new client once it is full would
// turn that bound into the flood's weapon: fill the map, and nobody else can
// ever be admitted. Quiet clients are forgotten instead, because a bucket that
// has refilled to full burst is indistinguishable from one that does not exist.
func TestFullLimiterStillAdmitsAClientOnceTheFloodGoesQuiet(t *testing.T) {
	l := newClientLimiter(10, 20)
	start := time.Now()

	for i := range maxRateLimitClients {
		if !l.allow(key(i), start) {
			t.Fatalf("client %d was refused while the map had room", i)
		}
	}
	if l.allow("latecomer", start) {
		t.Fatal("a full map of active clients admitted another; the bound is not enforced")
	}

	// Long enough for every bucket above to be back at full burst.
	later := start.Add(10 * time.Second)
	if !l.allow("latecomer", later) {
		t.Fatal("a new client was refused although every existing bucket had refilled: " +
			"a flood of addresses can lock out every later caller")
	}
}

func key(i int) string {
	return "ip:198.51.100." + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) +
		string(rune('a'+(i/676)%26))
}
