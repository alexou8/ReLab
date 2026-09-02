package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Conn is the subset of pgx used by every query in this repository. Both
// *pgxpool.Pool and pgx.Tx satisfy it, so a query function can be written once
// and called either standalone or inside a transaction. Nothing in the
// codebase should accept *pgxpool.Pool or pgx.Tx directly.
type Conn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Config describes how to reach PostgreSQL and how to size the pool.
type Config struct {
	// DSN is a libpq connection string or URL. It carries credentials and is
	// never logged.
	DSN string
	// MaxConns bounds the pool. The default is deliberately small: a worker
	// holds at most one transaction per concurrent task, and an unbounded pool
	// converts database saturation into a much less debuggable timeout storm.
	MaxConns int32
	// MaxConnLifetime recycles connections so that a failed-over primary does
	// not keep serving from stale backends indefinitely.
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	// ConnectTimeout bounds the initial connectivity check.
	ConnectTimeout time.Duration
}

// DefaultConfig returns the pool settings used by the server, the workers and
// the tests. Callers override individual fields; they should not build a
// Config from zero.
func DefaultConfig(dsn string) Config {
	return Config{
		DSN:             dsn,
		MaxConns:        10,
		MaxConnLifetime: 30 * time.Minute,
		MaxConnIdleTime: 5 * time.Minute,
		ConnectTimeout:  10 * time.Second,
	}
}

// DB is a connection pool with the helpers the rest of the system needs.
type DB struct {
	pool *pgxpool.Pool
}

// Open connects, verifies connectivity, and returns a pool. The caller must
// Close it. A failure here is fatal at startup: ReLab keeps no state outside
// PostgreSQL, so there is nothing useful it can do without it.
func Open(ctx context.Context, cfg Config) (*DB, error) {
	if cfg.DSN == "" {
		return nil, errors.New("store: empty dsn")
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		// The DSN carries a password; report the failure without echoing it.
		return nil, fmt.Errorf("store: parse dsn: %w", redact(err, cfg.DSN))
	}
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", redact(err, cfg.DSN))
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", redact(err, cfg.DSN))
	}
	return &DB{pool: pool}, nil
}

// Close releases every pooled connection. It is safe to call twice.
func (db *DB) Close() {
	if db != nil && db.pool != nil {
		db.pool.Close()
	}
}

// Conn returns the pool as a Conn, for single-statement work that needs no
// transaction.
func (db *DB) Conn() Conn { return db.pool }

// Pool exposes the underlying pool. Only main() and tests should need it.
func (db *DB) Pool() *pgxpool.Pool { return db.pool }

// Ping checks liveness, for the /healthz handler.
func (db *DB) Ping(ctx context.Context) error {
	return classify(db.pool.Ping(ctx))
}

// InTx runs fn inside a transaction, committing when it returns nil and rolling
// back on error or panic. fn must not retain the Conn past its return.
//
// Errors from fn are classified, so callers match on this package's sentinels
// regardless of whether the failure came from fn or from the commit.
func (db *DB) InTx(ctx context.Context, fn func(ctx context.Context, tx Conn) error) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", classify(err))
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// Rollback on the caller's context would be skipped if that context is
		// already cancelled, leaving the transaction open until the connection
		// is recycled. Use a fresh, bounded context instead.
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rbCtx)
	}()

	if err := fn(ctx, tx); err != nil {
		return classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", classify(err))
	}
	committed = true
	return nil
}
