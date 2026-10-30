package testkit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCrossProtocolGolden_userThreadFixture(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	p := filepath.Join(repoRoot, "testdata", "cross_protocol", "user_thread.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		CanonicalUserText string `json:"canonical_user_text"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.CanonicalUserText == "" {
		t.Fatal("empty golden")
	}
}
