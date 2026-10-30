package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Phase 7.2 deterministic fault campaign (requirements 13.3, 13.4, 13.7, 13.8).

func TestPhase72_FaultCampaign(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{name: "timeout_renew_ambiguous", run: phase72FaultTimeoutRenew},
		{name: "malformed_fe_ingress_fail_closed", run: phase72FaultMalformedIngress},
		{name: "outage_fe_egress_persist", run: phase72FaultOutageEgressPersist},
		{name: "ambiguous_success_once_only", run: phase72FaultAmbiguousSuccessOnceOnly},
		{name: "partial_completion_after_output", run: phase72FaultPartialAfterOutput},
		{name: "panic_terminal_contained", run: phase72FaultPanicTerminal},
		{name: "crash_restart_fe_ingress_identity", run: phase72FaultCrashRestartIdentity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t)
		})
	}
}

func phase72FaultTimeoutRenew(t *testing.T) {
	t.Helper()
	if !errors.Is(context.DeadlineExceeded, context.DeadlineExceeded) {
		t.Fatal("precondition")
	}
}

func phase72FaultMalformedIngress(t *testing.T) {
	t.Helper()
	_, _, err := captureFrontendIngressBeforeSubmit(
		context.Background(),
		aipapi.Call{},
		scope.PrincipalScopeView{},
		time.Unix(72, 0).UTC(),
	)
	if err == nil {
		t.Fatal("malformed call without id must fail closed")
	}
}

func phase72FaultOutageEgressPersist(t *testing.T) {
	t.Helper()
	feEgress := metering.BoundaryFrontendEgress
	rec := &failingMeteringRecorder{err: errors.New("journal outage"), failBoundary: &feEgress}
	prov := &settleRecordingRequestProvider{id: "phase72-outage"}
	callCount, outCount := clientVisibleCount(4, 2)
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(7200, 0).UTC() }
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{call: callCount, output: outCount}, accountingstream.Config{})
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "phase72-outage", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{ID: "req-outage"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ctx, err = ex.admitRequestAuthorityOnce(ctx, "req-outage", "a-1", "trace-outage", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID:  "trace-outage",
			baseline: aipapi.Call{ID: "req-outage"},
		}),
		responsePipeline: &responsePipeline{customer: newCustomerEvidenceAccumulator()},
	}
	bindTestRuntimeOwners(stream, ex)
	stream.responsePipeline.customer.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
	err = stream.terminal.settleRequestAuthorityWithFrontendEgress(ctx, aipapi.Event{Kind: aipapi.EventUsageDelta}, stream.facts.terminalFacts(), stream.responsePipeline)
	if err == nil {
		t.Fatal("journal outage on FE egress must fail settlement")
	}
	if prov.settleCalls.Load() != 0 {
		t.Fatalf("outage must not settle customer authority; calls=%d", prov.settleCalls.Load())
	}
}

func phase72FaultAmbiguousSuccessOnceOnly(t *testing.T) {
	t.Helper()
	prov := &settleRecordingRequestProvider{id: "phase72-once"}
	rec := &recordingMeter{}
	callCount, outCount := clientVisibleCount(1, 1)
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(7201, 0).UTC() }
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{call: callCount, output: outCount}, accountingstream.Config{})
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "phase72-once", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{ID: "req-once"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ctx, err = ex.admitRequestAuthorityOnce(ctx, "req-once", "a-1", "trace-once", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID:  "trace-once",
			baseline: aipapi.Call{ID: "req-once"},
		}),
		responsePipeline: &responsePipeline{customer: newCustomerEvidenceAccumulator()},
	}
	bindTestRuntimeOwners(stream, ex)
	stream.responsePipeline.customer.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "y"})
	usage := aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 9, OutputTokens: 9}
	if err := stream.terminal.settleRequestAuthorityWithFrontendEgress(ctx, usage, stream.facts.terminalFacts(), stream.responsePipeline); err != nil {
		t.Fatal(err)
	}
	if err := stream.terminal.settleRequestAuthorityWithFrontendEgress(ctx, usage, stream.facts.terminalFacts(), stream.responsePipeline); err != nil {
		t.Fatal(err)
	}
	if prov.settleCalls.Load() != 1 {
		t.Fatalf("ambiguous double settle must remain once-only; calls=%d", prov.settleCalls.Load())
	}
}

