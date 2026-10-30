package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

type previewClampProvider struct {
	id            string
	previewCalls  atomic.Int32
	admitCalls    atomic.Int32
	releaseCalls  atomic.Int32
	clampValue    int64
	nonconverging bool
	previewHolds  bool
}

func (p *previewClampProvider) PreviewAttempt(_ context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	n := p.previewCalls.Add(1)
	d := authority.Decision{Kind: authority.DecisionAllow, ProviderID: p.id}
	if p.previewHolds {
		d.Reservations = []authority.Reservation{{Handle: "bad-hold", Kind: authority.ReservationQuota}}
		return d, nil
	}
	value := p.clampValue
	if p.nonconverging {
		value += int64(n)
	}
	if value > 0 {
		d.Clamps = []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: value}}
	}
	_ = in
	return d, nil
}

func (p *previewClampProvider) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	p.admitCalls.Add(1)
	return authority.Decision{Kind: authority.DecisionAllow, ProviderID: p.id}, nil
}

func (p *previewClampProvider) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (p *previewClampProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	p.releaseCalls.Add(1)
	return nil
}

func TestFinalBackendExposure_PreviewForbidsHolds(t *testing.T) {
	t.Parallel()
	bad := &previewClampProvider{id: "preview-holds", previewHolds: true}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "preview-holds", Class: authoritycoord.AttemptPriorityHardSpend, Provider: bad, Strength: authority.StrengthRequired,
		}},
	}
	_, err := coord.PreviewClamps(context.Background(), authority.AttemptAdmission{
		RequestID: "r", AttemptID: "a", BLegID: "b",
		Perspective: metering.PerspectiveOperator,
		Lifecycle:   metering.LifecycleBackendAttempt,
	})
	if err == nil {
		t.Fatal("expected preview holds rejection")
	}
	if bad.admitCalls.Load() != 0 {
		t.Fatal("preview must not call AdmitAttempt")
	}
}

func TestFinalBackendExposure_PreviewAppliesTokenClamp(t *testing.T) {
	t.Parallel()
	prov := &previewClampProvider{id: "preview-clamp", clampValue: 42}
	ex := &Executor{}
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "preview-clamp", Class: authoritycoord.AttemptPriorityHardSpend, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	call := aipapi.Call{ID: "req-preview"}
	clamps, ran, err := ex.previewAndApplyAttemptClamps(context.Background(), &call,
		routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}, "a-1", "b-1")
	if err != nil {
		t.Fatal(err)
	}
	if !ran || len(clamps) != 1 || clamps[0].Value != 42 {
		t.Fatalf("clamps=%v ran=%v", clamps, ran)
	}
	if call.Options.MaxOutputTokens == nil || *call.Options.MaxOutputTokens != 42 {
		t.Fatalf("MaxOutputTokens=%v", call.Options.MaxOutputTokens)
	}
	if prov.previewCalls.Load() < 1 || prov.previewCalls.Load() > 4 {
		t.Fatalf("preview calls=%d", prov.previewCalls.Load())
	}
	if prov.admitCalls.Load() != 0 {
		t.Fatal("preview must not admit")
	}
}

func TestFinalBackendExposure_PreviewNonConvergenceFails(t *testing.T) {
	t.Parallel()
	prov := &previewClampProvider{id: "nc", clampValue: 10, nonconverging: true}
	ex := &Executor{}
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "nc", Class: authoritycoord.AttemptPriorityHardSpend, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	call := aipapi.Call{ID: "req-nc"}
	_, _, err := ex.previewAndApplyAttemptClamps(context.Background(), &call,
		routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}, "a-1", "b-1")
	if err == nil {
		t.Fatal("expected non-convergence error")
	}
	if prov.previewCalls.Load() != 4 {
		t.Fatalf("preview calls=%d want 4", prov.previewCalls.Load())
	}
}

func TestFinalBackendExposure_UnknownOutputOmittedNotPresentZero(t *testing.T) {
	t.Parallel()
	qs := attemptRatingQuantities(accountingpreflight.Decision{Count: accountingapp.CountResult{InputTokens: 3}})
	if quantityComponentPresent(qs, metering.ComponentOutputToken) {
		t.Fatalf("unknown output must be omitted, got %v", qs)
	}
}

func TestFinalBackendExposure_ExactClampMatchNoPostAdmitMutate(t *testing.T) {
	t.Parallel()
	previewed := []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: 20}}
	state := attemptAuthorityState{
		admitClamps: []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: 20}},
	}
	frozen := aipapi.Call{ID: "r"}
	limit := 20
	frozen.Options.MaxOutputTokens = &limit
	live := aipapi.CloneCall(frozen)
	ex := &Executor{}
	if err := ex.enforcePostAdmitClamps(context.Background(), &live, frozen, previewed, true, state,
		routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}, 1); err != nil {
		t.Fatal(err)
	}
	if !maxOutputEqual(frozen, live) {
		t.Fatal("exact match must not mutate call")
	}
	state.admitClamps[0].Value = 7
	if err := ex.enforcePostAdmitClamps(context.Background(), &live, frozen, previewed, true, state,
		routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}, 1); err == nil {
		t.Fatal("mismatch must reject")
	}
}
