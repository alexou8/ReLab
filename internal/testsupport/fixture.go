package testsupport

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/store"
)

// SeedRun inserts a minimal workflow and run so that tests which only care
// about the event journal do not have to build a whole workflow first. It
// returns the run id.
func SeedRun(t *testing.T, db *store.DB) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	workflowID := uuid.New()
	runID := uuid.New()

	err := db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO workflows (id, name, version, definition_yaml, definition_hash)
			VALUES ($1, $2, 1, 'name: fixture', 'fixture-hash')`,
			workflowID, "fixture-"+workflowID.String()[:8]); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO runs (id, workflow_id, status, seed)
			VALUES ($1, $2, 'CREATED', 42)`, runID, workflowID)
		return err
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return runID
}
