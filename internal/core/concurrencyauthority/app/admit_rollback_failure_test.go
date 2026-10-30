package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
)

// TestAdmit_RollbackReleaseFailureKeepsPrimaryCause proves a failed rollback
// release is reported alongside the admission failure that triggered it, so
// occupancy that could not be freed is not silently reported as clean.
func TestAdmit_RollbackReleaseFailureKeepsPrimaryCause(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	advOK := advisoryRule(5)
	advOK.ID = "rule-adv-ok"
	advFail := advisoryRule(5)
	advFail.ID = "rule-adv-fail"

	acquireErr := errors.New("second advisory acquire store fault")
	releaseErr := errors.New("rollback release store fault")
	store := &rollbackRecordingStore{
		memoryStore: newMemoryStore(),
		failRuleID:  "rule-adv-fail",
		err:         acquireErr,
		releaseErr:  releaseErr,
	}
	svc := newService(t, []domain.Rule{advOK, advFail}, store, now)

	res, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-rollback-failure", Scope: principalScope("alice"), Namespace: "default",
	})
	if err == nil || !errors.Is(err, acquireErr) {
		t.Fatalf("err=%v want wrapped admission failure %v", err, acquireErr)
	}
	if !errors.Is(err, releaseErr) {
		t.Fatalf("err=%v want rollback release failure %v", err, releaseErr)
	}
	if len(res.Leases) != 0 || res.LeaseID != "" {
		t.Fatalf("error result must carry no leases: %+v", res)
	}

	acquired, released := store.recordings()
	if len(acquired) != 1 || !sameIDs(acquired, released) {
		t.Fatalf("acquired=%v released=%v want one attempted rollback", acquired, released)
	}
}
