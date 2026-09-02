package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/examples"
	"github.com/alexou8/relab/internal/workflow"
	"github.com/alexou8/relab/sdk"
)

// defaultRegistry is the handler set the CLI validates against and executes
// with. A deployment with its own handlers builds its own binary around
// package sdk; the shipped binary carries the examples so that the quick start
// needs no code.
func defaultRegistry() *sdk.Registry {
	reg := sdk.NewRegistry()
	examples.MustRegister(reg)
	return reg
}

func newWorkflowCmd(g *global) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Validate and register workflow definitions",
	}
	cmd.AddCommand(newWorkflowValidateCmd(g), newWorkflowRegisterCmd(g), newWorkflowListCmd(g))
	return cmd
}

func newWorkflowValidateCmd(g *global) *cobra.Command {
	var checkHandlers bool
	cmd := &cobra.Command{
		Use:   "validate <file>",
		Short: "Check a workflow definition without touching the database",
		Long: "Parses a definition and reports every problem it finds: unknown fields, duplicate\n" +
			"step names, dependencies on steps that do not exist, cycles, and retry policies\n" +
			"that cannot work. It needs no database, so it is usable as a pre-commit check.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			def, err := parseFile(args[0], checkHandlers)
			if err != nil {
				return err
			}
			if g.json {
				return writeJSON(cmd, map[string]any{
					"name": def.Name, "version": def.Version,
					"steps": len(def.Steps), "hash": def.Hash, "valid": true,
				})
			}
			cmd.Printf("%s is valid\n", args[0])
			cmd.Printf("  workflow  %s v%d\n", def.Name, def.Version)
			cmd.Printf("  steps     %d\n", len(def.Steps))
			cmd.Printf("  roots     %v\n", def.Roots())
			cmd.Printf("  hash      %s\n", def.Hash)
			return nil
		},
	}
	cmd.Flags().BoolVar(&checkHandlers, "check-handlers", true,
		"require every handler to be registered in this binary")
	return cmd
}

func newWorkflowRegisterCmd(g *global) *cobra.Command {
	return &cobra.Command{
		Use:   "register <file>",
		Short: "Store a workflow definition so runs can be started from it",
		Long: "Registering the same definition twice is a no-op. Registering a different\n" +
			"definition under a name and version that already exist is refused: two runs\n" +
			"labelled the same version have to be comparable, or replay means nothing.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			def, err := parseFile(args[0], true)
			if err != nil {
				return err
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
			wf, err := eng.RegisterWorkflow(ctx, def)
			if err != nil {
				return err
			}
			if g.json {
				return writeJSON(cmd, map[string]any{
					"id": wf.ID, "name": wf.Name, "version": wf.Version, "hash": wf.Hash,
				})
			}
			cmd.Printf("registered %s v%d (%s)\n", wf.Name, wf.Version, wf.Hash[:12])
			return nil
		},
	}
}

func newWorkflowListCmd(g *global) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered workflows",
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
			workflows, err := eng.ListWorkflows(ctx, 100)
			if err != nil {
				return err
			}
			if g.json {
				return writeJSON(cmd, workflows)
			}
			if len(workflows) == 0 {
				cmd.Println("no workflows registered; try `relab workflow register examples/data-pipeline.yaml`")
				return nil
			}
			cmd.Printf("%-24s %-8s %-14s %s\n", "NAME", "VERSION", "HASH", "REGISTERED")
			for _, wf := range workflows {
				cmd.Printf("%-24s %-8d %-14s %s\n",
					wf.Name, wf.Version, wf.Hash[:12], wf.CreatedAt.Format("2006-01-02 15:04:05"))
			}
			return nil
		},
	}
}

// parseFile reads and validates a definition file.
func parseFile(path string, checkHandlers bool) (*workflow.Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var handlers map[string]bool
	if checkHandlers {
		handlers = defaultRegistry().Set()
	}
	def, err := workflow.Parse(data, handlers)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return def, nil
}

func writeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}
