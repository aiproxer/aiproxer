package runtimehost_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimehost_NoWaitGroupRequestRefcounter(t *testing.T) {
	t.Parallel()
	root := "."
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "sync.WaitGroup") {
			t.Fatalf("%s must not use sync.WaitGroup as request refcounter (req 10.4)", name)
		}
	}
}
