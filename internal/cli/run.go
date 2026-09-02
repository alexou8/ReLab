package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/fault"
	"github.com/alexou8/relab/internal/workflow"
)

func newRunCmd(g *global) *cobra.Command {
	var (
		scenarioPath string
		seed         int64
		correlation  string
		detach       bool
	)
	cmd := &cobra.Command{
		Use:   "run <workflow-file|workflow-name>",
		Short: "Start a run and drive it to completion",
		Long: "Accepts either a definition file, which is registered first, or the name of an\n" +
			"already-registered workflow. By default the run is executed in this process, so a\n" +
			"single command is enough to see a workflow through. With --detach the run is only\n" +
			"created, and a worker pool picks it up.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			wf, def, err := resolveWorkflow(ctx, eng, args[0])
			if err != nil {
				return err
			}

			opts := engine.CreateRunOptions{Seed: seed, CorrelationID: correlation}
			if scenarioPath != "" {
				// Scenario files are loaded and hashed here so that the run
				// records which scenario produced it; the injectors themselves
				// live in package fault and run inside the worker.
				name, hash, err := scenarioIdentity(scenarioPath)
				if err != nil {
					return err
				}
				opts.ScenarioName, opts.ScenarioHash = name, hash
			}

			run, err := eng.CreateRun(ctx, wf, def, opts)
			if err != nil {
				return err
			}

			if detach {
				if g.json {
					return writeJSON(cmd, map[string]any{"run_id": run.ID, "status": run.Status})
				}
				cmd.Printf("created run %s (%s v%d), waiting for a worker\n",
					run.ID, wf.Name, wf.Version)
				return nil
			}

			runner, err := engine.NewLocalRunner(ctx, eng, defaultRegistry(), nil)
			if err != nil {
				return err
			}
			started := time.Now()
			final, err := runner.Run(ctx, run.ID)
			if err != nil {
				return err
			}
			return reportRun(ctx, cmd, g, eng, final, time.Since(started))
		},
	}
	cmd.Flags().StringVar(&scenarioPath, "scenario", "", "fault scenario file to run under")
	cmd.Flags().Int64Var(&seed, "seed", 0, "seed for the run's deterministic RNG (0 chooses one and records it)")
	cmd.Flags().StringVar(&correlation, "correlation-id", "", "identifier tying this run to something outside ReLab")
	cmd.Flags().BoolVar(&detach, "detach", false, "create the run without executing it, for a worker pool to claim")
	// `run inspect <id>` rather than a top-level `inspect`: cobra matches a
	// subcommand name before falling through to the parent's positional
	// argument, so `relab run examples/data-pipeline.yaml` still works.
	cmd.AddCommand(newRunInspectCmd(g))
	return cmd
}

// resolveWorkflow accepts either a path to a definition, which it registers, or
// the name of a workflow that is already registered.
func resolveWorkflow(ctx context.Context, eng *engine.Engine, arg string) (engine.Workflow, *workflow.Definition, error) {
	if looksLikePath(arg) {
		def, err := parseFile(arg, true)
		if err != nil {
			return engine.Workflow{}, nil, err
		}
		wf, err := eng.RegisterWorkflow(ctx, def)
		if err != nil {
			return engine.Workflow{}, nil, err
		}
		return wf, def, nil
	}

	name, version := splitNameVersion(arg)
	wf, err := eng.WorkflowByName(ctx, name, version)
	if err != nil {
		return engine.Workflow{}, nil, err
	}
	def, err := eng.Definition(ctx, wf.ID)
	if err != nil {
		return engine.Workflow{}, nil, err
	}
	return wf, def, nil
}

func looksLikePath(arg string) bool {
	if strings.HasSuffix(arg, ".yaml") || strings.HasSuffix(arg, ".yml") {
		return true
	}
	_, err := os.Stat(arg)
	return err == nil
}

// splitNameVersion parses "data-pipeline@2". Without a version, 0 means the
// highest registered one.
func splitNameVersion(arg string) (string, int) {
	name, version, found := strings.Cut(arg, "@")
	if !found {
		return arg, 0
	}
	var v int
	if _, err := fmt.Sscanf(version, "%d", &v); err != nil {
		return arg, 0
	}
	return name, v
}

func reportRun(ctx context.Context, cmd *cobra.Command, g *global, eng *engine.Engine,
	run engine.Run, elapsed time.Duration) error {
	tasks, err := eng.Tasks(ctx, run.ID)
	if err != nil {
		return err
	}
	if g.json {
		return writeJSON(cmd, map[string]any{
			"run_id": run.ID, "status": run.Status, "workflow": run.WorkflowName,
			"version": run.WorkflowVer, "seed": run.Seed,
			"duration_ms": elapsed.Milliseconds(), "tasks": tasks,
		})
	}
	cmd.Printf("%s %s v%d\n", run.Status, run.WorkflowName, run.WorkflowVer)
	cmd.Printf("  run id    %s\n", run.ID)
	cmd.Printf("  seed      %d\n", run.Seed)
	cmd.Printf("  duration  %s\n", elapsed.Round(time.Millisecond))
	if run.FailureReason != "" {
		cmd.Printf("  reason    %s\n", run.FailureReason)
	}
	cmd.Println()
	for _, t := range tasks {
		attempts := ""
		if t.Attempt > 1 {
			attempts = fmt.Sprintf(" (%d attempts)", t.Attempt)
		}
		cmd.Printf("  %-10s %-12s%s\n", t.Status, t.Name, attempts)
		if t.Error != "" {
			cmd.Printf("             %s\n", firstLine(t.Error))
		}
	}
	// A failed run must not exit 0: `relab run` in a script has to be able to
	// tell success from failure without parsing this output.
	if run.Status != engine.RunSucceeded {
		return ErrAlreadyReported
	}
	return nil
}

