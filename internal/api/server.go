package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/store"
)

// Server exposes the HTTP API.
type Server struct {
	engine  *engine.Engine
	log     *slog.Logger
	version string
}

// NewServer returns a Server.
func NewServer(e *engine.Engine, log *slog.Logger, version string) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{engine: e, log: log, version: version}
}

// Routes returns the router.
//
// The route set is small on purpose: an endpoint that exists because it was
// convenient to write is an endpoint someone has to keep working.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(s.logRequests)
	// A request that outlives this deadline is holding a database connection
	// the workers need more than the caller does.
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/healthz", s.health)
	r.Get("/readyz", s.ready)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/workflows", s.listWorkflows)
		r.Get("/runs", s.listRuns)
		r.Get("/runs/{runID}", s.getRun)
		r.Get("/runs/{runID}/tasks", s.getRunTasks)
		r.Get("/runs/{runID}/events", s.getRunEvents)
		r.Get("/workers", s.listWorkers)
		r.Get("/stats", s.stats)
	})
	return r
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

// ready reports whether the process can actually serve, which for ReLab means
// the database is reachable and holds the schema this binary was built for. A
// probe that only proves the HTTP server is listening would keep a process in
// rotation that cannot do anything.
//
// The two conditions are reported separately because they need different
// responses: a database that is down comes back on its own, and a schema that
// does not match needs someone to deploy or roll back. The reason strings say
// which, and neither of them names a table, a query or a DSN.
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.DB().Ping(r.Context()); err != nil {
		s.notReady(w, r, "database is not reachable", err)
		return
	}

	expected, err := store.ExpectedSchemaVersion()
	if err != nil {
		s.notReady(w, r, "schema version could not be determined", err)
		return
	}
	applied, err := s.engine.DB().SchemaVersion(r.Context())
	if err != nil {
		s.notReady(w, r, "schema version could not be read", err)
		return
	}
	if applied != expected {
		// Logged with both numbers, answered with neither: an operator needs
		// them and reads the log, and an anonymous caller learns only that this
		// instance is not taking traffic.
		s.notReady(w, r, "database schema does not match this build",
			fmt.Errorf("schema is at version %d, this build expects %d", applied, expected))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ready", "schema_version": applied,
	})
}

func (s *Server) notReady(w http.ResponseWriter, r *http.Request, reason string, err error) {
	s.log.WarnContext(r.Context(), "not ready", "reason", reason, "error", err)
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"status": "unavailable", "reason": reason,
	})
}

func (s *Server) listWorkflows(w http.ResponseWriter, r *http.Request) {
	workflows, err := s.engine.ListWorkflows(r.Context(), intParam(r, "limit", 100))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": workflows})
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.engine.ListRuns(r.Context(), engine.ListRunsOptions{
		Status:   engine.RunStatus(r.URL.Query().Get("status")),
		Workflow: r.URL.Query().Get("workflow"),
		Limit:    intParam(r, "limit", 50),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	runID, ok := s.runID(w, r)
	if !ok {
		return
	}
	run, err := s.engine.RunByID(r.Context(), runID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) getRunTasks(w http.ResponseWriter, r *http.Request) {
	runID, ok := s.runID(w, r)
	if !ok {
		return
	}
	tasks, err := s.engine.Tasks(r.Context(), runID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) getRunEvents(w http.ResponseWriter, r *http.Request) {
	runID, ok := s.runID(w, r)
	if !ok {
		return
	}
	events, err := s.engine.Events(r.Context(), runID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) listWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := s.engine.ListWorkers(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workers": workers})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.engine.Stats(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// runID parses and validates the path parameter, answering the request itself
// when it is malformed.
func (s *Server) runID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	raw := chi.URLParam(r, "runID")
	id, err := uuid.Parse(raw)
	if err != nil {
		// The caller's own input is echoed, which is safe, but nothing about
		// the parse failure is: a malformed id says nothing about the system.
		writeJSON(w, http.StatusBadRequest, errorBody("run id must be a UUID"))
		return uuid.Nil, false
	}
	return id, true
}

// fail maps an internal error onto a status code and a message that does not
// leak the internals.
//
// The full error is logged with the request id; the caller gets the category.
// A database error message can carry a table name, a constraint, or a fragment
// of a query, and none of that helps a legitimate caller.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// The client went away or the request ran out of time; there is nobody
		// to answer, and this is not an internal failure.
		s.log.DebugContext(r.Context(), "request ended early", "path", r.URL.Path, "error", err)
	default:
		s.log.ErrorContext(r.Context(), "request failed",
			"path", r.URL.Path,
			"request_id", middleware.GetReqID(r.Context()),
			"error", err)
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
	}
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		// Health checks are the overwhelming majority of requests in a
		// deployment and say nothing; logging them buries everything else.
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			return
		}
		s.log.InfoContext(r.Context(), "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", middleware.GetReqID(r.Context()))
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// The API serves data derived from workflow definitions, which are supplied
	// by whoever runs ReLab. Telling the browser not to sniff a content type
	// costs nothing and closes the one XSS route a JSON API has.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already sent; there is nothing to do but stop.
		return
	}
}

func errorBody(message string) map[string]string {
	return map[string]string{"error": message}
}

func intParam(r *http.Request, name string, fallback int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	const maxLimit = 500
	if v > maxLimit {
		return maxLimit
	}
	return v
}
