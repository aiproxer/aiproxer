package standardplugins_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

// TestDefaultWireModel_builtinKinds proves the bundled builtin families keep
// their canonical default model, which is what the default route of a
// configuration without an explicit routing.default_route is built from.
func TestDefaultWireModel_builtinKinds(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"openai-responses": "gpt-4o-mini",
		"openai-legacy":    "gpt-4o-mini",
		"anthropic":        "claude-3-5-haiku-20241022",
		"gemini":           "gemini-2.0-flash",
		"bedrock":          "anthropic.claude-3-haiku-20240307-v1:0",
	}
	for kind, want := range cases {
		if got := standardplugins.DefaultWireModel(kind); got != want {
			t.Fatalf("DefaultWireModel(%q)=%q want %q", kind, got, want)
		}
	}
}

// TestDefaultWireModel_unknownKindHasNoDefault proves a kind without a
// canonical default model returns no model at all. The configuration layer used
// to substitute the literal placeholder "model" here, which no backend serves:
// every request routed to it was refused as an unknown model.
func TestDefaultWireModel_unknownKindHasNoDefault(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"claudeagentacp", "codex", "opencode", "cursorsdk", "", "  "} {
		if got := standardplugins.DefaultWireModel(kind); got != "" {
			t.Fatalf("DefaultWireModel(%q)=%q want no canonical default model", kind, got)
		}
	}
}
