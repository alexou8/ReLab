// Package testsupport provides the shared fixtures for tests that need a real
// PostgreSQL database.
//
// There are no mock databases in this repository. The behaviour under test —
// SKIP LOCKED claim races, advisory locks, constraint violations, gapless
// sequence allocation under contention — is behaviour of PostgreSQL, and a mock
// would only assert that the mock matches the test's assumptions.
package testsupport

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexou8/relab/internal/store"
)

// DSNEnv names the environment variable holding a connection string to a
// PostgreSQL instance the tests may create and drop databases on.
const DSNEnv = "RELAB_TEST_DSN"

var dbCounter atomic.Int64

// DB returns a migrated, empty database dedicated to this test, and registers
// its cleanup. Tests that call it are skipped when DSNEnv is unset, so that
// `go test ./...` still works on a machine without PostgreSQL; CI sets it.
//
// Each test gets its own database rather than a shared one with truncation
// between tests, because several of these tests deliberately hold locks and
// kill connections, and a shared database turns that into cross-test flakiness.
func DB(t *testing.T) *store.DB {
	t.Helper()

	adminDSN := os.Getenv(DSNEnv)
	if adminDSN == "" {
		t.Skipf("%s is not set; skipping test that requires PostgreSQL", DSNEnv)
	}

	ctx := context.Background()
	admin, err := store.Open(ctx, store.DefaultConfig(adminDSN))
	if err != nil {
		t.Fatalf("connect to %s: %v", DSNEnv, err)
	}
	defer admin.Close()

	name := fmt.Sprintf("relab_test_%d_%d", time.Now().UnixNano(), dbCounter.Add(1))
	// The database name is generated here from a counter and a timestamp, never
	// from test input, so interpolating it into DDL cannot inject.
	if _, err := admin.Conn().Exec(ctx, `CREATE DATABASE `+quoteIdent(name)); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}

	db, err := store.Open(ctx, store.DefaultConfig(replaceDatabase(adminDSN, name)))
	if err != nil {
		t.Fatalf("connect to fresh database %s: %v", name, err)
	}
	if _, err := db.Migrate(ctx); err != nil {
		db.Close()
		t.Fatalf("migrate %s: %v", name, err)
	}

	t.Cleanup(func() {
		db.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		dropper, err := store.Open(cleanupCtx, store.DefaultConfig(adminDSN))
		if err != nil {
			t.Logf("cleanup: reconnect to drop %s: %v", name, err)
			return
		}
		defer dropper.Close()
		if _, err := dropper.Conn().Exec(cleanupCtx,
			`DROP DATABASE IF EXISTS `+quoteIdent(name)+` WITH (FORCE)`); err != nil {
			t.Logf("cleanup: drop %s: %v", name, err)
		}
	})
	return db
}

// TestDSN returns the admin DSN, or skips.
func TestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(DSNEnv)
	if dsn == "" {
		t.Skipf("%s is not set; skipping test that requires PostgreSQL", DSNEnv)
	}
	return dsn
}

// quoteIdent quotes a SQL identifier. Identifiers cannot be parameterised, so
// DDL that names a database has to build a string; this makes that safe.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// replaceDatabase points a DSN at a different database, handling both the URL
// and the keyword/value forms.
func replaceDatabase(dsn, database string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		base, query, hasQuery := strings.Cut(dsn, "?")
		scheme, rest, _ := strings.Cut(base, "://")
		authority, _, _ := strings.Cut(rest, "/")
		out := scheme + "://" + authority + "/" + database
		if hasQuery {
			out += "?" + query
		}
		return out
	}
	fields := strings.Fields(dsn)
	out := make([]string, 0, len(fields)+1)
	replaced := false
	for _, f := range fields {
		if strings.HasPrefix(f, "dbname=") {
			out = append(out, "dbname="+database)
			replaced = true
			continue
		}
		out = append(out, f)
	}
	if !replaced {
		out = append(out, "dbname="+database)
	}
	return strings.Join(out, " ")
}

// DatabaseDSN returns a connection string for the database DB created, so that
// a test can hand it to a spawned process.
func DatabaseDSN(t *testing.T, db *store.DB) string {
	t.Helper()
	var name string
	if err := db.Conn().QueryRow(context.Background(), `SELECT current_database()`).Scan(&name); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	return replaceDatabase(TestDSN(t), name)
}
