package toolcallrepair_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"gopkg.in/yaml.v3"
)

func TestFeatureBundle_Defaults(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := toolcallrepair.DecodeConfig(n)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}

	bundle, err := toolcallrepair.FeatureBundle(cfg)
	if err != nil {
		t.Fatalf("FeatureBundle: %v", err)
	}

	if err := bundle.Validate(); err != nil {
		t.Fatalf("bundle.Validate: %v", err)
	}
	if bundle.SchemaVersion != aipfeature.SchemaVersionV1 {
		t.Fatalf("SchemaVersion=%d want %d", bundle.SchemaVersion, aipfeature.SchemaVersionV1)
	}

	finalizers := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizers)
	if len(finalizers) != 1 {
		t.Fatalf("want 1 finalizer, got %d", len(finalizers))
	}
	fin := finalizers[0]
	if fin.ID() != toolcallrepair.ID {
		t.Fatalf("fin.ID()=%q want %q", fin.ID(), toolcallrepair.ID)
	}
	if fin.Order() != toolcallrepair.DefaultFinalizerOrder {
		t.Fatalf("fin.Order()=%d want %d", fin.Order(), toolcallrepair.DefaultFinalizerOrder)
	}

	maxArgs := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	if maxArgs != toolcallrepair.DefaultMaxArgsBytes {
		t.Fatalf("maxArgs=%d want %d", maxArgs, toolcallrepair.DefaultMaxArgsBytes)
	}
	if maxArgs != repair.DefaultMaxArgsBytes {
		t.Fatalf("maxArgs=%d want repair %d", maxArgs, repair.DefaultMaxArgsBytes)
	}
}

func TestFeatureBundle_ZeroValueConfig(t *testing.T) {
	t.Parallel()
	bundle, err := toolcallrepair.FeatureBundle(toolcallrepair.Config{})
	if err != nil {
		t.Fatalf("FeatureBundle(Config{}): %v", err)
	}
	if err := bundle.Validate(); err != nil {
		t.Fatalf("bundle.Validate: %v", err)
	}

	finalizers := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizers)
	if len(finalizers) != 1 {
		t.Fatalf("want 1 finalizer, got %d", len(finalizers))
	}
	if finalizers[0].ID() != toolcallrepair.ID {
		t.Fatalf("fin.ID()=%q want %q", finalizers[0].ID(), toolcallrepair.ID)
	}
	if finalizers[0].Order() != toolcallrepair.DefaultFinalizerOrder {
		t.Fatalf("fin.Order()=%d want %d", finalizers[0].Order(), toolcallrepair.DefaultFinalizerOrder)
	}
	maxArgs := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	if maxArgs != toolcallrepair.DefaultMaxArgsBytes {
		t.Fatalf("maxArgs=%d want %d", maxArgs, toolcallrepair.DefaultMaxArgsBytes)
	}
}

