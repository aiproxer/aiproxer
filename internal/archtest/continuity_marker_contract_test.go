package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexContinuityMarkerContractIsPinnedAcrossModules(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	paths := []string{
		filepath.Join(root, "internal", "standardplugins", "reasoning_preservation_inject.go"),
		filepath.Join(root, "connectors", "codex", "internal", "codex", "native_context_marker.go"),
	}
	wantKey := `"aip.internal.openai_codex.reasoning_continuity.v1"`
	wantValue := "`{\"eligible\":true,\"dialect\":\"openai.responses.reasoning_item.v1\"}`"
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		if strings.Count(src, wantKey) != 1 || strings.Count(src, wantValue) != 1 {
			t.Fatalf("%s must contain each marker literal exactly once", filepath.ToSlash(path))
		}
	}
}
