// Command relab is the single ReLab binary. It runs the control plane
// (`relab server`), a worker (`relab worker`), and the operator verbs that
// register workflows, start runs, inspect history, replay it and assert on it.
//
// One binary rather than several keeps deployment to one artifact and
// guarantees that the server and the workers in a deployment agree on the event
// schema — a mismatch there is exactly the kind of failure this project exists
// to catch, and it should be caught in tests, not in production.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/alexou8/relab/internal/cli"
)

// version is set at build time with -ldflags. It is reported by `relab version`
// and recorded on every worker row, so a run's history says which build
// produced it.
var version = "dev"

func main() {
	if err := cli.Execute(version); err != nil {
		// cobra has already printed usage errors; anything else is reported
		// here, once, on stderr. The error is not logged as well — an error is
		// either handled or returned, never both.
		if !errors.Is(err, cli.ErrAlreadyReported) {
			fmt.Fprintln(os.Stderr, "relab:", err)
		}
		os.Exit(1)
	}
}
