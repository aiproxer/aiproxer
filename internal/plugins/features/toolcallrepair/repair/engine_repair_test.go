package repair_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func TestEngine_CoreBehaviors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		toolName       string
		args           []byte
		catalog        []aipapi.ToolDef
		wantKind       repair.OutcomeKind
		wantReason     string
		wantArgs       []byte // when non-nil, compare; when nil and wantNilArgs, expect nil
		wantNilArgs    bool
		assertDetached bool
		preserveInput  bool
	}{
		{
			name:     "valid_hot_path_preserves_args_bytes",
			toolName: "get_weather",
			args:     []byte(`{"location":"NYC"}`),
			catalog: []aipapi.ToolDef{{
				Name:       "get_weather",
				Parameters: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}`),
			}},
			wantKind:      repair.OutcomePass,
			wantReason:    toolcall.ReasonValidPassThrough,
			wantNilArgs:   true,
			preserveInput: true,
		},
		{
			name:     "refusal_preserves_originals",
			toolName: "get_weather",
			args:     []byte(`{"User-Id":"abc"}`),
			catalog: []aipapi.ToolDef{{
				Name:       "get_weather",
				Parameters: json.RawMessage(`{"type":"object","properties":{"user_id":{"type":"string"},"userId":{"type":"string"}},"additionalProperties":false}`),
			}},
			wantKind:       repair.OutcomeUnrepairable,
			wantReason:     toolcall.ReasonAmbiguousProperty,
			wantArgs:       []byte(`{"User-Id":"abc"}`),
			assertDetached: true,
		},
		{
			name:     "invalid_schema_truncated_args_no_syntax_rewrite",
			toolName: "broken",
			args:     []byte(`{"x":1`),
			catalog: []aipapi.ToolDef{{
				Name:       "broken",
				Parameters: json.RawMessage(`{"type":"object","properties":{`),
			}},
			wantKind:   repair.OutcomeUnrepairable,
			wantReason: toolcall.ReasonSchemaInvalid,
			// may be nil or a copy of originals
		},
		{
			name:           "empty_schema_still_syntax_only",
			toolName:       "echo",
			args:           []byte(`{"x":1`),
			catalog:        []aipapi.ToolDef{{Name: "echo", Parameters: nil}},
			wantKind:       repair.OutcomeRewrite,
			wantReason:     toolcall.ReasonSyntaxRepaired,
			wantArgs:       []byte(`{"x":1}`),
			assertDetached: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := append([]byte(nil), tc.args...)
			orig := append([]byte(nil), args...)
			eng := repair.NewEngine()
			out, err := eng.Repair(repair.Input{
				ToolCallID: "c1",
				ToolName:   tc.toolName,
				ArgsJSON:   args,
				Catalog:    tc.catalog,
			})
			if err != nil {
				t.Fatal(err)
			}
			if out.Kind != tc.wantKind || out.ReasonCode != tc.wantReason {
				t.Fatalf("got kind=%v reason=%q want kind=%v reason=%q", out.Kind, out.ReasonCode, tc.wantKind, tc.wantReason)
			}
			if tc.wantNilArgs {
				if out.ArgsJSON != nil {
					t.Fatalf("pass must leave ArgsJSON nil for original replay; got %s", out.ArgsJSON)
				}
			}
			if tc.wantArgs != nil && !bytes.Equal(out.ArgsJSON, tc.wantArgs) {
				t.Fatalf("args=%s want %s", out.ArgsJSON, tc.wantArgs)
			}
			if tc.name == "invalid_schema_truncated_args_no_syntax_rewrite" {
				if out.ArgsJSON != nil && !bytes.Equal(out.ArgsJSON, args) {
					t.Fatalf("must preserve originals, got %s", out.ArgsJSON)
				}
			}
			if tc.assertDetached {
				assertArgsDetached(t, args, out.ArgsJSON)
			}
			if tc.preserveInput && !bytes.Equal(args, orig) {
				t.Fatal("input args slice was mutated")
			}
		})
	}
}

func weatherObjectSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}`)
}

func TestEngine_ResolvedToolExactFastPath(t *testing.T) {
	t.Parallel()
	schema := weatherObjectSchema()
	eng := repair.NewEngine()

	t.Run("uses_provided_tool_with_empty_catalog", func(t *testing.T) {
		t.Parallel()
		out, err := eng.Repair(repair.Input{
			ToolName: "get_weather",
			ArgsJSON: []byte(`{"location":"NYC"}`),
			Tool:     aipapi.ToolDef{Name: "get_weather", Parameters: schema},
			Catalog:  nil,
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.Kind != repair.OutcomePass || out.ReasonCode != toolcall.ReasonValidPassThrough {
			t.Fatalf("got kind=%v reason=%q want pass", out.Kind, out.ReasonCode)
		}
	})

	t.Run("uses_provided_schema_not_catalog", func(t *testing.T) {
		t.Parallel()
		// Catalog empty-schema would pass {}; provided strict schema must refuse.
		out, err := eng.Repair(repair.Input{
			ToolName: "get_weather",
			ArgsJSON: []byte(`{}`),
			Tool:     aipapi.ToolDef{Name: "get_weather", Parameters: schema},
			Catalog:  []aipapi.ToolDef{{Name: "get_weather", Parameters: nil}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.Kind != repair.OutcomeUnrepairable || out.ReasonCode != toolcall.ReasonUnrepairable {
			t.Fatalf("got kind=%v reason=%q want unrepairable", out.Kind, out.ReasonCode)
		}
	})

	t.Run("name_mismatch_falls_back_to_catalog_normalize", func(t *testing.T) {
		t.Parallel()
		out, err := eng.Repair(repair.Input{
			ToolName: "Get-Weather",
			ArgsJSON: []byte(`{"location":"NYC"}`),
			Tool:     aipapi.ToolDef{Name: "get_weather", Parameters: schema},
			Catalog:  []aipapi.ToolDef{{Name: "get_weather", Parameters: schema}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.Kind != repair.OutcomeRewrite || out.ReasonCode != toolcall.ReasonToolNameNormalized {
			t.Fatalf("got kind=%v reason=%q want name normalize rewrite", out.Kind, out.ReasonCode)
		}
		if out.ToolName != "get_weather" {
			t.Fatalf("ToolName=%q want get_weather", out.ToolName)
		}
	})

	t.Run("ambiguous_normalized_still_unrepairable", func(t *testing.T) {
		t.Parallel()
		out, err := eng.Repair(repair.Input{
			ToolName: "getweather",
			ArgsJSON: []byte(`{"location":"NYC"}`),
			Tool:     aipapi.ToolDef{}, // assembler exact miss
			Catalog: []aipapi.ToolDef{
				{Name: "get_weather", Parameters: schema},
				{Name: "GetWeather", Parameters: schema},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.Kind != repair.OutcomeUnrepairable || out.ReasonCode != toolcall.ReasonAmbiguousToolName {
			t.Fatalf("got kind=%v reason=%q want ambiguous", out.Kind, out.ReasonCode)
		}
	})
}

func TestEngine_PostRepairArgsTooLarge(t *testing.T) {
	t.Parallel()
	// Default insertion grows the payload after validation failure; cap must catch post-repair size.
	args := []byte(`{"query":"x"}`)
	eng := repair.NewEngineWithCache(repair.NewSchemaCache(repair.DefaultSchemaLimits()))
	out, err := eng.Repair(repair.Input{
		ToolName:     "search",
		ArgsJSON:     args,
		MaxArgsBytes: len(args) + 4, // between input and {"query":"x","limit":10}
		Catalog: []aipapi.ToolDef{{
			Name: "search",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"query":{"type":"string"},
					"limit":{"type":"integer","default":10}
				},
				"required":["query","limit"],
				"additionalProperties":false
			}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != repair.OutcomeUnrepairable || out.ReasonCode != toolcall.ReasonArgsTooLarge {
		t.Fatalf("got kind=%v reason=%q want unrepairable args_too_large", out.Kind, out.ReasonCode)
	}
}

func TestEngine_ConcurrentRepair(t *testing.T) {
	t.Parallel()
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	eng := repair.NewEngineWithCache(cache)
	catalog := []aipapi.ToolDef{{
		Name:       "get_weather",
		Parameters: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}`),
	}}
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			in := repair.Input{
				ToolCallID: "c",
				ToolName:   "get_weather",
				ArgsJSON:   []byte(`{"location":"NYC"`),
				Catalog:    catalog,
			}
			if i%2 == 0 {
				in.ArgsJSON = []byte(`{"location":"NYC"}`)
			}
			out, err := eng.Repair(in)
			if err != nil {
				errs <- err
				return
			}
			if i%2 == 0 {
				if out.Kind != repair.OutcomePass {
					errs <- fmt.Errorf("want pass got kind=%v reason=%q", out.Kind, out.ReasonCode)
				}
				return
			}
			if out.Kind != repair.OutcomeRewrite || out.ReasonCode != toolcall.ReasonSyntaxRepaired {
				errs <- fmt.Errorf("want syntax rewrite got kind=%v reason=%q", out.Kind, out.ReasonCode)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
