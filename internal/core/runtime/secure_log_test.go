package runtime

import (
	"testing"
)

func TestHashOpaqueIDForLog_stableAndEmpty(t *testing.T) {
	t.Parallel()
	if s := HashOpaqueIDForLog(""); s != "" {
		t.Fatalf("empty: %q", s)
	}
	a := HashOpaqueIDForLog("session-abc")
	b := HashOpaqueIDForLog("session-abc")
	if a == "" || a != b {
		t.Fatalf("want stable non-empty, got %q %q", a, b)
	}
	if len(a) != 16 {
		t.Fatalf("want 16 hex chars, got %d", len(a))
	}
}
