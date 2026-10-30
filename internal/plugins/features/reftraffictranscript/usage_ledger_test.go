package reftraffictranscript

import (
	"testing"
)

func TestNewUsageLedger(t *testing.T) {
	t.Parallel()
	ledger := NewUsageLedger()
	if ledger == nil {
		t.Fatal("expected NewUsageLedger to return a non-nil instance")
	}
}
