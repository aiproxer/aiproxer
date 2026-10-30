package backendplugins_docs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocs_AuthorGuideCoversRequiredTopics(t *testing.T) {
	t.Parallel()
	// File lives next to this test (package directory is the test cwd).
	body := read(t, "author"+"ing.md")
	for _, want := range []string{
		"Closed manifest",
		"SDK server helper",
		"Capabilities",
		"Opaque config",
		"Exact trust",
		"Conformance",
		"Module versioning",
		"Installation",
		"Private companions",
		"connectors/localstub",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("guide missing %q", want)
		}
	}
}

func TestDocs_NoRootInternalImportInExamples(t *testing.T) {
	t.Parallel()
	needle := "aiproxer" + "/internal/"
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		base := filepath.Base(path)
		if base == "docs_test.go" {
			return nil
		}
		if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".md") &&
			!strings.HasSuffix(path, ".js") && !strings.HasSuffix(path, ".py") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), needle) {
			t.Errorf("%s references root internal/", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExample_PrivateBridgeSkeletonPresent(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{
		"examples/private-bridge/README.md",
		"examples/private-bridge/bridge.js",
		"examples/private-bridge/bridge.py",
	} {
		if _, err := os.Stat(rel); err != nil {
			t.Fatal(err)
		}
	}
}

func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
