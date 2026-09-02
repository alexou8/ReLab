package fault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// InjectedError marks a failure that a fault caused rather than the handler.
// It is distinguishable so that assertions can tell "the system failed" from
// "the system failed the way the scenario asked it to".
type InjectedError struct {
	Type   Type
	Detail string
}

func (e *InjectedError) Error() string {
	return fmt.Sprintf("injected %s fault: %s", e.Type, e.Detail)
}

// IsInjected reports whether err came from a fault injector.
func IsInjected(err error) bool {
	var injected *InjectedError
	return errors.As(err, &injected)
}

// apply performs one fault. Each type is a real degradation of the running
// system rather than a flag the scheduler consults — a fault the scheduler
// knows about is a fault the scheduler can be written to survive without the
// recovery path ever running.
func apply(ctx context.Context, f Fault) error {
	switch f.Type {
	case WorkerCrash:
		return applyWorkerCrash()
	case Latency:
		return applyLatency(ctx, f)
	case HTTPError:
		return applyHTTPError(f)
	case DBDisconnect:
		return applyDBDisconnect()
	case DuplicateDelivery:
		// Nothing happens *to* the running task. A duplicate delivery is the
		// handler being invoked a second time after the task has completed,
		// which the executor does by consulting ShouldDuplicate.
		return nil
	default:
		return fmt.Errorf("fault: no injector implements %q", f.Type)
	}
}

// applyWorkerCrash kills the worker process with SIGKILL.
//
// SIGKILL, not SIGTERM and not os.Exit: the point is that the process gets no
// chance to release its lease, flush a log, or run a deferred function. Any
// gentler exit tests a shutdown path rather than a crash, and the shutdown path
// is not the one that has to work when a machine loses power.
//
// This function does not return.
func applyWorkerCrash() error {
	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		return fmt.Errorf("fault: worker-crash could not signal its own process: %w", err)
	}
	// Unreachable: SIGKILL cannot be caught or ignored. Returning an error
	// rather than nothing keeps the function honest if that ever changes.
	return &InjectedError{Type: WorkerCrash, Detail: "the process did not die"}
}

// applyLatency delays the task, pushing it towards its lease and its timeout.
func applyLatency(ctx context.Context, f Fault) error {
	d, err := paramDuration(f.Params, "duration", 2*time.Second)
	if err != nil {
		return err
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		// The delay outlived the task. That is a legitimate outcome of a
		// latency fault, and the cancellation is what the task should report.
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// applyHTTPError fails the task as an outbound call would.
func applyHTTPError(f Fault) error {
	code, err := paramInt(f.Params, "status", 503)
	if err != nil {
		return err
	}
	return &InjectedError{
		Type:   HTTPError,
		Detail: fmt.Sprintf("upstream returned %d", code),
	}
}

// applyDBDisconnect fails the task as a dropped database connection would.
//
// It reports the failure rather than actually closing the pool. Closing the
// worker's shared pool would take out every other task on the worker, which
// makes the scenario test several things at once and none of them precisely;
// the observable behaviour under test is what the scheduler does with a task
// whose database work failed.
func applyDBDisconnect() error {
	return &InjectedError{
		Type:   DBDisconnect,
		Detail: "database connection lost",
	}
}

// ShouldDuplicate reports whether a duplicate-delivery fault applies to a task.
// The worker consults it at claim time and executes the task a second time,
// which is what exercises the idempotency ledger.
func (i *Injector) ShouldDuplicate(taskName string, attempt int) bool {
	return i.hasFault(DuplicateDelivery, taskName, attempt)
}

func (i *Injector) hasFault(typ Type, taskName string, attempt int) bool {
	if i == nil || i.scenario == nil {
		return false
	}
	for _, f := range i.scenario.Faults {
		if f.Type != typ {
			continue
		}
		if f.Target.Task != "" && f.Target.Task != taskName {
			continue
		}
		if f.Target.Attempt != 0 && f.Target.Attempt != attempt {
			continue
		}
		return true
	}
	return false
}
