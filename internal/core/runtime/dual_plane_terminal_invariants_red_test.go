package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	corestate "github.com/aiproxer/aiproxer/internal/core/state"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// Phase 1.1 RED contracts: customer-versus-operator terminal invariants
// (requirements 1.1–1.9, 2.1–2.9, 13.1–13.2; design D1, D2, D3, D13, D17).

func TestDualPlaneTerminalInvariants_ProviderUsageMustNotEnterCustomerSettlement(t *testing.T) {
	t.Parallel()

	prov := &settleRecordingRequestProvider{id: "customer-settle"}
	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "customer-settle", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         aipapi.Call{ID: "req-cust-usage"},
		CheckpointID: "fe",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ctx, err = ex.admitRequestAuthorityOnce(ctx, "req-cust-usage", "a-1", "trace-cust-usage", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	provider := aipapi.ScopedUsageDelta{
		InputTokens: 200, OutputTokens: 80, TotalTokens: 280,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	delivered := aipapi.ScopedUsageDelta{
		InputTokens: 40, OutputTokens: 12, TotalTokens: 52,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneClientVisible, Source: aipapi.UsageSourceLocalTokenizer, Authority: aipapi.UsageAuthorityEstimated,
		},
	}
	raw := aipapi.Event{
		Kind:        aipapi.EventUsageDelta,
		UsageScopes: []aipapi.ScopedUsageDelta{delivered, provider},
	}
	authorityEv := authorityUsageEvent([]aipapi.Event{raw})
	if authorityEv.InputTokens != provider.InputTokens {
		t.Fatalf("precondition: authorityUsageEvent must prefer provider input; got %d", authorityEv.InputTokens)
	}

	call, output := clientVisibleCount(delivered.InputTokens, delivered.OutputTokens)
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{call: call, output: output}, accountingstream.Config{})
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID:  "trace-cust-usage",
			baseline: aipapi.Call{ID: "req-cust-usage"},
		}),
		responsePipeline: &responsePipeline{customer: newCustomerEvidenceAccumulator()},
	}
	bindTestRuntimeOwners(stream, ex)
	stream.responsePipeline.customer.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "cust-delivered"})
	_ = stream.terminal.settleRequestAuthorityWithFrontendEgress(ctx, authorityEv, stream.facts.terminalFacts(), stream.responsePipeline)

	if prov.settleCalls.Load() != 1 {
		t.Fatalf("customer SettleRequest calls=%d want 1", prov.settleCalls.Load())
	}
	facts, _ := prov.lastFacts.Load().([]metering.Fact)
	if len(facts) != 1 {
		t.Fatalf("customer settle facts=%d want 1", len(facts))
	}
	fact := facts[0]
	if fact.Perspective != metering.PerspectiveCustomer || fact.Boundary != metering.BoundaryFrontendEgress {
		t.Fatalf("customer fact plane=%s/%s", fact.Perspective, fact.Boundary)
	}
	in, ok := checkpoint.QuantityComponentValue(fact.Quantities, metering.ComponentInputToken)
	if !ok {
		t.Fatal("customer FE egress must carry input_token")
	}
	if in != int64(delivered.InputTokens) {
		t.Fatalf("customer settlement input_token=%d want delivered %d (must not import provider usage %d)", in, delivered.InputTokens, provider.InputTokens)
	}
	out, ok := checkpoint.QuantityComponentValue(fact.Quantities, metering.ComponentOutputToken)
	if !ok || out != int64(delivered.OutputTokens) {
		t.Fatalf("customer settlement output_token=%d ok=%v want delivered %d", out, ok, delivered.OutputTokens)
	}
}

