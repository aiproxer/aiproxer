package authoritycoord_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

type previewOnlyProvider struct {
	calls int
	value int64
}

func (p *previewOnlyProvider) PreviewAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	p.calls++
	return authority.Decision{
		Kind:   authority.DecisionAllow,
		Clamps: []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: p.value}},
	}, nil
}

func (p *previewOnlyProvider) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (p *previewOnlyProvider) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (p *previewOnlyProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	return nil
}

type panicPreviewProvider struct{}

func (panicPreviewProvider) PreviewAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	panic("preview boom")
}

func (panicPreviewProvider) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (panicPreviewProvider) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (panicPreviewProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	return nil
}

type unknownClampPreviewProvider struct{}

func (unknownClampPreviewProvider) PreviewAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{
		Kind:   authority.DecisionAllow,
		Clamps: []authority.Clamp{{Kind: authority.ClampOther, Value: 1}},
	}, nil
}

func (unknownClampPreviewProvider) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (unknownClampPreviewProvider) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (unknownClampPreviewProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	return nil
}

func previewAdmission() authority.AttemptAdmission {
	return authority.AttemptAdmission{
		RequestID: "r", AttemptID: "a", BLegID: "b",
		Perspective: metering.PerspectiveOperator,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
		},
	}
}

func TestPreviewClamps_BoundedNoHolds(t *testing.T) {
	t.Parallel()
	p := &previewOnlyProvider{value: 10}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "p", Class: authoritycoord.AttemptPriorityHardSpend, Provider: p, Strength: authority.StrengthRequired,
		}},
	}
	clamps, err := coord.PreviewClamps(context.Background(), previewAdmission())
	if err != nil {
		t.Fatal(err)
	}
	if len(clamps) != 1 || clamps[0].Value != 10 {
		t.Fatalf("clamps=%v", clamps)
	}
	if p.calls != 1 {
		t.Fatalf("preview calls=%d want 1", p.calls)
	}
}

func TestPreviewClamps_IsolatesPanic(t *testing.T) {
	t.Parallel()
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "panic", Class: authoritycoord.AttemptPriorityHardSpend, Provider: panicPreviewProvider{}, Strength: authority.StrengthRequired,
		}},
	}
	_, err := coord.PreviewClamps(context.Background(), previewAdmission())
	if err == nil {
		t.Fatal("expected panic isolation error")
	}
}

func TestPreviewClamps_RejectsUnknownPriorityClass(t *testing.T) {
	t.Parallel()
	p := &previewOnlyProvider{value: 10}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "p", Class: authoritycoord.AttemptPriorityUnknown, Provider: p, Strength: authority.StrengthRequired,
		}},
	}
	_, err := coord.PreviewClamps(context.Background(), previewAdmission())
	if err == nil {
		t.Fatal("unknown attempt priority class must be rejected")
	}
	if !strings.Contains(err.Error(), "unknown attempt priority class") {
		t.Fatalf("got %v", err)
	}
	if p.calls != 0 {
		t.Fatalf("preview calls=%d want 0", p.calls)
	}
}

func TestPreviewClamps_RejectsUnknownClamp(t *testing.T) {
	t.Parallel()
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "unk", Class: authoritycoord.AttemptPriorityHardSpend, Provider: unknownClampPreviewProvider{}, Strength: authority.StrengthRequired,
		}},
	}
	_, err := coord.PreviewClamps(context.Background(), previewAdmission())
	if err == nil {
		t.Fatal("expected unknown clamp rejection")
	}
}

type mixedCurrencySpendProvider struct {
	currency string
	nano     int64
}

func (p mixedCurrencySpendProvider) PreviewAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{
		Kind: authority.DecisionAllow,
		Clamps: []authority.Clamp{{
			Kind: authority.ClampMaxSpend,
			Money: economics.Money{
				NanoUnits: p.nano,
				Currency:  p.currency,
				Present:   true,
			},
		}},
	}, nil
}

func (mixedCurrencySpendProvider) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (mixedCurrencySpendProvider) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (mixedCurrencySpendProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	return nil
}

func TestPreviewClamps_RejectsMixedCurrencySpend(t *testing.T) {
	t.Parallel()
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "usd", Class: authoritycoord.AttemptPriorityHardSpend, Provider: mixedCurrencySpendProvider{currency: "USD", nano: 100}, Strength: authority.StrengthRequired},
			{ID: "eur", Class: authoritycoord.AttemptPriorityHardSpend, Provider: mixedCurrencySpendProvider{currency: "EUR", nano: 50}, Strength: authority.StrengthRequired},
		},
	}
	_, err := coord.PreviewClamps(context.Background(), previewAdmission())
	if err == nil {
		t.Fatal("expected mixed-currency max_spend rejection")
	}
}
