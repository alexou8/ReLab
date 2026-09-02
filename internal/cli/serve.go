package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/alexou8/relab/internal/config"
	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/worker"
)

func newServerCmd(g *global, version string) *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the control plane: the recovery sweep and the HTTP API",
		Long: "The control plane owns nothing in memory. Several may run at once — the sweep's\n" +
			"queries take row locks with SKIP LOCKED, so they divide the work — and one that\n" +
			"restarts resumes by sweeping again.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			log := newLogger()

			db, err := g.openDB(ctx)
			if err != nil {
				return err
			}
			defer db.Close()

			// Migrating on start-up keeps `docker compose up` to one command.
			// It is safe under a race because the runner takes an advisory
			// lock; the workers do the same.
			if _, err := db.Migrate(ctx); err != nil {
				return err
			}

			timing, err := config.TimingFromEnv()
			if err != nil {
				return err
			}
			eng, err := engine.New(db, engine.Options{Timing: timing, Logger: log})
			if err != nil {
				return err
			}

			group, gctx := errgroup.WithContext(ctx)
			group.Go(func() error { return engine.NewCoordinator(eng, log).Run(gctx) })
			group.Go(func() error { return serveAPI(gctx, eng, addr, log, version) })
			if err := group.Wait(); err != nil && !errors.Is(err, ctx.Err()) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", config.String(config.EnvListenAddr, ":8080"),
		"address for the HTTP API (default: $"+config.EnvListenAddr+" or :8080)")
	return cmd
}

func newWorkerCmd(g *global, version string) *cobra.Command {
	var concurrency int
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run a worker that claims and executes tasks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			log := newLogger()

			db, err := g.openDB(ctx)
			if err != nil {
				return err
			}
			defer db.Close()
			if _, err := db.Migrate(ctx); err != nil {
				return err
			}

			timing, err := config.TimingFromEnv()
			if err != nil {
				return err
			}
			eng, err := engine.New(db, engine.Options{Timing: timing, Logger: log})
			if err != nil {
				return err
			}
			w, err := worker.New(ctx, eng, defaultRegistry(), worker.Options{
				Concurrency: concurrency,
				Version:     version,
				Logger:      log,
			})
			if err != nil {
				return err
			}
			return w.Run(ctx)
		},
	}
	defaultConcurrency, err := config.Int(config.EnvWorkerConcurrency, 4)
	if err != nil {
		// Reported when the command runs rather than at construction, so a bad
		// environment variable does not break every other subcommand's help.
		cmd.RunE = func(*cobra.Command, []string) error { return err }
		defaultConcurrency = 4
	}
	cmd.Flags().IntVar(&concurrency, "concurrency", defaultConcurrency,
		"tasks to execute at once (default: $"+config.EnvWorkerConcurrency+" or 4)")
	return cmd
}

func newWorkersCmd(g *global) *cobra.Command {
	return &cobra.Command{
		Use:   "workers",
		Short: "List registered workers and their liveness",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			db, err := g.openDB(ctx)
			if err != nil {
				return err
			}
			defer db.Close()
			eng, err := engine.New(db, engine.Options{})
			if err != nil {
				return err
			}
			workers, err := eng.ListWorkers(ctx)
			if err != nil {
				return err
			}
			if g.json {
				return writeJSON(cmd, workers)
			}
			if len(workers) == 0 {
				cmd.Println("no workers registered")
				return nil
			}
			cmd.Printf("%-38s %-9s %-18s %-10s %s\n", "ID", "STATUS", "HOSTNAME", "LOAD", "LAST HEARTBEAT")
			for _, w := range workers {
				cmd.Printf("%-38s %-9s %-18s %-10s %s\n",
					w.ID, w.Status, w.Hostname,
					fmt.Sprintf("%d/%d", w.ActiveTasks, w.Capacity),
					w.LastHeartbeat.Format("15:04:05"))
			}
			return nil
		},
	}
}

// newLogger builds the structured logger the long-running commands use.
//
// Text by default because a human is usually watching a local worker; JSON when
// asked, because a deployed one is usually being scraped. Every line carries
// the run and task ids, so a failure can be followed across processes.
func newLogger() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(config.String(config.EnvLogLevel, "info")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(config.String(config.EnvLogFormat, "text"), "json") {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
