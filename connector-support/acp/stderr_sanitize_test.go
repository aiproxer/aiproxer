package acp

import (
	"strings"
	"testing"
)

func TestSanitizeBoundStderr_clonesBeforeTruncate(t *testing.T) {
	t.Parallel()

	orig := []byte(strings.Repeat("a", MaxBoundStderrBytes+64))
	marker := byte('Z')
	orig[0] = marker
	_ = SanitizeBoundStderr(orig)
	if orig[0] != marker {
		t.Fatal("SanitizeBoundStderr must not mutate caller's byte 0")
	}
	orig[MaxBoundStderrBytes] = 'X'
	again := SanitizeBoundStderr(orig)
	if len(again) != MaxBoundStderrBytes {
		t.Fatalf("sanitized len = %d, want %d", len(again), MaxBoundStderrBytes)
	}
	if again[0] != marker {
		t.Fatalf("sanitized[0] = %q, want %q", again[0], marker)
	}
}

func TestSanitizeBoundStderr_stripsControlAndInvalidUTF8(t *testing.T) {
	t.Parallel()

	got := SanitizeBoundStderr([]byte("ok\x00\x01\x7f\nline\xff"))
	if strings.ContainsRune(got, 0) || strings.ContainsRune(got, 1) || strings.ContainsRune(got, 127) {
		t.Fatalf("control runes remain: %q", got)
	}
	if !strings.Contains(got, "ok") || !strings.Contains(got, "line") {
		t.Fatalf("printable text lost: %q", got)
	}
}
