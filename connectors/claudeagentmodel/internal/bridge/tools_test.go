package bridge

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func objectSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`)
}

func TestBuildCatalog_ReversibleNativeNames(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 80)
	unicode := "czytaj.plik.ü"
	tools := []aipapi.ToolDef{
		{Name: "Read", Description: "read a file", Parameters: objectSchema()},
		{Name: "Bash", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "functions.apply_patch", Parameters: objectSchema()},
		{Name: "mcp__x__foo", Parameters: objectSchema()},
		{Name: unicode, Parameters: objectSchema()},
		{Name: long, Parameters: objectSchema()},
	}
	catalog, warnings, err := BuildCatalog(tools, aipapi.ToolChoice{}, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = warnings
	if catalog.Len() != len(tools) {
		t.Fatalf("len=%d", catalog.Len())
	}
	for i, spec := range catalog.Specs() {
		if spec.Name != tools[i].Name {
			t.Fatalf("spec[%d] name=%q", i, spec.Name)
		}
		if len(spec.NativeName) > MaxNativeToolNameBytes {
			t.Fatalf("native name %q exceeds bound", spec.NativeName)
		}
		if spec.NativeName != MCPPrefix+spec.MCPName {
			t.Fatalf("native=%q mcp=%q", spec.NativeName, spec.MCPName)
		}
		got, ok := catalog.ByNative(spec.NativeName)
		if !ok || got.Name != spec.Name {
			t.Fatalf("native lookup for %q failed", spec.NativeName)
		}
		got, ok = catalog.ByMCP(spec.MCPName)
		if !ok || got.Name != spec.Name {
			t.Fatalf("mcp lookup for %q failed", spec.MCPName)
		}
	}
	// A plain MCP-safe name stays byte-identical and needs no hash suffix.
	if got := catalog.Specs()[0].NativeName; got != "mcp__aiproxer__Read" {
		t.Fatalf("Read native=%q", got)
	}
	if got := catalog.Specs()[2].MCPName; !strings.HasPrefix(got, "functions_apply_patch_") || len(got) != len("functions_apply_patch_")+10 {
		t.Fatalf("hashed name=%q", got)
	}
	if catalog.Specs()[2].NativeName == catalog.Specs()[3].NativeName {
		t.Fatal("distinct tools must map to distinct native names")
	}
	// The original description and schema bytes are preserved exactly.
	spec := catalog.Specs()[0]
	if spec.Description != "read a file" || string(spec.Parameters) != string(objectSchema()) {
		t.Fatalf("spec=%+v", spec)
	}
}

func TestBuildCatalog_Deterministic(t *testing.T) {
	t.Parallel()
	tools := []aipapi.ToolDef{{Name: "functions.apply_patch", Parameters: objectSchema()}}
	first, _, err := BuildCatalog(tools, aipapi.ToolChoice{}, false)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := BuildCatalog(tools, aipapi.ToolChoice{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Specs()[0].NativeName != second.Specs()[0].NativeName {
		t.Fatal("native name must be deterministic")
	}
	if first.Digest() != second.Digest() {
		t.Fatal("catalogue digest must be deterministic")
	}
	changed, _, err := BuildCatalog([]aipapi.ToolDef{{Name: "functions.apply_patch", Description: "changed", Parameters: objectSchema()}}, aipapi.ToolChoice{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Digest() == first.Digest() {
		t.Fatal("digest must change with the description")
	}
}

func deepSchema(depth int) json.RawMessage {
	value := `{"type":"string"}`
	for range depth {
		value = `{"type":"object","properties":{"x":` + value + `}}`
	}
	return json.RawMessage(value)
}

func TestBuildCatalog_AcceptsANestedSchemaVocabulary(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"path":{"type":"string","description":"file path","format":"uri"},
			"mode":{"enum":["fast","safe"]},
			"options":{"type":["object","null"],"additionalProperties":{"type":"string"}},
			"items":{"type":"array","items":{"anyOf":[{"type":"string"},{"type":"object","properties":{"n":{"type":"integer"}}}]}}
		},
		"required":["path"]
	}`)
	catalog, _, err := BuildCatalog([]aipapi.ToolDef{{Name: "Read", Parameters: schema}}, aipapi.ToolChoice{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(catalog.Specs()[0].Parameters) != string(schema) {
		t.Fatal("the admitted schema must be preserved byte for byte")
	}
}

func TestBuildCatalog_RootSchemaMustBeRepresentableByMCP(t *testing.T) {
	t.Parallel()
	// The installed MCP client rejects a non-object root input schema, so an empty
	// or scalar-root schema can never be published and must be refused before any
	// upstream I/O.
	for _, raw := range []string{`{}`, `{"type":"string"}`, `{"type":["object","null"]}`, `{"properties":{}}`} {
		if _, _, err := BuildCatalog([]aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(raw)}}, aipapi.ToolChoice{}, false); err == nil {
			t.Fatalf("root schema %s was admitted", raw)
		}
	}
	catalog, _, err := BuildCatalog([]aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(`{"type":"object"}`)}}, aipapi.ToolChoice{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(catalog.Specs()[0].Parameters) != `{"type":"object"}` {
		t.Fatalf("parameters=%s", catalog.Specs()[0].Parameters)
	}
}

