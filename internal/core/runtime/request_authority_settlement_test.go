package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type settleRecordingRequestProvider struct {
	id           string
	settleErr    error
	settleCalls  atomic.Int32
	releaseCalls atomic.Int32
	lastFacts    atomic.Value // []metering.Fact
	lastHandles  atomic.Value // []string
}

type failingMeteringRecorder struct {
	err error
	// failBoundary when set limits Append failures to that boundary (nil fails all).
	failBoundary *metering.Boundary
}

func (r *failingMeteringRecorder) Append(_ context.Context, fact metering.Fact) error {
	if r.err == nil {
		return nil
	}
	if r.failBoundary != nil && fact.Boundary != *r.failBoundary {
		return nil
	}
	return r.err
}

func (p *settleRecordingRequestProvider) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{
		Kind: authority.DecisionAllow,
		Reservations: []authority.Reservation{{
			Handle: p.id + "-h",
			Kind:   authority.ReservationQuota,
			Quantity: &metering.Quantity{
				Component: metering.ComponentInputToken,
				Unit:      metering.UnitToken,
				Value:     1,
				Present:   true,
			},
		}},
	}, nil
}

func (p *settleRecordingRequestProvider) SettleRequest(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	p.settleCalls.Add(1)
	p.lastFacts.Store(append([]metering.Fact(nil), in.Facts...))
	p.lastHandles.Store(append([]string(nil), in.Handles...))
	if p.settleErr != nil {
		return authority.Settlement{}, p.settleErr
	}
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (p *settleRecordingRequestProvider) ReleaseRequest(context.Context, authority.RequestRelease) error {
	p.releaseCalls.Add(1)
	return nil
}

func TestSettleRequestAuthority_PassesFrontendEgressFact(t *testing.T) {
	t.Parallel()

	prov := &settleRecordingRequestProvider{id: "usage-authority-request"}
	ex := &Executor{}
	ex.Now = func() time.Time { return time.Unix(50, 0).UTC() }
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "usage-authority-request", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         aipapi.Call{ID: "req-fe-settle"},
		CheckpointID: "fe",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ctx, err = ex.admitRequestAuthorityOnce(ctx, "req-fe-settle", "a-1", "trace-fe-settle", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	fact, ok := ex.emitFrontendEgressMeteringFact(ctx, "trace-fe-settle", aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: 2, OutputTokens: 5, TotalTokens: 7,
	})
	if !ok {
		t.Fatal("expected frontend-egress fact to be emitted")
	}
	if fact.Boundary != metering.BoundaryFrontendEgress {
		t.Fatalf("boundary=%s", fact.Boundary)
	}

	_ = ex.settleRequestAuthority(ctx, []metering.Fact{fact})
	if prov.settleCalls.Load() != 1 {
		t.Fatalf("SettleRequest calls=%d want 1", prov.settleCalls.Load())
	}
	gotFacts, _ := prov.lastFacts.Load().([]metering.Fact)
	if len(gotFacts) != 1 || gotFacts[0].FactID != fact.FactID {
		t.Fatalf("settle facts=%+v want emitted FE egress fact %q", gotFacts, fact.FactID)
	}
	if gotFacts[0].Boundary != metering.BoundaryFrontendEgress {
		t.Fatalf("settle fact boundary=%s", gotFacts[0].Boundary)
	}
}

func TestSettleRequestAuthority_FailureRemainsRetryable(t *testing.T) {
	t.Parallel()

	prov := &settleRecordingRequestProvider{id: "quota", settleErr: errors.New("settle unavailable")}
	ex := &Executor{}
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-retry", "a-1", "trace-retry", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	_ = ex.settleRequestAuthority(ctx, nil)
	st := requestAuthorityFrom(ctx)
	if st == nil {
		t.Fatal("expected request authority state")
	}
	if st.Settled || st.Released {
		t.Fatalf("settle failure must not mark Settled/Released; settled=%v released=%v", st.Settled, st.Released)
	}
	if prov.settleCalls.Load() != 1 {
		t.Fatalf("settle calls=%d want 1", prov.settleCalls.Load())
	}

	prov.settleErr = nil
	_ = ex.settleRequestAuthority(ctx, nil)
	if prov.settleCalls.Load() != 2 {
		t.Fatalf("retry settle calls=%d want 2 (failure must remain retryable)", prov.settleCalls.Load())
	}
	if !st.Settled || !st.Released {
		t.Fatalf("successful retry must mark settled/released; settled=%v released=%v", st.Settled, st.Released)
	}
}

func TestSettleRequestAuthority_AdvisoryFailureRemainsRetryable(t *testing.T) {
	t.Parallel()

	prov := &settleRecordingRequestProvider{id: "advisory", settleErr: errors.New("settle unavailable")}
	ex := &Executor{}
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{Slots: []authoritycoord.RequestSlot{{
		ID:              "advisory",
		Class:           authoritycoord.PriorityAdvisory,
		Provider:        prov,
		Strength:        authority.StrengthAdvisory,
		FailureBehavior: authority.FailureFailOpen,
	}}}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-advisory-retry", "a-1", "trace-advisory-retry", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	_ = ex.settleRequestAuthority(ctx, nil)
	st := requestAuthorityFrom(ctx)
	if st == nil || st.Settled || st.Released {
		t.Fatalf("advisory settle failure must retain retryable state: %+v", st)
	}
	prov.settleErr = nil
	_ = ex.settleRequestAuthority(ctx, nil)
	if prov.settleCalls.Load() != 2 {
		t.Fatalf("settle calls=%d want 2", prov.settleCalls.Load())
	}
	if !st.Settled || !st.Released {
		t.Fatalf("successful retry must finalize state: %+v", st)
	}
}

func TestSettleRequestAuthority_AppendFailureRemainsRetryable(t *testing.T) {
	t.Parallel()

	prov := &settleRecordingRequestProvider{id: "quota"}
	feEgress := metering.BoundaryFrontendEgress
	recorder := &failingMeteringRecorder{
		err:          errors.New("journal unavailable"),
		failBoundary: &feEgress,
	}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: recorder}}
	ex.Now = func() time.Time { return time.Unix(50, 0).UTC() }
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{ID: "req-append-retry"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ctx, err = ex.admitRequestAuthorityOnce(ctx, "req-append-retry", "a-1", "trace-append-retry", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	stream := &retryRecvStream{facts: testRecvTurnFacts(recvTurnFacts{
		traceID: "trace-append-retry",
	})}
	bindTestRuntimeOwners(stream, ex)
	usage := aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 2, OutputTokens: 3, TotalTokens: 5}

	_ = stream.terminal.settleRequestAuthorityWithFrontendEgress(ctx, usage, stream.facts.terminalFacts(), stream.responsePipeline)
	if prov.settleCalls.Load() != 0 {
		t.Fatalf("settle calls=%d want 0 while frontend-egress fact is not durable", prov.settleCalls.Load())
	}
	st := requestAuthorityFrom(ctx)
	if st == nil || st.Settled || st.Released {
		t.Fatalf("append failure must retain retryable authority state: %+v", st)
	}

	recorder.err = nil
	_ = stream.terminal.settleRequestAuthorityWithFrontendEgress(ctx, usage, stream.facts.terminalFacts(), stream.responsePipeline)
	if prov.settleCalls.Load() != 1 {
		t.Fatalf("settle calls=%d want 1 after durable append recovers", prov.settleCalls.Load())
	}
	if !st.Settled || !st.Released {
		t.Fatalf("successful retry must settle and release: %+v", st)
	}
}
