package store

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Sentinel errors returned by this package and by every package that talks to
// the database through it. Callers match with errors.Is; they must never
// inspect driver types directly, so that the driver stays replaceable.
var (
	// ErrNotFound reports that a query that expected exactly one row found none.
	ErrNotFound = errors.New("not found")
	// ErrConflict reports a unique or primary key violation.
	ErrConflict = errors.New("conflict")
	// ErrForeignKey reports a foreign key violation.
	ErrForeignKey = errors.New("foreign key violation")
	// ErrCheckViolation reports that a row failed a CHECK constraint. In this
	// codebase that almost always means an illegal state transition reached the
	// database, which is a bug rather than a user error.
	ErrCheckViolation = errors.New("check constraint violation")
	// ErrSerialization reports a serialization failure or deadlock. The caller
	// may retry the whole transaction.
	ErrSerialization = errors.New("serialization failure")
)

// PostgreSQL SQLSTATE codes, see appendix A of the PostgreSQL manual.
const (
	sqlStateUniqueViolation     = "23505"
	sqlStateForeignKeyViolation = "23503"
	sqlStateCheckViolation      = "23514"
	sqlStateSerializationFail   = "40001"
	sqlStateDeadlockDetected    = "40P01"
)

// ConstraintError names the constraint that a write violated. Recovery logic
// frequently needs the name — "did I lose the claim race, or did I violate the
// one-execution-per-attempt rule?" — and the name is the only way to tell.
type ConstraintError struct {
	Constraint string
	Table      string
	kind       error
}

func (e *ConstraintError) Error() string {
	return fmt.Sprintf("%s on %s.%s", e.kind, e.Table, e.Constraint)
}

// Unwrap exposes the sentinel so errors.Is(err, ErrConflict) matches.
func (e *ConstraintError) Unwrap() error { return e.kind }

// classify converts a driver error into one of this package's sentinels. It
// returns nil for a nil error so it can wrap a call site directly.
func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	kind := sentinelFor(pgErr.Code)
	if kind == nil {
		return err
	}
	if pgErr.ConstraintName == "" {
		return kind
	}
	return &ConstraintError{Constraint: pgErr.ConstraintName, Table: pgErr.TableName, kind: kind}
}

func sentinelFor(code string) error {
	switch code {
	case sqlStateUniqueViolation:
		return ErrConflict
	case sqlStateForeignKeyViolation:
		return ErrForeignKey
	case sqlStateCheckViolation:
		return ErrCheckViolation
	case sqlStateSerializationFail, sqlStateDeadlockDetected:
		return ErrSerialization
	default:
		return nil
	}
}

// Classify is the exported form of classify, for packages that run their own
// queries against a Conn obtained from this package.
func Classify(err error) error { return classify(err) }
