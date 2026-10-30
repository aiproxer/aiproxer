package runtimebundle

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stagingCacheRoot is the package TestMain-owned root under which every
// connector build output (aip-connector-build-*) and staged discovery root
// (aip-staged-root-*) is created. Removing it after m.Run cleans all cache
// entries that previously leaked directly under os.TempDir.
var stagingCacheRoot string

// TestMain owns one package-level cache root (mirroring the processhost
// windows fixture TestMain pattern) so connector binaries are built once per
// test binary while every cache entry stays contained under this root. The
// root is removed after the suite with removeAllRetry so Windows can release
// any handles before deletion.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "aip-runtimebundle-cache-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "runtimebundle staging cache root:", err)
		os.Exit(1)
	}
	stagingCacheRoot = dir
	code := m.Run()
	// A wider retry window than the production default absorbs a lagging
	// connector-process teardown that can briefly hold a cache binary open on
	// Windows after the last test returns.
	_ = removeAllRetry(dir, 20, 50*time.Millisecond)
	os.Exit(code)
}

// underStagingCacheRoot reports whether path resolves strictly inside the
// package cache root (never the root itself and never an escape).
func underStagingCacheRoot(tb testing.TB, path string) bool {
	tb.Helper()
	if strings.TrimSpace(path) == "" {
		return false
	}
	rel, err := filepath.Rel(stagingCacheRoot, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	if filepath.IsAbs(rel) {
		return false
	}
	return true
}

// TestStagingCache_EntriesContainedUnderPackageRoot proves every connector
// build output and staged discovery root created by the shared cache helpers
// lives under the single TestMain-owned root, and that the root is non-empty
// after cache writes (so the post-suite cleanup is meaningful).
func TestStagingCache_EntriesContainedUnderPackageRoot(t *testing.T) {
	t.Parallel()
	bin := cachedBuiltBinary(t, specLocalStub)
	root := sharedStagedRoot(t, "localstub", specLocalStub, localStubManifest)
	if !underStagingCacheRoot(t, bin.path) {
		t.Fatalf("connector build output %q must live under package cache root %q", bin.path, stagingCacheRoot)
	}
	if !underStagingCacheRoot(t, root) {
		t.Fatalf("staged discovery root %q must live under package cache root %q", root, stagingCacheRoot)
	}
	entries, err := os.ReadDir(stagingCacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("package cache root must be non-empty after cache writes")
	}
}

// TestStagingCache_CleanupRemovesNonEmptyRoot proves the removal mechanism the
// TestMain uses (removeAllRetry) deletes a non-empty nested tree, which is the
// precondition for reclaiming the aip-connector-build-* / aip-staged-root-*
// entries after m.Run.
func TestStagingCache_CleanupRemovesNonEmptyRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	deep := filepath.Join(root, "sub", "deep")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "top.bin"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeAllRetry(root, 8, 25*time.Millisecond); err != nil {
		t.Fatalf("removeAllRetry(non-empty root): %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("root %q still present after cleanup", root)
	}
}
