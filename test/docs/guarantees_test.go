// Package docs checks the claims the documentation makes about the test suite.
//
// docs/guarantees.md maps every public reliability claim onto the test that
// proves it. A matrix that cites a test which was renamed or deleted is worse
// than no matrix: it looks like evidence and is not. This test is what keeps it
// honest, and it needs no database and no processes.
package docs_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	// Test names as the matrix cites them: `TestSomething`, in backticks.
	citedTest = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")
	// Test names as Go declares them.
	declaredTest = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
)

func TestGuaranteeMatrixCitesTestsThatExist(t *testing.T) {
	root := repoRoot(t)

	matrix, err := os.ReadFile(filepath.Join(root, "docs", "guarantees.md"))
	if err != nil {
		t.Fatalf("docs/guarantees.md is the project's evidence table and must be readable: %v", err)
	}

	declared := declaredTests(t, root)
	seen := map[string]bool{}
	for _, match := range citedTest.FindAllStringSubmatch(string(matrix), -1) {
		name := match[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		if _, ok := declared[name]; !ok {
			t.Errorf("docs/guarantees.md cites %s, which no test declares: "+
				"the matrix claims evidence that does not exist", name)
		}
	}
	if len(seen) == 0 {
		t.Fatal("docs/guarantees.md cites no tests at all, which cannot be right")
	}
}

// declaredTests indexes every test function in the repository by name.
func declaredTests(t *testing.T, root string) map[string]string {
	t.Helper()
	found := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Vendored skill definitions and build output hold no tests of ours.
			switch d.Name() {
			case ".git", "node_modules", ".agents", "bin", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range declaredTest.FindAllStringSubmatch(string(source), -1) {
			found[match[1]] = path
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the repository: %v", err)
	}
	return found
}

// repoRoot walks up from the test's directory to the module root, so the test
// does not depend on where it was invoked from.
func repoRoot(t *testing.T) string {
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
			t.Fatal("no go.mod above the test directory; cannot locate the repository root")
		}
		dir = parent
	}
}
