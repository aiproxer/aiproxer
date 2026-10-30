package aipapi_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCanonicalTypesAreConstructible(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("ping")},
		}},
	}
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}

	ev := aipapi.Event{Kind: aipapi.EventResponseStarted}
	_ = ev

	caps := aipapi.CapabilitySet{Provides: []aipapi.Capability{aipapi.CapabilityStreaming}}
	_ = caps
}
