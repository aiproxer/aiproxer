package standardplugins

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"gopkg.in/yaml.v3"
)

func testRegistryWithStdBundle(t *testing.T) *pluginreg.Registry {
	t.Helper()
	r := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(r, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStandardBundle_buildsPreRequestPolicyHandlers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.md"), []byte("classify the request"), 0o600); err != nil {
		t.Fatal(err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal(fmt.Appendf(nil, `
prompt_dir: %q
handlers:
  - id: compliance
    priority: 3
    prompt_filename: policy.md
    model_routing_string: local:policy
    deny_pattern: DENY
`, dir), &n); err != nil {
		t.Fatal(err)
	}
	reg := testRegistryWithStdBundle(t)
	b, err := reg.BuildFeatureBundle("pre-request-policy", n)
	if err != nil {
		t.Fatal(err)
	}
	handlers := aipfeature.Get(b.PlaneSet, aipfeature.PlanePreRequestHandlers)
	if len(handlers) != 1 {
		t.Fatalf("pre-request handlers: %d", len(handlers))
	}
	if handlers[0].ID() != "compliance" {
		t.Fatalf("handler id: %q", handlers[0].ID())
	}
}

func TestStandardBundle_buildsSecretsGuard(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("action: block"), &n); err != nil {
		t.Fatal(err)
	}
	reg := testRegistryWithStdBundle(t)
	b, err := reg.BuildFeatureBundle("secrets-guard", n)
	if err != nil {
		t.Fatal(err)
	}
	guards := aipfeature.Get(b.PlaneSet, aipfeature.PlaneSecretGuards)
	if len(guards) != 1 {
		t.Fatalf("secret guards: %d", len(guards))
	}
	if guards[0].ID() != "secrets-guard" {
		t.Fatalf("guard id: %q", guards[0].ID())
	}
}

func TestStandardBundle_registersCompactionContinuity(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &n); err != nil {
		t.Fatal(err)
	}
	b, err := testRegistryWithStdBundle(t).BuildFeatureBundle("compaction-continuity", n)
	if err != nil {
		t.Fatalf("BuildFeatureBundle: %v", err)
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("FeatureBundle.Validate: %v", err)
	}
	if preservers := aipfeature.Get(b.PlaneSet, aipfeature.PlaneCompactionPreservers); len(preservers) != 0 {
		t.Fatalf("composition slice must remain no-op before semantic task: %d", len(preservers))
	}
}

func TestRequireEmptyFeatureYAML_acceptsEmptyMapping(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &n); err != nil {
		t.Fatal(err)
	}
	if err := requireEmptyFeatureYAML("parts-noop", n); err != nil {
		t.Fatal(err)
	}
}

func TestRequireEmptyFeatureYAML_rejectsUnknownKey(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("unexpected: true"), &n); err != nil {
		t.Fatal(err)
	}
	if err := requireEmptyFeatureYAML("parts-noop", n); err == nil {
		t.Fatal("expected error")
	}
}
