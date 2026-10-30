package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

func TestOperatorUsageForFinalizePrefersLastAuthorityUsage(t *testing.T) {
	t.Parallel()

	authoritative := aipapi.Event{
		Kind:         aipapi.EventUsageDelta,
		InputTokens:  10,
		OutputTokens: 4,
		TotalTokens:  14,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane:     aipapi.UsagePlaneProviderBillable,
			Source:    aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	estimate := aipapi.Event{
		Kind:         aipapi.EventUsageDelta,
		InputTokens:  99,
		OutputTokens: 99,
		TotalTokens:  198,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane:     aipapi.UsagePlaneProviderBillable,
			Source:    aipapi.UsageSourceLocalEstimator,
			Authority: aipapi.UsageAuthorityEstimated,
		},
	}
	s := &retryRecvStream{
		responsePipeline: &responsePipeline{
			lastAuthorityUsage: authoritative,
			seenEvents:         []aipapi.Event{estimate},
		},
	}

	got := s.responsePipeline.operatorUsageForFinalize()
	if got.InputTokens != authoritative.InputTokens || got.OutputTokens != authoritative.OutputTokens {
		t.Fatalf("operator finalize usage = %#v, want authoritative lastAuthorityUsage", got)
	}

	customer := s.responsePipeline.usageEvidenceOrEmpty()
	if customer.InputTokens != estimate.InputTokens {
		t.Fatalf("customer usageEvidenceOrEmpty = %#v, want seenEvents-first estimate", customer)
	}
}

func TestOperatorUsageForFinalizeFallsBackToSeenEventsThenEmptyShell(t *testing.T) {
	t.Parallel()

	seen := aipapi.Event{
		Kind:        aipapi.EventUsageDelta,
		InputTokens: 3,
		TotalTokens: 3,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane:     aipapi.UsagePlaneProviderBillable,
			Source:    aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	withSeen := &retryRecvStream{responsePipeline: &responsePipeline{seenEvents: []aipapi.Event{seen}}}
	got := withSeen.responsePipeline.operatorUsageForFinalize()
	if got.InputTokens != seen.InputTokens || got.Kind != aipapi.EventUsageDelta {
		t.Fatalf("seen-events fallback = %#v, want %#v", got, seen)
	}

	empty := (&retryRecvStream{responsePipeline: newResponsePipeline()}).responsePipeline.operatorUsageForFinalize()
	if empty.Kind != aipapi.EventUsageDelta || empty.InputTokens != 0 || empty.TotalTokens != 0 {
		t.Fatalf("unobserved shell = %#v, want empty UsageDelta shell", empty)
	}
	if !usageEventPresent(empty) {
		t.Fatal("empty operator shell must be present usage (req 2.9), not absent")
	}

	if got := (*responsePipeline)(nil).operatorUsageForFinalize(); got.Kind != aipapi.EventUsageDelta {
		t.Fatalf("nil owner = %#v, want empty UsageDelta shell", got)
	}
}

func TestSettleCancellationAuthorityUsesOperatorUsageForFinalize(t *testing.T) {
	t.Parallel()

	authoritative := aipapi.Event{
		Kind:         aipapi.EventUsageDelta,
		InputTokens:  7,
		OutputTokens: 2,
		TotalTokens:  9,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane:     aipapi.UsagePlaneProviderBillable,
			Source:    aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	estimate := aipapi.Event{
		Kind:         aipapi.EventUsageDelta,
		InputTokens:  40,
		OutputTokens: 40,
		TotalTokens:  80,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane:     aipapi.UsagePlaneProviderBillable,
			Source:    aipapi.UsageSourceLocalEstimator,
			Authority: aipapi.UsageAuthorityEstimated,
		},
	}

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-op-usage",
			ReservedAmount: authorityInputAmount(10),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	state := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(10),
		admissionResult: auth.admitResult,
	}
	cand := routing.AttemptCandidate{Key: "cand", Primary: routing.Primary{Backend: "b", Model: "m"}}
	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:  aLegID,
			traceID: "trace-op-usage",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-op", Seq: 1}, cand, testAuthorityLifecycle(ex, state, cand)),
		responsePipeline: &responsePipeline{
			lastAuthorityUsage: authoritative,
			seenEvents:         []aipapi.Event{estimate},
		},
		terminal: newTurnTerminal(),
	}

	rs.terminal.settleCancellationAuthorityForAttempt(context.Background(), rs.attempt.snapshot(), rs.responsePipeline)
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
	}
	got := auth.lastSettle()
	if got.FinalUsage.Value != int64(authoritative.InputTokens) {
		t.Fatalf("operator settle FinalUsage = %#v, want authoritative input %d (not seenEvents estimate)", got.FinalUsage, authoritative.InputTokens)
	}
}
