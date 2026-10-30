package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type capturingStreamCounter struct {
	call        accountingapp.CountResult
	output      accountingapp.CountResult
	lastText    string
	lastEventN  int
	outputCalls int
}

func (c *capturingStreamCounter) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return c.call, nil
}

func (c *capturingStreamCounter) CountOutput(_ context.Context, in accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	c.outputCalls++
	c.lastText = in.Text
	c.lastEventN = len(in.Events)
	return c.output, nil
}

func clientVisibleCount(in, out int) (accountingapp.CountResult, accountingapp.CountResult) {
	meta := aipapi.UsageAccountingMetadata{
		Plane: aipapi.UsagePlaneClientVisible, Source: aipapi.UsageSourceLocalTokenizer, Authority: aipapi.UsageAuthorityEstimated,
	}
	return accountingapp.CountResult{InputTokens: in, TotalTokens: in, Accounting: meta},
		accountingapp.CountResult{OutputTokens: out, TotalTokens: out, Accounting: meta}
}

func TestCustomerEvidenceAccumulator_ObservesReleasedContentOnly(t *testing.T) {
	t.Parallel()

	acc := newCustomerEvidenceAccumulator()
	acc.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"})
	acc.ObserveReleased(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "think"})
	acc.ObserveReleased(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, Delta: "{\"a\":1}"})
	acc.ObserveReleased(aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 9, OutputTokens: 9})
	acc.ObserveReleased(aipapi.Event{Kind: aipapi.EventWarning, WarningCode: "keepalive"})
	acc.ObserveReleased(aipapi.Event{Kind: aipapi.EventResponseFinished})

	text, reasoning, toolArgs, events := acc.Snapshot()
	if text != "hello" || reasoning != "think" || toolArgs != "{\"a\":1}" || events != 3 {
		t.Fatalf("snapshot text=%q reasoning=%q tool=%q events=%d", text, reasoning, toolArgs, events)
	}
	content := acc.contentEvents()
	if len(content) != 3 {
		t.Fatalf("contentEvents=%d want 3", len(content))
	}
	// The accumulator retains a compact kind/delta record per released content
	// event, so contentEvents must rebuild canonical events carrying exactly the
	// two observable fields and nothing inherited from the released event.
	want := []struct {
		kind  aipapi.EventKind
		delta string
	}{
		{aipapi.EventTextDelta, "hello"},
		{aipapi.EventReasoningDelta, "think"},
		{aipapi.EventToolCallArgsDelta, "{\"a\":1}"},
	}
	for i, w := range want {
		got := content[i]
		if got.Kind != w.kind || got.Delta != w.delta || got.InputTokens != 0 || got.OutputTokens != 0 || len(got.UsageScopes) != 0 {
			t.Fatalf("contentEvents[%d] = kind=%q delta=%q, want kind=%q delta=%q and no other carried payload",
				i, got.Kind, got.Delta, w.kind, w.delta)
		}
	}
}

func TestCustomerEvidenceAccumulator_MarkSettledOnce(t *testing.T) {
	t.Parallel()
	acc := newCustomerEvidenceAccumulator()
	if !acc.MarkSettled() {
		t.Fatal("first settle must succeed")
	}
	if acc.MarkSettled() {
		t.Fatal("second settle must be rejected")
	}
}

func TestCustomerPlaneUsageEvent_DropsProviderScopesAndMoney(t *testing.T) {
	t.Parallel()

	ev := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		CostNanoUnits: 99,
		Currency:      "USD",
		CostPresent:   true,
		CostSource:    string(aipapi.UsageSourceProviderReported),
		UsageScopes: []aipapi.ScopedUsageDelta{
			{
				InputTokens: 40, OutputTokens: 12, TotalTokens: 52,
				Accounting: aipapi.UsageAccountingMetadata{
					Plane: aipapi.UsagePlaneClientVisible, Source: aipapi.UsageSourceLocalTokenizer, Authority: aipapi.UsageAuthorityEstimated,
				},
			},
			{
				InputTokens: 200, OutputTokens: 80, TotalTokens: 280,
				Accounting: aipapi.UsageAccountingMetadata{
					Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
				},
			},
		},
	}
	got := customerPlaneUsageEvent(ev)
	if got.InputTokens != 40 || got.OutputTokens != 12 {
		t.Fatalf("customer plane tokens in=%d out=%d", got.InputTokens, got.OutputTokens)
	}
	if got.CostPresent || got.CostNanoUnits != 0 || got.Currency != "" {
		t.Fatalf("customer plane must strip money; got %+v", got)
	}
}

