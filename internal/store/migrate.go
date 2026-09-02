package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationLockID is an arbitrary but fixed key for the session-level advisory
// lock that serialises migration across processes. docker compose starts the
// server and several workers at once, and without it they would race to apply
// the same DDL.
const migrationLockID int64 = 0x52454c41 // ASCII for the project name, truncated to four bytes

// Migration is one numbered .sql file. Files are applied in ascending order and
// never edited once released: the checksum recorded at apply time is compared
// on every subsequent startup, so an edited file fails loudly rather than
// leaving half the fleet on a different schema.
type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

// LoadMigrations reads the embedded migrations. It rejects duplicate version
// numbers, which are the usual result of two branches adding a migration at
// the same time and are silently destructive if applied in the wrong order.
func LoadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations dir: %w", err)
	}
	migrations := make([]Migration, 0, len(entries))
	seen := make(map[int]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationName(entry.Name())
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("store: duplicate migration version %d: %s and %s", version, prev, entry.Name())
		}
		seen[version] = entry.Name()

		body, err := migrationFS.ReadFile(path.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("store: read migration %s: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(body)
		migrations = append(migrations, Migration{
			Version:  version,
			Name:     name,
			SQL:      string(body),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

// parseMigrationName splits "001_init.sql" into 1 and "init".
func parseMigrationName(filename string) (int, string, error) {
	base := strings.TrimSuffix(filename, ".sql")
	numeric, name, found := strings.Cut(base, "_")
	if !found || name == "" {
		return 0, "", fmt.Errorf("store: migration %q must be named <version>_<name>.sql", filename)
	}
	version, err := strconv.Atoi(numeric)
	if err != nil || version <= 0 {
		return 0, "", fmt.Errorf("store: migration %q has a non-positive-integer version", filename)
	}
	return version, name, nil
}

// Migrate applies every migration that has not been applied yet, each in its
// own transaction, and returns the versions it applied.
//
// It is safe to call concurrently from several processes: the advisory lock
// admits one at a time and the losers observe the winner's work.
func (db *DB) Migrate(ctx context.Context) ([]int, error) {
	migrations, err := LoadMigrations()
	if err != nil {
		return nil, err
	}

	// The lock is held on one dedicated connection for the whole run, so it
	// cannot be released early by a pooled connection being handed to someone
	// else mid-migration.
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: acquire migration conn: %w", classify(err))
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return nil, fmt.Errorf("store: acquire migration lock: %w", classify(err))
	}
	defer func() {
		// Best effort: releasing the connection also drops session locks.
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()

	if _, err := conn.Exec(ctx, createMigrationsTable); err != nil {
		return nil, fmt.Errorf("store: create schema_migrations: %w", classify(err))
	}

	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return nil, err
	}

	ran := make([]int, 0, len(migrations))
	for _, m := range migrations {
		if recorded, ok := applied[m.Version]; ok {
			if recorded != m.Checksum {
				return ran, fmt.Errorf(
					"store: migration %03d_%s was modified after it was applied (recorded %s, found %s); "+
						"add a new migration instead of editing a released one",
					m.Version, m.Name, recorded[:12], m.Checksum[:12])
			}
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return ran, fmt.Errorf("store: begin migration %03d: %w", m.Version, classify(err))
		}
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			return ran, fmt.Errorf("store: apply migration %03d_%s: %w", m.Version, m.Name, classify(err))
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`,
			m.Version, m.Name, m.Checksum); err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			return ran, fmt.Errorf("store: record migration %03d_%s: %w", m.Version, m.Name, classify(err))
		}
		if err := tx.Commit(ctx); err != nil {
			return ran, fmt.Errorf("store: commit migration %03d_%s: %w", m.Version, m.Name, classify(err))
		}
		ran = append(ran, m.Version)
	}
	return ran, nil
}

const createMigrationsTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    integer     PRIMARY KEY,
    name       text        NOT NULL,
    checksum   text        NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
)`

func appliedMigrations(ctx context.Context, conn Conn) (map[int]string, error) {
	rows, err := conn.Query(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", classify(err))
	}
	defer rows.Close()

	applied := map[int]string{}
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("store: scan schema_migrations: %w", classify(err))
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate schema_migrations: %w", classify(err))
	}
	return applied, nil
}

// SchemaVersion returns the highest applied migration version, or 0 when the
// database has never been migrated.
func (db *DB) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := db.pool.QueryRow(ctx,
		`SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", classify(err))
	}
	return version, nil
}
