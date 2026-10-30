//go:build integration

package tools_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// toolsCacheRoot is the package TestMain-owned root under which every built
// tool binary directory (aip-tools-bin-*) is created. Removing it after
// m.Run reclaims the cache entries that previously leaked directly under
// os.TempDir. The root is created lazily on the first getToolExe call so helper
// child processes spawned by bounded_orchestration tests (which re-execute this
// test binary but never build tools) never leave an empty root behind when they
// exit via os.Exit or are killed before TestMain cleanup.
var toolsCacheRoot string

// TestMain owns cleanup of the one package-level cache root after the suite,
// retrying with removeAllRetry so Windows can release any handles before
// deletion.
func TestMain(m *testing.M) {
	code := m.Run()
	if toolsCacheRoot != "" {
		// A wider retry window absorbs a lagging antivirus/scan handle that can
		// briefly hold a built tool .exe open on Windows after the last test.
		_ = removeAllRetry(toolsCacheRoot, 20, 50*time.Millisecond)
	}
	os.Exit(code)
}

// removeAllRetry deletes path, retrying briefly so Windows releases built tool
// binaries after the last test returns.
func removeAllRetry(path string, attempts int, delay time.Duration) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if attempts < 1 {
		attempts = 1
	}
	var last error
	for i := 0; i < attempts; i++ {
		last = os.RemoveAll(path)
		if last == nil {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return nil
			}
			last = fmt.Errorf("backendplugin tools cache: %q still present after RemoveAll", path)
		}
		if i+1 < attempts && delay > 0 {
			time.Sleep(delay)
		}
	}
	return last
}

// TestToolsBinaries_ContainedUnderPackageRoot proves every built tool binary
// lives under the single TestMain-owned root (never directly under
// os.TempDir), and that the root is non-empty after builds so the post-suite
// cleanup is meaningful.
func TestToolsBinaries_ContainedUnderPackageRoot(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"discover_modules", "package_plugins", "crossplatform_qa", "release_gates"} {
		exe := getToolExe(t, "./tools/backendplugin/"+tool)
		rel, err := filepath.Rel(toolsCacheRoot, exe)
		if err != nil {
			t.Fatal(err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			t.Fatalf("tool binary %q must live under package cache root %q", exe, toolsCacheRoot)
		}
	}
	entries, err := os.ReadDir(toolsCacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("tools cache root must be non-empty after binary builds")
	}
}

// TestToolsBinaries_CleanupRemovesNonEmptyRoot proves the removal mechanism
// the TestMain uses (removeAllRetry) deletes a non-empty nested tree, which is
// the precondition for reclaiming the aip-tools-bin-* entries after m.Run.
func TestToolsBinaries_CleanupRemovesNonEmptyRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	deep := filepath.Join(root, "sub")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "tool.bin"), []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := removeAllRetry(root, 20, 5*time.Millisecond); err != nil {
		t.Fatalf("removeAllRetry(non-empty root): %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("root %q still present after cleanup", root)
	}
}
