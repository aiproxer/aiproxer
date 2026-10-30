package archtest

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func setupTestScanRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"cmd/app/main.go":            "package main\nfunc main() {}\n",
		"cmd/app/main_test.go":       "package main\nimport \"testing\"\nfunc TestMain(t *testing.T) {}\n",
		"internal/core/core.go":      "package core\ntype Engine struct{}\n",
		"internal/core/core_test.go": "package core\nimport \"testing\"\nfunc TestEngine(t *testing.T) {}\n",
		"pkg/api/api.go":             "package api\ntype API interface{}\n",
		"pkg/api/api_test.go":        "package api\nimport \"testing\"\nfunc TestAPI(t *testing.T) {}\n",
		"internal/vendor/vend.go":    "package vendor\n",
		"pkg/testdata/fixture.go":    "package testdata\n",
		"cmd/node_modules/mod.go":    "package mod\n",
	}
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

func TestSourceScanCache_ProductionVsIncludeTestsPartition(t *testing.T) {
	t.Parallel()
	root := setupTestScanRepo(t)

	var prodFiles, allFiles, aliasFiles []string
	if err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		prodFiles = append(prodFiles, rel)
		return nil
	}); err != nil {
		t.Fatalf("WalkProductionGoFiles error: %v", err)
	}

	wantProd := []string{"cmd/app/main.go", "internal/core/core.go", "pkg/api/api.go"}
	if strings.Join(prodFiles, ",") != strings.Join(wantProd, ",") {
		t.Fatalf("WalkProductionGoFiles = %v, want %v", prodFiles, wantProd)
	}

	if err := WalkGoFiles(root, func(rel, abs string, src []byte) error {
		allFiles = append(allFiles, rel)
		return nil
	}); err != nil {
		t.Fatalf("WalkGoFiles error: %v", err)
	}

	wantAll := []string{
		"cmd/app/main.go", "cmd/app/main_test.go",
		"internal/core/core.go", "internal/core/core_test.go",
		"pkg/api/api.go", "pkg/api/api_test.go",
	}
	if strings.Join(allFiles, ",") != strings.Join(wantAll, ",") {
		t.Fatalf("WalkGoFiles = %v, want %v", allFiles, wantAll)
	}

	if err := WalkAllGoFiles(root, func(rel, abs string, src []byte) error {
		aliasFiles = append(aliasFiles, rel)
		return nil
	}); err != nil {
		t.Fatalf("WalkAllGoFiles error: %v", err)
	}
	if len(aliasFiles) != len(wantAll) {
		t.Fatalf("WalkAllGoFiles want %d files, got %d", len(wantAll), len(aliasFiles))
	}
}

func TestSourceScanCache_SnapshotReuse(t *testing.T) {
	t.Parallel()
	root := setupTestScanRepo(t)

	firstMap := make(map[string]string)
	if err := WalkGoFiles(root, func(rel, abs string, src []byte) error {
		firstMap[rel] = string(src)
		return nil
	}); err != nil {
		t.Fatalf("first WalkGoFiles: %v", err)
	}

	// Mutate files on disk: remove one and modify another
	_ = os.Remove(filepath.Join(root, filepath.FromSlash("internal/core/core_test.go")))
	_ = os.WriteFile(filepath.Join(root, filepath.FromSlash("cmd/app/main.go")), []byte("corrupted"), 0o644)

	secondMap := make(map[string]string)
	if err := WalkGoFiles(root, func(rel, abs string, src []byte) error {
		secondMap[rel] = string(src)
		return nil
	}); err != nil {
		t.Fatalf("second WalkGoFiles: %v", err)
	}

	if len(secondMap) != len(firstMap) {
		t.Fatalf("second WalkGoFiles returned %d files, want %d from snapshot", len(secondMap), len(firstMap))
	}
	for rel, orig := range firstMap {
		if got := secondMap[rel]; got != orig {
			t.Errorf("file %s content mismatch: got %q, want cached original %q", rel, got, orig)
		}
	}
}

