package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/alexou8/relab/internal/bench"
	"github.com/alexou8/relab/internal/config"
	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/workflow"
	"github.com/alexou8/relab/sdk"
)

func newBenchCmd(g *global, version string) *cobra.Command {
	var (
		workerCounts string
		faultRates   string
		runs         int
		csvPath      string
		timeout      time.Duration
	)
	cmd := &cobra.Command{
		Use:   "bench <workflow-file|workflow-name>",
		Short: "Measure throughput and recovery across worker counts and fault rates",
		Long: "Runs the workflow repeatedly at each point of a matrix and reports throughput and\n" +
			"latency percentiles, along with the hardware and versions the numbers came from.\n\n" +
			"Latency is reported as percentiles, never as a mean: a system that recovers in two\n" +
			"seconds ninety-nine times and ninety seconds once has a good mean and a bad p99,\n" +
			"and the p99 is the one an operator lives with.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			workers, err := parseIntList(workerCounts, "--workers")
			if err != nil {
				return err
			}
			rates, err := parseFloatList(faultRates, "--fault-rate")
			if err != nil {
				return err
			}
			if runs < 1 {
				return fmt.Errorf("--runs must be at least 1")
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
			env := bench.DescribeEnvironment(ctx, eng, version)

			var results []bench.Result
			for _, workerCount := range workers {
				for _, rate := range rates {
					params := bench.Params{
						Workers: workerCount, Runs: runs, FaultRate: rate, Workflow: wf.Name,
					}
					cmd.Printf("measuring: %d workers, %.1f%% fault rate, %d runs\n",
						workerCount, rate*100, runs)
					result, err := measure(ctx, eng, wf, def, params, env, timeout)
					if err != nil {
						return err
					}
					results = append(results, result)
				}
			}

			if csvPath != "" {
				if err := writeResults(csvPath, results); err != nil {
					return err
				}
				cmd.Printf("\nwrote %s\n", csvPath)
			}

			if g.json {
				return writeJSON(cmd, results)
			}
			cmd.Println()
			cmd.Print(bench.Summary(results))
			cmd.Printf("\nmeasured on %s/%s, %d cpus, go %s, postgresql %s\n",
				env.OS, env.Arch, env.NumCPU, env.GoVersion, env.PostgresVersion)
			return nil
		},
	}
	cmd.Flags().StringVar(&workerCounts, "workers", "1,5,10,25", "comma-separated worker counts")
	cmd.Flags().StringVar(&faultRates, "fault-rate", "0,0.01,0.05",
		"comma-separated fault rates, as fractions")
	cmd.Flags().IntVar(&runs, "runs", 20, "runs to execute at each point of the matrix")
	cmd.Flags().StringVar(&csvPath, "csv", "", "write the raw results here")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "give up on a matrix point after this long")
	return cmd
}

// measure executes one point of the matrix.
//
// The runs are created up front and then drained by a pool of in-process
// runners, which is what makes worker count the independent variable: the
// queue is saturated before the workers start, so the measurement is of how
// fast they drain it rather than of how fast runs are created.
func measure(ctx context.Context, eng *engine.Engine, wf engine.Workflow,
	def *workflow.Definition, params bench.Params, env bench.Environment,
	timeout time.Duration) (bench.Result, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	runIDs := make([]uuid.UUID, 0, params.Runs)
	for i := 0; i < params.Runs; i++ {
		run, err := eng.CreateRun(runCtx, wf, def, engine.CreateRunOptions{
			Seed: int64(i + 1),
		})
		if err != nil {
			return bench.Result{}, err
		}
		runIDs = append(runIDs, run.ID)
	}

	// Faults are injected by failing a fraction of attempts through the
	// benchmark's own handler wrapper rather than through a scenario file: the
	// matrix varies a *rate*, and a scenario describes a specific fault at a
	// specific point.
	registry, err := benchRegistry(params.FaultRate)
	if err != nil {
		return bench.Result{}, err
	}

	start := time.Now()

	// The coordinator sweeps while the pool works, so a fault that costs a
	// worker its lease is recovered rather than stalling the measurement.
	coordinatorCtx, stopCoordinator := context.WithCancel(runCtx)
	coordinatorDone := make(chan struct{})
	go func() {
		defer close(coordinatorDone)
		_ = engine.NewCoordinator(eng, newLogger()).Run(coordinatorCtx)
	}()

	var wg sync.WaitGroup
	errs := make(chan error, params.Workers)
	queue := make(chan uuid.UUID, len(runIDs))
	for _, id := range runIDs {
		queue <- id
	}
	close(queue)

	for i := 0; i < params.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runner, err := engine.NewLocalRunner(runCtx, eng, registry, newLogger())
			if err != nil {
				errs <- err
				return
			}
			for id := range queue {
				if _, err := runner.Run(runCtx, id); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	stopCoordinator()
	<-coordinatorDone
	close(errs)
	for err := range errs {
		return bench.Result{}, err
	}

	wall := time.Since(start)

	samples := make([]bench.Sample, 0, len(runIDs))
	for _, id := range runIDs {
		sample, err := bench.SampleFrom(ctx, eng, id)
		if err != nil {
			return bench.Result{}, err
		}
		samples = append(samples, sample)
	}
	return bench.Summarise(params, samples, wall, env), nil
}

func parseIntList(raw, flag string) ([]int, error) {
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil || v < 1 {
			return nil, fmt.Errorf("%s: %q is not a positive integer", flag, part)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no values given", flag)
	}
	return out, nil
}

func parseFloatList(raw, flag string) ([]float64, error) {
	parts := strings.Split(raw, ",")
	out := make([]float64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.ParseFloat(part, 64)
		if err != nil || v < 0 || v > 1 {
			return nil, fmt.Errorf("%s: %q is not a fraction between 0 and 1", flag, part)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no values given", flag)
	}
	return out, nil
}

// benchRegistry wraps every example handler so that a fraction of attempts
// fail.
//
// The failures are drawn from a per-attempt derived source rather than a
// package-level RNG, so a benchmark at a given fault rate produces the same
// pattern of failures on every machine — which is what makes two benchmark
// runs comparable at all.
func benchRegistry(faultRate float64) (*sdk.Registry, error) {
	base := defaultRegistry()
	if faultRate <= 0 {
		return base, nil
	}
	wrapped := sdk.NewRegistry()
	for _, name := range base.Names() {
		handler, ok := base.Lookup(name)
		if !ok {
			continue
		}
		inner := handler
		stepName := name
		if err := wrapped.Handle(name, func(ctx context.Context, tc *sdk.TaskContext) (any, error) {
			rng := engine.DerivedRand(int64(tc.Attempt), tc.RunID.String(), "bench-fault",
				tc.TaskName, stepName)
			if rng.Float64() < faultRate {
				return nil, fmt.Errorf("injected benchmark failure at %.1f%% fault rate",
					faultRate*100)
			}
			return inner(ctx, tc)
		}); err != nil {
			return nil, err
		}
	}
	return wrapped, nil
}

// writeResults writes the CSV and reports a failed close.
//
// A deferred Close whose error is discarded can lose the last buffered write,
// which for a benchmark means silently truncated results — the one thing a
// results file must never do.
func writeResults(path string, results []bench.Result) (err error) {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", path, closeErr)
		}
	}()
	return bench.WriteCSV(file, results)
}
