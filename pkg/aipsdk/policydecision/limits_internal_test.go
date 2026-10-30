package policydecision

import "testing"

func TestBoundUTF8NonPositiveMaxSafe(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{-1, 0} {
		if got := boundUTF8("hello", limit); got != "" {
			t.Fatalf("boundUTF8(%q, %d) = %q, want empty", "hello", limit, got)
		}
	}
	if got := boundUTF8("", -1); got != "" {
		t.Fatalf("boundUTF8(empty, -1) = %q, want empty", got)
	}
	// Positive regression: ASCII under bound is unchanged.
	if got := boundUTF8("abc", 5); got != "abc" {
		t.Fatalf("boundUTF8 under bound = %q, want %q", got, "abc")
	}
	// Positive regression: over-bound truncates without splitting a rune.
	if got := boundUTF8("abcdef", 3); got != "abc" {
		t.Fatalf("boundUTF8 over bound = %q, want %q", got, "abc")
	}
}