func TestBuildCatalog_SchemaDialect(t *testing.T) {
	t.Parallel()
	accepted := []string{
		`{"type":"object"}`,
		`{"type":"object","required":[]}`,
		`{"type":"object","required":["a","b"],"properties":{"a":{"type":"string"},"b":{"type":["object","null"]}}}`,
		`{"type":"object","minimum":0,"maximum":1.5,"exclusiveMinimum":0,"exclusiveMaximum":2,"multipleOf":0.5}`,
		`{"type":"object","minLength":0,"maxLength":10,"minItems":0,"maxItems":10,"uniqueItems":true}`,
		`{"type":"object","$defs":{"d":{"type":"object"}}}`,
		`{"type":"object","if":{"properties":{"a":{"const":1}}},"then":{"required":["b"]},"else":{"required":[]}}`,
		`{"type":"object","dependentSchemas":{"a":{"required":["b"]}},"dependentRequired":{"a":["b"]}}`,
		`{"type":"object","prefixItems":[{"type":"string"}],"contains":{"type":"string"},"propertyNames":{"minLength":1}}`,
		`{"type":"object","anyOf":[{"required":["a"]},{"required":["b"]}],"not":{"required":["c"]}}`,
		`{"type":"object","dependencies":{"a":["b"],"c":{"required":["d"]}}}`,
		`{"type":"object","additionalProperties":{"type":"string"},"unevaluatedProperties":false}`,
	}
	for _, raw := range accepted {
		if _, _, err := BuildCatalog([]aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(raw)}}, aipapi.ToolChoice{}, false); err != nil {
			t.Fatalf("rejected valid schema %s: %v", raw, err)
		}
	}
	rejected := []string{
		`{"type":"object","properties":{"x":{"type":[]}}}`,
		`{"type":"object","properties":{"x":{"type":["string","string"]}}}`,
		`{"type":"object","properties":{"x":{"type":"bogus"}}}`,
		`{"type":"object","required":["a","a"]}`,
		`{"type":"object","properties":{"x":{"type":"integer","minimum":"zero"}}}`,
		`{"type":"object","properties":{"x":{"type":"string","pattern":5}}}`,
		`{"type":"object","properties":{"x":{"type":"string","minLength":-1}}}`,
		`{"type":"object","patternProperties":{"^x":{"type":"string"}}}`,
		`{"type":"object","patternProperties":{"[":{"type":"string"}}}`,
		`{"type":"object","properties":{"x":{"pattern":"^[a-z]+$"}}}`,
		`{"type":"object","properties":{"x":{"pattern":"["}}}`,
		`{"type":"object","properties":{"x":{"type":"object","patternProperties":{"^x":{"type":"string"}}}}}`,
		`{"type":"object","properties":{"x":{"type":"object","patternProperties":{"[":{"type":"string"}}}}}`,
		`{"type":"object","$defs":{"d":{"type":"bogus"}}}`,
		`{"type":"object","if":{"type":"bogus"}}`,
		`{"type":"object","dependentSchemas":{"a":{"type":"bogus"}}}`,
		`{"type":"object","prefixItems":[{"type":"bogus"}]}`,
		`{"type":"object","contains":{"type":"bogus"}}`,
		`{"type":"object","propertyNames":{"type":"bogus"}}`,
		`{"type":"object","properties":{"x":{"$ref":"#/$defs/x"}}}`,
		`{"type":"object","unknownKeyword":true}`,
		`{"type":"object","properties":{"x":{"type":"number","multipleOf":0}}}`,
		`{"type":"object","properties":{"x":{"type":"number","multipleOf":-1}}}`,
		`{"type":"object","properties":{"x":{"type":"number","multipleOf":"two"}}}`,
		`{"type":"object","dependencies":{"a":"b"}}`,
	}
	for _, raw := range rejected {
		if _, _, err := BuildCatalog([]aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(raw)}}, aipapi.ToolChoice{}, false); err == nil {
			t.Fatalf("admitted invalid schema %s", raw)
		}
	}
}

