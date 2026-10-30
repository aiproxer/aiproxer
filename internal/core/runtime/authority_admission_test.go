package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

func TestExecutorAuthorityDisabledAllowsOpenWithoutAdmission(t *testing.T) {
	t.Parallel()

	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	out, err := openAuthorityCandidate(t, ex, aLegID)
	if err != nil {
		t.Fatalf("openAuthorityCandidate: %v", err)
	}
	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected backend to open when authority is disabled")
	}
	if backend.openCalls.Load() != 1 {
		t.Fatalf("backend open calls = %d, want 1", backend.openCalls.Load())
	}
}

func TestExecutorAuthorityDeniedBlocksOpenBeforeBackend(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: false,
			Outcome: usagelimit.DecisionOutcomeDeny,
			PolicyRecord: policydecision.Record{
				ReasonCode: "quota_exceeded",
			},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	_, err := openAuthorityCandidate(t, ex, aLegID)
	if err == nil {
		t.Fatal("expected authority denial error")
	}
	if !aipapi.IsPolicyDenied(err) {
		t.Fatalf("err must be policy denied, got %v", err)
	}
	if backend.openCalls.Load() != 0 {
		t.Fatalf("backend open calls = %d, want 0", backend.openCalls.Load())
	}
	if auth.admitCalls.Load() != 1 {
		t.Fatalf("admit calls = %d, want 1", auth.admitCalls.Load())
	}
	if auth.releaseCalls.Load() != 0 {
		t.Fatalf("release calls = %d, want 0", auth.releaseCalls.Load())
	}
}

func TestExecutorAuthorityReservationConflictIsPolicyDenied(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitErr: usagelimit.ErrReservationConflict,
		status:   controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	_, err := openAuthorityCandidate(t, ex, aLegID)
	if err == nil {
		t.Fatal("expected authority denial error")
	}
	if !aipapi.IsPolicyDenied(err) {
		t.Fatalf("err must be policy denied, got %v", err)
	}
	if backend.openCalls.Load() != 0 {
		t.Fatalf("backend open calls = %d, want 0", backend.openCalls.Load())
	}
}

func TestExecutorAuthorityAdmitPopulatesRequestCount(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-1",
			ReservedAmount: authorityInputAmount(12),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	out, err := openAuthorityCandidate(t, ex, aLegID)
	if err != nil {
		t.Fatalf("openAuthorityCandidate: %v", err)
	}
	if auth.admitCalls.Load() != 3 {
		t.Fatalf("admit calls = %d, want 3 (precheck + clamp preview + authoritative)", auth.admitCalls.Load())
	}
	calls := auth.admitInputs()
	if len(calls) != 3 {
		t.Fatalf("admit inputs = %d, want 3", len(calls))
	}
	if !calls[0].EstimateOnly {
		t.Fatal("precheck admission must be estimate-only")
	}
	if !calls[1].EstimateOnly || !calls[1].SkipEvidence {
		t.Fatalf("clamp preview must be EstimateOnly+SkipEvidence; got EstimateOnly=%v SkipEvidence=%v",
			calls[1].EstimateOnly, calls[1].SkipEvidence)
	}
	in := calls[2]
	if in.EstimateOnly {
		t.Fatal("real admission must not be estimate-only")
	}
	if in.Correlation.BLegID == "" || in.ReservationKey.BLegID == "" {
		t.Fatal("real admission must carry the allocated B-leg")
	}
	if in.Correlation.BLegID != out.ready.BLeg().BLegID {
		t.Fatalf("real admission BLegID = %q, want %q", in.Correlation.BLegID, out.ready.BLeg().BLegID)
	}
	if in.Correlation.BLegID != in.ReservationKey.BLegID {
		t.Fatalf("real admission BLegID = %q, want reservation key BLegID %q", in.Correlation.BLegID, in.ReservationKey.BLegID)
	}
	if in.RequestCount.Unit != usagelimit.AmountUnitRequests || in.RequestCount.Value != 1 {
		t.Fatalf("admit RequestCount = %v, want 1 requests", in.RequestCount)
	}
	if in.Request.Unit != usagelimit.AmountUnitInputTokens {
		t.Fatalf("admit Request unit = %q, want input_tokens", in.Request.Unit)
	}
}