func TestDualPlaneTerminalInvariants_ProviderCostMustNotEnterFrontendFacts(t *testing.T) {
	t.Parallel()

	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         aipapi.Call{ID: "req-fe-cost"},
		CheckpointID: "fe",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)

	usage := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		InputTokens:   3,
		OutputTokens:  1,
		TotalTokens:   4,
		CostNanoUnits: 42_000,
		Currency:      "USD",
		CostSource:    string(aipapi.UsageSourceProviderReported),
		CostPresent:   true,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	fact, ok := ex.emitFrontendEgressMeteringFact(ctx, "trace-fe-cost", usage)
	if !ok {
		t.Fatal("expected frontend-egress fact")
	}
	if fact.Perspective != metering.PerspectiveCustomer {
		t.Fatalf("perspective=%s", fact.Perspective)
	}
	if fact.Money != nil {
		t.Fatalf("customer FE egress must not carry provider money; got %+v", fact.Money)
	}
}

func TestDualPlaneTerminalInvariants_CompressionIngressPlanesAndDeliveredEgressOutput(t *testing.T) {
	t.Parallel()

	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{
			ID: "req-compress",
			Messages: []aipapi.Message{{
				Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("original long prompt")},
			}},
		},
		CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	holder.MergeFrontendIngressQuantities([]metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1000, Present: true},
	})
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call: aipapi.Call{
			ID: "req-compress",
			Messages: []aipapi.Message{{
				Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("compressed")},
			}},
		},
		AttemptID: "b-1", BLegID: "b-1", ALegID: "a-1",
		BackendID: "backend-1", Model: "model-1",
		CheckpointID: "be", StreamID: "be-stream",
		Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	holder.MergeBackendIngressQuantities("b-1", []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 200, Present: true},
	})

	feIn, _ := checkpoint.QuantityComponentValue(holder.FrontendIngress.Public.Quantities, metering.ComponentInputToken)
	beIn, _ := checkpoint.QuantityComponentValue(holder.BackendIngressFor("b-1").Public.Quantities, metering.ComponentInputToken)
	if feIn == beIn {
		t.Fatal("precondition: compression requires diverging FE-ingress vs BE-ingress input")
	}
	if feIn != 1000 || beIn != 200 {
		t.Fatalf("ingress inputs fe=%d be=%d", feIn, beIn)
	}

	const deliveredOut = 12
	provider := aipapi.ScopedUsageDelta{
		InputTokens: int(beIn), OutputTokens: 90, TotalTokens: int(beIn) + 90,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	delivered := aipapi.ScopedUsageDelta{
		InputTokens: 0, OutputTokens: deliveredOut, TotalTokens: deliveredOut,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneClientVisible, Source: aipapi.UsageSourceLocalTokenizer, Authority: aipapi.UsageAuthorityEstimated,
		},
	}
	beUsage := aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: int(beIn), OutputTokens: 90, TotalTokens: int(beIn) + 90,
		Accounting: provider.Accounting,
	}
	raw := aipapi.Event{
		Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{delivered, provider},
	}
	authorityEv := authorityUsageEvent([]aipapi.Event{raw})
	if authorityEv.OutputTokens != provider.OutputTokens {
		t.Fatalf("precondition: authorityUsageEvent prefers provider output; got %d", authorityEv.OutputTokens)
	}

	ctx := withMeteringHolder(context.Background(), holder)
	_, output := clientVisibleCount(0, deliveredOut)
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{
		call:   accountingapp.CountResult{},
		output: output,
	}, accountingstream.Config{})
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID:  "trace-compress",
			baseline: aipapi.Call{ID: "req-compress"},
		}), attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-1"}, routing.AttemptCandidate{}, authorityLifecycle{}),
		responsePipeline: &responsePipeline{customer: newCustomerEvidenceAccumulator()},
	}
	bindTestRuntimeOwners(stream, ex)
	stream.responsePipeline.customer.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "compressed-out"})
	stream.terminal.emitBackendEgressMeteringFactForAttempt(ctx, stream.attempt.snapshot(), metering.AttemptOutcomeWinner, metering.SurfacedYes, beUsage)
	customerEv := stream.responsePipeline.resolveCustomerUsage(ctx, authorityEv)
	stream.terminal.emitFrontendEgressMeteringFact(ctx, stream.facts.traceID, customerEv)

	if len(rec.facts) != 2 {
		t.Fatalf("facts=%d want BE+FE", len(rec.facts))
	}
	beFact, feFact := rec.facts[0], rec.facts[1]
	beFactIn, _ := checkpoint.QuantityComponentValue(beFact.Quantities, metering.ComponentInputToken)
	if beFactIn != beIn {
		t.Fatalf("operator BE egress input=%d want BE-ingress %d", beFactIn, beIn)
	}
	feOut, ok := checkpoint.QuantityComponentValue(feFact.Quantities, metering.ComponentOutputToken)
	if !ok {
		t.Fatal("customer FE egress must carry delivered output_token")
	}
	if feOut != deliveredOut {
		t.Fatalf("customer FE egress output=%d want delivered %d (provider was %d)", feOut, deliveredOut, provider.OutputTokens)
	}
}