func newRunsCmd(g *global) *cobra.Command {
	cmd := &cobra.Command{Use: "runs", Short: "Inspect runs"}
	cmd.AddCommand(newRunsListCmd(g))
	return cmd
}

func newRunsListCmd(g *global) *cobra.Command {
	var status, workflowName string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List runs, newest first",
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
			runs, err := eng.ListRuns(ctx, engine.ListRunsOptions{
				Status:   engine.RunStatus(strings.ToUpper(status)),
				Workflow: workflowName,
				Limit:    limit,
			})
			if err != nil {
				return err
			}
			if g.json {
				return writeJSON(cmd, runs)
			}
			if len(runs) == 0 {
				cmd.Println("no runs match")
				return nil
			}
			cmd.Printf("%-38s %-10s %-20s %-8s %s\n", "RUN ID", "STATUS", "WORKFLOW", "VERSION", "STARTED")
			for _, r := range runs {
				cmd.Printf("%-38s %-10s %-20s %-8d %s\n",
					r.ID, r.Status, r.WorkflowName, r.WorkflowVer,
					r.CreatedAt.Format("2006-01-02 15:04:05"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "filter by run status")
	cmd.Flags().StringVar(&workflowName, "workflow", "", "filter by workflow name")
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum rows to return")
	return cmd
}

func newRunInspectCmd(g *global) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <run-id>",
		Short: "Print a run's event timeline",
		Long: "The timeline is the run's complete recorded history, in sequence order. It is\n" +
			"the same data replay reduces, so what is printed here is what replay sees.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			runID, err := uuid.Parse(args[0])
			if err != nil {
				return fmt.Errorf("%q is not a run id: %w", args[0], err)
			}
			db, err := g.openDB(ctx)
			if err != nil {
				return err
			}
			defer db.Close()
			eng, err := engine.New(db, engine.Options{})
			if err != nil {
				return err
			}

			run, err := eng.RunByID(ctx, runID)
			if err != nil {
				return err
			}
			events, err := eng.Events(ctx, runID)
			if err != nil {
				return err
			}
			if g.json {
				return writeJSON(cmd, map[string]any{"run": run, "events": events})
			}

			cmd.Printf("run %s\n", run.ID)
			cmd.Printf("  workflow  %s v%d\n", run.WorkflowName, run.WorkflowVer)
			cmd.Printf("  status    %s\n", run.Status)
			cmd.Printf("  seed      %d\n", run.Seed)
			if run.ScenarioName != "" {
				cmd.Printf("  scenario  %s\n", run.ScenarioName)
			}
			if run.FailureReason != "" {
				cmd.Printf("  reason    %s\n", run.FailureReason)
			}
			cmd.Println()
			cmd.Printf("%-5s %-14s %-22s %-12s %s\n", "SEQ", "TIME", "TYPE", "TASK", "DETAIL")
			for _, evt := range events {
				cmd.Printf("%-5d %-14s %-22s %-12s %s\n",
					evt.Seq, evt.OccurredAt.Format("15:04:05.000"), evt.Type,
					evt.TaskName, summarise(evt))
			}
			return nil
		},
	}
}

// summarise renders the interesting fields of an event payload on one line.
// It reads the payload generically rather than switching on every type: the
// timeline is for a human scanning it, and a missing field is better than a
// switch that silently stops covering new event types.
func summarise(evt event.Event) string {
	var fields map[string]any
	if err := json.Unmarshal(evt.Payload, &fields); err != nil {
		return "(unreadable payload)"
	}
	interesting := []string{
		"attempt", "error", "reason", "detail", "delay_ms", "duration_ms",
		"next_attempt", "fault_type", "idempotency_key", "task_count",
		"tasks_succeeded", "leases_released", "missed_beats",
	}
	var parts []string
	for _, key := range interesting {
		if v, ok := fields[key]; ok {
			parts = append(parts, fmt.Sprintf("%s=%v", key, compact(v)))
		}
	}
	return strings.Join(parts, " ")
}

func compact(v any) string {
	s := fmt.Sprintf("%v", v)
	s = firstLine(s)
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// scenarioIdentity reads a scenario file's name and hash so the run records
// which scenario produced it.
func scenarioIdentity(path string) (name, hash string, err error) {
	sc, err := fault.LoadScenario(path)
	if err != nil {
		return "", "", err
	}
	return sc.Name, sc.Hash, nil
}
