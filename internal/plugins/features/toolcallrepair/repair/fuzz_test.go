package repair_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func FuzzCompleteJSONSuffix(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"a":1}`),
		[]byte(`{"a":"x"`),
		[]byte(`{"a":[1,2`),
		[]byte(`{"a":"\u12`),
		[]byte(`{"a":1]`),
		{0xff},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 4096 {
			t.Skip()
		}
		out, ok := repair.CompleteJSONSuffix(input)
		if !ok {
			return
		}
		if !bytes.HasPrefix(out, input) {
			t.Fatal("completion is not append-only")
		}
		if !json.Valid(out) {
			t.Fatal("successful completion is invalid JSON")
		}
		if len(out) > len(input)+4096 {
			t.Fatal("completion exceeded bounded suffix")
		}
	})
}

func FuzzSchemaPreScanCompile(f *testing.F) {
	for _, seed := range []string{
		`{"type":"object"}`,
		`{"$ref":"https://example.invalid/schema"}`,
		`{"$defs":{"node":{"$ref":"#/$defs/node"}},"$ref":"#/$defs/node"}`,
		`{"type":`,
		`{"a":1,"a":2}`,
		`{"a":{"b":{"c":{"d":1}}}}`,
		"{\"x\":\xff}",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			t.Skip()
		}
		limits := repair.DefaultSchemaLimits()
		limits.MaxSchemaBytes = 4096
		limits.MaxNodes = 256
		limits.MaxProperties = 128
		_, _ = repair.NewSchemaCache(limits).GetOrCompile(json.RawMessage(raw))
	})
}

func FuzzJSONTail(f *testing.F) {
	for _, seed := range []string{`{"a":1,`, `[1,2,`, `{"mode":`, `{,`, `[,]`, `{"a":tru,`, `{"a":"x,`, `{"a":1,}x`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			t.Skip()
		}
		out, err := repair.NewEngine().Repair(repair.Input{
			ToolName:     "run",
			ArgsJSON:     []byte(raw),
			Catalog:      []aipapi.ToolDef{{Name: "run"}},
			MaxArgsBytes: 4096,
		})
		if err != nil {
			t.Fatalf("Repair returned error: %v", err)
		}
		if len(out.ArgsJSON) > 4096 {
			t.Fatal("repair exceeded output bound")
		}
		if out.Kind == repair.OutcomeRewrite && !json.Valid(out.ArgsJSON) {
			t.Fatal("rewrite is invalid JSON")
		}
		if out.Kind == repair.OutcomeUnrepairable && !bytes.Equal(out.ArgsJSON, []byte(raw)) {
			t.Fatal("unrepairable must preserve exact original bytes")
		}
	})
}

func FuzzPendingRootValue(f *testing.F) {
	for _, seed := range []string{`{"mode":`, `{"enabled":`, `{"Mod":`, `{"outer":{"mode":`, `{"mode":"sa`, `{"mode":t`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, args string) {
		if len(args) > 4096 {
			t.Skip()
		}
		schema := json.RawMessage(`{"type":"object","properties":{"mode":{"const":"safe"},"enabled":{"default":true}}}`)
		out, err := repair.NewEngine().Repair(repair.Input{
			ToolName: "run", ArgsJSON: []byte(args), Catalog: []aipapi.ToolDef{{Name: "run", Parameters: schema}}, MaxArgsBytes: 4096,
		})
		if err != nil {
			t.Fatalf("Repair returned error: %v", err)
		}
		if out.Kind == repair.OutcomeRewrite {
			if !json.Valid(out.ArgsJSON) {
				t.Fatal("rewrite is invalid JSON")
			}
			compiled, err := repair.NewSchemaCache(repair.DefaultSchemaLimits()).GetOrCompile(schema)
			if err != nil {
				t.Fatal(err)
			}
			if err := compiled.Validate(out.ArgsJSON); err != nil {
				t.Fatalf("rewrite failed schema validation: %v", err)
			}
		}
		if out.Kind == repair.OutcomeUnrepairable && !bytes.Equal(out.ArgsJSON, []byte(args)) {
			t.Fatal("unrepairable must preserve exact original bytes")
		}
	})
}

func FuzzEngineRepair(f *testing.F) {
	schema := `{"type":"object","properties":{"value":{"type":"integer","default":1}},"additionalProperties":false}`
	for _, seed := range []string{
		`{"value":1}`, `{"Value":1}`, `{"value":1`, `{"value":"1"}`, `{}`,
		`{"value":1,"value":2}`, `[[[[[[[[[[[[[[[[[[1]]]]]]]]]]]]]]]]]]`,
		"{\"value\":\xff}",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, args string) {
		if len(args) > 4096 {
			t.Skip()
		}
		cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
		eng := repair.NewEngineWithCache(cache)
		out, err := eng.Repair(repair.Input{
			ToolName:     "run",
			ArgsJSON:     []byte(args),
			Catalog:      []aipapi.ToolDef{{Name: "run", Parameters: json.RawMessage(schema)}},
			MaxArgsBytes: 4096,
		})
		if err != nil {
			t.Fatalf("Repair returned error: %v", err)
		}
		if len(out.ArgsJSON) > 4096 {
			t.Fatal("repair exceeded output bound")
		}
		if out.Kind == repair.OutcomeRewrite {
			if !json.Valid(out.ArgsJSON) {
				t.Fatal("rewrite is invalid JSON")
			}
			cs, err := cache.GetOrCompile(json.RawMessage(schema))
			if err != nil {
				t.Fatalf("schema compile: %v", err)
			}
			if err := cs.Validate(out.ArgsJSON); err != nil {
				t.Fatalf("rewrite failed post-validation: %v", err)
			}
		}
	})
}