func TestDualPlaneTerminalInvariants_ResponseFilteringCustomerOutputFromDelivered(t *testing.T) {
	t.Parallel()

	prov := &settleRecordingRequestProvider{id: "filter-settle"}
	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "filter-settle", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{ID: "req-filter"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:      aipapi.Call{ID: "req-filter", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}}},
		AttemptID: "b-1", BLegID: "b-1", CheckpointID: "be", StreamID: "be-stream",
		Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ctx, err = ex.admitRequestAuthorityOnce(ctx, "req-filter", "a-1", "trace-filter", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	const providerOut, deliveredOut = 90, 25
	provider := aipapi.ScopedUsageDelta{
		InputTokens: 10, OutputTokens: providerOut, TotalTokens: 10 + providerOut,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	delivered := aipapi.ScopedUsageDelta{
		InputTokens: 10, OutputTokens: deliveredOut, TotalTokens: 10 + deliveredOut,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneClientVisible, Source: aipapi.UsageSourceLocalTokenizer, Authority: aipapi.UsageAuthorityEstimated,
		},
	}
	raw := aipapi.Event{Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{delivered, provider}}
	authorityEv := authorityUsageEvent([]aipapi.Event{raw})
	if authorityEv.OutputTokens != providerOut {
		t.Fatalf("precondition: authorityUsageEvent prefers provider output; got %d", authorityEv.OutputTokens)
	}

	call, output := clientVisibleCount(delivered.InputTokens, deliveredOut)
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{call: call, output: output}, accountingstream.Config{})
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID:  "trace-filter",
			baseline: aipapi.Call{ID: "req-filter"},
		}), attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-1"}, routing.AttemptCandidate{}, authorityLifecycle{}),
		responsePipeline: &responsePipeline{customer: newCustomerEvidenceAccumulator()},
	}
	bindTestRuntimeOwners(stream, ex)
	stream.responsePipeline.customer.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "filtered-out"})
	stream.terminal.emitBackendEgressMeteringFactForAttempt(ctx, stream.attempt.snapshot(), metering.AttemptOutcomeWinner, metering.SurfacedYes, authorityEv)
	_ = stream.terminal.settleRequestAuthorityWithFrontendEgress(ctx, authorityEv, stream.facts.terminalFacts(), stream.responsePipeline)

	var beEgress *metering.Fact
	for i := range rec.facts {
		if rec.facts[i].Boundary == metering.BoundaryBackendEgress {
			beEgress = &rec.facts[i]
			break
		}
	}
	if beEgress == nil {
		t.Fatalf("missing BE egress fact among %d facts", len(rec.facts))
	}
	beOut, _ := checkpoint.QuantityComponentValue(beEgress.Quantities, metering.ComponentOutputToken)
	if beOut != providerOut {
		t.Fatalf("precondition: operator BE egress output=%d want provider %d", beOut, providerOut)
	}
	facts, _ := prov.lastFacts.Load().([]metering.Fact)
	if len(facts) != 1 {
		t.Fatalf("customer facts=%d", len(facts))
	}
	out, ok := checkpoint.QuantityComponentValue(facts[0].Quantities, metering.ComponentOutputToken)
	if !ok {
		t.Fatal("customer fact missing output_token")
	}
	if out != deliveredOut {
		t.Fatalf("customer output_token=%d want delivered/filtered %d (provider was %d)", out, deliveredOut, providerOut)
	}
}

