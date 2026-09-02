package engine

import (
	"context"
	"log/slog"
	"time"
)

// Coordinator runs the recovery sweep on a timer.
//
// It holds no state. Any number of coordinators may run at once — the sweep's
// queries all take row locks with SKIP LOCKED, so two coordinators divide the
// work rather than duplicating it — and a coordinator that restarts resumes
// simply by sweeping again. That is the whole of "coordinator restart
// recovery": there is nothing to resume, because there was nothing in memory.
type Coordinator struct {
	engine   *Engine
	interval time.Duration
	log      *slog.Logger
}

// NewCoordinator returns a coordinator sweeping at the engine's configured
// reaper interval.
func NewCoordinator(e *Engine, log *slog.Logger) *Coordinator {
	if log == nil {
		log = slog.Default()
	}
	return &Coordinator{engine: e, interval: e.timing.ReaperInterval, log: log}
}

// Run sweeps until ctx ends.
func (c *Coordinator) Run(ctx context.Context) error {
	c.log.InfoContext(ctx, "coordinator started", "reaper_interval", c.interval)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.log.InfoContext(context.WithoutCancel(ctx), "coordinator stopped")
			return nil
		case <-ticker.C:
			result, err := c.engine.Reap(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				// A failed sweep is retried on the next tick. Exiting would
				// stop recovery entirely, which is strictly worse than a sweep
				// that failed once.
				c.log.ErrorContext(ctx, "reaper sweep failed", "error", err)
				continue
			}
			if result.Empty() {
				continue
			}
			c.log.InfoContext(ctx, "reaper sweep",
				"leases_expired", result.LeasesExpired,
				"tasks_requeued", result.TasksRequeued,
				"tasks_dead", result.TasksDead,
				"workers_suspect", result.WorkersSuspect,
				"workers_lost", result.WorkersLost,
				"retries_promoted", result.RetriesPromoted)
		}
	}
}
