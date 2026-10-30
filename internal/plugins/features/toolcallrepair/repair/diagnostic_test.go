package repair_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func TestDiagnosticEvent_AttrsOmitRawPayloads(t *testing.T) {
	t.Parallel()
	secretArgs := `{"location":"NYC","token":"super-secret"}`
	ev := repair.DiagnosticEvent{
		TraceID:       "tr-1",
		BLegID:        "b-1",
		ToolNameHash:  "abc",
		CatalogName:   "get_weather",
		SchemaDigest:  "sha256:deadbeef",
		ArgsByteCount: len(secretArgs),
		Action:        "rewrite",
		ReasonCode:    toolcall.ReasonSyntaxRepaired,
	}
	attrs := ev.Attrs()
	if attrs == nil {
		t.Fatal("Attrs() must return a non-nil safe attribute map")
	}
	forbidden := []string{"args", "args_json", "arguments", "raw_args", "schema", "schema_body", "parameters", "payload", "tool_arguments"}
	for _, key := range forbidden {
		if _, ok := attrs[key]; ok {
			t.Fatalf("diagnostics must not expose raw payload key %q", key)
		}
	}
	encoded := strings.ToLower(attrsString(attrs))
	if strings.Contains(encoded, "super-secret") || strings.Contains(encoded, secretArgs) {
		t.Fatal("diagnostics leaked raw argument value")
	}
	for _, key := range []string{"trace_id", "b_leg_id", "args_byte_count", "action", "reason_code"} {
		if _, ok := attrs[key]; !ok {
			t.Fatalf("missing required safe attr %q", key)
		}
	}
}

func attrsString(attrs map[string]any) string {
	var b strings.Builder
	for k, v := range attrs {
		b.WriteString(k)
		b.WriteByte('=')
		if s, ok := v.(string); ok {
			b.WriteString(s)
		}
		b.WriteByte(';')
	}
	return b.String()
}
