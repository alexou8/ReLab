package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/store"
)

// WorkerStatus is a worker's liveness as the coordinator sees it.
type WorkerStatus string

// The worker states. SUSPECT exists so that a single missed heartbeat never
// costs a worker its tasks: a GC pause or a scheduling hiccup is common, and
// reclaiming work from a healthy worker turns a hiccup into duplicate
// execution.
const (
	WorkerHealthy WorkerStatus = "HEALTHY"
	WorkerSuspect WorkerStatus = "SUSPECT"
	WorkerLost    WorkerStatus = "LOST"
)

// Worker is a registered execution process.
type Worker struct {
	ID            uuid.UUID
	Hostname      string
	Version       string
	Status        WorkerStatus
	Capacity      int
	ActiveTasks   int
	LastHeartbeat time.Time
	RegisteredAt  time.Time
}

// WorkerRegistration is what a process announces about itself.
type WorkerRegistration struct {
	Hostname string
	Version  string
	Capacity int
}

// RegisterWorker records a worker and returns its id.
func (e *Engine) RegisterWorker(ctx context.Context, reg WorkerRegistration) (uuid.UUID, error) {
	if reg.Capacity <= 0 {
		reg.Capacity = 1
	}
	id := uuid.New()
	now := e.now()
	_, err := e.db.Conn().Exec(ctx, `
		INSERT INTO workers (id, hostname, version, status, capacity, active_tasks, last_heartbeat, registered_at)
		VALUES ($1, $2, $3, 'HEALTHY', $4, 0, $5, $5)`,
		id, reg.Hostname, reg.Version, reg.Capacity, now)
	if err != nil {
		return uuid.Nil, fmt.Errorf("engine: register worker on %s: %w", reg.Hostname, store.Classify(err))
	}
	return id, nil
}

// Heartbeat records liveness and the worker's current load.
//
// A heartbeat from a worker the coordinator has already declared LOST does not
// resurrect it. The worker's leases have been handed to others, and letting it
// return to HEALTHY would leave two workers believing they own the same tasks.
// The worker sees the refusal and re-registers under a new id.
func (e *Engine) Heartbeat(ctx context.Context, workerID uuid.UUID, activeTasks int) error {
	now := e.now()
	tag, err := e.db.Conn().Exec(ctx, `
		UPDATE workers
		SET last_heartbeat = $1, active_tasks = $2, status = 'HEALTHY'
		WHERE id = $3 AND status <> 'LOST'`, now, activeTasks, workerID)
	if err != nil {
		return fmt.Errorf("engine: heartbeat for worker %s: %w", workerID, store.Classify(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("engine: heartbeat for worker %s: %w", workerID, ErrWorkerLost)
	}
	return nil
}

// ErrWorkerLost reports that the coordinator has already declared this worker
// dead and released its leases.
var ErrWorkerLost = fmt.Errorf("worker has been declared lost and its leases released")

// ListWorkers returns the registered workers, most recently seen first.
func (e *Engine) ListWorkers(ctx context.Context) ([]Worker, error) {
	rows, err := e.db.Conn().Query(ctx, `
		SELECT id, hostname, version, status, capacity, active_tasks, last_heartbeat, registered_at
		FROM workers ORDER BY last_heartbeat DESC`)
	if err != nil {
		return nil, fmt.Errorf("engine: list workers: %w", store.Classify(err))
	}
	defer rows.Close()

	var workers []Worker
	for rows.Next() {
		var w Worker
		if err := rows.Scan(&w.ID, &w.Hostname, &w.Version, &w.Status, &w.Capacity,
			&w.ActiveTasks, &w.LastHeartbeat, &w.RegisteredAt); err != nil {
			return nil, fmt.Errorf("engine: scan worker: %w", store.Classify(err))
		}
		workers = append(workers, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("engine: list workers: %w", store.Classify(err))
	}
	return workers, nil
}
