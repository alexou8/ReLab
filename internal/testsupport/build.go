package testsupport

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ModuleRoot returns the directory holding go.mod, walking up from the test's
// working directory.
//
// Tests that build the relab binary need it. Relying on the working directory
// instead — `go build <import path>` from wherever the test happens to run —
// worked locally and failed on CI with "no required module provides package",
// which is a confusing way to learn that the invocation depended on something
// it should not have.
func ModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found above %s", dir)
		}
		dir = parent
	}
}

// BuildRelab compiles the relab binary into a temporary directory and returns
// its path.
//
// The build runs from the module root with a relative package path, so it does
// not depend on where the calling test happens to live.
func BuildRelab(t *testing.T) string {
	t.Helper()
	root := ModuleRoot(t)
	out := filepath.Join(t.TempDir(), "relab")

	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", out, "./cmd/relab")
	cmd.Dir = root
	cmd.Env = os.Environ()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build relab in %s: %v\n%s", root, err, output)
	}
	return out
}