func TestDualPlaneTerminalInvariants_ExplicitZeroCostOperatorPresentCustomerAbsent(t *testing.T) {
	t.Parallel()

	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{ID: "req-zero"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:      aipapi.Call{ID: "req-zero", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}}},
		AttemptID: "b-1", BLegID: "b-1", CheckpointID: "be", StreamID: "be-stream",
		Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	stream := &retryRecvStream{facts: testRecvTurnFacts(recvTurnFacts{
		traceID: "trace-zero",
	}), attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-1"}, routing.AttemptCandidate{}, authorityLifecycle{})}
	bindTestRuntimeOwners(stream, ex)

	t.Run("absent_cost_must_not_synthesize_present_money_on_customer", func(t *testing.T) {
		absent := aipapi.Event{
			Kind: aipapi.EventUsageDelta, InputTokens: 1, OutputTokens: 1, TotalTokens: 2,
			Currency: "USD", CostNanoUnits: 0, CostPresent: false,
		}
		fe, ok := stream.terminal.emitFrontendEgressMeteringFact(ctx, stream.facts.traceID, absent)
		if !ok {
			t.Fatal("expected FE fact")
		}
		if fe.Money != nil && fe.Money.Present {
			t.Fatalf("absent cost must not become Present money on customer fact; got %+v", fe.Money)
		}
	})

	t.Run("provider_explicit_zero_operator_keeps_customer_omits", func(t *testing.T) {
		zero := aipapi.Event{
			Kind: aipapi.EventUsageDelta, InputTokens: 1, OutputTokens: 1, TotalTokens: 2,
			Currency: "USD", CostNanoUnits: 0, CostPresent: true,
			CostSource: string(aipapi.UsageSourceProviderReported),
		}
		stream.terminal.emitBackendEgressMeteringFactForAttempt(ctx, stream.attempt.snapshot(), metering.AttemptOutcomeWinner, metering.SurfacedYes, zero)
		fe, ok := stream.terminal.emitFrontendEgressMeteringFact(ctx, stream.facts.traceID, zero)
		if !ok {
			t.Fatal("expected FE fact")
		}
		var be *metering.Fact
		for i := range rec.facts {
			if rec.facts[i].Boundary == metering.BoundaryBackendEgress {
				be = &rec.facts[i]
			}
		}
		if be == nil || be.Money == nil || !be.Money.Present || be.Money.NanoUnits != 0 {
			t.Fatalf("operator BE must retain authoritative zero money; got %+v", be)
		}
		if fe.Money != nil {
			t.Fatalf("customer FE must omit provider money including explicit zero; got %+v", fe.Money)
		}
	})
}

