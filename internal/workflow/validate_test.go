package workflow_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alexou8/relab/internal/workflow"
)

const linearYAML = `
name: data-pipeline
version: 1
steps:
  - name: import
    handler: import_csv
    retry: {max_attempts: 3, initial_delay: 1s, multiplier: 2, max_delay: 30s, jitter: 0.2}
  - name: validate
    handler: validate_rows
    depends_on: [import]
  - name: analyze
    handler: analyze
    depends_on: [validate]
`

var linearHandlers = map[string]bool{"import_csv": true, "validate_rows": true, "analyze": true}

func TestParseLinearWorkflow(t *testing.T) {
	def, err := workflow.Parse([]byte(linearYAML), linearHandlers)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if def.Name != "data-pipeline" || def.Version != 1 || len(def.Steps) != 3 {
		t.Fatalf("parsed %s, want data-pipeline v1 with 3 steps", def)
	}
	if got := def.Roots(); len(got) != 1 || got[0] != "import" {
		t.Fatalf("roots are %v, want [import]", got)
	}
	if got := def.Dependents("import"); len(got) != 1 || got[0] != "validate" {
		t.Fatalf("dependents of import are %v, want [validate]", got)
	}
	retry := def.RetryFor("import")
	if retry.MaxAttempts != 3 || retry.InitialDelay.Duration() != time.Second {
		t.Fatalf("retry policy is %+v, want 3 attempts and a 1s initial delay", retry)
	}
	if got := def.RetryFor("validate"); got != workflow.DefaultRetryPolicy() {
		t.Fatalf("a step with no retry block got %+v, want the default policy", got)
	}
	if len(def.Hash) != 64 {
		t.Fatalf("hash is %q, want 64 hex characters", def.Hash)
	}
}

func TestHashIgnoresFormattingButNotSemantics(t *testing.T) {
	reformatted := strings.ReplaceAll(linearYAML, "depends_on: [import]", "depends_on:\n      - import")
	reformatted = strings.ReplaceAll(reformatted, "depends_on: [validate]", "depends_on:\n      - validate")
	reformatted = strings.Replace(reformatted, "name: data-pipeline\nversion: 1",
		"version:   1\nname:      data-pipeline", 1)

	base, err := workflow.Parse([]byte(linearYAML), linearHandlers)
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	same, err := workflow.Parse([]byte(reformatted), linearHandlers)
	if err != nil {
		t.Fatalf("parse reformatted: %v", err)
	}
	if base.Hash != same.Hash {
		t.Fatalf("reformatting changed the hash: %s vs %s", base.Hash, same.Hash)
	}

	changed, err := workflow.Parse(
		[]byte(strings.Replace(linearYAML, "max_attempts: 3", "max_attempts: 5", 1)), linearHandlers)
	if err != nil {
		t.Fatalf("parse changed: %v", err)
	}
	if changed.Hash == base.Hash {
		t.Fatal("changing max_attempts did not change the hash")
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "cycle",
			yaml: `
name: cyclic
version: 1
steps:
  - {name: a, handler: h, depends_on: [c]}
  - {name: b, handler: h, depends_on: [a]}
  - {name: c, handler: h, depends_on: [b]}
`,
			wantErr: "form a cycle",
		},
		{
			name: "self dependency",
			yaml: `
name: selfdep
version: 1
steps:
  - {name: a, handler: h, depends_on: [a]}
`,
			wantErr: "depends on itself",
		},
		{
			name: "duplicate step names",
			yaml: `
name: dup
version: 1
steps:
  - {name: a, handler: h}
  - {name: a, handler: h}
`,
			wantErr: "declared twice",
		},
		{
			name: "dependency on a step that does not exist",
			yaml: `
name: dangling
version: 1
steps:
  - {name: a, handler: h, depends_on: [nowhere]}
`,
			wantErr: `depends on "nowhere"`,
		},
		{
			name: "unknown handler",
			yaml: `
name: unknown-handler
version: 1
steps:
  - {name: a, handler: not_registered}
`,
			wantErr: "is not registered",
		},
		{
			name: "missing handler",
			yaml: `
name: no-handler
version: 1
steps:
  - {name: a}
`,
			wantErr: "handler is required",
		},
		{
			name: "no steps",
			yaml: `
name: empty
version: 1
steps: []
`,
			wantErr: "at least one step",
		},
		{
			name: "zero version",
			yaml: `
name: v0
version: 0
steps:
  - {name: a, handler: h}
`,
			wantErr: "version must be a positive integer",
		},
		{
			name: "step name with a separator character",
			yaml: `
name: badname
version: 1
steps:
  - {name: "a:b", handler: h}
`,
			wantErr: "may only contain letters",
		},
		{
			name: "unknown field",
			yaml: `
name: typo
version: 1
steps:
  - {name: a, handler: h, retires: 3}
`,
			wantErr: "field retires not found",
		},
		{
			name: "shrinking backoff",
			yaml: `
name: shrink
version: 1
steps:
  - {name: a, handler: h, retry: {max_attempts: 3, multiplier: 0.5, initial_delay: 1s}}
`,
			wantErr: "multiplier must be at least 1",
		},
		{
			name: "jitter out of range",
			yaml: `
name: jitter
version: 1
steps:
  - {name: a, handler: h, retry: {max_attempts: 2, multiplier: 2, jitter: 3}}
`,
			wantErr: "jitter must be a fraction",
		},
		{
			name: "initial delay above max delay",
			yaml: `
name: delays
version: 1
steps:
  - {name: a, handler: h, retry: {max_attempts: 2, multiplier: 2, initial_delay: 60s, max_delay: 30s}}
`,
			wantErr: "exceeds max_delay",
		},
		{
			name: "bare integer duration",
			yaml: `
name: bareint
version: 1
steps:
  - {name: a, handler: h, retry: {max_attempts: 2, multiplier: 2, initial_delay: 1}}
`,
			wantErr: "durations must be strings with a unit",
		},
		{
			name: "duplicate dependency",
			yaml: `
name: dupdep
version: 1
steps:
  - {name: a, handler: h}
  - {name: b, handler: h, depends_on: [a, a]}
`,
			wantErr: `lists "a" twice`,
		},
	}

	handlers := map[string]bool{"h": true}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := workflow.Parse([]byte(tt.yaml), handlers)
			if err == nil {
				t.Fatal("Parse accepted an invalid definition")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Parse said %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidationErrorMatchesErrInvalid(t *testing.T) {
	_, err := workflow.Parse([]byte("name: x\nversion: 1\nsteps: []\n"), nil)
	if !errors.Is(err, workflow.ErrInvalid) {
		t.Fatalf("validation error %v does not match workflow.ErrInvalid", err)
	}
}

func TestValidationReportsEveryProblem(t *testing.T) {
	const bad = `
name: many-problems
version: 0
steps:
  - {name: a, handler: ""}
  - {name: a, handler: nope}
`
	_, err := workflow.Parse([]byte(bad), map[string]bool{"h": true})
	if err == nil {
		t.Fatal("Parse accepted a definition with several problems")
	}
	var v *workflow.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("error is %T, want *workflow.ValidationError", err)
	}
	if len(v.Problems) < 4 {
		t.Fatalf("reported %d problems (%v); a single-problem-at-a-time report makes fixing "+
			"a workflow a guessing game", len(v.Problems), v.Problems)
	}
}

