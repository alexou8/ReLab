package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/store"
	"github.com/alexou8/relab/internal/workflow"
)

// ErrDefinitionChanged reports an attempt to register a different definition
// under a name and version that already exist.
//
// Registration is idempotent for an identical definition — re-running
// `relab workflow register` in a deploy script must not fail — but silently
// accepting a changed definition under the same version would make two runs
// labelled "data-pipeline v1" incomparable, which is precisely what replay
// exists to rule out.
var ErrDefinitionChanged = errors.New("a different definition is already registered under this name and version")

// RegisterWorkflow stores a definition and returns it. Registering the same
// bytes again returns the existing row.
func (e *Engine) RegisterWorkflow(ctx context.Context, def *workflow.Definition) (Workflow, error) {
	canonical, err := def.Canonical()
	if err != nil {
		return Workflow{}, err
	}

	existing, err := e.WorkflowByName(ctx, def.Name, def.Version)
	switch {
	case err == nil:
		if existing.Hash != def.Hash {
			return Workflow{}, fmt.Errorf(
				"engine: register %s v%d: %w (registered %s, offered %s); bump the version",
				def.Name, def.Version, ErrDefinitionChanged, existing.Hash[:12], def.Hash[:12])
		}
		return existing, nil
	case !errors.Is(err, store.ErrNotFound):
		return Workflow{}, err
	}

	wf := Workflow{
		ID:      uuid.New(),
		Name:    def.Name,
		Version: def.Version,
		YAML:    string(canonical),
		Hash:    def.Hash,
	}
	err = e.db.InTx(ctx, func(ctx context.Context, tx store.Conn) error {
		return tx.QueryRow(ctx, `
			INSERT INTO workflows (id, name, version, definition_yaml, definition_hash)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING created_at`,
			wf.ID, wf.Name, wf.Version, wf.YAML, wf.Hash).Scan(&wf.CreatedAt)
	})
	if err != nil {
		// Another process registered the same version between the read and the
		// write. Re-read: if it registered the same bytes, this call succeeds.
		if errors.Is(err, store.ErrConflict) {
			return e.registerRaced(ctx, def)
		}
		return Workflow{}, fmt.Errorf("engine: register %s v%d: %w", def.Name, def.Version, err)
	}
	return wf, nil
}

func (e *Engine) registerRaced(ctx context.Context, def *workflow.Definition) (Workflow, error) {
	existing, err := e.WorkflowByName(ctx, def.Name, def.Version)
	if err != nil {
		return Workflow{}, fmt.Errorf("engine: register %s v%d: %w", def.Name, def.Version, err)
	}
	if existing.Hash != def.Hash {
		return Workflow{}, fmt.Errorf(
			"engine: register %s v%d: %w (registered %s, offered %s); bump the version",
			def.Name, def.Version, ErrDefinitionChanged, existing.Hash[:12], def.Hash[:12])
	}
	return existing, nil
}

const workflowColumns = `id, name, version, definition_yaml, definition_hash, created_at`

// WorkflowByName returns a registered definition. Version 0 means "the highest
// registered version", which is what `relab run <name>` uses.
func (e *Engine) WorkflowByName(ctx context.Context, name string, version int) (Workflow, error) {
	query := `SELECT ` + workflowColumns + ` FROM workflows WHERE name = $1 AND version = $2`
	args := []any{name, version}
	if version <= 0 {
		query = `SELECT ` + workflowColumns + `
			FROM workflows WHERE name = $1 ORDER BY version DESC LIMIT 1`
		args = []any{name}
	}
	var wf Workflow
	err := e.db.Conn().QueryRow(ctx, query, args...).Scan(
		&wf.ID, &wf.Name, &wf.Version, &wf.YAML, &wf.Hash, &wf.CreatedAt)
	if err != nil {
		return Workflow{}, fmt.Errorf("engine: look up workflow %s: %w", name, store.Classify(err))
	}
	return wf, nil
}

// WorkflowByID returns a registered definition by id.
func (e *Engine) WorkflowByID(ctx context.Context, id uuid.UUID) (Workflow, error) {
	var wf Workflow
	err := e.db.Conn().QueryRow(ctx,
		`SELECT `+workflowColumns+` FROM workflows WHERE id = $1`, id).Scan(
		&wf.ID, &wf.Name, &wf.Version, &wf.YAML, &wf.Hash, &wf.CreatedAt)
	if err != nil {
		return Workflow{}, fmt.Errorf("engine: look up workflow %s: %w", id, store.Classify(err))
	}
	return wf, nil
}

// Definition parses a stored workflow back into a Definition. The stored YAML
// is the canonical form, so this round trips.
func (e *Engine) Definition(ctx context.Context, id uuid.UUID) (*workflow.Definition, error) {
	wf, err := e.WorkflowByID(ctx, id)
	if err != nil {
		return nil, err
	}
	// The handler set is not checked here: a definition stored by a build that
	// had a handler this process lacks is still readable, and the failure
	// belongs at claim time on the process that cannot run the step, not at
	// read time on one that only wants to inspect it.
	def, err := workflow.Parse([]byte(wf.YAML), nil)
	if err != nil {
		return nil, fmt.Errorf("engine: stored definition for %s v%d is unparseable: %w",
			wf.Name, wf.Version, err)
	}
	if def.Hash != wf.Hash {
		return nil, fmt.Errorf(
			"engine: stored definition for %s v%d hashes to %s but was recorded as %s; "+
				"the row has been modified outside ReLab",
			wf.Name, wf.Version, def.Hash[:12], wf.Hash[:12])
	}
	return def, nil
}

// ListWorkflows returns the registered definitions, newest first.
func (e *Engine) ListWorkflows(ctx context.Context, limit int) ([]Workflow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := e.db.Conn().Query(ctx,
		`SELECT `+workflowColumns+` FROM workflows ORDER BY name, version DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("engine: list workflows: %w", store.Classify(err))
	}
	defer rows.Close()

	workflows := make([]Workflow, 0, limit)
	for rows.Next() {
		var wf Workflow
		if err := rows.Scan(&wf.ID, &wf.Name, &wf.Version, &wf.YAML, &wf.Hash, &wf.CreatedAt); err != nil {
			return nil, fmt.Errorf("engine: scan workflow: %w", store.Classify(err))
		}
		workflows = append(workflows, wf)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("engine: list workflows: %w", store.Classify(err))
	}
	return workflows, nil
}

// now returns the engine's clock, which tests can fix.
func (e *Engine) now() time.Time {
	if e.clock != nil {
		return e.clock()
	}
	return time.Now().UTC()
}