func TestDualPlaneTerminalInvariants_SequentialFailoverOperatorSettlePerIncurredAttempt(t *testing.T) {
	t.Parallel()

	attProv := &recordingAttemptProvider{id: "failover-att"}
	reqProv := &settleRecordingRequestProvider{id: "failover-req"}
	ex := &Executor{}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "failover-req", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: reqProv, Strength: authority.StrengthRequired,
		}},
	}
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "failover-att", Class: authoritycoord.AttemptPriorityHardSpend, Provider: attProv, Strength: authority.StrengthRequired,
		}},
	}

	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-failover", "a-1", "trace-failover", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit request: %v", err)
	}
	decision := accountingpreflight.Decision{
		Count: accountingapp.CountResult{InputTokens: 5, OutputTokens: 5, TotalTokens: 10, TotalTokensPresent: true},
	}
	failState, err := ex.admitAttemptAuthority(ctx, "trace-failover", "a-1", b2bua.BLegRecord{BLegID: "b-fail", Seq: 1},
		aipapi.Call{ID: "req-failover"},
		routing.AttemptCandidate{Key: "b-fail", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
		decision, false)
	if err != nil {
		t.Fatalf("admit fail: %v", err)
	}
	winState, err := ex.admitAttemptAuthority(ctx, "trace-failover", "a-1", b2bua.BLegRecord{BLegID: "b-win", Seq: 2},
		aipapi.Call{ID: "req-failover"},
		routing.AttemptCandidate{Key: "b-win", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
		decision, false)
	if err != nil {
		t.Fatalf("admit win: %v", err)
	}
	if attProv.admitCalls.Load() != 2 {
		t.Fatalf("admit calls=%d want 2", attProv.admitCalls.Load())
	}

	failLife := ex.newAttemptAuthorityLifecycle(failState, routing.AttemptCandidate{Key: "b-fail"})
	winLife := ex.newAttemptAuthorityLifecycle(winState, routing.AttemptCandidate{Key: "b-win"})
	failLife.backendAttempted.Store(true)
	winLife.backendAttempted.Store(true)
	usage := aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 5, OutputTokens: 5, TotalTokens: 10}

	// Sequential failover terminal path: incurred failed attempt settles; winner settles.
	failLife.finalizeIncurredOrRelease(ctx, usagelimit.ReleaseKindLosing, usage)
	if !winLife.Settle(ctx, usagelimit.SettlementKindFinal, usage, false) {
		t.Fatal("winner settle must apply")
	}
	_ = ex.settleRequestAuthority(ctx, nil)

	if reqProv.settleCalls.Load() != 1 {
		t.Fatalf("customer SettleRequest=%d want 1", reqProv.settleCalls.Load())
	}
	// Every incurred attempt settles operator authority (req 1.5, 1.7).
	if attProv.settleCalls.Load() != 2 {
		t.Fatalf("operator SettleAttempt=%d want 2 (one per incurred attempt)", attProv.settleCalls.Load())
	}
}

