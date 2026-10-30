package repair_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func TestEngine_PatternPropertiesPreservedWithAdditionalPropertiesFalse(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{"id":{"type":"string"}},
		"patternProperties":{"^x_":{"type":"string"}},
		"required":["id"],
		"additionalProperties":false
	}`)
	args := []byte(`{"id":"1","x_meta":"v"}`)
	out, err := repair.NewEngine().Repair(repair.Input{
		ToolName: "t",
		ArgsJSON: args,
		Catalog:  []aipapi.ToolDef{{Name: "t", Parameters: schema}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != repair.OutcomePass || out.ReasonCode != toolcall.ReasonValidPassThrough {
		t.Fatalf("got kind=%v reason=%q (pattern key must not be stripped)", out.Kind, out.ReasonCode)
	}
	if out.ArgsJSON != nil {
		t.Fatalf("pass must keep ArgsJSON nil, got %s", out.ArgsJSON)
	}
}

func TestEngine_PatternPropertyInvalidValueNotDropped(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{"id":{"type":"string"}},
		"patternProperties":{"^x_":{"type":"string"}},
		"required":["id"],
		"additionalProperties":false
	}`)
	args := []byte(`{"id":"1","x_meta":1}`)
	out, err := repair.NewEngine().Repair(repair.Input{
		ToolName: "t",
		ArgsJSON: args,
		Catalog:  []aipapi.ToolDef{{Name: "t", Parameters: schema}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != repair.OutcomeUnrepairable || out.ReasonCode != toolcall.ReasonScalarCoercionDisabled {
		t.Fatalf("got kind=%v reason=%q want unrepairable scalar_coercion_disabled", out.Kind, out.ReasonCode)
	}
	if out.ArgsJSON != nil && !bytes.Equal(out.ArgsJSON, args) {
		t.Fatalf("must not drop/replace pattern key; got %s", out.ArgsJSON)
	}
}

func TestEngine_AmbiguousPatternPropertiesRefuse(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{
		"type":"object",
		"patternProperties":{
			"^x":{"type":"string"},
			"x_":{"type":"integer"}
		},
		"additionalProperties":false
	}`)
	args := []byte(`{"x_meta":"v"}`)
	out, err := repair.NewEngine().Repair(repair.Input{
		ToolName: "t",
		ArgsJSON: args,
		Catalog:  []aipapi.ToolDef{{Name: "t", Parameters: schema}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != repair.OutcomeUnrepairable {
		t.Fatalf("got kind=%v want unrepairable for ambiguous patterns", out.Kind)
	}
	if out.ArgsJSON != nil && !bytes.Equal(out.ArgsJSON, args) {
		t.Fatalf("mutated: %s", out.ArgsJSON)
	}
}

func TestEngine_RefDefaultInserted(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{
		"type":"object",
		"$defs":{"limit":{"type":"integer","default":10}},
		"properties":{
			"query":{"type":"string"},
			"limit":{"$ref":"#/$defs/limit"}
		},
		"required":["query","limit"],
		"additionalProperties":false
	}`)
	args := []byte(`{"query":"x"}`)
	out, err := repair.NewEngine().Repair(repair.Input{
		ToolName: "search",
		ArgsJSON: args,
		Catalog:  []aipapi.ToolDef{{Name: "search", Parameters: schema}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != repair.OutcomeRewrite || out.ReasonCode != toolcall.ReasonDefaultInserted {
		t.Fatalf("got kind=%v reason=%q", out.Kind, out.ReasonCode)
	}
	if !bytes.Equal(out.ArgsJSON, []byte(`{"query":"x","limit":10}`)) {
		t.Fatalf("args=%s", out.ArgsJSON)
	}
	assertArgsDetached(t, args, out.ArgsJSON)
}

func TestEngine_RefWithSiblingRefuse(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{
		"type":"object",
		"$defs":{"limit":{"type":"integer","default":10}},
		"properties":{
			"query":{"type":"string"},
			"limit":{"$ref":"#/$defs/limit","default":99}
		},
		"required":["query","limit"],
		"additionalProperties":false
	}`)
	args := []byte(`{"query":"x"}`)
	out, err := repair.NewEngine().Repair(repair.Input{
		ToolName: "search",
		ArgsJSON: args,
		Catalog:  []aipapi.ToolDef{{Name: "search", Parameters: schema}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != repair.OutcomeUnrepairable {
		t.Fatalf("got kind=%v reason=%q want unrepairable ($ref siblings)", out.Kind, out.ReasonCode)
	}
	if out.ArgsJSON == nil || !bytes.Equal(out.ArgsJSON, args) {
		t.Fatalf("must copy originals, got %s", out.ArgsJSON)
	}
	assertArgsDetached(t, args, out.ArgsJSON)
}

func TestEngine_NestedObjectDefaultPreservesOrder(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"opts":{
				"type":"object",
				"properties":{
					"b":{"type":"integer"},
					"a":{"type":"integer"}
				},
				"default":{"b":1,"a":2},
				"additionalProperties":false
			}
		},
		"required":["opts"],
		"additionalProperties":false
	}`)
	out, err := repair.NewEngine().Repair(repair.Input{
		ToolName: "t",
		ArgsJSON: []byte(`{}`),
		Catalog:  []aipapi.ToolDef{{Name: "t", Parameters: schema}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != repair.OutcomeRewrite || out.ReasonCode != toolcall.ReasonDefaultInserted {
		t.Fatalf("got kind=%v reason=%q", out.Kind, out.ReasonCode)
	}
	if !bytes.Equal(out.ArgsJSON, []byte(`{"opts":{"b":1,"a":2}}`)) {
		t.Fatalf("want schema key order preserved, got %s", out.ArgsJSON)
	}
}

func TestEngine_DuplicateKeysUnrepairable(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"integer"}},"additionalProperties":false}`)
	args := []byte(`{"a":1,"a":2}`)
	out, err := repair.NewEngine().Repair(repair.Input{
		ToolName: "t",
		ArgsJSON: args,
		Catalog:  []aipapi.ToolDef{{Name: "t", Parameters: schema}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != repair.OutcomeUnrepairable || out.ReasonCode != toolcall.ReasonUnrepairable {
		t.Fatalf("got kind=%v reason=%q", out.Kind, out.ReasonCode)
	}
	if out.ArgsJSON != nil && !bytes.Equal(out.ArgsJSON, args) {
		t.Fatalf("mutated: %s", out.ArgsJSON)
	}
}

func TestEngine_NestedDuplicateKeysUnrepairable(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{"nested":{"type":"object","additionalProperties":true}},
		"additionalProperties":false
	}`)
	args := []byte(`{"nested":{"a":1,"a":2}}`)
	out, err := repair.NewEngine().Repair(repair.Input{
		ToolName: "t",
		ArgsJSON: args,
		Catalog:  []aipapi.ToolDef{{Name: "t", Parameters: schema}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != repair.OutcomeUnrepairable {
		t.Fatalf("got kind=%v want unrepairable", out.Kind)
	}
	if out.ArgsJSON == nil || !bytes.Equal(out.ArgsJSON, args) {
		t.Fatalf("must preserve originals, got %s", out.ArgsJSON)
	}
	assertArgsDetached(t, args, out.ArgsJSON)
}

func TestEngine_ScalarCoercionTypeArrayAndNull(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		schema string
		args   string
	}{
		{
			name:   "type_array_number_to_string",
			schema: `{"type":"object","properties":{"v":{"type":["string","null"]}},"required":["v"],"additionalProperties":false}`,
			args:   `{"v":1}`,
		},
		{
			name:   "null_to_integer",
			schema: `{"type":"object","properties":{"v":{"type":"integer"}},"required":["v"],"additionalProperties":false}`,
			args:   `{"v":null}`,
		},
		{
			name:   "string_null_union_bool",
			schema: `{"type":"object","properties":{"v":{"type":["string","null"]}},"required":["v"],"additionalProperties":false}`,
			args:   `{"v":true}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := []byte(tc.args)
			out, err := repair.NewEngine().Repair(repair.Input{
				ToolName: "t",
				ArgsJSON: args,
				Catalog:  []aipapi.ToolDef{{Name: "t", Parameters: json.RawMessage(tc.schema)}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if out.Kind != repair.OutcomeUnrepairable || out.ReasonCode != toolcall.ReasonScalarCoercionDisabled {
				t.Fatalf("got kind=%v reason=%q", out.Kind, out.ReasonCode)
			}
			if out.ArgsJSON != nil && !bytes.Equal(out.ArgsJSON, args) {
				t.Fatalf("mutated: %s", out.ArgsJSON)
			}
		})
	}
}
