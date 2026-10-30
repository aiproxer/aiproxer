package knowledge_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("go.mod not found")
	return ""
}

func TestKnowledge_AuthoritativeSourcesLinkADR0008(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	paths := []string{
		"AGENTS.md",
		".kiro/steering/structure.md",
		".kiro/steering/tech.md",
		"docs/architecture.md",
	}
	for _, rel := range paths {
		body := read(t, filepath.Join(root, filepath.FromSlash(rel)))
		if !strings.Contains(body, "0008") {
			t.Errorf("%s must reference ADR 0008", rel)
		}
		if strings.Contains(body, "internal/plugins/backends/openrouter/") ||
			strings.Contains(body, "internal/plugins/backends/openaicodex/") ||
			strings.Contains(body, "internal/core/codexcatalog") {
			t.Errorf("%s still references migrated in-tree optional paths", rel)
		}
	}
}

func TestKnowledge_SteeringAlignsWithHybridConnectors(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	structure := strings.ToLower(read(t, filepath.Join(root, ".kiro", "steering", "structure.md")))
	tech := strings.ToLower(read(t, filepath.Join(root, ".kiro", "steering", "tech.md")))
	for _, body := range []string{structure, tech} {
		if !strings.Contains(body, "connectors/") {
			t.Error("steering must mention connectors/")
		}
		if !strings.Contains(body, "hybrid") {
			t.Error("steering must describe hybrid composition")
		}
	}
	if strings.Contains(structure, "add optional connectors to a fixed table") {
		t.Error("structure must not instruct fixed-table optional registration")
	}
}

func TestKnowledge_OperatorGuideLinked(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	readme := read(t, filepath.Join(root, "README.md"))
	if !strings.Contains(readme, "docs/backend-plugins/operator.md") {
		t.Fatal("README must link docs/backend-plugins/operator.md")
	}
	op := filepath.Join(root, "docs", "backend-plugins", "operator.md")
	if _, err := os.Stat(op); err != nil {
		t.Fatalf("operator.md missing: %v", err)
	}
	threat := filepath.Join(root, "docs", "backend-plugins", "threat-model.md")
	if _, err := os.Stat(threat); err != nil {
		t.Fatalf("threat-model.md missing: %v", err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
