// Package scenarios runs every file in examples/scenarios as a regression
// suite.
//
// The corpus is discovered from the directory rather than listed here, so a
// scenario added to the repository is a scenario that runs in CI. A corpus that
// has to be registered in two places is a corpus that quietly stops covering
// the newest cases.
package scenarios

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexou8/relab/internal/fault"
	"github.com/alexou8/relab/internal/testsupport"
)

// compressed recovery windows, so the suite observes real lease expiry without
// spending thirty seconds per scenario.
var timingEnv = []string{
	"RELAB_LEASE_DURATION=2s",
	"RELAB_LEASE_RENEW_INTERVAL=500ms",
	"RELAB_HEARTBEAT_INTERVAL=300ms",
	"RELAB_REAPER_INTERVAL=200ms",
	"RELAB_LOG_LEVEL=error",
}

const workflowFile = "../../examples/data-pipeline.yaml"

func TestScenarioCorpus(t *testing.T) {
	if testing.Short() {
		t.Skip("the scenario corpus spawns processes and waits on lease expiry; skipped in -short")
	}

	files, err := filepath.Glob("../../examples/scenarios/*.yaml")
	if err != nil {
		t.Fatalf("find scenarios: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no scenarios found; the corpus is the regression suite and must not be empty")
	}

	binary := buildBinary(t)

	for _, file := range files {
		t.Run(strings.TrimSuffix(filepath.Base(file), ".yaml"), func(t *testing.T) {
			// Each scenario gets its own database, so one scenario's runs
			// cannot be claimed by another's workers.
			db := testsupport.DB(t)
			dsn := testsupport.DatabaseDSN(t, db)

			scenario, err := fault.LoadScenario(file)
			if err != nil {
				t.Fatalf("load scenario: %v", err)
			}
			if !scenario.Deterministic() {
				t.Fatalf("scenario %q uses probability-driven faults; a corpus entry that passes "+
					"or fails by luck is not a regression test", scenario.Name)
			}

			report := runScenario(t, binary, dsn, file)
			if !report.Passed {
				t.Fatalf("scenario %s failed its assertions: %+v", report.Scenario, report.Results)
			}
			t.Logf("%s: recovery %dms, retries %d, lost %d, duplicates %d, faults %d, final %s",
				report.Scenario, report.RecoveryTimeMS, report.Retries, report.LostTasks,
				report.DuplicateEffects, report.FaultsInjected, report.FinalState)
		})
	}
}

// report mirrors assert.Report's JSON, decoded from the command's output rather
// than obtained by calling into the package. The corpus tests the command a CI
// pipeline actually runs, including its exit code.
type report struct {
	Scenario         string `json:"scenario"`
	Passed           bool   `json:"passed"`
	RecoveryTimeMS   int64  `json:"recovery_time_ms"`
	Retries          int    `json:"retries"`
	LostTasks        int    `json:"lost_tasks"`
	DuplicateEffects int    `json:"duplicate_effects"`
	FaultsInjected   int    `json:"faults_injected"`
	FinalState       string `json:"final_state"`
	Results          []struct {
		Name     string `json:"name"`
		Passed   bool   `json:"passed"`
		Expected string `json:"expected"`
		Actual   string `json:"actual"`
	} `json:"results"`
}

func runScenario(t *testing.T, binary, dsn, scenarioFile string) report {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, "--json", "test", workflowFile,
		"--scenario", scenarioFile, "--timeout", "60s")
	cmd.Env = append(append(os.Environ(), timingEnv...), "RELAB_DSN="+dsn)
	cmd.Stderr = &testWriter{t: t}

	out, err := cmd.Output()
	// A non-zero exit means the assertions failed, which the decoded report
	// explains. It is not itself a test failure to report separately.
	if err != nil && len(out) == 0 {
		t.Fatalf("relab test produced no output: %v", err)
	}

	var r report
	if decodeErr := json.Unmarshal(out, &r); decodeErr != nil {
		t.Fatalf("could not decode the report (%v); output was:\n%s", decodeErr, out)
	}
	// Exit code and report must agree, or CI would pass on a failing scenario.
	if r.Passed && err != nil {
		t.Errorf("the report says the scenario passed but the command exited non-zero: %v", err)
	}
	if !r.Passed && err == nil {
		t.Error("the report says the scenario failed but the command exited zero; a CI step " +
			"would not notice")
	}
	return r
}

func buildBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "relab")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", out,
		"github.com/alexou8/relab/cmd/relab")
	cmd.Env = os.Environ()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build relab: %v\n%s", err, output)
	}
	return out
}

type testWriter struct{ t *testing.T }

func (w *testWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			w.t.Logf("  %s", line)
		}
	}
	return len(p), nil
}
