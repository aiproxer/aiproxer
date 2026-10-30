package archtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"gopkg.in/yaml.v3"
)

var genericCompatibleKinds = []string{
	standardplugins.CustomOpenAILegacyCompatibleID,
	standardplugins.CustomOpenAIResponsesCompatibleID,
	standardplugins.CustomAnthropicCompatibleID,
}

// TestGenericCompatible_remainBuiltIn documents the Task 1.1 built-in contract.
// ExactAllowlist already locks EssentialBackendBundle membership; this probe names
// the three generic kinds explicitly for the active generic-compatible-backend-modes spec.
func TestGenericCompatible_remainBuiltIn(t *testing.T) {
	t.Parallel()
	present := map[string]bool{}
	for _, id := range standardplugins.EssentialBackendKinds() {
		present[id] = true
	}
	for _, e := range standardplugins.EssentialBackendBundle(standardplugins.UpstreamAPIKeys{}).Backends {
		present[e.ID] = true
	}
	for _, kind := range genericCompatibleKinds {
		if !present[kind] {
			t.Fatalf("generic kind %q must remain in EssentialBackendKinds/Bundle", kind)
		}
		if !standardplugins.IsEssentialBackendKind(kind) {
			t.Fatalf("generic kind %q must remain essential/built-in", kind)
		}
	}
}

// TestGenericCompatible_absentFromConnectorManifestsAndHostPackages fails when a
// generic kind is externalized into executable-plugin manifests or host branches.
func TestGenericCompatible_absentFromConnectorManifestsAndHostPackages(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	connectorsRoot := filepath.Join(root, "connectors")
	ents, err := os.ReadDir(connectorsRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		manifestPath := filepath.Join(connectorsRoot, ent.Name(), "manifest", "template.backendplugin.json")
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		var m struct {
			Exports []struct {
				Kind string `json:"kind"`
			} `json:"exports"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v", manifestPath, err)
		}
		for _, exp := range m.Exports {
			for _, kind := range genericCompatibleKinds {
				if strings.TrimSpace(exp.Kind) == kind {
					t.Fatalf("connector manifest %s exports generic compatible kind %q", manifestPath, kind)
				}
			}
		}
		if strings.Contains(string(raw), "custom-openai") || strings.Contains(string(raw), "custom-anthropic") {
			t.Fatalf("connector manifest %s must not mention generic compatible kinds", manifestPath)
		}
	}

	hostDirs := []string{
		filepath.Join(root, "internal", "infra", "backendplugins"),
		filepath.Join(root, "pkg", "aipsdk", "backendplugin"),
	}
	for _, dir := range hostDirs {
		if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				name := info.Name()
				if name == "testdata" || name == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			src := string(raw)
			for _, kind := range genericCompatibleKinds {
				if strings.Contains(src, kind) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s names generic compatible kind %q", filepath.ToSlash(rel), kind)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestGenericCompatible_notNamedByInternalCore fails when production core sources
// hard-code generic compatible factory kinds.
func TestGenericCompatible_notNamedByInternalCore(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	coreRoot := filepath.Join(root, "internal", "core")
	err := filepath.Walk(coreRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		for _, kind := range genericCompatibleKinds {
			if strings.Contains(src, kind) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s names generic compatible kind %q", filepath.ToSlash(rel), kind)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestGenericCompatible_usableInMinimalDistributionWithoutPluginDirectory proves
// all three built-in compatible kinds register and build without optional plugins.
func TestGenericCompatible_usableInMinimalDistributionWithoutPluginDirectory(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range genericCompatibleKinds {
		if !reg.HasBackend(kind) {
			t.Fatalf("kind %q missing from essential registry", kind)
		}
		source, ok := reg.BackendRegistrationSource(kind)
		if !ok || source != pluginreg.BackendSourceBuiltinCompatible {
			t.Fatalf("kind %q source=%q ok=%v", kind, source, ok)
		}
	}
	configs := map[string]string{
		standardplugins.CustomOpenAILegacyCompatibleID: `backend_prefix: min-legacy
base_url: http://127.0.0.1:9/v1
models:
  source: inline
  items:
    - canonical_id: min-legacy/m
      native_id: m
`,
		standardplugins.CustomOpenAIResponsesCompatibleID: `backend_prefix: min-responses
base_url: http://127.0.0.1:9/v1
models:
  source: inline
  items:
    - canonical_id: min-responses/m
      native_id: m
`,
		standardplugins.CustomAnthropicCompatibleID: `backend_prefix: min-anthropic
base_url: http://127.0.0.1:9
models:
  source: inline
  items:
    - canonical_id: min-anthropic/m
      native_id: m
`,
	}
	instanceIDs := map[string]string{
		standardplugins.CustomOpenAILegacyCompatibleID:    "min-legacy",
		standardplugins.CustomOpenAIResponsesCompatibleID: "min-responses",
		standardplugins.CustomAnthropicCompatibleID:       "min-anthropic",
	}
	for kind, raw := range configs {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
			t.Fatalf("%s yaml: %v", kind, err)
		}
		if _, err := reg.BuildBackendWithLifecycle(kind, instanceIDs[kind], node, nil, pluginreg.BackendFactoryDeps{}); err != nil {
			t.Fatalf("BuildBackendWithLifecycle(%s): %v", kind, err)
		}
	}
}
