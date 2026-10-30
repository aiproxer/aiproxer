package repair_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func policyFrom(onUnrepairable string, maxArgs int) repair.FinalizerPolicy {
	p := repair.FinalizerPolicy{
		ID:             repair.DefaultFinalizerID,
		MaxArgsBytes:   repair.DefaultMaxArgsBytes,
		OnUnrepairable: repair.OnUnrepairablePassThrough,
		Order:          repair.DefaultFinalizerOrder,
		Schema:         repair.DefaultSchemaLimits(),
	}
	if onUnrepairable != "" {
		p.OnUnrepairable = onUnrepairable
	}
	if maxArgs > 0 {
		p.MaxArgsBytes = maxArgs
	}
	return p
}

func TestFinalizer_CoreBehaviors(t *testing.T) {
	t.Parallel()
	weatherCatalog := []aipapi.ToolDef{{
		Name:       "get_weather",
		Parameters: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}`),
	}}
	brokenCatalog := []aipapi.ToolDef{{
		Name:       "broken",
		Parameters: json.RawMessage(`{"type":"object","properties":{`),
	}}
	echoCatalog := []aipapi.ToolDef{{Name: "echo", Parameters: nil}}

	cases := []struct {
		name           string
		policy         repair.FinalizerPolicy
		toolName       string
		args           []byte
		catalog        []aipapi.ToolDef
		wantAction     toolcall.Action
		wantReason     string
		wantArgs       []byte
		assertDetached bool
		rejectNoLeak   bool
	}{
		{
			name:       "valid_pass_nil_args",
			policy:     policyFrom("", 0),
			toolName:   "get_weather",
			args:       []byte(`{"location":"NYC"}`),
			catalog:    weatherCatalog,
			wantAction: toolcall.ActionPass,
			wantReason: toolcall.ReasonValidPassThrough,
			wantArgs:   nil,
		},
		{
			name:           "rewrite_syntax",
			policy:         policyFrom("", 0),
			toolName:       "get_weather",
			args:           []byte(`{"location":"NYC"`),
			catalog:        weatherCatalog,
			wantAction:     toolcall.ActionRewrite,
			wantReason:     toolcall.ReasonSyntaxRepaired,
			wantArgs:       []byte(`{"location":"NYC"}`),
			assertDetached: true,
		},
		{
			name:       "unrepairable_pass_through",
			policy:     policyFrom(repair.OnUnrepairablePassThrough, 0),
			toolName:   "get_weather",
			args:       []byte(`{}`),
			catalog:    weatherCatalog,
			wantAction: toolcall.ActionPass,
			wantReason: toolcall.ReasonUnrepairable,
			wantArgs:   nil,
		},
		{
			name:         "unrepairable_reject",
			policy:       policyFrom(repair.OnUnrepairableError, 0),
			toolName:     "get_weather",
			args:         []byte(`{}`),
			catalog:      weatherCatalog,
			wantAction:   toolcall.ActionReject,
			wantReason:   toolcall.ReasonUnrepairable,
			wantArgs:     nil,
			rejectNoLeak: true,
		},
		{
			name:       "empty_schema_syntax_rewrite",
			policy:     policyFrom("", 0),
			toolName:   "echo",
			args:       []byte(`{"x":1`),
			catalog:    echoCatalog,
			wantAction: toolcall.ActionRewrite,
			wantReason: toolcall.ReasonSyntaxRepaired,
			wantArgs:   []byte(`{"x":1}`),
		},
		{
			name:       "invalid_schema_no_speculative_mutation",
			policy:     policyFrom("", 0),
			toolName:   "broken",
			args:       []byte(`{"x":1}`),
			catalog:    brokenCatalog,
			wantAction: toolcall.ActionPass,
			wantReason: toolcall.ReasonSchemaInvalid,
			wantArgs:   nil,
		},
		{
			name:       "invalid_schema_truncated_pass_through",
			policy:     policyFrom(repair.OnUnrepairablePassThrough, 0),
			toolName:   "broken",
			args:       []byte(`{"x":1`),
			catalog:    brokenCatalog,
			wantAction: toolcall.ActionPass,
			wantReason: toolcall.ReasonSchemaInvalid,
			wantArgs:   nil,
		},
		{
			name:       "invalid_schema_truncated_reject",
			policy:     policyFrom(repair.OnUnrepairableError, 0),
			toolName:   "broken",
			args:       []byte(`{"x":1`),
			catalog:    brokenCatalog,
			wantAction: toolcall.ActionReject,
			wantReason: toolcall.ReasonSchemaInvalid,
			wantArgs:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fin := repair.NewFinalizer(tc.policy)
			args := append([]byte(nil), tc.args...)
			res, err := fin.Finalize(context.Background(), toolcall.CompletedCall{
				ToolCallID: "c1",
				ToolName:   tc.toolName,
				ArgsJSON:   args,
			}, tc.catalog[0], tc.catalog, toolcall.Meta{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Action != tc.wantAction || res.ReasonCode != tc.wantReason {
				t.Fatalf("got action=%v reason=%q want action=%v reason=%q", res.Action, res.ReasonCode, tc.wantAction, tc.wantReason)
			}
			if tc.wantArgs == nil {
				if res.ArgsJSON != nil {
					t.Fatalf("want nil ArgsJSON, got %s", res.ArgsJSON)
				}
			} else if !bytes.Equal(res.ArgsJSON, tc.wantArgs) {
				t.Fatalf("args=%s want %s", res.ArgsJSON, tc.wantArgs)
			}
			if tc.assertDetached {
				if len(res.ArgsJSON) == 0 {
					t.Fatal("expected rewritten ArgsJSON")
				}
				orig := append([]byte(nil), args...)
				res.ArgsJSON[0] ^= 0xff
				if !bytes.Equal(args, orig) {
					t.Fatal("Finalize ArgsJSON aliases caller input")
				}
			}
			if tc.rejectNoLeak && (strings.Contains(res.ReasonCode, "{") || strings.Contains(res.ReasonCode, "location")) {
				t.Fatalf("reason leaked payload: %q", res.ReasonCode)
			}
		})
	}
}

func TestFinalizer_MappingTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		policy     repair.FinalizerPolicy
		toolName   string
		args       []byte
		catalog    []aipapi.ToolDef
		wantAction toolcall.Action
		wantReason string
	}{
		{
			name:     "tool_name_normalize_rewrite",
			policy:   policyFrom("", 0),
			toolName: "Get-Weather",
			args:     []byte(`{"location":"NYC"}`),
			catalog: []aipapi.ToolDef{{
				Name:       "get_weather",
				Parameters: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}`),
			}},
			wantAction: toolcall.ActionRewrite,
			wantReason: toolcall.ReasonToolNameNormalized,
		},
		{
			name:     "ambiguous_name_unrepairable_reject",
			policy:   policyFrom(repair.OnUnrepairableError, 0),
			toolName: "getweather",
			args:     []byte(`{"location":"NYC"}`),
			catalog: []aipapi.ToolDef{
				{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"additionalProperties":false}`)},
				{Name: "GetWeather", Parameters: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"additionalProperties":false}`)},
			},
			wantAction: toolcall.ActionReject,
			wantReason: toolcall.ReasonAmbiguousToolName,
		},
		{
			name:     "args_too_large",
			policy:   policyFrom("", 8),
			toolName: "get_weather",
			args:     []byte(`{"location":"NYC"}`),
			catalog: []aipapi.ToolDef{{
				Name:       "get_weather",
				Parameters: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"additionalProperties":false}`),
			}},
			wantAction: toolcall.ActionPass,
			wantReason: toolcall.ReasonArgsTooLarge,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fin := repair.NewFinalizer(tc.policy)
			res, err := fin.Finalize(context.Background(), toolcall.CompletedCall{
				ToolCallID: "c1",
				ToolName:   tc.toolName,
				ArgsJSON:   append([]byte(nil), tc.args...),
			}, lookupTool(tc.catalog, tc.toolName), tc.catalog, toolcall.Meta{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Action != tc.wantAction || res.ReasonCode != tc.wantReason {
				t.Fatalf("got action=%v reason=%q want action=%v reason=%q", res.Action, res.ReasonCode, tc.wantAction, tc.wantReason)
			}
		})
	}
}

func lookupTool(catalog []aipapi.ToolDef, name string) aipapi.ToolDef {
	for _, t := range catalog {
		if strings.EqualFold(t.Name, name) || t.Name == name {
			return t
		}
	}
	if len(catalog) > 0 {
		return catalog[0]
	}
	return aipapi.ToolDef{}
}

func TestFinalizer_ResolvedToolExactFastPath(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}`)
	tool := aipapi.ToolDef{Name: "get_weather", Parameters: schema}
	fin := repair.NewFinalizer(policyFrom("", 0))

	res, err := fin.Finalize(context.Background(), toolcall.CompletedCall{
		ToolCallID: "c1",
		ToolName:   "get_weather",
		ArgsJSON:   []byte(`{"location":"NYC"}`),
	}, tool, nil, toolcall.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != toolcall.ActionPass || res.ReasonCode != toolcall.ReasonValidPassThrough {
		t.Fatalf("got action=%v reason=%q want pass via resolved tool", res.Action, res.ReasonCode)
	}

	// Provided tool schema wins over empty-schema catalog entry.
	res, err = fin.Finalize(context.Background(), toolcall.CompletedCall{
		ToolCallID: "c2",
		ToolName:   "get_weather",
		ArgsJSON:   []byte(`{}`),
	}, tool, []aipapi.ToolDef{{Name: "get_weather", Parameters: nil}}, toolcall.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != toolcall.ActionPass || res.ReasonCode != toolcall.ReasonUnrepairable {
		t.Fatalf("got action=%v reason=%q want unrepairable from resolved schema", res.Action, res.ReasonCode)
	}
}
