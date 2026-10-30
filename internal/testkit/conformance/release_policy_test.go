package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleasePolicyUsesBoundedSentinelAndNoProductConstruction(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, name := range []string{"sentinel.go", "sentinel_test.go"} {
		path := filepath.Join(root, "internal", "testkit", "conformance", name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "AllCells") || strings.Contains(string(src), "len(fe)*len(be)") {
			t.Fatalf("sentinel source contains Cartesian construction: %s", path)
		}
	}
	if got := len(BoundedSentinelCases()); got == 0 || got > maxBoundedSentinelCases {
		t.Fatalf("sentinel count=%d outside bound", got)
	}
}
