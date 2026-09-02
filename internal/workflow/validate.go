package workflow

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// nameRunes bounds what may appear in a step or workflow name. Names end up in
// event rows, in idempotency keys and in metric labels; allowing arbitrary text
// would let a workflow author put a colon in a step name and quietly collide
// two idempotency keys.
const nameMaxLen = 64

// ValidationError collects every problem found in a definition. Reporting them
// one at a time turns fixing a workflow into a guessing game, so validate keeps
// going after the first failure wherever it can do so safely.
type ValidationError struct {
	Workflow string
	Problems []string
}

func (e *ValidationError) Error() string {
	name := e.Workflow
	if name == "" {
		name = "workflow"
	}
	if len(e.Problems) == 1 {
		return fmt.Sprintf("%s is invalid: %s", name, e.Problems[0])
	}
	return fmt.Sprintf("%s is invalid:\n  - %s", name, strings.Join(e.Problems, "\n  - "))
}

// Is lets callers match with errors.Is(err, workflow.ErrInvalid).
func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

// ErrInvalid matches any validation failure.
var ErrInvalid = errors.New("workflow is invalid")

func (d *Definition) validate(knownHandlers map[string]bool) error {
	v := &ValidationError{Workflow: d.Name}

	if err := validName(d.Name); err != nil {
		v.Problems = append(v.Problems, fmt.Sprintf("workflow name: %s", err))
	}
	if d.Version <= 0 {
		v.Problems = append(v.Problems,
			fmt.Sprintf("version must be a positive integer, got %d", d.Version))
	}
	if len(d.Steps) == 0 {
		v.Problems = append(v.Problems, "a workflow must declare at least one step")
		return v
	}

	names := make(map[string]int, len(d.Steps))
	for i, step := range d.Steps {
		where := fmt.Sprintf("step %d", i+1)
		if step.Name != "" {
			where = fmt.Sprintf("step %q", step.Name)
		}

		if err := validName(step.Name); err != nil {
			v.Problems = append(v.Problems, fmt.Sprintf("%s: name: %s", where, err))
		} else if first, dup := names[step.Name]; dup {
			v.Problems = append(v.Problems, fmt.Sprintf(
				"step %q is declared twice, at positions %d and %d", step.Name, first+1, i+1))
		} else {
			names[step.Name] = i
		}

		if step.Handler == "" {
			v.Problems = append(v.Problems, fmt.Sprintf("%s: handler is required", where))
		} else if knownHandlers != nil && !knownHandlers[step.Handler] {
			v.Problems = append(v.Problems, fmt.Sprintf(
				"%s: handler %q is not registered (registered handlers: %s)",
				where, step.Handler, formatSet(knownHandlers)))
		}

		if step.Timeout < 0 {
			v.Problems = append(v.Problems, fmt.Sprintf("%s: timeout must not be negative", where))
		}
		if step.Retry != nil {
			for _, problem := range step.Retry.problems() {
				v.Problems = append(v.Problems, fmt.Sprintf("%s: retry: %s", where, problem))
			}
		}
	}

	// Dependency checks need the full name set, so they run in a second pass.
	for i, step := range d.Steps {
		where := fmt.Sprintf("step %d", i+1)
		if step.Name != "" {
			where = fmt.Sprintf("step %q", step.Name)
		}
		seen := make(map[string]bool, len(step.DependsOn))
		for _, dep := range step.DependsOn {
			switch {
			case dep == step.Name:
				v.Problems = append(v.Problems, fmt.Sprintf("%s depends on itself", where))
			case seen[dep]:
				v.Problems = append(v.Problems, fmt.Sprintf("%s lists %q twice in depends_on", where, dep))
			default:
				if _, known := names[dep]; !known {
					v.Problems = append(v.Problems, fmt.Sprintf(
						"%s depends on %q, which is not a step in this workflow", where, dep))
				}
			}
			seen[dep] = true
		}
	}

	// Cycle detection runs only when every edge points at a real step;
	// otherwise it would report a confusing second failure for a typo already
	// reported above.
	if len(v.Problems) == 0 {
		if cycle := d.findCycle(); cycle != nil {
			v.Problems = append(v.Problems, fmt.Sprintf(
				"steps form a cycle: %s", strings.Join(cycle, " -> ")))
		}
		if len(d.Roots()) == 0 {
			// Unreachable once a cycle is reported, but a workflow whose every
			// step has a dependency and yet has no cycle is impossible, so this
			// guards against a future edit to findCycle.
			v.Problems = append(v.Problems,
				"no step is a root: every step depends on another, so nothing can start")
		}
	}

	if len(v.Problems) > 0 {
		return v
	}
	return nil
}

