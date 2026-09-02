package worker

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/config"
	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/telemetry"
	"github.com/alexou8/relab/sdk"
)

// Options configures a worker.
type Options struct {
	// Concurrency is how many tasks the worker executes at once. It is also
	// the capacity it reports, which is what the dashboard shows as load.
	Concurrency int
	// Version is recorded on the worker row so a run's history says which
	// build executed it.
	Version string
	// Hostname defaults to the OS hostname.
	Hostname string
	// PollInterval is how long to wait after finding no work. Claiming is a
	// database round trip, so an idle fleet polling too eagerly is pure load.
	PollInterval time.Duration
	Logger       *slog.Logger
	// Faults, when set, injects the scenario a run was started under. A plain
	// `relab worker` leaves it nil and injects nothing.
	Faults engine.FaultSource
}

// Worker executes tasks claimed from the queue.
type Worker struct {
	engine   *engine.Engine
	executor *engine.Executor
	registry *sdk.Registry
	id       uuid.UUID
	opts     Options
	timing   config.Timing
	log      *slog.Logger

	// held maps each in-flight task to the function that cancels its execution.
	// The renewal loop uses it both to know what to renew and to stop work on a
	// task whose lease it could not keep. It is guarded because the execution
	// goroutines and the renewal loop both touch it.
	mu   sync.Mutex
	held map[uuid.UUID]context.CancelFunc
}

// New registers a worker and returns it ready to run.
func New(ctx context.Context, eng *engine.Engine, reg *sdk.Registry, opts Options) (*Worker, error) {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.Hostname == "" {
		host, err := os.Hostname()
		if err != nil {
			// A worker without a hostname is still usable; the hostname is for
			// humans reading `relab workers`, not for correctness.
			host = "unknown"
		}
		opts.Hostname = host
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 200 * time.Millisecond
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	id, err := eng.RegisterWorker(ctx, engine.WorkerRegistration{
		Hostname: opts.Hostname,
		Version:  opts.Version,
		Capacity: opts.Concurrency,
	})
	if err != nil {
		return nil, err
	}
	log := opts.Logger.With("worker_id", id, "hostname", opts.Hostname)
	executor := engine.NewExecutor(eng, reg, id, log)
	if opts.Faults != nil {
		executor = executor.WithFaults(opts.Faults)
	}
	return &Worker{
		engine:   eng,
		executor: executor,
		registry: reg,
		id:       id,
		opts:     opts,
		timing:   eng.Timing(),
		log:      log,
		held:     make(map[uuid.UUID]context.CancelFunc),
	}, nil
}

// ID returns the worker's identity.
func (w *Worker) ID() uuid.UUID { return w.id }

// Run claims and executes tasks until ctx ends.
//
// It returns nil on a clean shutdown. Shutdown does not wait for in-flight
// tasks beyond the context's own deadline: a worker that refuses to exit is
// worse than one whose task is recovered by lease expiry, which is a path the
// system exercises constantly anyway.
func (w *Worker) Run(ctx context.Context) error {
	w.log.InfoContext(ctx, "worker started",
		"concurrency", w.opts.Concurrency, "version", w.opts.Version)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); w.heartbeatLoop(ctx) }()
	go func() { defer wg.Done(); w.renewLoop(ctx) }()

	err := w.claimLoop(ctx)
	wg.Wait()

	w.log.InfoContext(context.WithoutCancel(ctx), "worker stopped")
	return err
}

// claimLoop is the worker's main loop: take as much work as there is capacity
// for, run it, repeat.
func (w *Worker) claimLoop(ctx context.Context) error {
	// The semaphore bounds concurrency. Claiming only up to the free capacity
	// keeps a busy worker from taking work it cannot start, which would hold
	// leases it is not renewing attention to and slow the whole pool.
	slots := make(chan struct{}, w.opts.Concurrency)
	var running sync.WaitGroup

	// Shutdown waits for the tasks already in flight. A worker that abandoned
	// them on SIGTERM would make every ordinary redeploy produce the same
	// recovery events a crash does, which is a lot of noise for no benefit.
	defer running.Wait()

	for {
		if ctx.Err() != nil {
			return nil
		}

		free := w.opts.Concurrency - len(slots)
		if free <= 0 {
			w.backOff(ctx)
			continue
		}

		claimed, err := w.engine.ClaimTasks(ctx, w.id, free)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// A claim failure is usually the database being briefly
			// unreachable. Log it and back off rather than exiting: a worker
			// that dies on a transient error turns a blip into a recovery
			// event for every task it was holding.
			w.log.ErrorContext(ctx, "claim failed", "error", err)
			w.backOff(ctx)
			continue
		}

		if len(claimed) == 0 {
			w.backOff(ctx)
			continue
		}

		for _, task := range claimed {
			slots <- struct{}{}
			running.Add(1)
			go func(task engine.ClaimedTask) {
				defer running.Done()
				defer func() { <-slots }()
				w.execute(ctx, task)
			}(task)
		}
	}
}

