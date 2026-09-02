package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/fault"
	"github.com/alexou8/relab/internal/workflow"
)

// runScenarioWithPool executes a run against spawned worker processes.
//
// It is how the worker-crash scenario is tested. That fault SIGKILLs the
// process executing the task, so it cannot be injected in the process that is
// also driving and asserting on the run — killing the test is not a test. The
// workers are real subprocesses of this command, the coordinator runs here, and
// this process survives to replay the journal afterwards.
func runScenarioWithPool(ctx context.Context, eng *engine.Engine, wf engine.Workflow,
	def *workflow.Definition, scenario *fault.Scenario, scenarioPath string,
	seedOverride int64, workers int, timeout time.Duration) (uuid.UUID, error) {
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
		return uuid.Nil, err
	}

	self, err := os.Executable()
	if err != nil {
		return uuid.Nil, fmt.Errorf("locate the relab binary to spawn workers: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pool, err := spawnWorkers(runCtx, self, scenarioPath, workers)
	// Workers are always cleaned up, including on a spawn failure partway
	// through: an orphaned worker would keep claiming against a database the
	// next scenario is about to use.
	defer pool.stop()
	if err != nil {
		return uuid.Nil, err
	}

	// The coordinator runs here. A worker killed by the scenario releases
	// nothing, so without a sweep the run would simply hang, and the scenario
	// would be testing the test's patience rather than ReLab's recovery.
	coordinatorDone := make(chan struct{})
	coordinatorCtx, stopCoordinator := context.WithCancel(runCtx)
	defer func() { stopCoordinator(); <-coordinatorDone }()
	go func() {
		defer close(coordinatorDone)
		_ = engine.NewCoordinator(eng, newLogger()).Run(coordinatorCtx)
	}()

	final, err := waitForRun(runCtx, eng, run.ID)
	if err != nil {
		return uuid.Nil, err
	}
	if !final.Status.Terminal() {
		return uuid.Nil, fmt.Errorf("run %s did not finish within %s", run.ID, timeout)
	}
	return run.ID, nil
}

// workerPool supervises the spawned workers.
//
// It restarts a worker that exits, which is what makes a crash scenario mean
// anything: without supervision the pool can tolerate exactly as many crashes
// as it has workers, and the scenario would be measuring the pool size rather
// than ReLab's recovery. Every real deployment restarts workers — the compose
// stack uses `restart: unless-stopped` — so an unsupervised pool would also be
// testing a system nobody runs.
type workerPool struct {
	mu       sync.Mutex
	procs    map[int]*exec.Cmd
	stopping bool
	wg       sync.WaitGroup
	restarts atomic.Int64
}

func spawnWorkers(ctx context.Context, binary, scenarioPath string, count int) (*workerPool, error) {
	pool := &workerPool{procs: map[int]*exec.Cmd{}}
	for i := 0; i < count; i++ {
		if err := pool.start(ctx, i, binary, scenarioPath); err != nil {
			return pool, fmt.Errorf("start worker %d of %d: %w", i+1, count, err)
		}
	}
	return pool, nil
}

// start launches one worker and supervises it until the pool is stopped.
func (p *workerPool) start(ctx context.Context, slot int, binary, scenarioPath string) error {
	// Not bound to ctx: the pool is stopped explicitly, and a context-bound
	// command would be killed at an unpredictable moment relative to the
	// assertions.
	cmd := exec.Command(binary, "worker", "--concurrency", "1", //nolint:noctx // stopped explicitly
		"--scenario", scenarioPath)
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// Its own process group, so stopping the pool cannot signal this process.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}

	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		_ = cmd.Process.Kill()
		return nil
	}
	p.procs[slot] = cmd
	p.mu.Unlock()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		_ = cmd.Wait()

		p.mu.Lock()
		stopping := p.stopping
		p.mu.Unlock()
		if stopping || ctx.Err() != nil {
			return
		}
		// The worker died — killed by the scenario, most likely. Replace it,
		// with a short pause so a worker that fails immediately on start does
		// not spin.
		p.restarts.Add(1)
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
		if err := p.start(ctx, slot, binary, scenarioPath); err != nil {
			fmt.Fprintf(os.Stderr, "relab: could not restart worker %d: %v\n", slot, err)
		}
	}()
	return nil
}

// Restarts reports how many workers had to be replaced, which is a direct count
// of how many times a fault killed one.
func (p *workerPool) Restarts() int64 { return p.restarts.Load() }

func (p *workerPool) stop() {
	p.mu.Lock()
	p.stopping = true
	procs := make([]*exec.Cmd, 0, len(p.procs))
	for _, cmd := range p.procs {
		procs = append(procs, cmd)
	}
	p.mu.Unlock()

	for _, cmd := range procs {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	p.wg.Wait()
}

// waitForRun polls until the run reaches a terminal state or the context ends.
func waitForRun(ctx context.Context, eng *engine.Engine, runID uuid.UUID) (engine.Run, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		run, err := eng.RunByID(ctx, runID)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return engine.Run{}, nil
			}
			return engine.Run{}, err
		}
		if run.Status.Terminal() {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return run, nil
		case <-ticker.C:
		}
	}
}
