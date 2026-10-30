package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

func TestAuthoritativeBillingSuccessFinishSettlesAttemptAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "authoritative-success-reservation",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	executor, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "request-authoritative-success", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			aLegID:   aLegID,
			traceID:  "trace-authoritative-success",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: aLegID, Seq: 1}, authorityCandidate(), authorityLifecycle{}, newAttemptAccountingTracker(time.Unix(1, 0))),
	}
	testAttemptSession(stream).authority = testAuthorityLifecycle(executor, attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(7),
		admissionResult: auth.admitResult,
	}, authorityCandidate())
	installTestTurnTerminal(stream)
	bindTestRuntimeOwners(stream, executor)

	_, ok, err := stream.terminal.finalizeResponseFinishedAuthority(context.Background(), aipapi.Event{Kind: aipapi.EventResponseFinished}, stream.facts.terminalFacts(), stream.attempt.snapshot(), stream.responsePipeline)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected no synthesized usage when StreamUsage is unset")
	}
	if got := auth.settleCalls.Load(); got != 1 {
		t.Fatalf("attempt authority settle calls = %d, want 1 on authoritative success finish", got)
	}
	if !testAttemptSession(stream).authority.Settled() {
		t.Fatal("authoritative success finish left attempt authority unsettled")
	}
	if got := auth.lastSettle(); got.Kind != usagelimit.SettlementKindFinal {
		t.Fatalf("settle kind = %q, want final", got.Kind)
	}
}

func TestAuthoritativeBillingPreservesProtocolUsageProjection(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "authoritative-protocol-usage",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	executor, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	executor.StreamUsage = accountingstream.New(&stubStreamCounter{
		call:   accountingapp.CountResult{InputTokens: 7, TotalTokens: 7},
		output: accountingapp.CountResult{OutputTokens: 3, TotalTokens: 10},
	}, accountingstream.Config{})
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "request-authoritative-protocol", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			aLegID:   aLegID,
			traceID:  "trace-authoritative-protocol",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: aLegID, Seq: 1}, authorityCandidate(), authorityLifecycle{}, newAttemptAccountingTracker(time.Unix(1, 0))),
	}
	testAttemptSession(stream).authority = testAuthorityLifecycle(executor, attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(7),
		admissionResult: auth.admitResult,
	}, authorityCandidate())
	installTestTurnTerminal(stream)
	bindTestRuntimeOwners(stream, executor)

	usage, ok, err := stream.terminal.finalizeResponseFinishedAuthority(context.Background(), aipapi.Event{Kind: aipapi.EventResponseFinished}, stream.facts.terminalFacts(), stream.attempt.snapshot(), stream.responsePipeline)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || usage.Kind == "" || usage.TotalTokens == 0 {
		t.Fatalf("protocol usage projection missing under authoritative cutover: ok=%t usage=%+v", ok, usage)
	}
	if usage.CostPresent || usage.CostNanoUnits != 0 || usage.CostSource != "" {
		t.Fatalf("stream-time monetary enrichment leaked into protocol usage: %+v", usage)
	}
	if got := auth.settleCalls.Load(); got != 1 {
		t.Fatalf("attempt authority settle calls = %d, want 1", got)
	}
	if !testAttemptSession(stream).authority.Settled() {
		t.Fatal("authoritative protocol-usage finish left attempt authority unsettled")
	}
}

func TestAuthoritativeBillingKeepsNonMoneyAuthorityCoordination(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Reserved:       true,
			ReservationID:  "authoritative-cutover-reservation",
			ReservedAmount: authorityInputAmount(7),
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	executor, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: aLegID,
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{}, authorityCandidate(), authorityLifecycle{}),
	}
	testAttemptSession(stream).authority = testAuthorityLifecycle(executor, attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(7),
		admissionResult: auth.admitResult,
	}, authorityCandidate())
	bindTestRuntimeOwners(stream, executor)

	stream.terminal.recordPartialTokenAccounting(context.Background(), stream.attempt.snapshot(), "authoritative-cutover", nil, stream.facts.terminalFacts(), stream.responsePipeline)
	if got := auth.settleCalls.Load(); got != 1 {
		t.Fatalf("non-money authority settle calls = %d, want 1", got)
	}
	if !testAttemptSession(stream).authority.Settled() {
		t.Fatal("non-money authority lifecycle was not finalized")
	}
}

func TestAttemptAuthorityUsageAmountUsesQuantityEstimate(t *testing.T) {
	t.Parallel()
	got := attemptAuthorityUsageAmount(aipapi.Event{InputTokens: 3}, usagelimit.Amount{Unit: usagelimit.AmountUnitInputTokens, Value: 4})
	if got.Unit != usagelimit.AmountUnitInputTokens || got.Value != 3 {
		t.Fatalf("usage=%#v", got)
	}
}
