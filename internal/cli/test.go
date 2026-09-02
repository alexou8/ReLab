package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/alexou8/relab/internal/assert"
	"github.com/alexou8/relab/internal/config"
	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/event"
	"github.com/alexou8/relab/internal/fault"
	"github.com/alexou8/relab/internal/faultengine"
	"github.com/alexou8/relab/internal/idem"
	"github.com/alexou8/relab/internal/replay"
	"github.com/alexou8/relab/internal/workflow"
)

func newTestCmd(g *global) *cobra.Command {
	var (
		scenarioPath string
		seed         int64
		repeat       int
		allowRandom  bool
		workers      int
		timeout      time.Duration
	)
	cmd := &cobra.Command{
		Use:   "test <workflow-file|workflow-name> --scenario <file>",
		Short: "Run a workflow under a fault scenario and assert on the recovery",
		Long: "Runs the workflow with the scenario's faults injected, replays the resulting\n" +
			"journal, and checks the scenario's assertions against it. Exits non-zero when any\n" +
			"assertion fails, so it is usable directly as a CI step.\n\n" +
			"Assertions are answered from the event journal rather than from counters the\n" +
			"runtime kept as it went: a counter records what the code that increments it\n" +
			"noticed, and the journal records what happened.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if scenarioPath == "" {
				return fmt.Errorf("--scenario is required; `relab run` is the command for running " +
					"a workflow without one")
			}
			scenario, err := fault.LoadScenario(scenarioPath)
			if err != nil {
				return err
			}
			if !scenario.Deterministic() && !allowRandom {
				return fmt.Errorf(
					"scenario %q uses probability-driven faults, so it passes or fails by luck; "+
						"give each fault an explicit `at:` trigger point, or pass --allow-random "+
						"for an exploratory run",
					scenario.Name)
			}
			if repeat < 1 {
				repeat = 1
			}

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
			wf, def, err := resolveWorkflow(ctx, eng, args[0])
			if err != nil {
				return err
			}

			// A scenario that kills the worker process cannot be injected in the
			// process that is also driving and asserting on the run: killing
			// the test is not a test. Those scenarios are run against spawned
			// workers, and the choice is made from the scenario rather than
			// left to the caller to remember.
			pooled := workers > 0 || scenario.NeedsSeparateWorkers()
			if pooled && workers <= 0 {
				workers = 2
			}

			failures := 0
			for i := 0; i < repeat; i++ {
				var report *assert.Report
				var err error
				if pooled {
					report, err = runScenarioPooled(ctx, eng, wf, def, scenario, scenarioPath,
						seed, workers, timeout)
				} else {
					report, err = runScenario(ctx, eng, wf, def, scenario, seed, timeout)
				}
				if err != nil {
					return err
				}
				if g.json {
					if err := writeJSON(cmd, report); err != nil {
						return err
					}
				} else {
					if repeat > 1 {
						cmd.Printf("run %d of %d\n", i+1, repeat)
					}
					cmd.Print(report.Human())
					if i < repeat-1 {
						cmd.Println()
					}
				}
				if !report.Passed {
					failures++
				}
			}

			if failures > 0 {
				if !g.json {
					cmd.Printf("\n%d of %d runs failed their assertions\n", failures, repeat)
				}
				// A failing assertion has to exit non-zero without anyone
				// parsing the output; that is the whole point of the command.
				return ErrAlreadyReported
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scenarioPath, "scenario", "", "scenario file to run under (required)")
	cmd.Flags().Int64Var(&seed, "seed", 0, "override the scenario's seed")
	cmd.Flags().IntVar(&repeat, "repeat", 1, "run the scenario this many times; all must pass")
	cmd.Flags().BoolVar(&allowRandom, "allow-random", false,
		"permit probability-driven faults, which do not reproduce")
	cmd.Flags().IntVar(&workers, "workers", 0,
		"run against this many spawned worker processes instead of in this process; "+
			"implied by any scenario that kills a worker")
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "give up on a run after this long")
	return cmd
}

