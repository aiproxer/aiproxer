package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

// optionalConnectorKinds are the optional backends served through external
// connector modules rather than the essential/standard static bundles.
var optionalConnectorKinds = []string{
	"openrouter", "nvidia", "huggingface",
	"ollama", "ollama-cloud",
	"llamacpp", "lmstudio", "vllm",
}

func TestExternalConnectorModulesPresent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, name := range []string{"openrouter", "nvidia", "huggingface", "ollama", "llamacpp", "lmstudio", "vllm"} {
		mod := filepath.Join(root, "connectors", name, "go.mod")
		if _, err := os.Stat(mod); err != nil {
			t.Fatalf("missing %s: %v", mod, err)
		}
		rel := filepath.Join(root, "connectors", name, "release.yaml")
		if _, err := os.Stat(rel); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	support := filepath.Join(root, "connector-support", "openaicompat", "go.mod")
	b, err := os.ReadFile(support)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "module github.com/aiproxer/aiproxer/connector-support/openaicompat") {
		t.Fatalf("unexpected support module: %s", support)
	}
	if strings.Contains(text, "internal/") {
		t.Fatal("openaicompat support go.mod must not reference internal/")
	}
}

// TestDefaultWireModelOmitsOptionalKinds proves an optional kind has no
// canonical default model. The generic placeholder the configuration layer used
// to synthesize for such a kind is not a model id, so no route is derived from
// it: the configuration names the route explicitly.
func TestDefaultWireModelOmitsOptionalKinds(t *testing.T) {
	t.Parallel()
	for _, kind := range optionalConnectorKinds {
		got := standardplugins.DefaultWireModel(kind)
		if got != "" {
			t.Fatalf("DefaultWireModel(%q)=%q want no canonical default model", kind, got)
		}
	}
}

func TestNonOpenRouterConnectorsOmitOpenRouterKeys(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, name := range []string{"nvidia", "huggingface", "ollama", "llamacpp", "lmstudio", "vllm"} {
		dir := filepath.Join(root, "connectors", name, "internal", "service")
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "openrouter.") {
				t.Fatalf("%s/%s contains openrouter.* coupling", name, e.Name())
			}
		}
	}
}

func TestOpenRouter_supportHasNoProviderCouplingInSupportModule(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "connector-support", "openaicompat")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"openrouter", "nvidia", "huggingface", "ollama", "llamacpp", "lmstudio", "vllm", "internal/core/", "internal/plugins/"}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(b))
		for _, f := range forbidden {
			if strings.Contains(lower, strings.ToLower(f)) {
				t.Fatalf("%s contains forbidden %q", e.Name(), f)
			}
		}
	}
}
