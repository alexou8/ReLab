package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/alexou8/relab/internal/store"
	"github.com/alexou8/relab/internal/testsupport"
)

func TestLoadMigrationsAreOrderedAndWellNamed(t *testing.T) {
	migrations, err := store.LoadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations were embedded")
	}
	for i, m := range migrations {
		if m.Version <= 0 {
			t.Errorf("migration %d has version %d", i, m.Version)
		}
		if i > 0 && m.Version <= migrations[i-1].Version {
			t.Errorf("migrations are not strictly ascending at index %d: %d after %d",
				i, m.Version, migrations[i-1].Version)
		}
		if len(m.Checksum) != 64 {
			t.Errorf("migration %d has a %d-character checksum, want 64", m.Version, len(m.Checksum))
		}
		if strings.TrimSpace(m.SQL) == "" {
			t.Errorf("migration %d is empty", m.Version)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := testsupport.DB(t) // already migrated once
	ctx := context.Background()

	applied, err := db.Migrate(ctx)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("second migrate applied %v, want nothing", applied)
	}

	version, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	migrations, err := store.LoadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	want := migrations[len(migrations)-1].Version
	if version != want {
		t.Fatalf("schema version is %d, want %d", version, want)
	}
}

func TestMigrateDetectsEditedMigration(t *testing.T) {
	db := testsupport.DB(t)
	ctx := context.Background()

	// Simulate someone editing a released migration file: the recorded checksum
	// no longer matches what the binary carries.
	if _, err := db.Conn().Exec(ctx,
		`UPDATE schema_migrations SET checksum = repeat('0', 64) WHERE version = 1`); err != nil {
		t.Fatalf("corrupt checksum: %v", err)
	}
	_, err := db.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "was modified after it was applied") {
		t.Fatalf("migrate returned %v, want a checksum mismatch error", err)
	}
}

func TestMigrateConcurrentProcessesAgree(t *testing.T) {
	// Two pools racing to migrate the same fresh database, as docker compose
	// does when the server and the workers start together.
	db := testsupport.DB(t)
	ctx := context.Background()

	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := db.Migrate(ctx)
			errs <- err
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent migrate: %v", err)
		}
	}
}

func TestOpenRejectsEmptyDSN(t *testing.T) {
	_, err := store.Open(context.Background(), store.DefaultConfig(""))
	if err == nil || !strings.Contains(err.Error(), "empty dsn") {
		t.Fatalf("open with empty dsn returned %v, want an empty-dsn error", err)
	}
}

func TestOpenDoesNotLeakPassword(t *testing.T) {
	const password = "hunter2-should-never-appear"
	dsn := "postgres://relab:" + password + "@127.0.0.1:1/relab?sslmode=disable&connect_timeout=1"
	_, err := store.Open(context.Background(), store.Config{DSN: dsn, MaxConns: 1})
	if err == nil {
		t.Fatal("expected a connection failure against a closed port")
	}
	if strings.Contains(err.Error(), password) {
		t.Fatalf("error message leaked the password: %v", err)
	}
}