func TestDualPlaneTerminalInvariants_ParallelLoserOperatorSettlePerIncurredAttempt(t *testing.T) {
	t.Parallel()

	attProv := &recordingAttemptProvider{id: "parallel-att"}
	reqProv := &settleRecordingRequestProvider{id: "parallel-req"}
	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "parallel-req", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: reqProv, Strength: authority.StrengthRequired,
		}},
	}
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "parallel-att", Class: authoritycoord.AttemptPriorityHardSpend, Provider: attProv, Strength: authority.StrengthRequired,
		}},
	}

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{ID: "req-parallel"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b-loser", "b-winner"} {
		_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
			Call:      aipapi.Call{ID: "req-parallel", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(id)}}}},
			AttemptID: id, BLegID: id, CheckpointID: "be-" + id, StreamID: "be-" + id,
			Now: time.Unix(2, 0).UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ctx, err = ex.admitRequestAuthorityOnce(ctx, "req-parallel", "a-1", "trace-parallel", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit request: %v", err)
	}
	decision := accountingpreflight.Decision{
		Count: accountingapp.CountResult{InputTokens: 7, OutputTokens: 3, TotalTokens: 10, TotalTokensPresent: true},
	}
	loserState, err := ex.admitAttemptAuthority(ctx, "trace-parallel", "a-1", b2bua.BLegRecord{BLegID: "b-loser", Seq: 1},
		aipapi.Call{ID: "req-parallel"},
		routing.AttemptCandidate{Key: "b-loser", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
		decision, false)
	if err != nil {
		t.Fatalf("admit loser: %v", err)
	}
	winnerState, err := ex.admitAttemptAuthority(ctx, "trace-parallel", "a-1", b2bua.BLegRecord{BLegID: "b-winner", Seq: 2},
		aipapi.Call{ID: "req-parallel"},
		routing.AttemptCandidate{Key: "b-winner", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
		decision, false)
	if err != nil {
		t.Fatalf("admit winner: %v", err)
	}

	loserLife := ex.newAttemptAuthorityLifecycle(loserState, routing.AttemptCandidate{Key: "b-loser"})
	winnerLife := ex.newAttemptAuthorityLifecycle(winnerState, routing.AttemptCandidate{Key: "b-winner"})
	loserLife.backendAttempted.Store(true)
	winnerLife.backendAttempted.Store(true)
	usage := aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: 7, OutputTokens: 3, TotalTokens: 10,
		CostNanoUnits: 55, Currency: "USD", CostPresent: true, CostSource: string(aipapi.UsageSourceProviderReported),
	}
	loserObserved := aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: 11, OutputTokens: 2, TotalTokens: 13,
		CostNanoUnits: 77, Currency: "USD", CostPresent: true, CostSource: string(aipapi.UsageSourceProviderReported),
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}

	legs := []*parallelLeg{{
		authority: loserLife,
		bleg:      b2bua.BLegRecord{BLegID: "b-loser"},
	}}
	legs[0].observedUsage.Store(loserObserved)
	_ = ex.releaseLosers(ctx, nil, legs)
	if !winnerLife.Settle(ctx, usagelimit.SettlementKindFinal, usage, false) {
		t.Fatal("winner settle must apply")
	}
	stream := &retryRecvStream{facts: testRecvTurnFacts(recvTurnFacts{
		traceID: "trace-parallel",
	}), attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-winner"}, routing.AttemptCandidate{}, authorityLifecycle{})}
	bindTestRuntimeOwners(stream, ex)
	_ = stream.terminal.settleRequestAuthorityWithFrontendEgress(ctx, usage, stream.facts.terminalFacts(), stream.responsePipeline)

	if reqProv.settleCalls.Load() != 1 {
		t.Fatalf("customer settlements=%d want 1", reqProv.settleCalls.Load())
	}
	if attProv.releaseCalls.Load() != 0 {
		t.Fatalf("incurred loser must SettleAttempt, not ReleaseAttempt; releaseCalls=%d", attProv.releaseCalls.Load())
	}
	if attProv.settleCalls.Load() != 2 {
		t.Fatalf("operator SettleAttempt=%d want 2 (winner + incurred loser)", attProv.settleCalls.Load())
	}
	var loserBE *metering.Fact
	var feMoney *metering.MoneyObservation
	for i := range rec.facts {
		f := &rec.facts[i]
		if f.Boundary == metering.BoundaryBackendEgress && f.AttemptOutcome == metering.AttemptOutcomeLoser {
			loserBE = f
		}
		if f.Boundary == metering.BoundaryFrontendEgress {
			feMoney = f.Money
		}
	}
	if loserBE == nil {
		t.Fatal("loser BE egress fact missing")
	}
	in, ok := checkpoint.QuantityComponentValue(loserBE.Quantities, metering.ComponentInputToken)
	if !ok || in != 11 {
		t.Fatalf("loser BE input_token=%d ok=%v want observed 11", in, ok)
	}
	out, ok := checkpoint.QuantityComponentValue(loserBE.Quantities, metering.ComponentOutputToken)
	if !ok || out != 2 {
		t.Fatalf("loser BE output_token=%d ok=%v want observed 2", out, ok)
	}
	if loserBE.Money == nil || !loserBE.Money.Present || loserBE.Money.NanoUnits != 77 {
		t.Fatalf("loser BE money=%+v want observed 77 USD", loserBE.Money)
	}
	if feMoney != nil {
		t.Fatalf("customer FE must not inherit provider money; got %+v", feMoney)
	}
}

