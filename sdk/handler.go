package sdk

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Handler executes one step of a workflow.
//
// The returned value is marshalled to JSON and recorded as the step's output,
// where it becomes the input of the steps that depend on it. Return nil when a
// step produces nothing.
//
// A handler must respect ctx. Cancellation means either the run was cancelled
// or the step exceeded its timeout, and a handler that ignores it keeps holding
// a lease that the coordinator has already given up on.
type Handler func(ctx context.Context, tc *TaskContext) (any, error)

// Registry maps handler names to implementations.
//
// The registry is the authority for "can this workflow run here": a definition
// is validated against it at registration time, so a name that nothing
// implements fails where a human is watching rather than mid-run.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]Handler)}
}

// Handle registers a handler under a name. Registering the same name twice is
// an error rather than a silent replacement: two packages each thinking they
// own "notify" is a bug that would otherwise show up as the wrong side effect.
func (r *Registry) Handle(name string, h Handler) error {
	if name == "" {
		return errors.New("sdk: handler name must not be empty")
	}
	if h == nil {
		return fmt.Errorf("sdk: handler %q is nil", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[name]; exists {
		return fmt.Errorf("sdk: handler %q is already registered", name)
	}
	r.handlers[name] = h
	return nil
}

// MustHandle is Handle for package-level registration, where returning an error
// has nowhere to go. It panics on conflict, at process start, before any work
// has been claimed.
func (r *Registry) MustHandle(name string, h Handler) {
	if err := r.Handle(name, h); err != nil {
		panic(err)
	}
}

// Lookup returns the handler registered under a name.
func (r *Registry) Lookup(name string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[name]
	return h, ok
}

// Names returns the registered handler names, sorted, for error messages.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.handlers))
	for name := range r.handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Set returns the registered names as a set, which is the form
// workflow.Parse takes.
func (r *Registry) Set() map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	set := make(map[string]bool, len(r.handlers))
	for name := range r.handlers {
		set[name] = true
	}
	return set
}

// PermanentError marks a failure that retrying cannot fix — malformed input,
// a rejected request, a missing record. The scheduler dead-letters the task
// immediately instead of spending its remaining attempts.
//
// Everything else is treated as retryable. The default is deliberate: a failure
// wrongly retried costs time, while a failure wrongly given up on loses work.
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string { return "permanent: " + e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent wraps err so the scheduler stops retrying it.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent reports whether err, or anything it wraps, is permanent.
func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}
