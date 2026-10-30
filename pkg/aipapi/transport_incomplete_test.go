package aipapi_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNegotiateTransport_skipsIncompleteInvocationMetadata(t *testing.T) {
	t.Parallel()

	res := aipapi.NegotiateTransport(aipapi.Invocation{}, nil, aipapi.TransportFallbackExact)
	if res.Kind != aipapi.NegotiationLossless {
		t.Fatalf("got %s", res.Kind)
	}
	if res.Selected != "" {
		t.Fatalf("selected = %q", res.Selected)
	}
}
