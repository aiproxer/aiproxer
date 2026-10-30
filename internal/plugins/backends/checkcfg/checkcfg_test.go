package checkcfg

import (
	"strings"
	"testing"
)

func TestRequireNonEmpty(t *testing.T) {
	t.Parallel()
	if err := RequireNonEmpty("b", "f", "ok"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if err := RequireNonEmpty("b", "f", ""); err == nil {
		t.Fatal("expected error")
	} else if !strings.Contains(err.Error(), "b:") || !strings.Contains(err.Error(), "f") {
		t.Fatalf("unexpected message: %v", err)
	}
	if err := RequireNonEmpty("b", "f", " \t\n"); err == nil {
		t.Fatal("expected error for whitespace-only")
	}
}
