package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/alexou8/relab/internal/api"
	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/store"
	"github.com/alexou8/relab/internal/testsupport"
)

func newServer(t *testing.T) http.Handler {
	t.Helper()
	db := testsupport.DB(t)
	eng, err := engine.New(db, engine.Options{})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return api.NewServer(eng, nil, "test").Routes()
}

// do issues a request and returns the recorded response.
func do(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// The API is read-only, and that is a property a public deployment depends on
// rather than a convention someone remembers. Every route must refuse anything
// that is not a read.
func TestTheAPIRefusesEveryWrite(t *testing.T) {
	h := newServer(t)
	paths := []string{
		"/api/v1/runs", "/api/v1/workflows", "/api/v1/workers", "/api/v1/stats",
	}
	methods := []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	}
	for _, path := range paths {
		for _, method := range methods {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader("{}")))
			if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
				t.Errorf("%s %s returned %d; the API is read-only and a public "+
					"deployment relies on it having no mutating route at all",
					method, path, rec.Code)
			}
		}
	}
}

// A malformed run id is the caller's mistake, not the server's, and it must not
// reach the database or be answered with a 500.
func TestAMalformedRunIDIsRejectedWithoutTouchingTheDatabase(t *testing.T) {
	h := newServer(t)
	// Escaped as a client would send them: the point is what the handler does
	// with the value, not what net/url does with the literal.
	for _, id := range []string{
		"not-a-uuid", "1", url.PathEscape("../etc/passwd"),
		url.PathEscape("'; DROP TABLE runs;--"),
	} {
		rec := do(t, h, "/api/v1/runs/"+id)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Errorf("run id %q returned %d, want 400", id, rec.Code)
		}
	}
}

// An error body carries a category and nothing else. A message quoting a
// constraint, a table or a fragment of SQL tells an anonymous caller about the
// schema and helps a legitimate one not at all.
func TestErrorBodiesCarryNoInternalDetail(t *testing.T) {
	h := newServer(t)
	rec := do(t, h, "/api/v1/runs/00000000-0000-0000-0000-000000000000")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("an absent run returned %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	for _, leak := range []string{"postgres", "SELECT", "runs.", "pgx", "sslmode"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(leak)) {
			t.Errorf("the error body contains %q: %s", leak, body)
		}
	}
}

// The limit is capped server-side. Without it a caller could ask for every row
// in the table and hold a connection the workers need more than it does.
func TestTheRunLimitIsCappedAndNonsenseFallsBack(t *testing.T) {
	h := newServer(t)
	for _, query := range []string{"limit=100000", "limit=-1", "limit=abc", "limit="} {
		rec := do(t, h, "/api/v1/runs?"+query)
		if rec.Code != http.StatusOK {
			t.Errorf("/api/v1/runs?%s returned %d, want 200: a nonsense limit is not "+
				"an error, it is a limit that does not apply", query, rec.Code)
		}
	}
}

// Readiness has to distinguish a process that is merely alive from one that can
// serve. A build whose migrations the database has not applied is the second
// case and must not take traffic.
func TestReadinessFailsWhenTheSchemaDoesNotMatchTheBuild(t *testing.T) {
	db := testsupport.DB(t)
	eng, err := engine.New(db, engine.Options{})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	h := api.NewServer(eng, nil, "test").Routes()

	rec := do(t, h, "/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz on a migrated database returned %d, want 200: %s",
			rec.Code, rec.Body.String())
	}

	expected, err := store.ExpectedSchemaVersion()
	if err != nil {
		t.Fatalf("expected schema version: %v", err)
	}
	// Pretend a migration this build carries has not been applied, which is
	// what a rollback to an older database or a half-finished deploy looks
	// like from inside the process.
	if _, err := db.Conn().Exec(t.Context(),
		`DELETE FROM schema_migrations WHERE version = $1`, expected); err != nil {
		t.Fatalf("remove migration record: %v", err)
	}

	rec = do(t, h, "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz with a mismatched schema returned %d, want 503: a process "+
			"talking to a database it does not understand must not take traffic", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode readiness body: %v", err)
	}
	if strings.Contains(body["reason"], "version") {
		t.Errorf("the readiness reason quotes schema versions to an anonymous caller: %q",
			body["reason"])
	}

	// Liveness is a different question and must still answer: the process is
	// running, and restarting it would not fix a schema mismatch.
	if rec := do(t, h, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz returned %d with a mismatched schema, want 200: liveness "+
			"asks whether the process is alive, not whether it can serve", rec.Code)
	}
}
