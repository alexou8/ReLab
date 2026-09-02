package replay

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/store"
)

// Load reads a run's journal and reduces it.
//
// This is the only function in the package that touches the database, and it
// does so before the reducer runs, never during: Reduce stays pure.
func Load(ctx context.Context, conn store.Conn, runID uuid.UUID) (*RunState, error) {
	events, err := event.Read(ctx, conn, runID)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("replay: run %s has no events: %w", runID, store.ErrNotFound)
	}
	return Reduce(runID, events)
}

// StoredArtifacts reads the artifacts table for a run.
//
// The artifacts table and the TASK_SUCCEEDED payloads are two records of the
// same fact, written in the same transaction. Comparing them is a genuine
// integrity check rather than a tautology: they can only disagree if a row was
// changed outside ReLab, which is exactly what `--diff` is asked to detect.
func StoredArtifacts(ctx context.Context, conn store.Conn, runID uuid.UUID) (map[string][]Artifact, error) {
	rows, err := conn.Query(ctx, `
		SELECT task_name, name, sha256, size, content_type
		FROM artifacts WHERE run_id = $1 ORDER BY task_name, name`, runID)
	if err != nil {
		return nil, fmt.Errorf("replay: read artifacts of run %s: %w", runID, store.Classify(err))
	}
	defer rows.Close()

	byTask := map[string][]Artifact{}
	for rows.Next() {
		var task string
		var a Artifact
		if err := rows.Scan(&task, &a.Name, &a.SHA256, &a.Size, &a.ContentType); err != nil {
			return nil, fmt.Errorf("replay: scan artifact: %w", store.Classify(err))
		}
		byTask[task] = append(byTask[task], a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("replay: read artifacts of run %s: %w", runID, store.Classify(err))
	}
	return byTask, nil
}

// VerifyArtifacts compares the artifacts the journal describes against the rows
// in the artifacts table, and returns the divergences.
func VerifyArtifacts(state *RunState, stored map[string][]Artifact) []Divergence {
	var out []Divergence
	for _, name := range state.TaskNames() {
		fromJournal := &TaskState{Name: name, Artifacts: state.Tasks[name].Artifacts}
		fromTable := &TaskState{Name: name, Artifacts: stored[name]}
		report := &Report{}
		compareArtifacts(report, name, fromJournal, fromTable)
		for i := range report.Divergences {
			report.Divergences[i].Detail = "the event journal and the artifacts table disagree; " +
				"they are written in one transaction, so a row has been changed outside ReLab"
		}
		out = append(out, report.Divergences...)
	}
	return out
}