func TestParseSkipsHandlerCheckWhenRegistryIsNil(t *testing.T) {
	// `relab workflow validate` checks a file's shape without a handler
	// registry to hand.
	_, err := workflow.Parse([]byte(linearYAML), nil)
	if err != nil {
		t.Fatalf("parse with a nil registry: %v", err)
	}
}

func TestFanOutFanInIsValid(t *testing.T) {
	const diamond = `
name: diamond
version: 2
steps:
  - {name: fetch, handler: h}
  - {name: left, handler: h, depends_on: [fetch]}
  - {name: right, handler: h, depends_on: [fetch]}
  - {name: join, handler: h, depends_on: [left, right]}
`
	def, err := workflow.Parse([]byte(diamond), map[string]bool{"h": true})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := def.Dependents("fetch"); len(got) != 2 {
		t.Fatalf("fetch has %d dependents, want 2", len(got))
	}
	if got := def.Roots(); len(got) != 1 || got[0] != "fetch" {
		t.Fatalf("roots are %v, want [fetch]", got)
	}
}

func TestCycleReportIsThePathNotJustAnAssertion(t *testing.T) {
	const withTail = `
name: tail
version: 1
steps:
  - {name: start, handler: h}
  - {name: a, handler: h, depends_on: [start, c]}
  - {name: b, handler: h, depends_on: [a]}
  - {name: c, handler: h, depends_on: [b]}
`
	_, err := workflow.Parse([]byte(withTail), map[string]bool{"h": true})
	if err == nil {
		t.Fatal("Parse accepted a cyclic workflow")
	}
	msg := err.Error()
	for _, name := range []string{"a", "b", "c"} {
		if !strings.Contains(msg, name) {
			t.Fatalf("cycle report %q does not name step %q", msg, name)
		}
	}
	if strings.Contains(msg, "start") {
		t.Fatalf("cycle report %q includes 'start', which leads into the cycle but is not part of it", msg)
	}
}