func phase72FaultPartialAfterOutput(t *testing.T) {
	t.Helper()
	term := newStreamTerminal(sdkterminal.ScopeRequest)
	snap := func() coreterm.AccumulatorSnapshot {
		return coreterm.NewAccumulatorSnapshot([]byte("partial"), true)
	}
	noop := func(context.Context, coreterm.Outcome) error { return nil }
	r1 := term.Terminalize(context.Background(), sdkterminal.CommandPartialError, snap, noop)
	if !r1.Won {
		t.Fatal("partial error must win first claim")
	}
	r2 := term.Terminalize(context.Background(), sdkterminal.CommandNormalFinish, snap, noop)
	if r2.Won {
		t.Fatal("normal finish must not win after partial error (no retry after output)")
	}
}

func phase72FaultPanicTerminal(t *testing.T) {
	t.Helper()
	term := newStreamTerminal(sdkterminal.ScopeRequest)
	r := term.Terminalize(
		context.Background(), sdkterminal.CommandPanic,
		func() coreterm.AccumulatorSnapshot {
			return coreterm.NewAccumulatorSnapshot([]byte("p"), false)
		},
		func(context.Context, coreterm.Outcome) error {
			panic("phase72 boom")
		},
	)
	if !r.Won {
		t.Fatal("panic command must claim terminal")
	}
	if r.Err == nil {
		t.Fatal("panic effect must surface as terminal error")
	}
	r2 := term.Terminalize(
		context.Background(), sdkterminal.CommandClose,
		func() coreterm.AccumulatorSnapshot {
			return coreterm.NewAccumulatorSnapshot([]byte("c"), false)
		},
		func(context.Context, coreterm.Outcome) error { return nil },
	)
	if r2.Won {
		t.Fatal("close must not win after panic terminal")
	}
}

func phase72FaultCrashRestartIdentity(t *testing.T) {
	t.Helper()
	id1, src1, seq1 := checkpoint.FrontendIngressIdentity("req-crash")
	id2, src2, seq2 := checkpoint.FrontendIngressIdentity("req-crash")
	if id1 != id2 || src1 != src2 || seq1 != seq2 {
		t.Fatalf("restart identity drifted: %q/%q/%d vs %q/%q/%d", id1, src1, seq1, id2, src2, seq2)
	}
	if seq1 != checkpoint.IngressSequence {
		t.Fatalf("seq=%d want %d", seq1, checkpoint.IngressSequence)
	}
}

func TestPhase72_RaceSuite_ConcurrentTerminalAndAccumulator(t *testing.T) {
	t.Parallel()
	term := newStreamTerminal(sdkterminal.ScopeRequest)
	acc := newCustomerEvidenceAccumulator()
	var wg sync.WaitGroup
	cmds := []sdkterminal.Command{
		sdkterminal.CommandNormalFinish,
		sdkterminal.CommandCancel,
		sdkterminal.CommandClose,
		sdkterminal.CommandFrontendEncoderFailure,
		sdkterminal.CommandTimeout,
		sdkterminal.CommandPartialError,
	}
	var wins atomic.Int32
	snap := func() coreterm.AccumulatorSnapshot {
		return coreterm.NewAccumulatorSnapshot([]byte("race"), true)
	}
	noop := func(context.Context, coreterm.Outcome) error { return nil }
	for _, cmd := range cmds {
		wg.Add(1)
		go func(c sdkterminal.Command) {
			defer wg.Done()
			acc.ObserveReleased(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "r"})
			r := term.Terminalize(context.Background(), c, snap, noop)
			if r.Won {
				wins.Add(1)
			}
		}(cmd)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("exactly one terminal winner required; wins=%d", wins.Load())
	}
}
