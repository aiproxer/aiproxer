package runtimebundle

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/billingbinding"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

type stubScreen struct{}

func (stubScreen) Check(context.Context, billing.CreditScreenInput) (billing.CreditScreenResult, error) {
	return billing.CreditScreenResult{Decision: billing.CreditAllow, StoreID: "store-test", AccountID: "acct-1"}, nil
}

type stubQuoter struct{}

func (stubQuoter) Quote(context.Context, economics.QuoteInput) (economics.ExposureQuote, error) {
	return economics.ExposureQuote{}, nil
}

type stubAdmitter struct{}

func (stubAdmitter) Admit(context.Context, billing.ExposureAdmissionInput) (billing.ExposureHandle, error) {
	return billing.ExposureHandle{StoreID: "store-test", BillingCallID: "call-1", ExposureID: "exp-1", QuoteID: "q-1"}, nil
}

type stubTerminal struct{}

func (stubTerminal) AppendTerminal(context.Context, billing.TerminalEnvelope) (billing.TerminalAck, error) {
	return billing.TerminalAck{StoreID: "store-test", EnvelopeID: "env-1", Revision: 1}, nil
}

type stubMaintenance struct {
	observed int
}

func (s *stubMaintenance) ObserveProviderMaintenance(context.Context, billing.MaintenanceUsage) error {
	if s == nil {
		return errors.New("nil maintenance observer")
	}
	s.observed++
	return nil
}

func testBinding() billing.Binding {
	return billing.Binding{
		ID:      "composition-test",
		Version: billing.BindingVersionV1,
		Scope: billing.BindingScope{
			StoreID: "store-test",
			Tariff:  economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-snap", Version: "v1"}},
			Policy:  economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-snap", Version: "v1"}, PolicyID: "policy-1"},
		},
		CreditScreen: stubScreen{},
		Quoter:       stubQuoter{},
		Admission:    stubAdmitter{},
		Terminal:     stubTerminal{},
	}
}

func TestExternalBillingCompositionUsesSameChokepoints(t *testing.T) {
	t.Parallel()
	maintenance := &stubMaintenance{}
	binding := testBinding()
	binding.Maintenance = maintenance
	adapter, err := billingbinding.NewAdapter(binding)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	var prod ProductionOptions
	gate, admission, sink, identity := adapter.Chokepoints()
	prod.BillingCreditGate = gate
	prod.BillingExposureAdmission = admission
	prod.BillingTerminalUsageSink = sink
	prod.BillingIdentity = identity
	// The exact assignment BuildWithBilling applies: the binding's optional
	// maintenance attachment lands on the existing production port unchanged.
	if observer := adapter.MaintenanceObserver(); observer != nil {
		prod.MaintenanceAccounting = observer
	}
	if prod.MaintenanceAccounting != billing.MaintenanceObserver(maintenance) {
		t.Fatalf("maintenance port = %#v, want the binding-attached instance", prod.MaintenanceAccounting)
	}
	if prod.BillingCreditGate == nil || prod.BillingExposureAdmission == nil || prod.BillingTerminalUsageSink == nil {
		t.Fatal("external binding must occupy the same runtime chokepoints as the reference composition")
	}
	if prod.BillingIdentity.AccountID == nil || prod.BillingIdentity.StoreID == nil {
		t.Fatal("external binding must resolve runtime billing identity")
	}
	if !externalBillingBindingConfigured(prod) {
		t.Fatal("external binding mode must be detected without an internal store")
	}
}

func TestExternalBillingCompositionRejectsPartialPorts(t *testing.T) {
	t.Parallel()
	var prod ProductionOptions
	adapter, err := billingbinding.NewAdapter(testBinding())
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	gate, admission, sink, identity := adapter.Chokepoints()
	_ = gate
	prod.BillingExposureAdmission = admission
	prod.BillingTerminalUsageSink = sink
	prod.BillingIdentity = identity
	if externalBillingBindingConfigured(prod) {
		t.Fatal("partial external ports must not count as a complete binding")
	}
}

func TestStockCompositionStaysNonMoney(t *testing.T) {
	t.Parallel()
	var prod ProductionOptions
	if externalBillingBindingConfigured(prod) {
		t.Fatal("empty production options must not detect external billing")
	}
}
