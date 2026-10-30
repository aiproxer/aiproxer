package testkit_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestReadGoldenBytes(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	_ = testkit.ReadGoldenBytes(t, root, "golden", "sample.json")
}

func TestAssertJSONEqual(t *testing.T) {
	t.Parallel()

	a := []byte(`{"b":2,"a":1}`)
	b := []byte(`{"a":1,"b":2}`)
	testkit.AssertJSONEqual(t, a, b)
}

func TestProviderStubInvoke(t *testing.T) {
	t.Parallel()

	var stub testkit.ProviderStub
	events, err := stub.Invoke(context.Background(), aipapi.Call{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	testkit.AssertEventCount(t, events, 0)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestGoldenSampleIsValidJSON(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	b := testkit.ReadGoldenBytes(t, root, "golden", "sample.json")
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}
