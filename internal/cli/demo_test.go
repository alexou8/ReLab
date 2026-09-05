package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The demo ships copies of two files from examples/, so that `relab demo` needs
// nothing but a database. Copies drift. If the corpus's scenario changes and
// the embedded one does not, the demo tells a story the tests no longer prove —
// which is the one failure this project cannot afford, since the demo is the
// argument.
//
// The scenario differs in exactly one line: its `workflow:` path, because the
// embedded pair sit next to each other in a temporary directory rather than in
// examples/. That difference is asserted rather than ignored.
func TestEmbeddedDemoFilesMatchTheCorpus(t *testing.T) {
	for _, c := range []struct {
		embedded string
		original string
		rewrites map[string]string
	}{
		{
			embedded: "demoassets/effectful.yaml",
			original: "../../examples/effectful.yaml",
		},
		{
			embedded: "demoassets/worker-crash-effectful.yaml",
			original: "../../examples/scenarios/worker-crash-effectful.yaml",
			rewrites: map[string]string{
				"workflow: examples/effectful.yaml": "workflow: effectful.yaml",
			},
		},
	} {
		t.Run(filepath.Base(c.embedded), func(t *testing.T) {
			embedded, err := demoAssets.ReadFile(c.embedded)
			if err != nil {
				t.Fatalf("read embedded %s: %v", c.embedded, err)
			}
			original, err := os.ReadFile(c.original)
			if err != nil {
				t.Fatalf("read %s: %v", c.original, err)
			}
			want := string(original)
			for from, to := range c.rewrites {
				if !strings.Contains(want, from) {
					t.Fatalf("%s no longer contains %q, so the rewrite this test allows is stale",
						c.original, from)
				}
				want = strings.ReplaceAll(want, from, to)
			}
			if string(embedded) != want {
				t.Errorf("%s has drifted from %s: `relab demo` would tell a story the "+
					"corpus does not run. Copy the file across (rewriting only the workflow path).",
					c.embedded, c.original)
			}
		})
	}
}
