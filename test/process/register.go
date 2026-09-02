package process

import (
	"testing"

	"github.com/alexou8/relab/internal/examples"
	"github.com/alexou8/relab/sdk"
)

// registerExamples gives the test's own registry the same handlers the spawned
// workers have, so a definition validated here is one they can execute.
func registerExamples(t *testing.T, reg *sdk.Registry) {
	t.Helper()
	if err := examples.Register(reg); err != nil {
		t.Fatalf("register example handlers: %v", err)
	}
}
