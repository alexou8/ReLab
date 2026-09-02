package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/alexou8/relab/internal/config"
	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/event"
)

// Snapshot is a self-contained copy of what the read API would serve for a set
// of runs.
//
// It exists so the dashboard can be deployed somewhere that cannot reach a
// control plane — a static hosting platform — and still show a real run rather
// than an invented one. The shapes are the engine's own types, so a snapshot
// and a live API are the same thing to a reader; nothing in the dashboard gets
// a second code path it could quietly diverge on.
type Snapshot struct {
	// Provenance, because a recording presented without it is indistinguishable
	// from a fabrication.
	RecordedAt time.Time `json:"recorded_at"`
	Version    string    `json:"relab_version"`
	Note       string    `json:"note,omitempty"`

	Runs    []SnapshotRun   `json:"runs"`
	Workers []engine.Worker `json:"workers"`
	Stats   engine.Stats    `json:"stats"`
}

// SnapshotRun carries one run with everything the run detail page reads.
type SnapshotRun struct {
	Run    engine.Run    `json:"run"`
	Tasks  []engine.Task `json:"tasks"`
	Events []event.Event `json:"events"`
}

func newExportCmd(g *global, version string) *cobra.Command {
	var (
		limit int
		note  string
	)
	cmd := &cobra.Command{
		Use:   "export [run-id...]",
		Short: "Write a JSON snapshot of recorded runs to stdout",
		Long: "Exports runs, their tasks, their complete event journals, the worker table and\n" +
			"the aggregate counters as one JSON document, in exactly the shapes the read API\n" +
			"serves.\n\n" +
			"With no run ids it exports the most recent runs. The output is a recording: it is\n" +
			"what the database held at the moment of export, and nothing reduces or summarises\n" +
			"it on the way out, so a snapshot replays through the same reducer the live journal\n" +
			"does.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			db, err := g.openDB(ctx)
			if err != nil {
				return err
			}
			defer db.Close()

			timing, err := config.TimingFromEnv()
			if err != nil {
				return err
			}
			eng, err := engine.New(db, engine.Options{Timing: timing, Logger: newLogger()})
			if err != nil {
				return err
			}

			ids, err := exportIDs(ctx, eng, args, limit)
			if err != nil {
				return err
			}

			snapshot := Snapshot{
				RecordedAt: time.Now().UTC(),
				Version:    version,
				Note:       note,
				Runs:       make([]SnapshotRun, 0, len(ids)),
			}
			for _, id := range ids {
				run, err := eng.RunByID(ctx, id)
				if err != nil {
					return fmt.Errorf("export run %s: %w", id, err)
				}
				tasks, err := eng.Tasks(ctx, id)
				if err != nil {
					return fmt.Errorf("export tasks for run %s: %w", id, err)
				}
				events, err := eng.Events(ctx, id)
				if err != nil {
					return fmt.Errorf("export events for run %s: %w", id, err)
				}
				snapshot.Runs = append(snapshot.Runs, SnapshotRun{Run: run, Tasks: tasks, Events: events})
			}

			if snapshot.Workers, err = eng.ListWorkers(ctx); err != nil {
				return err
			}
			if snapshot.Stats, err = eng.Stats(ctx); err != nil {
				return err
			}
			return writeJSON(cmd, snapshot)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "how many recent runs to export when no ids are given")
	cmd.Flags().StringVar(&note, "note", "",
		"one line recorded in the snapshot saying what it is a recording of")
	return cmd
}

// exportIDs resolves the run ids to export, either from the arguments or from
// the most recent runs.
func exportIDs(ctx context.Context, eng *engine.Engine, args []string, limit int) ([]uuid.UUID, error) {
	if len(args) > 0 {
		ids := make([]uuid.UUID, 0, len(args))
		for _, raw := range args {
			id, err := uuid.Parse(raw)
			if err != nil {
				return nil, fmt.Errorf("%q is not a run id: %w", raw, err)
			}
			ids = append(ids, id)
		}
		return ids, nil
	}
	runs, err := eng.ListRuns(ctx, engine.ListRunsOptions{Limit: limit})
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
	}
	return ids, nil
}