func (w *Worker) execute(ctx context.Context, task engine.ClaimedTask) {
	log := w.log.With("run_id", task.RunID, "task", task.Task.Name, "attempt", task.Task.Attempt)

	// Execution is detached from the shutdown context's cancellation but keeps
	// its values. A task cancelled the instant a worker receives SIGTERM would
	// be recorded as failed when nothing was wrong with it; letting it run to
	// its own timeout is both faster and more truthful, and the process exits
	// when it finishes.
	//
	// It is deliberately NOT bounded by the lease deadline. The lease is a
	// claim the renewal loop keeps alive, not a budget for the work: binding
	// execution to the deadline the task was claimed under would make renewal
	// pointless and would guarantee failure for any task that takes longer than
	// one lease. The step's own timeout bounds the work, and losing the lease
	// cancels this context through the entry the renewal loop holds.
	execCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()

	w.track(task.Task.ID, cancel)
	defer w.untrack(task.Task.ID)

	if err := w.executor.Execute(execCtx, task); err != nil {
		switch {
		case errors.Is(err, engine.ErrLeaseLost):
			// The reaper decided this worker was gone and handed the task on.
			// Discarding the result is correct: the newer attempt owns it.
			log.WarnContext(execCtx, "lease lost, discarding result")
		case errors.Is(err, engine.ErrConcurrentAttempt):
			// Two workers on one attempt is a scheduler bug, not a transient
			// condition. It is logged at error level and counted, so it cannot
			// be missed: duplicate_executions_total should always be zero, and
			// a non-zero value means something is wrong rather than busy.
			log.ErrorContext(execCtx, "another worker is executing this attempt", "error", err)
			metrics, _ := telemetry.Meter()
			metrics.RecordDuplicateExecution(execCtx, task.Task.Name)
		default:
			log.ErrorContext(execCtx, "failed to record task outcome", "error", err)
		}
		return
	}
	log.DebugContext(execCtx, "task finished")
}

// heartbeatLoop tells the coordinator this worker is alive.
//
// It runs independently of execution so that a blocked handler cannot make a
// healthy worker look dead. A worker that has been declared LOST stops: its
// leases are gone, and continuing to claim under an identity the coordinator
// has written off would put two workers on the same tasks.
func (w *Worker) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(w.timing.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.engine.Heartbeat(ctx, w.id, w.heldCount()); err != nil {
				if errors.Is(err, engine.ErrWorkerLost) {
					w.log.ErrorContext(ctx, "declared lost by the coordinator; stopping", "error", err)
					return
				}
				if ctx.Err() == nil {
					w.log.WarnContext(ctx, "heartbeat failed", "error", err)
				}
			}
		}
	}
}

// renewLoop extends the leases on the tasks this worker holds, and stops work on
// the ones it could not keep.
//
// The interval is at most half the lease duration — config.Timing.Validate
// enforces it — so a single failed renewal does not cost the worker its task.
// A task that was not renewed has been handed to another worker; cancelling it
// here frees the slot immediately instead of spending the rest of the handler's
// timeout on a result that will be discarded.
func (w *Worker) renewLoop(ctx context.Context) {
	ticker := time.NewTicker(w.timing.LeaseRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ids := w.heldIDs()
			if len(ids) == 0 {
				continue
			}
			renewed, err := w.engine.RenewLease(ctx, w.id, ids)
			if err != nil {
				// A failed renewal call is not proof the leases are gone — the
				// database may simply be briefly unreachable. Cancelling the
				// work here would throw away tasks this worker still owns, so
				// it waits for the next tick; if the leases really are lost,
				// recording the outcome fails with ErrLeaseLost anyway.
				if ctx.Err() == nil {
					w.log.WarnContext(ctx, "lease renewal failed", "error", err, "tasks", len(ids))
				}
				continue
			}
			if len(renewed) == len(ids) {
				continue
			}
			kept := make(map[uuid.UUID]struct{}, len(renewed))
			for _, id := range renewed {
				kept[id] = struct{}{}
			}
			for _, id := range ids {
				if _, ok := kept[id]; ok {
					continue
				}
				w.log.WarnContext(ctx, "lease lost; cancelling the task", "task_id", id)
				w.cancelTask(id)
			}
		}
	}
}

// backOff waits out one poll interval, or returns at once when the worker is
// shutting down. The next loop iteration checks ctx.Err().
func (w *Worker) backOff(ctx context.Context) {
	timer := time.NewTimer(w.opts.PollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (w *Worker) track(id uuid.UUID, cancel context.CancelFunc) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.held[id] = cancel
}

// cancelTask stops the execution of a task this worker no longer holds.
func (w *Worker) cancelTask(id uuid.UUID) {
	w.mu.Lock()
	cancel, ok := w.held[id]
	w.mu.Unlock()
	if ok {
		cancel()
	}
}

func (w *Worker) untrack(id uuid.UUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.held, id)
}

func (w *Worker) heldCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.held)
}

func (w *Worker) heldIDs() []uuid.UUID {
	w.mu.Lock()
	defer w.mu.Unlock()
	ids := make([]uuid.UUID, 0, len(w.held))
	for id := range w.held {
		ids = append(ids, id)
	}
	return ids
}
