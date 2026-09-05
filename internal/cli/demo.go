package cli

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/alexou8/relab/internal/config"
	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/fault"
	"github.com/alexou8/relab/internal/replay"
)

// The demo's workflow and scenario travel inside the binary.
//
// A first run should need a database and nothing else — no checkout, no
// examples directory, no remembering which of the eight scenarios tells the
// story. They are the same files the corpus runs, copied at build time rather
// than rewritten, so the demo cannot drift into being a nicer story than the
// tests actually prove.
//
//go:embed demoassets/*.yaml
var demoAssets embed.FS

// demoTiming compresses the production defaults so a reviewer watches a
// recovery rather than waiting for one. The relationships are what matter and
// are preserved: renewal at a third of the lease, LOST at five beats.
var demoTiming = map[string]string{
	// The demo's output is the story, not the logs. An operator debugging one
	// sets RELAB_LOG_LEVEL themselves and this leaves it alone.
	config.EnvLogLevel:           "warn",
	config.EnvLeaseDuration:      "2s",
	config.EnvLeaseRenewInterval: "700ms",
	config.EnvHeartbeatInterval:  "400ms",
	config.EnvReaperInterval:     "200ms",
}

func newDemoCmd(g *global) *cobra.Command {
	var (
		keep    bool
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "demo",
		Short: "Run the whole argument once: charge a customer, kill the worker, recover",
		Long: "Registers a three-step workflow, starts two worker processes, and kills the one\n" +
			"holding the charge step after it has performed the charge but before it could\n" +
			"acknowledge it. The task comes back, the charge is not repeated, and the run\n" +
			"finishes.\n\n" +
			"Everything it needs is inside the binary; it needs a PostgreSQL and nothing else.\n" +
			"The workflow and the scenario are the same files the test corpus runs.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			dir, cleanup, err := unpackDemo(keep)
			if err != nil {
				return err
			}
			defer cleanup()

			scenarioPath := filepath.Join(dir, "worker-crash-effectful.yaml")
			scenario, err := fault.LoadScenario(scenarioPath)
			if err != nil {
				return err
			}
			def, err := parseFile(filepath.Join(dir, "effectful.yaml"), true)
			if err != nil {
				return err
			}

			// The compressed timings are set for this process and inherited by
			// the workers it spawns, so the demo does not depend on what the
			// caller happens to have exported.
			for name, value := range demoTiming {
				if _, ok := os.LookupEnv(name); ok {
					continue
				}
				if err := os.Setenv(name, value); err != nil {
					return fmt.Errorf("set %s for the demo: %w", name, err)
				}
			}

			step(cmd, 1, "Connecting to PostgreSQL and applying migrations")
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
			eng, err := engine.New(db, engine.Options{Timing: timing, Logger: newLogger()})
			if err != nil {
				return err
			}

			step(cmd, 2, "Registering the workflow: import, charge, report")
			wf, err := eng.RegisterWorkflow(ctx, def)
			if err != nil {
				return err
			}

			step(cmd, 3, "Starting two worker processes and running the workflow")
			step(cmd, 4, "Killing the worker holding `charge`, after the charge and before the acknowledgement")
			runID, err := runScenarioWithPool(ctx, eng, wf, def, scenario, scenarioPath, 0, 2, timeout)
			if err != nil {
				return err
			}

			step(cmd, 5, "Reading back what the journal recorded")
			events, err := eng.Events(ctx, runID)
			if err != nil {
				return err
			}
			state, err := replay.Reduce(runID, events)
			if err != nil {
				return err
			}

			if g.json {
				return writeJSON(cmd, map[string]any{
					"run_id": runID, "status": state.Status, "events": len(events),
				})
			}
			narrate(cmd, events)
			cmd.Printf("\nRun %s finished %s, from %d recorded events.\n",
				runID, state.Status, len(events))
			cmd.Printf("\nWhat happened, in full:  relab run inspect %s\n", runID)
			cmd.Printf("The same thing as a test: relab test %s --scenario %s\n",
				"examples/effectful.yaml", "examples/scenarios/worker-crash-effectful.yaml")
			cmd.Printf("What is guaranteed here:  docs/reliability.md\n")
			return nil
		},
	}
	cmd.Flags().BoolVar(&keep, "keep-files", false,
		"leave the unpacked workflow and scenario on disk, and print where")
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "give up on the run after this long")
	return cmd
}

// narrate prints the milestones of the run in plain language beside the event
// type that records each one. The type is never dropped: it is what someone
// checks the sentence against.
func narrate(cmd *cobra.Command, events []event.Event) {
	meaning := map[event.Type]string{
		event.FaultInjected:     "the worker holding `charge` was killed, after charging and before acknowledging",
		event.WorkerLost:        "its heartbeats stopped, so it was declared gone and its leases released",
		event.TaskLeaseExpired:  "nobody renewed the hold on the task",
		event.TaskRequeued:      "the task became claimable again",
		event.SideEffectSkipped: "the retry asked to charge again; the ledger already had that key, so it did not",
		event.RunSucceeded:      "every task finished",
	}
	cmd.Println()
	// A claim before the kill and a claim after it are the same event type and
	// a different sentence, and calling the first one "another worker" would be
	// the kind of small untruth this project spends its time removing.
	broken := false
	for _, e := range events {
		text, ok := meaning[e.Type]
		if e.Type == event.FaultInjected {
			broken = true
		}
		if e.Type == event.TaskLeased {
			text, ok = "a worker claimed it", true
			if broken {
				text = "another worker claimed it"
			}
		}
		if !ok {
			continue
		}
		cmd.Printf("  #%-3d %-22s %-9s %s\n", e.Seq, e.Type, e.TaskName, text)
	}
}

func step(cmd *cobra.Command, n int, text string) {
	cmd.Printf("[%d/5] %s\n", n, text)
}

// unpackDemo writes the embedded files somewhere the spawned workers can read
// them, because a worker is a separate process and cannot see this binary's
// embedded filesystem.
func unpackDemo(keep bool) (string, func(), error) {
	dir, err := os.MkdirTemp("", "relab-demo-")
	if err != nil {
		return "", nil, fmt.Errorf("make a directory for the demo files: %w", err)
	}
	entries, err := demoAssets.ReadDir("demoassets")
	if err != nil {
		return "", nil, fmt.Errorf("read the embedded demo files: %w", err)
	}
	for _, entry := range entries {
		data, err := demoAssets.ReadFile(filepath.Join("demoassets", entry.Name()))
		if err != nil {
			return "", nil, fmt.Errorf("read embedded %s: %w", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dir, entry.Name()), data, 0o600); err != nil {
			return "", nil, fmt.Errorf("write %s: %w", entry.Name(), err)
		}
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if keep {
		cleanup = func() { fmt.Printf("\nDemo files left in %s\n", dir) }
	}
	return dir, cleanup, nil
}
