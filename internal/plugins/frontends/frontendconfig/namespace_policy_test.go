package frontendconfig

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestDecode_NamespaceToolsKeyIsFrontendSpecific pins that the shared decoder
// keeps rejecting the OpenAI Responses namespace policy key for frontends that
// do not implement it: an accepted-but-ignored config knob is hidden
// degradation for the operator.
func TestDecode_NamespaceToolsKeyIsFrontendSpecific(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`namespace_tools: reject`), &n); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(n, "openai-legacy"); err == nil || !strings.Contains(err.Error(), `unknown config key "namespace_tools"`) {
		t.Fatalf("a non-Responses frontend must reject namespace_tools, got %v", err)
	}
	cfg, err := DecodeWithToolNamespacePolicy(n, "openai-responses")
	if err != nil {
		t.Fatalf("the Responses frontend must accept namespace_tools: %v", err)
	}
	if cfg.NamespaceTools != "reject" {
		t.Fatalf("namespace_tools = %q, want reject", cfg.NamespaceTools)
	}
}

func TestDecodeWithToolNamespacePolicy_UnknownValueRejected(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`namespace_tools: sometimes`), &n); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeWithToolNamespacePolicy(n, "openai-responses"); err == nil {
		t.Fatal("an unknown namespace_tools value must fail decoding")
	}
}
