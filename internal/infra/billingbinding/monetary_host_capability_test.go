package billingbinding

import (
	"context"
	"testing"

	runtimecore "github.com/aiproxer/aiproxer/internal/core/runtime"
	sdkbilling "github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// The monetary-host capability is published by the adapter, and the adapter
// exists only for a binding that passed the owning public validation:
// pkg/aipsdk/billing.Binding.Validate requires a valid binding id and
// version, a valid scope, and non-nil (typed-nil safe) credit screen, quoter,
// admission, terminal and lifecycle ports. A binding that fails that
// validation never becomes runtime ports, so it can never publish the
// capability a customer-funded operation depends on.

type capabilityScreen struct{}

func (capabilityScreen) Check(context.Context, sdkbilling.CreditScreenInput) (sdkbilling.CreditScreenResult, error) {
	return sdkbilling.CreditScreenResult{Decision: sdkbilling.CreditAllow, StoreID: "store-capability", AccountID: "acct-1"}, nil
}

type capabilityQuoter struct{}

func (capabilityQuoter) Quote(context.Context, economics.QuoteInput) (economics.ExposureQuote, error) {
	return economics.ExposureQuote{}, nil
}

type capabilityAdmitter struct{}

func (capabilityAdmitter) Admit(context.Context, sdkbilling.ExposureAdmissionInput) (sdkbilling.ExposureHandle, error) {
	return sdkbilling.ExposureHandle{StoreID: "store-capability", BillingCallID: "call-1", ExposureID: "exp-1", QuoteID: "q-1"}, nil
}

type capabilityTerminal struct{}

func (capabilityTerminal) AppendTerminal(context.Context, sdkbilling.TerminalEnvelope) (sdkbilling.TerminalAck, error) {
	return sdkbilling.TerminalAck{StoreID: "store-capability", EnvelopeID: "env-1", Revision: 1}, nil
}

func capabilityBinding() sdkbilling.Binding {
	return sdkbilling.Binding{
		ID:      "capability-binding",
		Version: sdkbilling.BindingVersionV1,
		Scope: sdkbilling.BindingScope{
			StoreID: "store-capability",
			Tariff:  economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-snap", Version: "v1"}},
			Policy:  economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-snap", Version: "v1"}, PolicyID: "policy-1"},
		},
		CreditScreen: capabilityScreen{},
		Quoter:       capabilityQuoter{},
		Admission:    capabilityAdmitter{},
		Terminal:     capabilityTerminal{},
	}
}

// durableTerminal is a binding terminal port that holds the obligation
// durably before it acknowledges the terminal append.
type durableTerminal struct {
	capabilityTerminal
	ready bool
}

func (d durableTerminal) TerminalHandoffReady() (bool, string) {
	if !d.ready {
		return false, "durable handoff is not ready"
	}
	return true, ""
}

// TestBindingWithoutADurableHandoffClaimsNoDurability proves that a
// validated binding claims a durable handoff only when its terminal port
// actually implements the capability: the binding validation alone (ports and
// lifecycle present) is not evidence that the obligation is held durably.
func TestBindingWithoutADurableHandoffClaimsNoDurability(t *testing.T) {
	t.Parallel()
	adapter, err := NewAdapter(capabilityBinding())
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	if got := adapter.Identity().MonetaryHost.TerminalHandoff; got != runtimecore.TerminalHandoffNone {
		t.Fatalf("a binding without a durable handoff claimed %q", got)
	}
	if ready, _ := adapter.TerminalHandoffReady(); ready {
		t.Fatal("a binding without a durable handoff reported itself ready")
	}
}

// TestBindingWithADurableHandoffForwardsTheReadiness proves that a binding
// whose terminal port holds the obligation durably states the durable
// handoff and forwards the live readiness to the runtime.
func TestBindingWithADurableHandoffForwardsTheReadiness(t *testing.T) {
	t.Parallel()
	binding := capabilityBinding()
	binding.Terminal = durableTerminal{capabilityTerminal: capabilityTerminal{}, ready: true}
	adapter, err := NewAdapter(binding)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	if got := adapter.Identity().MonetaryHost.TerminalHandoff; got != runtimecore.TerminalHandoffDurable {
		t.Fatalf("the durable handoff statement = %q, want %q", got, runtimecore.TerminalHandoffDurable)
	}
	ready, reason := adapter.TerminalHandoffReady()
	if !ready {
		t.Fatalf("the adapter did not forward the readiness: %s", reason)
	}
	binding.Terminal = durableTerminal{capabilityTerminal: capabilityTerminal{}, ready: false}
	notReady, err := NewAdapter(binding)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	if ready, _ := notReady.TerminalHandoffReady(); ready {
		t.Fatal("the adapter hid an unready durable handoff")
	}
}

// TestValidatedBindingPublishesTheMonetaryHostCapability proves that a
// complete, validated binding publishes the capability through the identity
// bundle the host composition hands to the runtime.
func TestValidatedBindingPublishesTheMonetaryHostCapability(t *testing.T) {
	t.Parallel()
	adapter, err := NewAdapter(capabilityBinding())
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	identity := adapter.Identity()
	if identity.MonetaryHost.Authority != runtimecore.MonetaryAuthorityExternalBinding {
		t.Fatalf("the monetary host authority = %q, want %q", identity.MonetaryHost.Authority, runtimecore.MonetaryAuthorityExternalBinding)
	}
	if identity.MonetaryHost.BindingID != "capability-binding" {
		t.Fatalf("the monetary host binding id = %q, want the validated binding id", identity.MonetaryHost.BindingID)
	}
}

// TestIncompleteBindingsPublishNoMonetaryHostCapability is the negative
// construction matrix. Every case fails the owning public validation, so no
// adapter exists, so no identity bundle can carry the capability: a customer
// funded operation can never treat these hosts as a monetary host.
func TestIncompleteBindingsPublishNoMonetaryHostCapability(t *testing.T) {
	t.Parallel()
	typedNilScreen := (*capabilityScreen)(nil)
	cases := []struct {
		name    string
		mutate  func(*sdkbilling.Binding)
		wantErr error
	}{
		{
			name:   "the zero binding has no identity",
			mutate: func(b *sdkbilling.Binding) { *b = sdkbilling.Binding{} },
		},
		{
			name:   "an unsupported version is refused",
			mutate: func(b *sdkbilling.Binding) { b.Version = sdkbilling.BindingVersionV1 + 1 },
		},
		{
			name:   "an invalid scope is refused",
			mutate: func(b *sdkbilling.Binding) { b.Scope = sdkbilling.BindingScope{} },
		},
		{
			name:   "a missing credit screen is refused",
			mutate: func(b *sdkbilling.Binding) { b.CreditScreen = nil },
		},
		{
			name:   "a missing quoter is refused",
			mutate: func(b *sdkbilling.Binding) { b.Quoter = nil },
		},
		{
			name:   "a missing admission port is refused",
			mutate: func(b *sdkbilling.Binding) { b.Admission = nil },
		},
		{
			name:   "a missing terminal port is refused",
			mutate: func(b *sdkbilling.Binding) { b.Terminal = nil },
		},
		{
			name:   "a typed nil credit screen is refused",
			mutate: func(b *sdkbilling.Binding) { b.CreditScreen = typedNilScreen },
		},
		{
			name:   "a typed nil terminal port is refused",
			mutate: func(b *sdkbilling.Binding) { b.Terminal = (*capabilityTerminal)(nil) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			binding := capabilityBinding()
			tc.mutate(&binding)
			adapter, err := NewAdapter(binding)
			if err == nil {
				t.Fatal("NewAdapter accepted an incomplete binding")
			}
			if adapter != nil {
				t.Fatal("NewAdapter published an adapter for an incomplete binding")
			}
		})
	}
}
