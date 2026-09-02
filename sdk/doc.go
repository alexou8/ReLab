// Package sdk is the public Go interface for defining ReLab workflows.
//
// A workflow is a YAML definition plus a set of Go handlers. The YAML says what
// the steps are and how they depend on each other; the handlers say what a step
// does. They are kept separate on purpose: the definition is data that can be
// hashed, versioned, diffed and replayed, and the handler is code that cannot.
//
// A minimal program:
//
//	reg := sdk.NewRegistry()
//	reg.MustHandle("import_csv", func(ctx context.Context, tc *sdk.TaskContext) (any, error) {
//		rows, err := readCSV(tc.Input())
//		if err != nil {
//			return nil, err
//		}
//		return map[string]int{"rows": len(rows)}, nil
//	})
//
// Handlers must be safe to run more than once. ReLab delivers at least once,
// so a handler that is retried after a lease expiry may run while a previous
// attempt is still in flight. Wrap anything with an external effect in
// TaskContext.Do, which records the effect in the idempotency ledger and skips
// it on a repeat.
package sdk
