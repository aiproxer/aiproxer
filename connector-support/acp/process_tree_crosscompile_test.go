package acp_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestProcessTree_CrossCompileSources compiles process-tree OS files for
// linux/darwin/windows. Cross-compile is not a substitute for native Darwin
// runtime proof (see phase6-task63-macos-process-tree-blocker.md).
func TestProcessTree_CrossCompileSources(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	targets := []struct{ goos, goarch string }{
		{"linux", "amd64"},
		{"darwin", "amd64"},
		{"darwin", "arm64"},
		{"windows", "amd64"},
	}
	for _, pair := range targets {
		if pair.goos == runtime.GOOS && pair.goarch == runtime.GOARCH {
			continue
		}
		out := filepath.Join(dir, pair.goos+"_"+pair.goarch+".test")
		cmd := exec.Command("go", "test", "-c", "-o", out, ".")
		cmd.Env = append(os.Environ(), "GOOS="+pair.goos, "GOARCH="+pair.goarch, "CGO_ENABLED=0", "GOWORK=off")
		combined, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cross-compile %s/%s: %v\n%s", pair.goos, pair.goarch, err, combined)
		}
	}
}
