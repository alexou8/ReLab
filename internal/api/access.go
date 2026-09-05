package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alexou8/relab/internal/config"
)

const maxRateLimitClients = 10_000

type accessControl struct {
	tokens  []tokenIdentity
	limiter *clientLimiter
}

type tokenIdentity struct {
	digest [sha256.Size]byte
	role   config.APIRole
}

type roleContextKey struct{}

func newAccessControl(cfg config.APIConfig) *accessControl {
	tokens := make([]tokenIdentity, 0, len(cfg.Tokens))
	limiter := newClientLimiter(float64(cfg.RateLimitPerSecond), cfg.RateLimitBurst)
	now := time.Now()
	for _, token := range cfg.Tokens {
		digest := sha256.Sum256([]byte(token.Value))
		tokens = append(tokens, tokenIdentity{
			digest: digest,
			role:   token.Role,
		})
		// Authenticated identities are finite configuration, so reserve their
		// buckets before untrusted IP addresses can fill the client map.
		limiter.reserve("token:"+string(digest[:]), now)
	}
	return &accessControl{
		tokens:  tokens,
		limiter: limiter,
	}
}

func (s *Server) authorizeAndRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		digest := sha256.Sum256([]byte(bearerToken(r.Header.Get("Authorization"))))
		matched := 0
		var role config.APIRole
		for _, configured := range s.access.tokens {
			equal := subtle.ConstantTimeCompare(digest[:], configured.digest[:])
			matched |= equal
			if equal == 1 {
				role = configured.role
			}
		}
		if len(s.access.tokens) == 0 {
			role = config.RoleViewer
		}

		clientKey := "ip:" + remoteIP(r.RemoteAddr)
		if matched == 1 {
			clientKey = "token:" + string(digest[:])
		}
		if !s.access.limiter.allow(clientKey, time.Now()) {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusTooManyRequests, errorBody("rate limited"))
			return
		}
		if len(s.access.tokens) > 0 && matched != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, errorBody("unauthorized"))
			return
		}

		ctx := context.WithValue(r.Context(), roleContextKey{}, role)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func roleFromContext(ctx context.Context) config.APIRole {
	role, _ := ctx.Value(roleContextKey{}).(config.APIRole)
	return role
}

func (s *Server) limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > s.config.MaxBodyBytes {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorBody("request too large"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, s.config.MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func bearerToken(header string) string {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t") {
		return ""
	}
	return token
}

func remoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return host
	}
	return remoteAddr
}

type clientLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	clients map[string]bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newClientLimiter(rate float64, burst int) *clientLimiter {
	return &clientLimiter{
		rate:    rate,
		burst:   float64(burst),
		clients: make(map[string]bucket),
	}
}

func (l *clientLimiter) reserve(key string, now time.Time) {
	l.clients[key] = bucket{tokens: l.burst, last: now}
}

// evictRefilled drops the buckets that have sat idle long enough to be back at
// full burst, which is exactly the state in which forgetting a client changes
// nothing: a new bucket starts full too. The caller holds the mutex.
func (l *clientLimiter) evictRefilled(now time.Time) {
	if l.rate <= 0 {
		return
	}
	idleUntilFull := time.Duration(l.burst/l.rate*float64(time.Second)) + time.Second
	for key, b := range l.clients {
		if now.Sub(b.last) >= idleUntilFull {
			delete(l.clients, key)
		}
	}
}

func (l *clientLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.clients[key]
	if !ok {
		if len(l.clients) >= maxRateLimitClients {
			// The map is bounded so a flood of source addresses cannot grow it
			// without limit. Dropping every new client once it is full would
			// hand that same flood a way to lock out everyone else, so full
			// buckets — clients that have been quiet long enough to have
			// refilled — are evicted first, and only a map that is genuinely
			// all-active refuses.
			l.evictRefilled(now)
			if len(l.clients) >= maxRateLimitClients {
				return false
			}
		}
		b = bucket{tokens: l.burst, last: now}
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		l.clients[key] = b
		return false
	}
	b.tokens--
	l.clients[key] = b
	return true
}
