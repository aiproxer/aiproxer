package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveConfigPath_relativeUnderCwd(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	const name = "aip-test-config.yaml"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveConfigPath(name)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, name) {
		t.Fatalf("unexpected resolved path %q", got)
	}
}

func TestResolveConfigPath_empty(t *testing.T) {
	t.Parallel()
	_, err := resolveConfigPath("   ")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestResolveConfigPath_parentSegmentsReachRepo(t *testing.T) {
	t.Parallel()
	// go test sets the working directory to this package's source dir; match loader_test bootstrap path.
	got, err := resolveConfigPath(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(filepath.ToSlash(got), "config/config.yaml") {
		t.Fatalf("unexpected resolved path %q", got)
	}
}