func TestBuildCatalog_NativeCollisionRejects(t *testing.T) {
	t.Parallel()
	// The second name is exactly the derived native short name of the first, so
	// both map to the same MCP tool name.
	first, _, err := BuildCatalog([]aipapi.ToolDef{{Name: "functions.apply_patch", Parameters: objectSchema()}}, aipapi.ToolChoice{}, false)
	if err != nil {
		t.Fatal(err)
	}
	colliding := first.Specs()[0].MCPName
	_, _, err = BuildCatalog([]aipapi.ToolDef{
		{Name: "functions.apply_patch", Parameters: objectSchema()},
		{Name: colliding, Parameters: objectSchema()},
	}, aipapi.ToolChoice{}, false)
	if err == nil || !strings.Contains(err.Error(), "native name") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildCatalog_RejectsInvalidCatalogues(t *testing.T) {
	t.Parallel()
	oversizedDescription := strings.Repeat("d", aipapi.MaxToolDescriptionBytes+1)
	oversizedSchema := json.RawMessage(`{"type":"object","properties":{"x":{"description":"` + strings.Repeat("s", aipapi.MaxToolParametersBytes) + `"}}}`)
	cases := []struct {
		name  string
		tools []aipapi.ToolDef
	}{
		{"duplicate original", []aipapi.ToolDef{
			{Name: "Read", Parameters: objectSchema()},
			{Name: "Read", Parameters: objectSchema()},
		}},
		{"blank name", []aipapi.ToolDef{{Name: "  ", Parameters: objectSchema()}}},
		{"control character", []aipapi.ToolDef{{Name: "bad\x01name", Parameters: objectSchema()}}},
		{"oversized name", []aipapi.ToolDef{{Name: strings.Repeat("n", aipapi.MaxToolNameBytes+1), Parameters: objectSchema()}}},
		{"oversized description", []aipapi.ToolDef{{Name: "Read", Description: oversizedDescription, Parameters: objectSchema()}}},
		{"missing parameters", []aipapi.ToolDef{{Name: "Read"}}},
		{"invalid json", []aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(`{`)}}},
		{"non-object schema", []aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(`[]`)}}},
		{"invalid schema type", []aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(`{"type":5}`)}}},
		{"invalid properties", []aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(`{"type":"object","properties":[]}`)}}},
		{"invalid required", []aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(`{"type":"object","required":"x"}`)}}},
		{"invalid nested type", []aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(`{"type":"object","properties":{"x":{"type":"not-a-json-schema-type"}}}`)}}},
		{"invalid nested properties", []aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(`{"type":"object","properties":{"x":{"properties":[]}}}`)}}},
		{"schema reference", []aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"#/$defs/x"}}}`)}}},
		{"invalid items", []aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(`{"type":"array","items":5}`)}}},
		{"invalid anyOf", []aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(`{"anyOf":[]}`)}}},
		{"deep nesting", []aipapi.ToolDef{{Name: "Read", Parameters: deepSchema(40)}}},
		{"oversized schema", []aipapi.ToolDef{{Name: "Read", Parameters: oversizedSchema}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := BuildCatalog(tc.tools, aipapi.ToolChoice{}, false); err == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
	if _, _, err := BuildCatalog(make([]aipapi.ToolDef, MaxCatalogTools+1), aipapi.ToolChoice{}, false); err == nil {
		t.Fatal("expected catalogue bound rejection")
	}
}

func TestBuildCatalog_ToolChoiceHandling(t *testing.T) {
	t.Parallel()
	tools := []aipapi.ToolDef{{Name: "Read", Parameters: objectSchema()}}

	none, warnings, err := BuildCatalog(tools, aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}, false)
	if err != nil {
		t.Fatal(err)
	}
	if none.Len() != 0 || len(warnings) != 0 {
		t.Fatalf("none len=%d warnings=%v", none.Len(), warnings)
	}
	// ToolChoice:none also stops validation: nothing is exposed.
	if _, _, err := BuildCatalog([]aipapi.ToolDef{{Name: "Read"}}, aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}, false); err != nil {
		t.Fatalf("none should not validate unused tools: %v", err)
	}

	forced, warnings, err := BuildCatalog(tools, aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired}, false)
	if err != nil {
		t.Fatal(err)
	}
	if forced.Len() != 1 || len(warnings) != 1 || warnings[0].WarningCode != "tool_choice_degraded" {
		t.Fatalf("forced=%v warnings=%v", forced.Len(), warnings)
	}
	if _, _, err := BuildCatalog(tools, aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired}, true); !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("strict err=%v", err)
	}

	subset, warnings, err := BuildCatalog(tools, aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{"Read"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if subset.Len() != 1 || len(warnings) != 1 || warnings[0].WarningCode != "tool_choice_allowed_subset_degraded" {
		t.Fatalf("subset=%v warnings=%v", subset.Len(), warnings)
	}
}

func TestShortToolName_BoundedAndStable(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"Read",
		"functions.apply_patch",
		"ümlaut",
		strings.Repeat("x", 300),
		"a b\tc",
	} {
		short := shortToolName(name)
		if len(MCPPrefix)+len(short) > MaxNativeToolNameBytes {
			t.Fatalf("name %q produced %q over the bound", name, short)
		}
		if short == "" {
			t.Fatalf("name %q produced an empty short name", name)
		}
		if short != shortToolName(name) {
			t.Fatalf("name %q is not deterministic", name)
		}
	}
}

func TestShortToolName_TotalLengthBudget(t *testing.T) {
	t.Parallel()
	exact := strings.Repeat("a", MaxNativeToolNameBytes-len(MCPPrefix))
	if got := shortToolName(exact); got != exact {
		t.Fatalf("a name that exactly fills the budget must stay unchanged, got %q", got)
	}
	if got := len(MCPPrefix) + len(shortToolName(exact)); got != MaxNativeToolNameBytes {
		t.Fatalf("native length=%d", got)
	}
	over := strings.Repeat("a", MaxNativeToolNameBytes-len(MCPPrefix)+1)
	short := shortToolName(over)
	if got := len(MCPPrefix) + len(short); got != MaxNativeToolNameBytes {
		t.Fatalf("hashed native length=%d", got)
	}
	stem := max(MaxNativeToolNameBytes-len(MCPPrefix)-1-nameHashBytes, 1)
	if short[:stem] != over[:stem] {
		t.Fatalf("hashed stem=%q", short[:stem])
	}
	if short[stem] != '_' {
		t.Fatalf("hashed name separator=%q", short[stem])
	}
	for _, r := range short[stem+1:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			t.Fatalf("hashed suffix is not hex: %q", short[stem+1:])
		}
	}
	if len(short[stem+1:]) != nameHashBytes {
		t.Fatalf("hash suffix length=%d", len(short[stem+1:]))
	}
}

func TestBuildCatalog_TotalByteBudget(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"x":{"description":"` + strings.Repeat("s", 250<<10) + `"}}}`)
	tools := make([]aipapi.ToolDef, 0, 5)
	for i := range 5 {
		tools = append(tools, aipapi.ToolDef{Name: "tool" + string(rune('a'+i)), Parameters: schema})
	}
	if _, _, err := BuildCatalog(tools, aipapi.ToolChoice{}, false); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("err=%v", err)
	}
}
