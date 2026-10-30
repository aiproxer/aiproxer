package execbackend

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// WireBackend is the optional internal interface implemented by backends
// supporting direct wire execution (design section 9, Requirements 7, 8, 9).
type WireBackend = largebody.WireBackend

// WireOpener is the optional internal interface implemented by backends
// supporting opening direct wire attempts (design section 9, Requirement 8).
type WireOpener = largebody.WireOpener

// backendWireAdapter adapts a Backend value into largebody.WireBackend.
type backendWireAdapter struct {
	be Backend
}

func (a backendWireAdapter) ResolveWireRequest(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
	return EffectiveWireRequestSupport(ctx, a.be, facts, cand)
}

func (a backendWireAdapter) ResolveWireDomain(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
	return EffectiveWireDomainSupport(ctx, a.be, facts)
}

var _ largebody.WireBackend = backendWireAdapter{}

// AsWireBackend adapts be into largebody.WireBackend.
func (be Backend) AsWireBackend() largebody.WireBackend {
	return backendWireAdapter{be: be}
}

var _ largebody.WireBackendProvider = Backend{}

// EffectiveWireRequestSupport resolves pure exact wire compatibility for one
// candidate (design section 9, Requirements 8, 9).
//
// Rules (Task 8.1):
//   - Nil resolver / unknown => canonical (Compatible: false).
//   - Output can declare rewrite need only if supplied rewrite semantics support it;
//     declaring NeedsModelRewrite when facts.Rewrite does not support it fails closed to canonical.
//   - Pure, no I/O.
func EffectiveWireRequestSupport(
	ctx context.Context,
	be Backend,
	facts largebody.WireRequestFacts,
	cand routing.AttemptCandidate,
) largebody.WireRequestSupport {
	if ctx != nil && ctx.Err() != nil {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}
	if err := facts.Validate(largebody.SemanticFactBudget(ctx)); err != nil {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}

	var res largebody.WireRequestSupport
	switch {
	case be.ResolveWireRequest != nil:
		res = be.ResolveWireRequest(ctx, facts, cand)
	case be.WireBackend != nil:
		res = be.WireBackend.ResolveWireRequest(ctx, facts, cand)
	default:
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}

	// Output can declare rewrite need only if supplied rewrite semantics support it.
	if res.NeedsModelRewrite && !facts.Rewrite.NeedsModelRewrite() {
		res.Compatible = false
		res.NeedsModelRewrite = false
		res.Reason = largebody.WireSupportReasonRewriteUnsupported
		return res
	}

	if !res.Compatible {
		res.NeedsModelRewrite = false
		if res.Reason == largebody.WireSupportReasonNone {
			res.Reason = largebody.WireSupportReasonUnsupported
		}
		return res
	}

	res.Reason = largebody.WireSupportReasonNone
	return res
}

// EffectiveWireDomainSupport resolves pure late-route domain wire compatibility
// (Requirement 7, design section 9).
//
// Rules (Task 8.1):
// - Nil resolver / unknown => canonical (Compatible: false).
// - Universal domain requires AnyAcceptedModel; otherwise fails closed to canonical.
// - Pure, no I/O.
func EffectiveWireDomainSupport(
	ctx context.Context,
	be Backend,
	facts largebody.WireDomainFacts,
) largebody.WireDomainSupport {
	if ctx != nil && ctx.Err() != nil {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}
	if err := facts.Validate(largebody.SemanticFactBudget(ctx)); err != nil {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}

	var res largebody.WireDomainSupport
	switch {
	case be.ResolveWireDomain != nil:
		res = be.ResolveWireDomain(ctx, facts)
	case be.WireBackend != nil:
		res = be.WireBackend.ResolveWireDomain(ctx, facts)
	default:
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}

	if !res.Compatible {
		if res.Reason == largebody.WireSupportReasonNone {
			res.Reason = largebody.WireSupportReasonUnsupported
		}
		return res
	}

	// Universal domain requires backend universal model certification.
	if facts.UniversalModel && !res.AnyAcceptedModel {
		res.Compatible = false
		res.Reason = largebody.WireSupportReasonModelUnsupported
		return res
	}

	res.Reason = largebody.WireSupportReasonNone
	return res
}

// EffectiveWireOpen opens a wire attempt on be, using OpenWire or WireBackend (design section 9, Requirement 8).
func EffectiveWireOpen(ctx context.Context, be Backend, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if be.OpenWire != nil {
		return be.OpenWire(ctx, req)
	}
	if opener, ok := largebody.AsWireOpener(be.WireBackend); ok {
		return opener.OpenWire(ctx, req)
	}
	return nil, fmt.Errorf("execbackend: backend %q does not support OpenWire", req.Candidate.Primary.Backend)
}

// AsWireOpener adapts be into largebody.WireOpener if supported.
func (be Backend) AsWireOpener() largebody.WireOpener {
	if be.OpenWire != nil {
		return wireOpenerFunc(be.OpenWire)
	}
	if opener, ok := largebody.AsWireOpener(be.WireBackend); ok {
		return opener
	}
	return nil
}

type wireOpenerFunc func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error)

func (f wireOpenerFunc) OpenWire(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
	return f(ctx, req)
}