func TestSourceScanCache_CallbackMutationIsolation(t *testing.T) {
	t.Parallel()
	root := setupTestScanRepo(t)
	var origBytes, subsequentBytes []byte
	targetRel := "pkg/api/api.go"

	if err := WalkGoFiles(root, func(rel, abs string, src []byte) error {
		if rel == targetRel {
			origBytes = append([]byte(nil), src...)
			for i := range src {
				src[i] = 0xFF
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("first WalkGoFiles: %v", err)
	}

	if err := WalkGoFiles(root, func(rel, abs string, src []byte) error {
		if rel == targetRel {
			subsequentBytes = append([]byte(nil), src...)
		}
		return nil
	}); err != nil {
		t.Fatalf("second WalkGoFiles: %v", err)
	}

	if !bytes.Equal(origBytes, subsequentBytes) {
		t.Fatalf("cache was mutated by callback: got %q, want original %q", subsequentBytes, origBytes)
	}
}

func TestSourceScanCache_RootAliasRelativeAndAbsolute(t *testing.T) {
	t.Parallel()
	root := setupTestScanRepo(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	relRoot, err := filepath.Rel(cwd, root)
	if err != nil {
		t.Fatalf("filepath.Rel: %v", err)
	}

	var loadCount atomic.Int32
	customLoader := func(absRoot string, includeTests bool) ([]sourceFileEntry, error) {
		loadCount.Add(1)
		return loadSourceFilesFromDisk(absRoot, includeTests)
	}

	entriesAbs, err := loadSourceFilesWith(root, false, customLoader)
	if err != nil || loadCount.Load() != 1 {
		t.Fatalf("loadSourceFilesWith(abs) err=%v count=%d", err, loadCount.Load())
	}

	entriesRel, err := loadSourceFilesWith(relRoot, false, customLoader)
	if err != nil || loadCount.Load() != 1 {
		t.Fatalf("loadSourceFilesWith(rel) err=%v count=%d", err, loadCount.Load())
	}
	if len(entriesAbs) != len(entriesRel) {
		t.Fatalf("entries count mismatch: abs=%d rel=%d", len(entriesAbs), len(entriesRel))
	}

	if runtime.GOOS == "windows" {
		upperRoot := strings.ToUpper(root)
		lowerRoot := strings.ToLower(root)
		if canonicalScanPathKey(upperRoot) != canonicalScanPathKey(lowerRoot) {
			t.Fatalf("canonicalScanPathKey case mismatch on Windows: %q vs %q",
				canonicalScanPathKey(upperRoot), canonicalScanPathKey(lowerRoot))
		}

		entriesUpper, err := loadSourceFilesWith(upperRoot, false, customLoader)
		if err != nil || loadCount.Load() != 1 {
			t.Fatalf("loadSourceFilesWith(upperRoot) err=%v count=%d", err, loadCount.Load())
		}
		if len(entriesUpper) != len(entriesAbs) {
			t.Fatalf("entries count mismatch for upperRoot: got %d, want %d", len(entriesUpper), len(entriesAbs))
		}
	}
}

func TestSourceScanCache_CanonicalizerError(t *testing.T) {
	t.Parallel()
	sentinelCanonicalErr := errors.New("canonicalize failure sentinel")
	failingCanonicalizer := func(root string) (string, string, error) {
		return "", "", sentinelCanonicalErr
	}

	var loaderCalled bool
	dummyLoader := func(absRoot string, includeTests bool) ([]sourceFileEntry, error) {
		loaderCalled = true
		return []sourceFileEntry{{rel: "foo.go", abs: "/foo.go", src: []byte("pkg")}}, nil
	}

	entries, err := loadSourceFilesWithCanonicalizer("some/root", false, dummyLoader, failingCanonicalizer)
	if !errors.Is(err, sentinelCanonicalErr) {
		t.Fatalf("expected sentinelCanonicalErr, got %v", err)
	}
	if entries != nil {
		t.Fatalf("expected nil entries on canonicalizer error, got %v", entries)
	}
	if loaderCalled {
		t.Fatalf("loader must not be called when canonicalizer fails")
	}

	// Verify cache map was untouched
	sourceScanCacheMu.Lock()
	_, exists := sourceScanCache[sourceScanCacheKey{canonicalRoot: "", includeTests: false}]
	sourceScanCacheMu.Unlock()
	if exists {
		t.Fatalf("cache was populated on canonicalizer failure")
	}
}

func TestSourceScanCache_WalkErrorPropagation(t *testing.T) {
	t.Parallel()
	root := setupTestScanRepo(t)
	sentinelCallbackErr := errors.New("callback error sentinel")

	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		return sentinelCallbackErr
	})
	if !errors.Is(err, sentinelCallbackErr) {
		t.Fatalf("WalkProductionGoFiles expected callback error, got %v", err)
	}

	err = WalkGoFiles(root, func(rel, abs string, src []byte) error {
		return sentinelCallbackErr
	})
	if !errors.Is(err, sentinelCallbackErr) {
		t.Fatalf("WalkGoFiles expected callback error, got %v", err)
	}

	err = WalkAllGoFiles(root, func(rel, abs string, src []byte) error {
		return sentinelCallbackErr
	})
	if !errors.Is(err, sentinelCallbackErr) {
		t.Fatalf("WalkAllGoFiles expected callback error, got %v", err)
	}
}
