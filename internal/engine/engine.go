package engine

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/config"
	"github.com/alexou8/relab/internal/store"
	"github.com/alexou8/relab/internal/workflow"
)

// Engine is the single writer for run and task state.
//
// It is safe for concurrent use: every operation is one transaction, and the
// operations that race — claiming a task, recording an outcome, closing a run —
// are written so that losing the race is an ordinary outcome rather than an
// error. There is deliberately no in-memory scheduler state: a coordinator that
// restarts has to be able to resume from the database alone, and the surest way
// to guarantee that is to keep nothing anywhere else.
type Engine struct {
	db     *store.DB
	timing config.Timing
	log    *slog.Logger

	// clock is overridable for tests. Production leaves it nil.
	clock func() time.Time
}

// Options configures an Engine.
type Options struct {
	Timing config.Timing
	Logger *slog.Logger
	// Clock overrides time.Now, for tests that need deterministic lease
	// arithmetic.
	Clock func() time.Time
}

// New returns an Engine. It validates the timing, because a lease shorter than
// its own renewal interval produces "random" duplicate executions that look
// like a scheduler bug for a long time before anyone checks the configuration.
func New(db *store.DB, opts Options) (*Engine, error) {
	if db == nil {
		return nil, fmt.Errorf("engine: nil database")
	}
	timing := opts.Timing
	if timing == (config.Timing{}) {
		timing = config.DefaultTiming()
	}
	if err := timing.Validate(); err != nil {
		return nil, err
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Engine{db: db, timing: timing, log: log, clock: opts.Clock}, nil
}

// DB exposes the pool for packages that run their own reads, such as the API's
// list endpoints. Writers go through the Engine.
func (e *Engine) DB() *store.DB { return e.db }

// Timing returns the configured durations.
func (e *Engine) Timing() config.Timing { return e.timing }

// NewSeed returns a random seed for a run that was not given one.
//
// crypto/rand rather than math/rand: seeds are recorded and re-used to
// reproduce a run, so two runs started in the same millisecond by different
// processes must not collide on a time-derived seed.
func NewSeed() (int64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, fmt.Errorf("engine: generate seed: %w", err)
	}
	// Mask the sign bit: the seed is stored in a bigint and printed in
	// scenario files, where a negative number is a needless surprise.
	return int64(binary.BigEndian.Uint64(buf[:]) &^ (1 << 63)), nil
}

// idempotencyPrefix derives the task-scoped prefix of an idempotency key.
// Handlers append their own operation name; see sdk.IdempotencyKey.
func idempotencyPrefix(runID uuid.UUID, taskName string) string {
	return runID.String() + ":" + taskName
}

// resolveDefinition parses the stored definition for a run.
func (e *Engine) resolveDefinition(ctx context.Context, workflowID uuid.UUID) (*workflow.Definition, error) {
	return e.Definition(ctx, workflowID)
}
