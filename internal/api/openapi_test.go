package api_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/alexou8/relab/internal/engine"
	"github.com/alexou8/relab/internal/event"
)

// The API serialises the engine's structs as they are, with no JSON tags, so
// every field name in docs/openapi.yaml is a Go identifier. That makes the
// document silently wrong the moment a field is renamed — and a published API
// description nobody can trust is worse than none.
//
// This test is the enforcement. It needs no database and no server: it compares
// the properties of each documented schema against the struct the handler
// actually returns.
func TestOpenAPIMatchesTheTypesTheAPIReturns(t *testing.T) {
	spec := loadSpec(t)

	for _, c := range []struct {
		schema string
		value  any
	}{
		{"Workflow", engine.Workflow{}},
		{"Run", engine.Run{}},
		{"Task", engine.Task{}},
		{"Worker", engine.Worker{}},
		{"Event", event.Event{}},
	} {
		t.Run(c.schema, func(t *testing.T) {
			documented := propertiesOf(t, spec, c.schema)
			actual := fieldsOf(c.value)

			for name := range actual {
				if _, ok := documented[name]; !ok {
					t.Errorf("%s.%s is returned by the API and missing from docs/openapi.yaml: "+
						"a caller reading the document would not know the field exists", c.schema, name)
				}
			}
			for name := range documented {
				if _, ok := actual[name]; !ok {
					t.Errorf("docs/openapi.yaml documents %s.%s, which the API does not return: "+
						"either the field was renamed or the document was written from a guess", c.schema, name)
				}
			}
		})
	}
}

// Stats is the one response with JSON tags, so its documented properties are
// compared against the tags rather than the field names.
func TestOpenAPIMatchesTheStatsTags(t *testing.T) {
	spec := loadSpec(t)
	documented := propertiesOf(t, spec, "Stats")

	typ := reflect.TypeOf(engine.Stats{})
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		if _, ok := documented[tag]; !ok {
			t.Errorf("stats field %q is missing from docs/openapi.yaml", tag)
		}
		delete(documented, tag)
	}
	for name := range documented {
		t.Errorf("docs/openapi.yaml documents stats field %q, which the API does not return", name)
	}
}

// The documented enums are the states the engine can actually be in. A new
// status that never reaches the document leaves a caller switching on a value
// they were never told about.
func TestOpenAPIEnumsCoverEveryStatus(t *testing.T) {
	spec := loadSpec(t)

	for _, c := range []struct {
		schema string
		values []string
	}{
		{"RunStatus", []string{
			string(engine.RunCreated), string(engine.RunQueued), string(engine.RunRunning),
			string(engine.RunSucceeded), string(engine.RunFailed), string(engine.RunCancelled),
		}},
		{"WorkerStatus", []string{
			string(engine.WorkerHealthy), string(engine.WorkerSuspect),
			string(engine.WorkerLost), string(engine.WorkerStopped),
		}},
	} {
		t.Run(c.schema, func(t *testing.T) {
			documented := enumOf(t, spec, c.schema)
			for _, want := range c.values {
				if !documented[want] {
					t.Errorf("%s %q is missing from docs/openapi.yaml", c.schema, want)
				}
			}
		})
	}
}

func loadSpec(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatalf("the API description is part of the public surface and must be readable: %v", err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("docs/openapi.yaml does not parse as YAML: %v", err)
	}
	return spec
}

func propertiesOf(t *testing.T, spec map[string]any, schema string) map[string]struct{} {
	t.Helper()
	node := schemaNode(t, spec, schema)
	props, ok := node["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema %s has no properties", schema)
	}
	names := make(map[string]struct{}, len(props))
	for name := range props {
		names[name] = struct{}{}
	}
	return names
}

func enumOf(t *testing.T, spec map[string]any, schema string) map[string]bool {
	t.Helper()
	node := schemaNode(t, spec, schema)
	values, ok := node["enum"].([]any)
	if !ok {
		t.Fatalf("schema %s has no enum", schema)
	}
	found := make(map[string]bool, len(values))
	for _, v := range values {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("schema %s has a non-string enum value %v", schema, v)
		}
		found[s] = true
	}
	return found
}

func schemaNode(t *testing.T, spec map[string]any, schema string) map[string]any {
	t.Helper()
	components, ok := spec["components"].(map[string]any)
	if !ok {
		t.Fatal("docs/openapi.yaml has no components section")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("docs/openapi.yaml has no component schemas")
	}
	node, ok := schemas[schema].(map[string]any)
	if !ok {
		t.Fatalf("docs/openapi.yaml does not describe %s, which the API returns", schema)
	}
	return node
}

// fieldsOf reports the names a value serialises under, which for these structs
// is the exported field names: no JSON tags, so encoding/json uses them as they
// are. Unexported fields never reach the wire and are skipped.
func fieldsOf(v any) map[string]struct{} {
	typ := reflect.TypeOf(v)
	names := make(map[string]struct{}, typ.NumField())
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		if tag := f.Tag.Get("json"); tag != "" {
			if tag == "-" {
				continue
			}
			names[tagName(tag)] = struct{}{}
			continue
		}
		names[f.Name] = struct{}{}
	}
	return names
}

func tagName(tag string) string {
	for i, r := range tag {
		if r == ',' {
			return tag[:i]
		}
	}
	return tag
}

// Guards for the two assumptions the comparison above rests on: that these
// values really do serialise under their field names, and that a time is a
// string on the wire rather than an object.
func TestEngineTypesSerialiseUnderTheirFieldNames(t *testing.T) {
	body, err := json.Marshal(engine.Run{CreatedAt: time.Unix(0, 0).UTC()})
	if err != nil {
		t.Fatalf("marshal run: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal run: %v", err)
	}
	if _, ok := decoded["WorkflowName"]; !ok {
		t.Fatal("a run no longer serialises under its Go field names; docs/openapi.yaml describes names that are not on the wire")
	}
	if _, ok := decoded["CreatedAt"].(string); !ok {
		t.Fatal("a timestamp is no longer a string on the wire; the date-time formats in docs/openapi.yaml are wrong")
	}
}