func TestFeatureBundle_CustomConfig_BehavioralIntegration(t *testing.T) {
	t.Parallel()
	rawYAML := `
order: 75
max_args_bytes: 8192
on_unrepairable: "error"
schema:
  max_schema_bytes: 500
  max_nesting_depth: 10
  max_nodes: 50
  max_properties: 20
  max_local_ref_depth: 5
  max_cache_entries: 16
  max_cache_bytes: 65536
`
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(rawYAML), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := toolcallrepair.DecodeConfig(n)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}

	bundle, err := toolcallrepair.FeatureBundle(cfg)
	if err != nil {
		t.Fatalf("FeatureBundle: %v", err)
	}
	if err := bundle.Validate(); err != nil {
		t.Fatalf("bundle.Validate: %v", err)
	}

	finalizers := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizers)
	if len(finalizers) != 1 {
		t.Fatalf("want 1 finalizer, got %d", len(finalizers))
	}
	fin := finalizers[0]
	if fin.ID() != toolcallrepair.ID {
		t.Fatalf("fin.ID()=%q want %q", fin.ID(), toolcallrepair.ID)
	}
	if fin.Order() != 75 {
		t.Fatalf("fin.Order()=%d want 75", fin.Order())
	}

	maxArgsPlane := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	if maxArgsPlane != 8192 {
		t.Fatalf("maxArgsPlane=%d want 8192", maxArgsPlane)
	}

	ctx := context.Background()
	validSchema := json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}`)
	weatherTool := aipapi.ToolDef{Name: "get_weather", Parameters: validSchema}
	weatherCatalog := []aipapi.ToolDef{weatherTool}

	t.Run("valid_pass_through", func(t *testing.T) {
		t.Parallel()
		res, err := fin.Finalize(ctx, toolcall.CompletedCall{
			ToolCallID: "c1",
			ToolName:   "get_weather",
			ArgsJSON:   []byte(`{"location":"NYC"}`),
		}, weatherTool, weatherCatalog, toolcall.Meta{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Action != toolcall.ActionPass || res.ReasonCode != toolcall.ReasonValidPassThrough {
			t.Fatalf("got action=%v reason=%q, want pass / valid_pass_through", res.Action, res.ReasonCode)
		}
		if res.ArgsJSON != nil {
			t.Fatalf("expected nil ArgsJSON on pass, got %s", res.ArgsJSON)
		}
	})

	t.Run("syntax_repair_rewrite", func(t *testing.T) {
		t.Parallel()
		res, err := fin.Finalize(ctx, toolcall.CompletedCall{
			ToolCallID: "c2",
			ToolName:   "get_weather",
			ArgsJSON:   []byte(`{"location":"NYC"`),
		}, weatherTool, weatherCatalog, toolcall.Meta{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Action != toolcall.ActionRewrite || res.ReasonCode != toolcall.ReasonSyntaxRepaired {
			t.Fatalf("got action=%v reason=%q, want rewrite / syntax_repaired", res.Action, res.ReasonCode)
		}
		if want := `{"location":"NYC"}`; string(res.ArgsJSON) != want {
			t.Fatalf("got rewritten args %s, want %s", res.ArgsJSON, want)
		}
	})

	t.Run("on_unrepairable_error_causes_reject", func(t *testing.T) {
		t.Parallel()
		res, err := fin.Finalize(ctx, toolcall.CompletedCall{
			ToolCallID: "c3",
			ToolName:   "get_weather",
			ArgsJSON:   []byte(`{}`),
		}, weatherTool, weatherCatalog, toolcall.Meta{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Action != toolcall.ActionReject || res.ReasonCode != toolcall.ReasonUnrepairable {
			t.Fatalf("got action=%v reason=%q, want reject / unrepairable", res.Action, res.ReasonCode)
		}
	})

	t.Run("schema_limit_max_schema_bytes_enforced", func(t *testing.T) {
		t.Parallel()
		pad := strings.Repeat("a", 550)
		oversizedSchema := json.RawMessage(fmt.Sprintf(`{"type":"object","description":%q,"properties":{"location":{"type":"string"}},"required":["location"]}`, pad))
		largeTool := aipapi.ToolDef{Name: "get_weather", Parameters: oversizedSchema}

		res, err := fin.Finalize(ctx, toolcall.CompletedCall{
			ToolCallID: "c4",
			ToolName:   "get_weather",
			ArgsJSON:   []byte(`{"location":"NYC"}`),
		}, largeTool, []aipapi.ToolDef{largeTool}, toolcall.Meta{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Action != toolcall.ActionReject || res.ReasonCode != toolcall.ReasonSchemaInvalid {
			t.Fatalf("got action=%v reason=%q, want reject / schema_invalid", res.Action, res.ReasonCode)
		}
	})

	t.Run("max_args_bytes_enforced", func(t *testing.T) {
		t.Parallel()
		hugeVal := strings.Repeat("x", 9000)
		hugeArgs := fmt.Appendf(nil, `{"location":%q}`, hugeVal)

		res, err := fin.Finalize(ctx, toolcall.CompletedCall{
			ToolCallID: "c5",
			ToolName:   "get_weather",
			ArgsJSON:   hugeArgs,
		}, weatherTool, weatherCatalog, toolcall.Meta{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Action != toolcall.ActionReject || res.ReasonCode != toolcall.ReasonArgsTooLarge {
			t.Fatalf("got action=%v reason=%q, want reject / args_too_large", res.Action, res.ReasonCode)
		}
	})
}

func TestFeatureBundle_ValidationAndErrorAttribution(t *testing.T) {
	t.Parallel()
	badOrder := -10
	cases := []struct {
		name string
		cfg  toolcallrepair.Config
	}{
		{
			name: "invalid_mode",
			cfg: toolcallrepair.Config{
				Mode: "aggressive",
			},
		},
		{
			name: "invalid_on_unrepairable",
			cfg: toolcallrepair.Config{
				OnUnrepairable: "panic",
			},
		},
		{
			name: "negative_max_args_bytes",
			cfg: toolcallrepair.Config{
				MaxArgsBytes: -1,
			},
		},
		{
			name: "too_large_max_args_bytes",
			cfg: toolcallrepair.Config{
				MaxArgsBytes: aipapi.MaxEventDeltaBytes + 1,
			},
		},
		{
			name: "negative_order",
			cfg: toolcallrepair.Config{
				Order: &badOrder,
			},
		},
		{
			name: "schema_negative_limit",
			cfg: toolcallrepair.Config{
				Schema: toolcallrepair.SchemaConfig{
					MaxSchemaBytes: -1,
				},
			},
		},
		{
			name: "schema_exceeding_cap",
			cfg: toolcallrepair.Config{
				Schema: toolcallrepair.SchemaConfig{
					MaxSchemaBytes: toolcallrepair.MaxSchemaBytesCap + 1,
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := toolcallrepair.FeatureBundle(tc.cfg)
			if err == nil {
				t.Fatal("expected error for invalid config")
			}
			expectedPrefix := toolcallrepair.ID + ": "
			if !strings.HasPrefix(err.Error(), expectedPrefix) {
				t.Fatalf("error %q does not have expected attribution prefix %q", err.Error(), expectedPrefix)
			}
		})
	}
}
