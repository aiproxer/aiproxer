package archtest

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

// Essential-only backends: optional connectors must stay out of the
// essential/standard_table fixed tables and reach the runtime through
// manifests only (AGENTS.md hybrid-backends guardrail).

func TestEssentialOnly_StandardBackendBundleIsEssentialOnly(t *testing.T) {
	t.Parallel()
	keys := standardplugins.UpstreamAPIKeys{}
	got := idsOf(standardplugins.StandardBackendBundle(keys))
	want := append([]string(nil), standardplugins.EssentialBackendKinds()...)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("StandardBackendBundle=%v want essential-only %v", got, want)
	}
	for _, id := range got {
		if !standardplugins.IsEssentialBackendKind(id) {
			t.Fatalf("non-essential kind %q remains in StandardBackendBundle", id)
		}
	}
}

func TestEssentialOnly_AllowlistExact(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	ids := reg.BackendFactoryIDs()
	want := append([]string(nil), standardplugins.EssentialBackendKinds()...)
	slices.Sort(ids)
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("InstallStandardBackendsOn=%v want essential-only %v", ids, want)
	}
}

func TestDynamic_NoOptionalKindNamesInGenericPluginreg(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "pluginreg")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	forbiddenLits := []string{
		`"openrouter"`, `"opencode"`, `"opencode-go"`, `"opencode-zen"`,
		`"openai-codex"`, `"openai-codex-app-server"`, `"ollama"`, `"cursorcliacp"`,
		`"huggingface"`, `"llamacpp"`, `"lmstudio"`, `"nvidia"`, `"vllm"`,
		`"geminicliacp"`, `"agycliacp"`, `"acp"`,
	}
	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			for _, bad := range forbiddenLits {
				if lit.Value == bad {
					t.Errorf("pluginreg/%s contains optional kind literal %s", ent.Name(), bad)
				}
			}
			return true
		})
	}
}

func TestDynamic_OptionalConnectorManifestsDoNotRequireRootGoEdit(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	connectorsRoot := filepath.Join(root, "connectors")
	ents, err := os.ReadDir(connectorsRoot)
	if err != nil {
		t.Fatal(err)
	}
	essential := map[string]struct{}{}
	for _, k := range standardplugins.EssentialBackendKinds() {
		essential[k] = struct{}{}
	}
	bundleIDs := map[string]struct{}{}
	for _, id := range idsOf(standardplugins.StandardBackendBundle(standardplugins.UpstreamAPIKeys{})) {
		bundleIDs[id] = struct{}{}
	}
	var optionalKinds []string
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
			kind := strings.TrimSpace(exp.Kind)
			if kind == "" {
				continue
			}
			if _, ok := essential[kind]; ok {
				t.Fatalf("connector manifest %s exports essential kind %q", manifestPath, kind)
			}
			if _, ok := bundleIDs[kind]; ok {
				t.Fatalf("optional kind %q from %s is still statically registered in StandardBackendBundle", kind, manifestPath)
			}
			optionalKinds = append(optionalKinds, kind)
		}
	}
	if len(optionalKinds) == 0 {
		t.Fatal("expected at least one optional connector manifest export under connectors/*/manifest")
	}
	// Wire-model defaults must not keep special cases for optional kinds.
	// Identity YAML keys (e.g. openrouter attribution) are wire-compat and out of scope.
	wireModel := filepath.Join(root, "internal", "standardplugins", "default_wire_model.go")
	raw, err := os.ReadFile(wireModel)
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, kind := range optionalKinds {
		needle := `case "` + kind + `"`
		if strings.Contains(src, needle) {
			t.Errorf("default_wire_model.go still has optional kind switch %s; optional kinds must be manifest-only", needle)
		}
	}
}

func idsOf(b standardplugins.Bundle) []string {
	out := make([]string, 0, len(b.Backends))
	for _, e := range b.Backends {
		out = append(out, e.ID)
	}
	return out
}
