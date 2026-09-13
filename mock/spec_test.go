package mock

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sbalaji6/agent-platform-contracts/platform"
)

// These tests keep the mock and the platform types in step with api/openapi.yaml and the contract
// schema, without a YAML or OpenAPI dependency.

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var methodLine = regexp.MustCompile(`^    (get|post|put|patch|delete):$`)

// specOperations maps "METHOD /path" to operationId for every operation under paths:.
func specOperations(spec string) map[string]string {
	ops := map[string]string{}
	inPaths := false
	var path, method string
	for _, line := range strings.Split(spec, "\n") {
		switch {
		case line == "paths:":
			inPaths = true
		case !inPaths || line == "":
		case line[0] != ' ':
			return ops
		case strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":"):
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
		case methodLine.MatchString(line):
			method = strings.ToUpper(methodLine.FindStringSubmatch(line)[1])
		case strings.HasPrefix(line, "      operationId: "):
			ops[method+" "+path] = strings.TrimPrefix(line, "      operationId: ")
		}
	}
	return ops
}

func TestRoutesMatchSpec(t *testing.T) {
	want := specOperations(readFile(t, "../api/openapi.yaml"))
	if len(want) == 0 {
		t.Fatal("found no operations in the spec")
	}
	got := map[string]string{}
	for _, rt := range routes {
		got[rt.method+" "+rt.path] = string(rt.op)
	}
	if !maps.Equal(got, want) {
		t.Errorf("mock routes %v\nspec operations %v", got, want)
	}
}

func TestErrorCodesMatchSpec(t *testing.T) {
	spec := readFile(t, "../api/openapi.yaml")
	start := strings.Index(spec, "\n    Error:\n")
	if start < 0 {
		t.Fatal("no Error schema in the spec")
	}
	rest := spec[start:]
	enum := strings.Index(rest, "\n          enum:\n")
	if enum < 0 {
		t.Fatal("no code enum in the Error schema")
	}
	var codes []platform.ErrorCode
	for _, line := range strings.Split(rest[enum+len("\n          enum:\n"):], "\n") {
		if !strings.HasPrefix(line, "            - ") {
			break
		}
		codes = append(codes, platform.ErrorCode(strings.TrimPrefix(line, "            - ")))
	}
	slices.Sort(codes)
	if !slices.Equal(codes, platform.Codes()) {
		t.Errorf("spec codes %v\nplatform codes %v", codes, platform.Codes())
	}
	// Every code must also have a row in the table in the schema description.
	for _, c := range codes {
		if !strings.Contains(rest, "| "+string(c)+" |") {
			t.Errorf("code %s has no row in the Error table", c)
		}
	}
}

func jsonNames(t reflect.Type) []string {
	var names []string
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func schemaProps(t *testing.T, schema map[string]any) []string {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %v", schema)
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func TestContractTypesMatchSchema(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(readFile(t, "../schemas/agent-version-contract.schema.json")), &schema); err != nil {
		t.Fatal(err)
	}
	props := schema["properties"].(map[string]any)
	tests := []struct {
		name   string
		schema map[string]any
		typ    reflect.Type
	}{
		{"contract", schema, reflect.TypeOf(platform.AgentVersionContract{})},
		{"limits", props["limits"].(map[string]any), reflect.TypeOf(platform.Limits{})},
		{"model", props["model"].(map[string]any), reflect.TypeOf(platform.Model{})},
		{"deprecation", props["deprecation"].(map[string]any), reflect.TypeOf(platform.Deprecation{})},
	}
	for _, tt := range tests {
		if got, want := jsonNames(tt.typ), schemaProps(t, tt.schema); !slices.Equal(got, want) {
			t.Errorf("%s: Go fields %v, schema properties %v", tt.name, got, want)
		}
	}
}

// specComponent returns the top-level property names and required list of a component in an OpenAPI
// file. It scans indentation rather than parsing YAML, which keeps this package dependency-free.
func specComponent(t *testing.T, path, component string) (props, required []string) {
	t.Helper()
	lines := strings.Split(readFile(t, path), "\n")
	// components.responses can hold an entry of the same name, so scan only the schemas section.
	schemas := slices.Index(lines, "  schemas:")
	if schemas < 0 {
		t.Fatalf("%s has no components.schemas section", path)
	}
	start := -1
	for i, line := range lines[schemas:] {
		if line == "    "+component+":" {
			start = schemas + i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s defines no schema component %s", path, component)
	}
	inProps := false
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case !strings.HasPrefix(line, "     "): // dedented out of the component
			return props, required
		case strings.HasPrefix(line, "      ") && !strings.HasPrefix(line, "       "): // a key of the component
			inProps = trimmed == "properties:"
			if list, ok := strings.CutPrefix(trimmed, "required: ["); ok && required == nil {
				for _, name := range strings.Split(strings.TrimSuffix(list, "]"), ",") {
					required = append(required, strings.TrimSpace(name))
				}
			}
		case inProps && strings.HasPrefix(line, "        ") && !strings.HasPrefix(line, "         "):
			if name, _, ok := strings.Cut(trimmed, ":"); ok {
				props = append(props, name)
			}
		}
	}
	return props, required
}

// The contract schema is inlined into both specs so they render in OpenAPI viewers, which do not
// resolve external file references. This fails if a copy drifts from the canonical JSON Schema.
func TestContractSchemaInlinedInSpecs(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(readFile(t, "../schemas/agent-version-contract.schema.json")), &schema); err != nil {
		t.Fatal(err)
	}
	wantProps := schemaProps(t, schema)
	var wantRequired []string
	for _, name := range schema["required"].([]any) {
		wantRequired = append(wantRequired, name.(string))
	}
	slices.Sort(wantRequired)

	for _, path := range []string{"../api/openapi.yaml", "../api/asoar-designer-api.yaml"} {
		props, required := specComponent(t, path, "AgentVersionContract")
		slices.Sort(props)
		slices.Sort(required)
		if !slices.Equal(props, wantProps) {
			t.Errorf("%s AgentVersionContract properties %v, schema has %v", path, props, wantProps)
		}
		if !slices.Equal(required, wantRequired) {
			t.Errorf("%s AgentVersionContract required %v, schema has %v", path, required, wantRequired)
		}
	}
}

// Viewers such as Swagger UI do not fetch external files, so neither spec may reference one.
func TestSpecsHaveNoExternalRefs(t *testing.T) {
	for _, path := range []string{"../api/openapi.yaml", "../api/asoar-designer-api.yaml"} {
		for i, line := range strings.Split(readFile(t, path), "\n") {
			ref := strings.Index(line, "$ref:")
			if ref < 0 {
				continue
			}
			target := strings.TrimSpace(line[ref+len("$ref:"):])
			target = strings.Trim(target, "'\"}, ")
			if !strings.HasPrefix(target, "#/") {
				t.Errorf("%s:%d references %s outside the document", path, i+1, target)
			}
		}
	}
}

func TestServedContractValidatesAgainstSchema(t *testing.T) {
	s := New(Options{})
	defer s.Close()
	if err := s.AddAgentVersion(AlertTriage()); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/agents/alert-triage/versions/3", nil)
	req.Header.Set(platform.HeaderTenantID, "t1")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	schema := readFile(t, "../schemas/agent-version-contract.schema.json")
	errs, err := BasicValidator{}.ValidateInput(json.RawMessage(schema), rec.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) > 0 {
		t.Errorf("served contract violates the schema: %+v", errs)
	}
}
