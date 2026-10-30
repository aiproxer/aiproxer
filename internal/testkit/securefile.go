package testkit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MustReadFileUnderRoot reads path after verifying its absolute location is under root
// (root may be relative; both are cleaned). Use for golden and fixture loaders so test
// path segments cannot escape the repository tree.
func MustReadFileUnderRoot(tb testing.TB, root, path string) []byte {
	tb.Helper()
	base, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		tb.Fatalf("abs root: %v", err)
	}
	target, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		tb.Fatalf("abs path: %v", err)
	}
	if !filePathUnderRoot(base, target) {
		tb.Fatalf("path %q escapes root %q", path, root)
	}
	// security: target is absolute and verified under base by [filePathUnderRoot].
	b, err := os.ReadFile(target) // #nosec G304
	if err != nil {
		tb.Fatalf("read file %s: %v", target, err)
	}
	return b
}

func filePathUnderRoot(rootAbs, targetAbs string) bool {
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
