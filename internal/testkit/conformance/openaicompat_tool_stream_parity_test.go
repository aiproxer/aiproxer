package conformance

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestParity_OpenAICompat_toolCallStreamFinishContract locks the dual-mapper
// finish contract for chat tool-call SSE (finding M4).
//
// Root go.mod must not require connector-support/ (ADR 0008 / archtest), so this
// package cannot import both mappers into one in-process comparison. Instead we
// prove the mirrored goldens stay wired: essential chatStream tests and the
// connector-support decoder tests both assert EventToolCallFinished for the
// same finish_reason=="tool_calls" SSE shape (including multi-tool index order).
func TestParity_OpenAICompat_toolCallStreamFinishContract(t *testing.T) {
	t.Parallel()
	root := repoRootFromConformance(t)

	essential := filepath.Join(root, "internal", "plugins", "backends", "openaicompat", "chat_events_test.go")
	connector := filepath.Join(root, "connector-support", "openaicompat", "stream_tool_finish_test.go")
	for _, path := range []string{essential, connector} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(b)
		for _, needle := range []string{
			`finish_reason":"tool_calls"`,
			"EventToolCallFinished",
			"call_ma", // multi-tool index order lock
			"call_mb",
			"call_idonly", // id-only ToolCallStarted split-chunk lock
			"call_split",  // id-then-name split-chunk lock
		} {
			if !strings.Contains(text, needle) {
				t.Fatalf("%s missing dual-mapper finish contract needle %q", path, needle)
			}
		}
	}
}

func repoRootFromConformance(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// .../internal/testkit/conformance/<thisfile>
	dir := filepath.Dir(file)
	return filepath.Clean(filepath.Join(dir, "..", "..", ".."))
}
