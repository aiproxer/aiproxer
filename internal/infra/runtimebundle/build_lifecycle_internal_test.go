package runtimebundle

import (
	"errors"
	"strings"
	"testing"
)

// TestDisposeClosers_PartialStartupDisposesReverseOrder tests the canonical
// disposal owner directly (in-package), replacing the deleted exported
// DisposeProcessClosersForTest production test API.
func TestDisposeClosers_PartialStartupDisposesReverseOrder(t *testing.T) {
	t.Parallel()

	order := make([]string, 0, 4)

	err := disposeClosers([]func() error{
		func() error {
			order = append(order, "first")
			return nil
		},
		func() error {
			order = append(order, "second")
			return errors.New("second failed")
		},
		func() error {
			order = append(order, "third")
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected joined disposal error")
	}
	if !strings.Contains(err.Error(), "second failed") {
		t.Fatalf("expected disposal error to include closer failure, got %v", err)
	}
	want := []string{"third", "second", "first"}
	if len(order) != 3 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("dispose order=%v want reverse registration %v", order, want)
	}
}
