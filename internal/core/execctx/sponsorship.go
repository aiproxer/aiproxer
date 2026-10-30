package execctx

import (
	"context"
	"slices"
	"strings"
)

// SponsorshipMode names the operator-declared funding decision for background
// work whose provider call is payable.
//
// The mode says whether a leg is charged to a customer or is an
// operator-sponsored leg that no customer pays for. It never names an account:
// the account is the one the host's identity seam resolves from the trusted
// scope and freezes at the trusted boundary. The mode is bound by the
// operator composition and never by a request, a canonical extension, a model,
// a project label, a memory document, a tool argument or an MCP path.
type SponsorshipMode string

const (
	// SponsorshipModeUndeclared is the default: the operator declared no
	// funding decision, so the background work is refused before any provider
	// dispatch. The proxy never silently enables a free provider call.
	SponsorshipModeUndeclared SponsorshipMode = ""
	// SponsorshipModeCustomerFunded charges the leg to the account the host's
	// identity seam resolved and froze. The leg is only admitted on a host
	// that carries a complete monetary binding.
	SponsorshipModeCustomerFunded SponsorshipMode = "customer_funded"
	// SponsorshipModeOperatorSponsored runs the leg as usage-only: the
	// provider is opened, the usage is metered, and no customer money moves.
	SponsorshipModeOperatorSponsored SponsorshipMode = "operator_sponsored"
)

// Sponsorship is the operator-declared funding decision carried in trusted
// internal context. It names a mode and, optionally, the declared sponsor the
// work runs under.
//
// A sponsor key names a sponsor, never an account. The key alone is never
// financial authority: the account that a customer-funded leg is charged
// against is resolved by the host's identity seam from the trusted scope and
// frozen when the operation is admitted.
type Sponsorship struct {
	// Mode is the declared funding decision. The empty mode is undeclared.
	Mode SponsorshipMode
	// SponsorKey is the selected sponsor key. It is optional; an empty
	// selection names no sponsor.
	SponsorKey string
	// DeclaredSponsorKeys are the sponsor keys the operator declared. A
	// selected key outside this set is refused and counted.
	DeclaredSponsorKeys []string
}

// ModeDeclared reports whether the operator declared a known funding mode.
func (s Sponsorship) ModeDeclared() bool {
	switch s.Mode {
	case SponsorshipModeCustomerFunded, SponsorshipModeOperatorSponsored:
		return true
	default:
		return false
	}
}

// SponsorKeyDeclared reports whether the selected sponsor key is one the
// operator declared. An empty selection names no sponsor and is accepted; a
// selection outside the declared set is not.
func (s Sponsorship) SponsorKeyDeclared() bool {
	key := strings.TrimSpace(s.SponsorKey)
	if key == "" {
		return true
	}
	for _, declared := range s.DeclaredSponsorKeys {
		if strings.TrimSpace(declared) == key {
			return true
		}
	}
	return false
}

// Clone returns a defensive copy of the declaration, so a caller cannot
// mutate the declared key set a trusted context already carries.
func (s Sponsorship) Clone() Sponsorship {
	out := s
	out.SponsorKey = strings.TrimSpace(s.SponsorKey)
	if len(s.DeclaredSponsorKeys) > 0 {
		out.DeclaredSponsorKeys = slices.Clone(s.DeclaredSponsorKeys)
	}
	return out
}

type sponsorshipContextKey struct{}

// WithSponsorship marks ctx with the operator-declared funding decision of
// one background operation. The decision travels in trusted internal context
// only: it is never copied into a canonical call, a wire payload or a public
// auxiliary request, so no client can select or spoof it.
func WithSponsorship(ctx context.Context, decl Sponsorship) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	return context.WithValue(ctx, sponsorshipContextKey{}, decl.Clone())
}

// SponsorshipFromContext returns the trusted funding decision, if ctx carries
// one.
//
// The bool distinguishes "no declaration travels with this work" from "a
// declaration travels with an undeclared mode". Work without a declaration is
// not gated: it was not submitted as sponsored background work, and the
// runtime leaves it exactly as it was. A declaration whose mode is
// undeclared is an operator misconfiguration, and the runtime refuses it.
//
// The returned declaration owns its declared key set: a reader that mutates
// what it received cannot change what the next read accepts, and a captured
// job keeps the key set it was submitted with.
func SponsorshipFromContext(ctx context.Context) (Sponsorship, bool) {
	if ctx == nil {
		return Sponsorship{}, false
	}
	decl, ok := ctx.Value(sponsorshipContextKey{}).(Sponsorship)
	if !ok {
		return Sponsorship{}, false
	}
	return decl.Clone(), true
}