func TestExecutorAuthorityRealAdmitFailureReleasesBudget(t *testing.T) {
	t.Parallel()

	auth := &estimateThenFailAuthority{
		estimateResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-precheck",
			ReservedAmount: authorityInputAmount(12),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		realErr: usagelimit.ErrReservationConflict,
	}
	ex, store, backend, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)

	budget := &attemptBudget{max: 1}
	req := authorityOpenRequest(t, aLegID, budget)
	plan := candidatePlan{
		cand: authorityCandidate(),
	}
	_, err := ex.evaluateAndOpenCandidate(context.Background(), req, plan)
	if err == nil {
		t.Fatal("expected authority denial error from real admit")
	}
	if !aipapi.IsPolicyDenied(err) {
		t.Fatalf("err must be policy denied, got %v", err)
	}
	if backend.openCalls.Load() != 0 {
		t.Fatalf("backend open calls = %d, want 0 (backend must never open)", backend.openCalls.Load())
	}
	if got, want := auth.admitCalls.Load(), int64(3); got != want {
		t.Fatalf("admit calls = %d, want %d (precheck + clamp preview + real)", got, want)
	}
	if budget.usedNow() != 0 {
		t.Fatalf("budget used = %d, want 0 (slot must be released on real admit failure)", budget.usedNow())
	}
	// A second acquire must succeed because the leaked slot was refunded.
	if !budget.tryAcquire() {
		t.Fatal("budget tryAcquire after failure must succeed when slot was released")
	}
	if budget.usedNow() != 1 {
		t.Fatalf("budget used after re-acquire = %d, want 1", budget.usedNow())
	}

	// B-leg sequence is NOT restorable: the b2bua Store has no rollback API, so
	// the seq consumed before the real admit stays consumed (next seq is 2).
	// Orphaned seqToBLeg entries are functionally invisible (only RecordAttempt
	// reads them, never for a rolled-back b-leg; LoadAttempts needs no contiguous
	// seqs) and are reclaimed on A-leg eviction; a rollback API would break the
	// stable continuity.Store contract (contract test pins the method set) and
	// nextSeq-- is ABA-unsafe under concurrent allocators.
	next, err := store.NextBLeg(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("next b-leg after failure: %v", err)
	}
	if next.Seq != 2 {
		t.Fatalf("next b-leg seq = %d, want 2 (B-leg sequence is not restorable; no rollback API)", next.Seq)
	}
}

// TestExecutorAuthorityRealAdmitFailureHoldsNoReservation pins the L9 invariant:
// reserve-then-deny is impossible under the current authority-app semantics. A
// reservation is applied only when Admit returns Allowed with no error
// (admission.go:70); on every error path admitAttemptAuthority returns an EMPTY
// attemptAuthorityState (Reserved=false), so an authorityLifecycle owner built
// over it would be inactive and its Release a no-op, so nothing can leak. This drives the real (non-estimate) admit failure
// path directly — a stub authority service whose Admit returns
// ErrReservationConflict (deny) — and asserts the returned authority state is
// empty. It locks the invariant so a future authority-app change cannot silently
// reintroduce a leak on the real-admit failure path in openPlannedCandidate.
func TestExecutorAuthorityRealAdmitFailureHoldsNoReservation(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitErr: usagelimit.ErrReservationConflict,
		status:   controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	state, err := ex.admitAttemptAuthority(
		context.Background(),
		"trace-pin",
		aLegID,
		b2bua.BLegRecord{BLegID: "bleg-pin", Seq: 1},
		aipapi.Call{ID: "request-pin", Route: aipapi.RouteIntent{Selector: "backend-1:model-1"}},
		authorityCandidate(),
		accountingpreflight.Decision{},
		false, // real (non-estimate) admit — the only path that allocates reservations
	)
	if err == nil {
		t.Fatal("expected authority denial error from real admit")
	}
	if !aipapi.IsPolicyDenied(err) {
		t.Fatalf("err must be policy denied (ErrReservationConflict maps to Deny), got %v", err)
	}
	// L9 invariant: on an admit error the returned state is the zero value, so
	// an authorityLifecycle owner built over it is inactive (its Release no-ops
	// when TraceID=="" || !Reserved) and has nothing to release. Nothing was
	// reserved, so nothing can leak.
	if state.admissionResult.Reserved {
		t.Fatalf("admissionResult.Reserved = true, want false (reserve-then-deny must be impossible)")
	}
	if state.admissionInput.Correlation.TraceID != "" {
		t.Fatalf("admissionInput.Correlation.TraceID = %q, want empty (zero-value state on admit error)", state.admissionInput.Correlation.TraceID)
	}
	if got, want := auth.admitCalls.Load(), int64(1); got != want {
		t.Fatalf("admit calls = %d, want %d", got, want)
	}
}
