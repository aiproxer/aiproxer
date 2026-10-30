package runtime

import (
	"context"
	"sync"
	"testing"

	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestExecutor_PrepareRequest_LeakOnBillingIDFailure(t *testing.T) {
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-leak-test",
			ReservedAmount: authorityInputAmount(10),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}

	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	attachPhase6Coordinators(ex)

	call := &aipapi.Call{
		ID:      "request-leak-test",
		Session: aipapi.SessionRef{AuthoritativeSessionID: "session-leak-test"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}

	ctx := context.WithValue(context.Background(), testInjectBillingErrorKey{}, true)
	_, _, _, err := ex.prepareRequest(ctx, call)
	if err == nil {
		t.Fatal("expected prepareRequest to fail on billing ID allocation error")
	}

	// Verify that the request authority was admitted once
	if got := auth.admitCalls.Load(); got != 1 {
		t.Errorf("admitCalls = %d; want 1", got)
	}

	// Verify that the request authority was released once
	if got := auth.releaseCalls.Load(); got != 1 {
		t.Errorf("releaseCalls = %d; want 1", got)
	}

	// Verify no invalid scope cleanup (A-leg scope was never started, and should not be canceled)
	aScope := ex.lifecycleCoordinator().StartALeg(aLegID)
	if err := aScope.Err(); err != nil {
		t.Errorf("expected A-leg scope not to be canceled/cleaned up: %v", err)
	}
}

func TestPreStreamGuard_ConcurrentHandoffVsClose(t *testing.T) {
	for i := range 200 {
		auth := &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "reservation-concurrent",
				ReservedAmount: authorityInputAmount(10),
				PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			},
			status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		attachPhase6Coordinators(ex)

		ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "request-concurrent", aLegID, "trace-concurrent", scope.PrincipalScopeView{})
		if err != nil {
			t.Fatalf("admitRequestAuthorityOnce failed: %v", err)
		}

		guard := &preStreamGuard{
			executor:                 ex,
			ctx:                      ctx,
			requestAuthorityAdmitted: true,
		}

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			guard.Handoff()
		}()

		go func() {
			defer wg.Done()
			guard.Close()
		}()

		wg.Wait()

		releases := auth.releaseCalls.Load()
		if releases > 1 {
			t.Errorf("iteration %d: double release detected (releases = %d)", i, releases)
		}

		guard.mu.Lock()
		wasClosed := guard.closed
		wasHandedOver := guard.handedOver
		guard.mu.Unlock()

		if !wasClosed && !wasHandedOver {
			t.Errorf("iteration %d: expected guard to be closed or handed over", i)
		}

		if wasClosed && releases != 1 {
			t.Errorf("iteration %d: guard closed but release calls = %d, want 1", i, releases)
		}

		if !wasClosed && wasHandedOver && releases != 0 {
			t.Errorf("iteration %d: guard handed over but release calls = %d, want 0", i, releases)
		}
	}
}
