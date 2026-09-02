package cli

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/alexou8/relab/internal/replay"
)

func newReplayCmd(g *global) *cobra.Command {
	var diff bool
	cmd := &cobra.Command{
		Use:   "replay <run-id>",
		Short: "Reconstruct a run's state from its event journal",
		Long: "Replay reduces the recorded events into the run's logical state and prints it.\n\n" +
			"It does not re-execute handlers. What it reconstructs is which tasks ran, how many\n" +
			"attempts each took, what they produced, which faults fired and how the run ended.\n" +
			"Anything a handler did that was not recorded is not reconstructed, and wall-clock\n" +
			"timings are not reproduced.\n\n" +
			"With --diff it re-reduces the journal and compares the result against the stored\n" +
			"artifacts, reporting any divergence by category.",
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

			state, err := replay.Load(ctx, db.Conn(), runID)
			if err != nil {
				return err
			}
			if !diff {
				return renderState(cmd, g, state)
			}

			stored, err := replay.StoredArtifacts(ctx, db.Conn(), runID)
			if err != nil {
				return err
			}
			report := &replay.Report{Divergences: replay.VerifyArtifacts(state, stored)}

			// Re-reducing the same journal must produce the same state. A
			// difference here would mean the reducer is not a pure function,
			// which is the one property everything else rests on.
			again, err := replay.Load(ctx, db.Conn(), runID)
			if err != nil {
				return err
			}
			report.Divergences = append(report.Divergences, replay.Compare(state, again).Divergences...)

			return renderReport(cmd, g, state, report)
		},
	}
	cmd.Flags().BoolVar(&diff, "diff", false,
		"compare the reconstruction against the stored artifacts and report divergences")
	return cmd
}

func renderState(cmd *cobra.Command, g *global, state *replay.RunState) error {
	if g.json {
		return writeJSON(cmd, state)
	}
	cmd.Printf("run %s\n", state.RunID)
	cmd.Printf("  workflow    %s v%d (%s)\n", state.Workflow, state.Version, short(state.DefinitionHash))
	cmd.Printf("  status      %s\n", state.Status)
	cmd.Printf("  seed        %d\n", state.Seed)
	if state.Scenario != "" {
		cmd.Printf("  scenario    %s\n", state.Scenario)
	}
	cmd.Printf("  events      %d\n", state.EventCount)
	if state.FailureReason != "" {
		cmd.Printf("  reason      %s\n", state.FailureReason)
	}
	cmd.Println()
	cmd.Printf("%-10s %-14s %-9s %-8s %-8s %s\n",
		"STATUS", "TASK", "ATTEMPTS", "RETRIES", "REQUEUE", "ARTIFACTS")
	for _, name := range state.TaskNames() {
		t := state.Tasks[name]
		artifacts := ""
		for i, a := range t.Artifacts {
			if i > 0 {
				artifacts += " "
			}
			artifacts += a.Name + "@" + short(a.SHA256)
		}
		cmd.Printf("%-10s %-14s %-9d %-8d %-8d %s\n",
			t.Status, t.Name, t.Attempts, t.Retries, t.Requeues, artifacts)
	}
	if len(state.Faults) > 0 {
		cmd.Println()
		cmd.Printf("faults injected: %d\n", len(state.Faults))
		for _, f := range state.Faults {
			cmd.Printf("  %-20s at %-18s %s\n", f.Type, f.Point, f.Task)
		}
	}
	if len(state.SkippedEffects) > 0 {
		cmd.Println()
		cmd.Printf("side effects suppressed on retry: %d\n", len(state.SkippedEffects))
		for _, e := range state.SkippedEffects {
			cmd.Printf("  %s (attempt %d repeated attempt %d)\n", e.Key, e.Attempt, e.FirstAttempt)
		}
	}
	return nil
}

func renderReport(cmd *cobra.Command, g *global, state *replay.RunState, report *replay.Report) error {
	if g.json {
		out, err := report.JSON()
		if err != nil {
			return err
		}
		cmd.Println(string(out))
		if !report.Match() {
			return ErrAlreadyReported
		}
		return nil
	}

	if report.Match() {
		cmd.Printf("MATCH  run %s\n", state.RunID)
		cmd.Printf("  %d events reduce to the same state, and every artifact hash agrees with the "+
			"artifacts table\n", state.EventCount)
		return nil
	}

	cmd.Printf("DIVERGED  run %s (%d divergences)\n\n", state.RunID, len(report.Divergences))
	for _, d := range report.Divergences {
		cmd.Println("  " + d.String())
	}
	// A divergence is a failure: `relab replay --diff` in CI has to exit
	// non-zero without anyone parsing this output.
	return ErrAlreadyReported
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	if hash == "" {
		return "(none)"
	}
	return hash
}