func TestDualPlaneTerminalInvariants_SwallowedFinalizeRetainsSeenUsage(t *testing.T) {
	t.Parallel()

	attProv := &recordingAttemptProvider{id: "swallow-att"}
	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "swallow-att", Class: authoritycoord.AttemptPriorityHardSpend, Provider: attProv, Strength: authority.StrengthRequired,
		}},
	}

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{ID: "req-swallow"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:      aipapi.Call{ID: "req-swallow", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}}},
		AttemptID: "b-sw", BLegID: "b-sw", CheckpointID: "be-sw", StreamID: "be-sw", Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	decision := accountingpreflight.Decision{
		Count: accountingapp.CountResult{InputTokens: 3, OutputTokens: 1, TotalTokens: 4, TotalTokensPresent: true},
	}
	state, err := ex.admitAttemptAuthority(ctx, "trace-sw", "a-1", b2bua.BLegRecord{BLegID: "b-sw", Seq: 1},
		aipapi.Call{ID: "req-swallow"},
		routing.AttemptCandidate{Key: "b-sw", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
		decision, false)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	life := ex.newAttemptAuthorityLifecycle(state, routing.AttemptCandidate{Key: "b-sw"})
	life.backendAttempted.Store(true)

	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID: "trace-sw",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-sw"}, routing.AttemptCandidate{}, life),
		responsePipeline: &responsePipeline{seenEvents: []aipapi.Event{{
			Kind: aipapi.EventUsageDelta, InputTokens: 9, OutputTokens: 4, TotalTokens: 13,
			CostNanoUnits: 33, Currency: "USD", CostPresent: true, CostSource: string(aipapi.UsageSourceProviderReported),
			Accounting: aipapi.UsageAccountingMetadata{
				Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
			},
		}}},
	}
	bindTestRuntimeOwners(stream, ex)
	usage := stream.responsePipeline.operatorUsageForFinalize()
	testAttemptSession(stream).authority.finalizeIncurredOrRelease(ctx, usagelimit.ReleaseKindSwallowed, usage)
	stream.terminal.emitBackendEgressMeteringFactForAttempt(ctx, stream.attempt.snapshot(), metering.AttemptOutcomeFailed, metering.SurfacedNo, usage)

	if attProv.settleCalls.Load() != 1 {
		t.Fatalf("swallowed incurred SettleAttempt=%d want 1", attProv.settleCalls.Load())
	}
	var be *metering.Fact
	for i := range rec.facts {
		if rec.facts[i].Boundary == metering.BoundaryBackendEgress {
			be = &rec.facts[i]
			break
		}
	}
	if be == nil {
		t.Fatal("BE egress fact missing")
	}
	in, ok := checkpoint.QuantityComponentValue(be.Quantities, metering.ComponentInputToken)
	if !ok || in != 9 {
		t.Fatalf("BE input_token=%d ok=%v want seen 9", in, ok)
	}
	if be.Money == nil || be.Money.NanoUnits != 33 {
		t.Fatalf("BE money=%+v want seen 33", be.Money)
	}
}

func TestDualPlaneTerminalInvariants_UnobservedIncurredKeepsEmptyShell(t *testing.T) {
	t.Parallel()

	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	holder := &checkpoint.RequestHolder{}
	_, err := holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:      aipapi.Call{ID: "req-empty", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}}},
		AttemptID: "b-empty", BLegID: "b-empty", CheckpointID: "be-empty", StreamID: "be-empty", Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ex.emitBackendEgressMeteringFact(ctx, "b-empty", metering.AttemptOutcomeLoser, metering.SurfacedNo, emptyOperatorUsageShell())
	if len(rec.facts) != 1 {
		t.Fatalf("facts=%d want 1", len(rec.facts))
	}
	if len(rec.facts[0].Quantities) != 0 || rec.facts[0].Money != nil {
		t.Fatalf("unobserved shell must omit quantities/money; got q=%v money=%+v", rec.facts[0].Quantities, rec.facts[0].Money)
	}
}

