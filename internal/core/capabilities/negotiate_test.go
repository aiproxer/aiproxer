package capabilities_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestDefaultNegotiator_lossless(t *testing.T) {
	t.Parallel()

	var n capabilities.DefaultNegotiator
	res := n.Negotiate(
		context.Background(),
		[]aipapi.Capability{aipapi.CapabilityTools},
		aipapi.NewBackendCaps(aipapi.CapabilityTools, aipapi.CapabilityStreaming),
	)
	if res.Kind != aipapi.NegotiationLossless {
		t.Fatalf("got %s", res.Kind)
	}
}

func TestRequire_negotiatesCall(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools: []aipapi.ToolDef{{Name: "t"}},
	}

	res := capabilities.Require(context.Background(), call,
		aipapi.NewBackendCaps(aipapi.CapabilityTools), nil)
	if res.Kind != aipapi.NegotiationLossless {
		t.Fatalf("got %s", res.Kind)
	}
}

func TestRequire_rejectMissing(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools: []aipapi.ToolDef{{Name: "t"}},
	}

	res := capabilities.Require(context.Background(), call,
		aipapi.NewBackendCaps(aipapi.CapabilityStreaming), nil)
	if res.Kind != aipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
}
