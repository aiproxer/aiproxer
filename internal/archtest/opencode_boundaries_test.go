package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeConnectorDoesNotImportConcreteProviderBackends(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "connectors", "opencode")
	forbidden := []string{
		"/internal/plugins/backends/anthropic",
		"/internal/plugins/backends/gemini",
		"/internal/plugins/backends/openrouter",
		"/internal/plugins/backends/nvidia",
	}
	var walk func(string) error
	walk = func(path string) error {
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, e := range entries {
			p := filepath.Join(path, e.Name())
			if e.IsDir() {
				if err := walk(p); err != nil {
					return err
				}
				continue
			}
			if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			text := string(b)
			for _, f := range forbidden {
				if strings.Contains(text, f) {
					t.Fatalf("%s must not import %q", p, f)
				}
			}
		}
		return nil
	}
	if err := walk(dir); err != nil {
		t.Fatal(err)
	}
}
