package accounting_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accounting"
)

// Phase 2.2: authoritative zero costs, explicit-zero rates, and checked overflow.

func TestSubMoneyChecked(t *testing.T) {
	t.Parallel()

	got, ok := accounting.SubMoneyChecked(10, 3)
	if !ok || got != 7 {
		t.Fatalf("SubMoneyChecked(10,3)=%d,%v want 7,true", got, ok)
	}
	if _, ok := accounting.SubMoneyChecked(3, 10); ok {
		t.Fatal("expected underflow failure")
	}
}
