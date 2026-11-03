package runtimebundle

import (
	"context"
	"errors"
	"testing"
)

var errInventoryRollbackCleanup = errors.New("inventory ledger cleanup failed")

// joinRollbackFailure must surface a failed inventory rollback next to the
// model-runtime failure that triggered it, so a cleanup that did not run is
// visible instead of silently dropped.
func TestRollbackInventoryLedgerOnFailureJoinsRollbackError(t *testing.T) {
	primary := errors.New("inventory model runtime failed")
	ledger := NewResourceLedger()
	ledger.Add("failing-cleanup", PhasePrepare, func(context.Context) error {
		return errInventoryRollbackCleanup
	})
	err := joinRollbackFailure(context.Background(), primary, ledger)
	if err == nil {
		t.Fatal("rollback helper returned nil, want the primary failure")
	}
	if !errors.Is(err, primary) {
		t.Fatalf("error = %v, want it to preserve the primary failure", err)
	}
	if !errors.Is(err, errInventoryRollbackCleanup) {
		t.Fatalf("error = %v, want it to preserve the rollback failure", err)
	}
}

func TestRollbackInventoryLedgerOnFailureReturnsPrimaryWhenRollbackClean(t *testing.T) {
	primary := errors.New("inventory model runtime failed")
	err := joinRollbackFailure(context.Background(), primary, NewResourceLedger())
	if !errors.Is(err, primary) {
		t.Fatalf("error = %v, want it to preserve the primary failure", err)
	}
}