// findCycle returns one cycle as a path, or nil. Depth-first search with an
// explicit colouring, so the reported path is the actual cycle rather than
// "a cycle exists somewhere".
func (d *Definition) findCycle() []string {
	const (
		white = 0 // unvisited
		grey  = 1 // on the current path
		black = 2 // fully explored
	)
	colour := make(map[string]int, len(d.Steps))
	deps := make(map[string][]string, len(d.Steps))
	order := make([]string, 0, len(d.Steps))
	for _, s := range d.Steps {
		deps[s.Name] = s.DependsOn
		order = append(order, s.Name)
	}

	var path []string
	var walk func(string) []string
	walk = func(name string) []string {
		colour[name] = grey
		path = append(path, name)
		for _, dep := range deps[name] {
			switch colour[dep] {
			case grey:
				// Trim the prefix that leads into the cycle but is not part of
				// it, then close the loop for readability.
				for i, n := range path {
					if n == dep {
						return append(append([]string{}, path[i:]...), dep)
					}
				}
				return append(append([]string{}, path...), dep)
			case white:
				if cycle := walk(dep); cycle != nil {
					return cycle
				}
			}
		}
		path = path[:len(path)-1]
		colour[name] = black
		return nil
	}

	for _, name := range order {
		if colour[name] == white {
			path = path[:0]
			if cycle := walk(name); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}

func (p *RetryPolicy) problems() []string {
	var problems []string
	if p.MaxAttempts < 1 {
		problems = append(problems, fmt.Sprintf(
			"max_attempts must be at least 1 (1 means no retry), got %d", p.MaxAttempts))
	}
	if p.MaxAttempts > 100 {
		problems = append(problems, fmt.Sprintf(
			"max_attempts is %d; more than 100 attempts is a stuck run, not a retry policy", p.MaxAttempts))
	}
	if p.InitialDelay < 0 {
		problems = append(problems, "initial_delay must not be negative")
	}
	if p.MaxAttempts > 1 && p.Multiplier < 1 {
		problems = append(problems, fmt.Sprintf(
			"multiplier must be at least 1, got %v; a shrinking backoff retries faster the "+
				"longer the dependency stays down", p.Multiplier))
	}
	if p.MaxDelay > 0 && p.InitialDelay > p.MaxDelay {
		problems = append(problems, fmt.Sprintf(
			"initial_delay %s exceeds max_delay %s", p.InitialDelay, p.MaxDelay))
	}
	if p.Jitter < 0 || p.Jitter > 1 {
		problems = append(problems, fmt.Sprintf(
			"jitter must be a fraction between 0 and 1, got %v", p.Jitter))
	}
	return problems
}

// validName enforces the character set that keeps names safe in idempotency
// keys, metric labels and file paths.
func validName(name string) error {
	if name == "" {
		return errors.New("must not be empty")
	}
	if len(name) > nameMaxLen {
		return fmt.Errorf("must be at most %d characters, got %d", nameMaxLen, len(name))
	}
	for i, r := range name {
		valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_'
		if !valid {
			return fmt.Errorf(
				"may only contain letters, digits, '-' and '_'; found %q at position %d "+
					"(names appear in idempotency keys, where a separator would collide)",
				r, i+1)
		}
	}
	return nil
}

func formatSet(set map[string]bool) string {
	if len(set) == 0 {
		return "none"
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
