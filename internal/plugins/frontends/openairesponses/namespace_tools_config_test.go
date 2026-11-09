package openairesponses_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"gopkg.in/yaml.v3"
)

func TestDecodeConfig_namespaceToolsPolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "default", raw: `{}`, want: ""},
		{name: "project", raw: `namespace_tools: project`, want: "project"},
		{name: "reject", raw: `namespace_tools: reject`, want: "reject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var n yaml.Node
			if err := yaml.Unmarshal([]byte(tc.raw), &n); err != nil {
				t.Fatal(err)
			}
			cfg, err := openairesponses.DecodeConfig(n)
			if err != nil {
				t.Fatalf("decode config: %v", err)
			}
			if cfg.NamespaceTools != tc.want {
				t.Fatalf("namespace_tools = %q, want %q", cfg.NamespaceTools, tc.want)
			}
		})
	}
}

func TestDecodeConfig_namespaceToolsUnknownValueRejected(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`namespace_tools: sometimes`), &n); err != nil {
		t.Fatal(err)
	}
	_, err := openairesponses.DecodeConfig(n)
	if err == nil {
		t.Fatal("an unknown namespace_tools value must fail config decoding")
	}
	if !strings.Contains(err.Error(), "namespace_tools") {
		t.Fatalf("config refusal must name the key: %v", err)
	}
}
