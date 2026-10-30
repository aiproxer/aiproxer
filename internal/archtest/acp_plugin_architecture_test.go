package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestACP_supportModuleExistsIndependently(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	mod := filepath.Join(root, "connector-support", "acp", "go.mod")
	b, err := os.ReadFile(mod)
	if err != nil {
		t.Fatalf("connector-support/acp/go.mod: %v", err)
	}
	text := string(b)
	if !strings.Contains(text, "module github.com/aiproxer/aiproxer/connector-support/acp") {
		t.Fatalf("unexpected module path in %s", mod)
	}
	if strings.Contains(text, "internal/") {
		t.Fatal("support go.mod must not reference internal packages")
	}
}

func TestACP_supportSourceHasNoInternalCoreImports(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "connector-support", "acp")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"internal/core/",
		"internal/infra/",
		"internal/pluginreg",
		"internal/plugins/backends/cursorcliacp",
		"internal/plugins/backends/geminicliacp",
		"internal/plugins/backends/agycliacp",
		"routing.AttemptCandidate",
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		for _, f := range forbidden {
			if strings.Contains(text, f) {
				t.Fatalf("%s contains forbidden %q", e.Name(), f)
			}
		}
		for _, product := range []string{"cursorcliacp", "geminicliacp", "agycliacp", "CURSOR_AGENT"} {
			if strings.Contains(text, `"`+product+`"`) && strings.Contains(e.Name(), "doc.go") {
				continue
			}
		}
	}
}

func TestACP_productPackagesRemovedFromRoot(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		"internal/plugins/backends/cursorcliacp",
		"internal/plugins/backends/geminicliacp",
		"internal/plugins/backends/agycliacp",
	} {
		_, err := os.Stat(filepath.Join(root, rel))
		if err == nil {
			t.Fatalf("%s must be deleted after Phase 6 cutover", rel)
		} else if !os.IsNotExist(err) {
			t.Fatalf("unexpected error statting %s: %v", rel, err)
		}
	}
}

func TestACP_externalConnectorModulesPresent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, name := range []string{"acp", "cursorcliacp", "geminicliacp", "agycliacp", "claudeagentacp"} {
		mod := filepath.Join(root, "connectors", name, "go.mod")
		if _, err := os.Stat(mod); err != nil {
			t.Fatalf("missing %s: %v", mod, err)
		}
		rel := filepath.Join(root, "connectors", name, "release.yaml")
		if _, err := os.Stat(rel); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
}

func TestACP_internalBackendPackageDeleted(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	rel := "internal/plugins/backends/acp"
	_, err := os.Stat(filepath.Join(root, rel))
	if err == nil {
		t.Fatalf("%s must be deleted after ACP deduplication", rel)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error statting %s: %v", rel, err)
	}
}
