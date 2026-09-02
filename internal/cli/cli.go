// Package cli builds the relab command tree.
//
// Commands are thin: they parse flags, open a database, call into the package
// that owns the behaviour, and render the result. No business logic lives here,
// so that everything the CLI can do is equally reachable from the API and from
// tests.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/alexou8/relab/internal/config"
	"github.com/alexou8/relab/internal/store"
)

// ErrAlreadyReported marks an error whose message has already reached the user,
// so main exits non-zero without printing it twice.
var ErrAlreadyReported = errors.New("already reported")

// global holds the flags every subcommand shares.
type global struct {
	dsn  string
	json bool
}

// Execute runs the command tree and returns the first error.
func Execute(version string) error {
	g := &global{}

	root := &cobra.Command{
		Use:   "relab",
		Short: "Prove that your distributed workflows actually recover when things break",
		Long: "ReLab runs multi-step workflows across worker processes, records an append-only\n" +
			"event history, replays that history to reconstruct run state, injects deterministic\n" +
			"faults, and asserts that recovery behaves correctly.\n\n" +
			"ReLab is a reliability testing and replay tool. It is not a Temporal replacement.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	root.PersistentFlags().StringVar(&g.dsn, "dsn", "",
		"PostgreSQL connection string (default: $"+config.EnvDSN+")")
	root.PersistentFlags().BoolVar(&g.json, "json", false,
		"emit machine-readable JSON instead of human-readable output")

	root.AddCommand(
		newMigrateCmd(g),
		newWorkflowCmd(g),
		newRunCmd(g),
		newRunsCmd(g),
	)

	// A cancelled context reaches every command, so Ctrl-C stops a worker or a
	// server through the same shutdown path a SIGTERM from an orchestrator
	// would take.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return root.ExecuteContext(ctx)
}

// openDB resolves the DSN and connects. Every command that touches the database
// goes through it, so the resolution order is stated in exactly one place.
func (g *global) openDB(ctx context.Context) (*store.DB, error) {
	dsn, err := config.DSN(g.dsn)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(ctx, store.DefaultConfig(dsn))
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return db, nil
}

func newMigrateCmd(g *global) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending database migrations",
		Long: "Applies every migration the binary carries that the target database has not seen.\n" +
			"It is safe to run concurrently from several processes and safe to run again on an\n" +
			"already-migrated database.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			db, err := g.openDB(ctx)
			if err != nil {
				return err
			}
			defer db.Close()

			applied, err := db.Migrate(ctx)
			if err != nil {
				return err
			}
			version, err := db.SchemaVersion(ctx)
			if err != nil {
				return err
			}
			if len(applied) == 0 {
				cmd.Printf("schema is up to date at version %d\n", version)
				return nil
			}
			cmd.Printf("applied %d migration(s), schema is now at version %d\n", len(applied), version)
			return nil
		},
	}
}
