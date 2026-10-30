package testkit

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ProviderStub is a minimal no-op backend stand-in for conformance and wiring tests.
type ProviderStub struct {
	ID string
}

// Invoke returns an empty stream placeholder until the canonical stream engine exists.
func (ProviderStub) Invoke(context.Context, aipapi.Call) ([]aipapi.Event, error) {
	return nil, nil
}
