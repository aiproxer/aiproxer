package aipapi_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestPreferredTransportMode_mapsDeliveryMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		delivery aipapi.DeliveryMode
		want     aipapi.TransportMode
	}{
		{aipapi.DeliveryModeStreaming, aipapi.TransportModeStreaming},
		{aipapi.DeliveryModeNonStreaming, aipapi.TransportModeNonStreaming},
	}
	for _, tc := range cases {
		if got := aipapi.PreferredTransportMode(tc.delivery); got != tc.want {
			t.Fatalf("PreferredTransportMode(%q) = %q, want %q", tc.delivery, got, tc.want)
		}
	}
}

func TestNegotiateTransport_exact_acceptsDeclaredSupport(t *testing.T) {
	t.Parallel()

	inv := aipapi.Invocation{
		Operation:    aipapi.OperationOpenAIChatCompletions,
		DeliveryMode: aipapi.DeliveryModeStreaming,
	}
	caps := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
	})

	res := aipapi.NegotiateTransport(inv, caps, aipapi.TransportFallbackExact)
	if res.Kind != aipapi.NegotiationLossless {
		t.Fatalf("got %s", res.Kind)
	}
	if res.Selected != aipapi.TransportModeStreaming {
		t.Fatalf("selected = %q", res.Selected)
	}
}

func TestNegotiateTransport_exact_rejectsMissingSupport(t *testing.T) {
	t.Parallel()

	inv := aipapi.Invocation{
		Operation:    aipapi.OperationOpenAIChatCompletions,
		DeliveryMode: aipapi.DeliveryModeNonStreaming,
	}
	caps := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
	})

	res := aipapi.NegotiateTransport(inv, caps, aipapi.TransportFallbackExact)
	if res.Kind != aipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
	err := res.Err()
	if err == nil {
		t.Fatal("expected reject error")
	}
	if !errors.Is(err, aipapi.ErrTransportReject) {
		t.Fatalf("expected ErrTransportReject: %v", err)
	}
}

func TestNegotiateTransport_compatibility_acceptsOmittedCaps(t *testing.T) {
	t.Parallel()

	inv := aipapi.Invocation{
		Operation:    aipapi.OperationOpenAIResponses,
		DeliveryMode: aipapi.DeliveryModeNonStreaming,
	}

	res := aipapi.NegotiateTransport(inv, nil, aipapi.TransportFallbackCompatibility)
	if res.Kind != aipapi.NegotiationLossless {
		t.Fatalf("got %s", res.Kind)
	}
	if res.Selected != aipapi.TransportModeNonStreaming {
		t.Fatalf("selected = %q", res.Selected)
	}
}

func TestNegotiateTransport_compatibility_acceptsUndeclaredOperation(t *testing.T) {
	t.Parallel()

	inv := aipapi.Invocation{
		Operation:    aipapi.OperationOpenAIResponses,
		DeliveryMode: aipapi.DeliveryModeStreaming,
	}
	caps := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
	})

	res := aipapi.NegotiateTransport(inv, caps, aipapi.TransportFallbackCompatibility)
	if res.Kind != aipapi.NegotiationLossless {
		t.Fatalf("got %s", res.Kind)
	}
}

func TestNegotiateTransport_compatibility_rejectsDeclaredUnsupportedMode(t *testing.T) {
	t.Parallel()

	inv := aipapi.Invocation{
		Operation:    aipapi.OperationOpenAIChatCompletions,
		DeliveryMode: aipapi.DeliveryModeNonStreaming,
	}
	caps := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
	})

	res := aipapi.NegotiateTransport(inv, caps, aipapi.TransportFallbackCompatibility)
	if res.Kind != aipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
	err := res.Err()
	if err == nil {
		t.Fatal("expected reject error")
	}
	if !errors.Is(err, aipapi.ErrTransportReject) {
		t.Fatalf("expected ErrTransportReject: %v", err)
	}
}

func TestBackendTransportCaps_noProviderSpecificIdentifiers(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
	})
	if !caps.Supports(aipapi.OperationOpenAIChatCompletions, aipapi.TransportModeStreaming) {
		t.Fatal("expected generic operation transport support")
	}
}