func TestCustomerSettlement_AccumulatorDrivesOutputWhenSettleInputIsProviderAuthority(t *testing.T) {
	t.Parallel()

	prov := &settleRecordingRequestProvider{id: "cust-acc-auth"}
	rec := &recordingMeter{}
	call, output := clientVisibleCount(40, 12)
	counter := &capturingStreamCounter{call: call, output: output}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.StreamUsage = accountingstream.New(counter, accountingstream.Config{})
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "cust-acc-auth", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{ID: "req-acc-auth"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ctx, err = ex.admitRequestAuthorityOnce(ctx, "req-acc-auth", "a-1", "trace-acc-auth", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	provider := aipapi.ScopedUsageDelta{
		InputTokens: 200, OutputTokens: 80, TotalTokens: 280,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	raw := aipapi.Event{Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{provider}}
	authorityEv := authorityUsageEvent([]aipapi.Event{raw})
	if authorityEv.InputTokens != provider.InputTokens {
		t.Fatalf("precondition: authority input=%d", authorityEv.InputTokens)
	}

	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID:  "trace-acc-auth",
			baseline: aipapi.Call{ID: "req-acc-auth"},
		}),
		responsePipeline: &responsePipeline{customer: newCustomerEvidenceAccumulator()},
	}
	bindTestRuntimeOwners(stream, ex)
	stream.responsePipeline.customer.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "released-body"})
	if err := stream.terminal.settleRequestAuthorityWithFrontendEgress(ctx, authorityEv, stream.facts.terminalFacts(), stream.responsePipeline); err != nil {
		t.Fatalf("settle: %v", err)
	}

	if counter.outputCalls < 1 || counter.lastText != "released-body" {
		t.Fatalf("CountOutput must see accumulator text; calls=%d text=%q", counter.outputCalls, counter.lastText)
	}
	facts, _ := prov.lastFacts.Load().([]metering.Fact)
	if len(facts) != 1 {
		t.Fatalf("facts=%d", len(facts))
	}
	in, _ := checkpoint.QuantityComponentValue(facts[0].Quantities, metering.ComponentInputToken)
	out, _ := checkpoint.QuantityComponentValue(facts[0].Quantities, metering.ComponentOutputToken)
	if in != 40 || out != 12 {
		t.Fatalf("customer quantities in=%d out=%d want 40/12 from StreamUsage (not authority %d/%d)",
			in, out, authorityEv.InputTokens, authorityEv.OutputTokens)
	}
}

func TestCustomerSettlement_CountOutputUsesTextNotReasoningBuffer(t *testing.T) {
	t.Parallel()

	call, output := clientVisibleCount(1, 2)
	counter := &capturingStreamCounter{call: call, output: output}
	ex := &Executor{}
	ex.StreamUsage = accountingstream.New(counter, accountingstream.Config{})
	stream := &retryRecvStream{responsePipeline: &responsePipeline{customer: newCustomerEvidenceAccumulator()}, facts: testRecvTurnFacts(recvTurnFacts{
		baseline: aipapi.Call{ID: "c"},
	})}
	bindTestRuntimeOwners(stream, ex)
	stream.responsePipeline.customer.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ab"})
	stream.responsePipeline.customer.ObserveReleased(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "REASONING-NOT-IN-TEXT"})
	stream.responsePipeline.customer.ObserveReleased(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, Delta: "{\"x\":1}"})

	ev := stream.responsePipeline.resolveCustomerUsage(context.Background(), aipapi.Event{})
	if counter.lastText != "ab" {
		t.Fatalf("OutputText must be released text only; got %q", counter.lastText)
	}
	if counter.lastEventN != 3 {
		t.Fatalf("CountOutput Events must include text+reasoning+tool; got %d", counter.lastEventN)
	}
	if ev.OutputTokens != 2 {
		t.Fatalf("output=%d want stub CountOutput 2", ev.OutputTokens)
	}
}

