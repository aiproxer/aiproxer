// Package capabilities provides the core-owned capability negotiation boundary.
//
// It wraps the canonical aipapi negotiation types with a package-level home
// for future capability expansion, cross-plugin capability aggregation,
// and capability-aware routing integration.
package capabilities

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Negotiator compares required capabilities against a backend's declared set.
// The default implementation delegates to aipapi.Negotiate.
type Negotiator interface {
	Negotiate(ctx context.Context, required []aipapi.Capability, backend aipapi.BackendCaps) aipapi.NegotiationResult
}

// DefaultNegotiator delegates to the canonical aipapi.Negotiate function.
type DefaultNegotiator struct{}

var _ Negotiator = DefaultNegotiator{}

func (DefaultNegotiator) Negotiate(_ context.Context, required []aipapi.Capability, backend aipapi.BackendCaps) aipapi.NegotiationResult {
	return aipapi.Negotiate(required, backend)
}

// Require derives required capabilities from a canonical call and negotiates
// against the given backend capabilities.
func Require(ctx context.Context, c aipapi.Call, backend aipapi.BackendCaps, n Negotiator) aipapi.NegotiationResult {
	if n == nil {
		n = DefaultNegotiator{}
	}
	required := aipapi.RequiredCapabilities(c)
	return n.Negotiate(ctx, required, backend)
}