// runScenario executes one run under a scenario and evaluates the assertions.
func runScenario(ctx context.Context, eng *engine.Engine, wf engine.Workflow,
	def *workflow.Definition, scenario *fault.Scenario, seedOverride int64,
	timeout time.Duration) (*assert.Report, error) {
	runSeed := scenario.Seed
	if seedOverride != 0 {
		runSeed = seedOverride
	}

	run, err := eng.CreateRun(ctx, wf, def, engine.CreateRunOptions{
		ScenarioName: scenario.Name,
		ScenarioHash: scenario.Hash,
		Seed:         runSeed,
	})
	if err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The runner drives the run in this process with the fault source attached,
	// so `relab test` needs nothing else running. A worker-crash fault would
	// kill this process, so scenarios using it are run against a worker pool
	// instead; the scenario's own documentation says which is which.
	source := faultengine.NewSource(eng, faultengine.StaticLookup(scenario))
	if err := eng.DriveWithFaults(runCtx, run.ID, defaultRegistry(), source); err != nil {
		return nil, err
	}

	return evaluateRun(ctx, eng, scenario, run.ID)
}

// evaluateRun replays a finished run and checks the scenario's assertions.
func evaluateRun(ctx context.Context, eng *engine.Engine, scenario *fault.Scenario,
	runID uuid.UUID) (*assert.Report, error) {
	events, err := eng.Events(ctx, runID)
	if err != nil {
		return nil, err
	}
	state, err := replay.Reduce(runID, events)
	if err != nil {
		return nil, err
	}
	duplicates, err := countDuplicateEffects(ctx, eng, runID, events)
	if err != nil {
		return nil, err
	}
	return assert.Evaluate(scenario, state, events, duplicates), nil
}

// countDuplicateEffects compares how many effects the ledger holds against how
// many distinct keys were used.
//
// A duplicate effect is by definition one the ledger did not suppress, so it
// cannot be counted by asking the ledger. What can be counted is the
// discrepancy: every key should have exactly one row, and a repeat that was
// suppressed leaves a SIDE_EFFECT_SKIPPED rather than a second row. More rows
// than keys would mean the ledger's own uniqueness failed, which the primary
// key makes impossible; fewer suppressions than repeats is what this detects.
func countDuplicateEffects(ctx context.Context, eng *engine.Engine, runID uuid.UUID,
	events []event.Event) (int, error) {
	ledger := idem.New(eng.DB())
	recorded, err := ledger.CountForRun(ctx, runID)
	if err != nil {
		return 0, err
	}

	// Count how many times an effect-performing attempt ran. Each attempt of a
	// task that uses the ledger either records an effect or is suppressed, so
	// attempts beyond (recorded + suppressed) performed work nothing accounted
	// for.
	suppressed := 0
	for _, e := range events {
		if e.Type == event.SideEffectSkipped {
			suppressed++
		}
	}
	attemptsWithEffects, err := effectAttempts(ctx, eng, runID)
	if err != nil {
		return 0, err
	}
	duplicates := attemptsWithEffects - recorded - suppressed
	if duplicates < 0 {
		duplicates = 0
	}
	return duplicates, nil
}

// effectAttempts counts the attempts of tasks that touched the ledger.
func effectAttempts(ctx context.Context, eng *engine.Engine, runID uuid.UUID) (int, error) {
	var count int
	err := eng.DB().Conn().QueryRow(ctx, `
		SELECT coalesce(count(*), 0)
		FROM task_attempts a
		JOIN tasks t ON t.id = a.task_id
		WHERE t.run_id = $1
		  AND t.task_name IN (SELECT DISTINCT task_name FROM side_effects WHERE run_id = $1)`,
		runID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count effect attempts: %w", err)
	}
	return count, nil
}

// runScenarioPooled executes one run against spawned workers and evaluates the
// assertions against the journal they produced.
func runScenarioPooled(ctx context.Context, eng *engine.Engine, wf engine.Workflow,
	def *workflow.Definition, scenario *fault.Scenario, scenarioPath string,
	seedOverride int64, workers int, timeout time.Duration) (*assert.Report, error) {
	runID, err := runScenarioWithPool(ctx, eng, wf, def, scenario, scenarioPath,
		seedOverride, workers, timeout)
	if err != nil {
		return nil, err
	}
	return evaluateRun(ctx, eng, scenario, runID)
}