func TestCustomerSettlement_UsesAccumulatorOrderingAndOnceOnly(t *testing.T) {
	t.Parallel()

	prov := &settleRecordingRequestProvider{id: "cust-acc-once"}
	rec := &recordingMeter{}
	call, output := clientVisibleCount(2, 4)
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{call: call, output: output}, accountingstream.Config{})
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "cust-acc-once", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{ID: "req-acc"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ctx, err = ex.admitRequestAuthorityOnce(ctx, "req-acc", "a-1", "trace-acc", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	stream := &retryRecvStream{facts: testRecvTurnFacts(recvTurnFacts{
		traceID:  "trace-acc",
		baseline: aipapi.Call{ID: "req-acc"},
	}), responsePipeline: &responsePipeline{customer: newCustomerEvidenceAccumulator()}}
	bindTestRuntimeOwners(stream, ex)
	stream.responsePipeline.customer.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ab"})
	stream.responsePipeline.customer.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "cd"})

	authorityEv := aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: 99, OutputTokens: 99, TotalTokens: 198,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	if err := stream.terminal.settleRequestAuthorityWithFrontendEgress(ctx, authorityEv, stream.facts.terminalFacts(), stream.responsePipeline); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if err := stream.terminal.settleRequestAuthorityWithFrontendEgress(ctx, authorityEv, stream.facts.terminalFacts(), stream.responsePipeline); err != nil {
		t.Fatalf("settle replay: %v", err)
	}

	if prov.settleCalls.Load() != 1 {
		t.Fatalf("SettleRequest calls=%d want 1", prov.settleCalls.Load())
	}
	text, _, _, events := stream.responsePipeline.customer.Snapshot()
	if text != "abcd" || events != 2 {
		t.Fatalf("accumulator text=%q events=%d", text, events)
	}
	var fe *metering.Fact
	for i := range rec.facts {
		if rec.facts[i].Boundary == metering.BoundaryFrontendEgress {
			fe = &rec.facts[i]
		}
	}
	if fe == nil || fe.Money != nil {
		t.Fatalf("FE fact=%+v", fe)
	}
	out, ok := checkpoint.QuantityComponentValue(fe.Quantities, metering.ComponentOutputToken)
	if !ok || out != 4 {
		t.Fatalf("customer output=%d ok=%v", out, ok)
	}
}

func TestRememberClientEvent_ObservesGateReplacementPath(t *testing.T) {
	t.Parallel()

	stream := &retryRecvStream{responsePipeline: &responsePipeline{customer: newCustomerEvidenceAccumulator()}}
	// Mirrors handleGatedPath / popGateDrainHead: observe only the event actually returned.
	stream.responsePipeline.rememberClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "gated-out"})
	stream.responsePipeline.rememberClientEvent(aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 1})
	text, _, _, n := stream.responsePipeline.customer.Snapshot()
	if text != "gated-out" || n != 1 {
		t.Fatalf("gated release text=%q n=%d", text, n)
	}
	if stream.responsePipeline.releasedOutputText() != "gated-out" {
		t.Fatalf("releasedOutputText=%q", stream.responsePipeline.releasedOutputText())
	}
}

func TestCustomerEvidenceAccumulator_SnapshotTracksLaterDeltas(t *testing.T) {
	t.Parallel()

	acc := newCustomerEvidenceAccumulator()
	acc.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ab"})
	early, _, _, earlyEvents := acc.Snapshot()
	acc.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "cd"})
	late, _, _, lateEvents := acc.Snapshot()
	if early != "ab" || earlyEvents != 1 {
		t.Fatalf("first snapshot text=%q events=%d", early, earlyEvents)
	}
	if late != "abcd" || lateEvents != 2 {
		t.Fatalf("second snapshot text=%q events=%d", late, lateEvents)
	}
	// The first snapshot stays the value it observed: a later delta invalidates
	// the derived concatenation for the next reader, never rewrites the string
	// already handed out.
	if early != "ab" {
		t.Fatalf("first snapshot changed after further deltas: %q", early)
	}
	acc.resetContent()
	if text, _, _, events := acc.Snapshot(); text != "" || events != 0 {
		t.Fatalf("reset snapshot text=%q events=%d", text, events)
	}
}

func TestCustomerEvidenceAccumulator_AttemptTextTracksBoundary(t *testing.T) {
	t.Parallel()

	acc := newCustomerEvidenceAccumulator()
	acc.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "prior"})
	acc.markAttemptBoundary()
	acc.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "-current"})
	if got := acc.attemptText(); got != "-current" {
		t.Fatalf("attempt text=%q", got)
	}
	if text, _, _, _ := acc.Snapshot(); text != "prior-current" {
		t.Fatalf("cumulative text=%q", text)
	}
	acc.resetContent()
	if got := acc.attemptText(); got != "" {
		t.Fatalf("attempt text after reset=%q", got)
	}
}

var benchmarkEvidenceText string

// BenchmarkCustomerEvidenceAccumulator_StreamText measures the per-delta record
// and the settlement-time view for two realistic stream shapes: token-sized
// deltas and the 64 KiB chunks of the multi-MiB canonical accounting workload.
func BenchmarkCustomerEvidenceAccumulator_StreamText(b *testing.B) {
	for _, tc := range []struct {
		name   string
		deltas int
		size   int
	}{
		{name: "token_deltas", deltas: 4096, size: 16},
		{name: "chunked_64KiB", deltas: 80, size: 64 << 10},
	} {
		b.Run(tc.name, func(b *testing.B) {
			chunk := strings.Repeat("x", tc.size)
			b.SetBytes(int64(tc.deltas * tc.size))
			b.ReportAllocs()
			for b.Loop() {
				acc := newCustomerEvidenceAccumulator()
				for range tc.deltas {
					acc.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: chunk})
				}
				benchmarkEvidenceText, _, _, _ = acc.Snapshot()
			}
		})
	}
}
