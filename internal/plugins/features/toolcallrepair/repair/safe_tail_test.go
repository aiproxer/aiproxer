package repair_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func TestEngine_TerminalCommaRepairsCompleteValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		args   string
		want   string
		schema string
	}{
		{
			name: "object",
			args: `{"a":1,`,
			want: `{"a":1}`,
		},
		{
			name: "array",
			args: `[1,2,`,
			want: `[1,2]`,
		},
		{
			name: "nested",
			args: `{"outer":{"items":[1,2,`,
			want: `{"outer":{"items":[1,2]}}`,
		},
		{
			name: "whitespace_preserved",
			args: "{\"a\":1,   ",
			want: "{\"a\":1   }",
		},
		{
			name: "comma_inside_string",
			args: `{"a":"x,`,
			want: `{"a":"x,"}`,
		},
		{
			name:   "schema_validated",
			args:   `{"a":1,`,
			want:   `{"a":1}`,
			schema: `{"type":"object","properties":{"a":{"type":"integer"}},"required":["a"],"additionalProperties":false}`,
		},
		{
			name:   "reason_precedence_after_schema_fill",
			args:   `{"a":1,`,
			want:   `{"a":1,"b":"x"}`,
			schema: `{"type":"object","properties":{"a":{"type":"integer"},"b":{"const":"x"}},"required":["a","b"],"additionalProperties":false}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schema := tc.schema
			var raw json.RawMessage
			if schema != "" {
				raw = json.RawMessage(schema)
			}
			args := []byte(tc.args)
			out, err := repair.NewEngine().Repair(repair.Input{
				ToolName: "run",
				ArgsJSON: args,
				Catalog:  []aipapi.ToolDef{{Name: "run", Parameters: raw}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if out.Kind != repair.OutcomeRewrite || out.ReasonCode != toolcall.ReasonSyntaxRepaired {
				t.Fatalf("kind=%v reason=%q want syntax rewrite", out.Kind, out.ReasonCode)
			}
			if !bytes.Equal(out.ArgsJSON, []byte(tc.want)) {
				t.Fatalf("args=%q want %q", out.ArgsJSON, tc.want)
			}
			if !json.Valid(out.ArgsJSON) {
				t.Fatal("candidate must be valid JSON")
			}
		})
	}
}

func TestEngine_TerminalCommaRefusesUnsafeShapes(t *testing.T) {
	t.Parallel()
	for _, args := range []string{
		`{,`, `[,]`, `[1,,`, `{"a":,`, `{"a":tru,`, `{"a":1,}x`, `[1]x`, `[1,2]x`,
	} {
		t.Run(args, func(t *testing.T) {
			t.Parallel()
			original := []byte(args)
			out, err := repair.NewEngine().Repair(repair.Input{
				ToolName: "run",
				ArgsJSON: original,
				Catalog:  []aipapi.ToolDef{{Name: "run"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if out.Kind != repair.OutcomeUnrepairable {
				t.Fatalf("kind=%v want unrepairable args=%q", out.Kind, args)
			}
			if out.ArgsJSON == nil || !bytes.Equal(out.ArgsJSON, original) {
				t.Fatalf("failure must preserve original args: %q", out.ArgsJSON)
			}
		})
	}
}

func TestEngine_PendingRootValueUsesDeterministicSchemaSources(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		schema string
		want   string
		reason string
	}{
		{
			name:   "const",
			schema: `{"type":"object","properties":{"mode":{"const":"safe"}},"required":["mode"]}`,
			want:   `{"mode":"safe"}`,
			reason: toolcall.ReasonConstInserted,
		},
		{
			name:   "single_enum",
			schema: `{"type":"object","properties":{"mode":{"enum":["safe"]}},"required":["mode"]}`,
			want:   `{"mode":"safe"}`,
			reason: toolcall.ReasonEnumInserted,
		},
		{
			name:   "default",
			schema: `{"type":"object","properties":{"enabled":{"type":"boolean","default":true}},"required":["enabled"]}`,
			want:   `{"enabled":true}`,
			reason: toolcall.ReasonDefaultInserted,
		},
		{
			name:   "explicit_null_const",
			schema: `{"type":"object","properties":{"value":{"const":null}},"required":["value"]}`,
			want:   `{"value":null}`,
			reason: toolcall.ReasonConstInserted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := repair.NewEngine().Repair(repair.Input{
				ToolName: "run",
				ArgsJSON: []byte(`{"` + map[string]string{"const": "mode", "single_enum": "mode", "default": "enabled", "explicit_null_const": "value"}[tc.name] + `":`),
				Catalog:  []aipapi.ToolDef{{Name: "run", Parameters: json.RawMessage(tc.schema)}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if out.Kind != repair.OutcomeRewrite || out.ReasonCode != tc.reason {
				t.Fatalf("kind=%v reason=%q want %s", out.Kind, out.ReasonCode, tc.reason)
			}
			if string(out.ArgsJSON) != tc.want {
				t.Fatalf("args=%q want %q", out.ArgsJSON, tc.want)
			}
		})
	}
}

func TestEngine_PendingRootValueRefusesInferenceAndSpeculation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		args   string
		schema string
	}{
		{"no_deterministic_value", `{"mode":`, `{"type":"object","properties":{"mode":{"type":"string"}}}`},
		{"nested", `{"outer":{"mode":`, `{"type":"object","properties":{"outer":{"type":"object","properties":{"mode":{"const":"x"}}}}}}`},
		{"misspelled", `{"Mod":`, `{"type":"object","properties":{"mode":{"const":"x"}}}`},
		{"partial_string", `{"mode":"sa`, `{"type":"object","properties":{"mode":{"const":"safe"}}}`},
		{"partial_literal", `{"enabled":t`, `{"type":"object","properties":{"enabled":{"const":true}}}`},
		{"ambiguous_branch", `{"mode":`, `{"type":"object","oneOf":[{"properties":{"mode":{"const":"a"}}},{"properties":{"mode":{"const":"b"}}}]}`},
		{"external_ref", `{"mode":`, `{"type":"object","properties":{"mode":{"$ref":"https://example.invalid/value"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			original := []byte(tc.args)
			out, err := repair.NewEngine().RepairWithContext(context.Background(), repair.Input{
				ToolName: "run",
				ArgsJSON: original,
				Catalog:  []aipapi.ToolDef{{Name: "run", Parameters: json.RawMessage(tc.schema)}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if out.Kind != repair.OutcomeUnrepairable {
				t.Fatalf("kind=%v want unrepairable", out.Kind)
			}
			if out.ArgsJSON == nil || !bytes.Equal(out.ArgsJSON, original) {
				t.Fatalf("original args must be preserved: %q", out.ArgsJSON)
			}
		})
	}
}

func TestEngine_PendingRootValueAcceptsJSONSlashEscapeInPropertyName(t *testing.T) {
	t.Parallel()
	out, err := repair.NewEngine().Repair(repair.Input{
		ToolName: "run",
		ArgsJSON: []byte(`{"a\/b":`),
		Catalog:  []aipapi.ToolDef{{Name: "run", Parameters: json.RawMessage(`{"type":"object","properties":{"a/b":{"const":true}},"required":["a/b"]}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != repair.OutcomeRewrite || out.ReasonCode != toolcall.ReasonConstInserted {
		t.Fatalf("kind=%v reason=%q want const rewrite", out.Kind, out.ReasonCode)
	}
	if string(out.ArgsJSON) != `{"a\/b":true}` {
		t.Fatalf("args=%q want escaped property preserved", out.ArgsJSON)
	}
}

func TestEngine_TailCancellationIsStable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := repair.NewEngine().RepairWithContext(ctx, repair.Input{
		ToolName: "run",
		ArgsJSON: []byte(`{"a":1,`),
		Catalog:  []aipapi.ToolDef{{Name: "run"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != repair.OutcomeUnrepairable || out.ReasonCode != toolcall.ReasonCanceled {
		t.Fatalf("kind=%v reason=%q want canceled", out.Kind, out.ReasonCode)
	}
}