func TestDualPlaneTerminalInvariants_AuxiliaryCallParentScopeSeparatesPlanes(t *testing.T) {
	t.Parallel()

	rec := &recordingMeter{}
	var ex *Executor
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		State: corestate.NewMem(nil),
		Aux: auxreq.NewClient(func() auxreq.ExecutorRunner {
			return ex
		}),
	})
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, st)
	var openScope atomic.Value
	ex = TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = true
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.MeteringRecorder = rec
	ex.Now = func() time.Time { return time.Unix(2000, 0).UTC() }
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if sc, ok := scope.ScopeFromContext(ctx); ok {
					openScope.Store(sc)
				}
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventTextDelta, Delta: "aux-out"},
					{
						Kind: aipapi.EventUsageDelta, InputTokens: 2, OutputTokens: 1, TotalTokens: 3,
						CostNanoUnits: 99, Currency: "USD", CostPresent: true,
						CostSource: string(aipapi.UsageSourceProviderReported),
						Accounting: aipapi.UsageAccountingMetadata{
							Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
						},
					},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(3)

	parent := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("parent-user"),
		Origin:      scope.OriginClient,
	}
	ctx := scope.WithScope(context.Background(), parent)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("aux")},
		}},
	}
	stream, err := snap.Aux().Stream(ctx, auxiliary.Request{
		ParentTraceID: "trace-parent",
		Call:          call,
	})
	if err != nil {
		t.Fatalf("aux Stream: %v", err)
	}
	for {
		_, err := stream.Recv(ctx)
		if err != nil {
			break
		}
	}
	_ = stream.Close()

	got, ok := openScope.Load().(scope.PrincipalScopeView)
	if !ok {
		t.Fatal("expected auxiliary Open to observe parent-derived scope")
	}
	if got.Origin != scope.OriginInternal {
		t.Fatalf("aux origin=%q want internal", got.Origin)
	}
	if got.ParentTraceID.String() != "trace-parent" {
		t.Fatalf("ParentTraceID=%q want trace-parent", got.ParentTraceID.String())
	}

	var fe *metering.Fact
	for i := range rec.facts {
		if rec.facts[i].Boundary == metering.BoundaryFrontendEgress &&
			rec.facts[i].Perspective == metering.PerspectiveCustomer {
			fe = &rec.facts[i]
			break
		}
	}
	if fe == nil {
		t.Fatal("auxiliary child ResponseFinished terminalization must record customer FE egress fact")
	}
	if fe.Money != nil {
		t.Fatalf("auxiliary customer FE fact must omit provider money; got %+v", fe.Money)
	}
}

func TestDualPlaneTerminalInvariants_CaptureFrontendIngressPropagatesCallIDAsTrace(t *testing.T) {
	t.Parallel()

	const traceID = "trace-corr-real"
	call := aipapi.Call{
		ID:      traceID,
		Session: aipapi.SessionRef{ALegID: "a-leg-corr"},
	}
	ctx, holder, err := captureFrontendIngressBeforeSubmit(context.Background(), call, scope.PrincipalScopeView{}, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	_ = ctx
	if holder == nil || holder.FrontendIngress == nil {
		t.Fatal("expected FE ingress holder")
	}
	fe := holder.FrontendIngress.Public
	if fe.Correlation.RequestID != traceID || fe.Correlation.ALegID != "a-leg-corr" {
		t.Fatalf("correlation=%+v", fe.Correlation)
	}
	// Legal seam already receives call.ID (runtime trace/request identity). Task 1.4
	// must propagate it into Correlation.TraceID; today it stays empty.
	if fe.Correlation.TraceID != traceID {
		t.Fatalf("FE ingress TraceID=%q want call.ID %q (captureFrontendIngressBeforeSubmit omits TraceID)", fe.Correlation.TraceID, traceID)
	}

	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call: aipapi.Call{
			ID: traceID, Session: aipapi.SessionRef{ALegID: "a-leg-corr"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
		},
		AttemptID: "b-1", BLegID: "b-1", ALegID: "a-leg-corr",
		BackendID: "backend-1", Model: "model-1",
		CheckpointID: "be", StreamID: "be-ingress:b-1",
		Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	be := holder.BackendIngressFor("b-1").Public
	if be.Correlation.TraceID == fe.StreamID {
		t.Fatalf("backend TraceID must not reuse FE stream id %q", fe.StreamID)
	}
}
