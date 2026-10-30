package auth

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// SelfDefenseHooks is the optional ingress self-defense observation seam the
// standard stack supplies when self-defense is enabled. The zero value observes
// nothing, which is the structural disabled posture: [Middleware] is then exactly
// the pre-existing request path. The hooks never decide an authentication
// outcome; the provider chain stays authoritative for status, headers and body,
// and both hooks are no-ops unless the outer gate published a source-address
// snapshot for this request.
type SelfDefenseHooks struct {
	// RecordAuthFailure records exactly one counted unauthenticated-failure
	// offense for the resolved source address of a terminal pre-principal 401.
	RecordAuthFailure func(addr netip.Addr)
	// ClearSource clears the adaptive hostile state of that exact source address
	// after a full successful authentication chain.
	ClearSource func(addr netip.Addr)
}

// SelfDefenseMiddleware is the observed variant the standard stack builds when
// ingress self-defense is enabled. It runs the same provider chain, renders the
// same responses and delegates to the same route mux as [Middleware]; the hooks
// only add authoritative adaptive state observation, and the zero
// [SelfDefenseHooks] value is exactly [Middleware].
func SelfDefenseMiddleware(log *slog.Logger, providers []httpauth.Provider, hooks SelfDefenseHooks, next http.Handler) http.Handler {
	return middleware(log, providers, hooks, next)
}

// recordPrePrincipalFailure scores one counted unauthenticated failure, and only
// for a terminal reject/challenge that resolves to 401 with no provider principal
// established before it. A 403 authorization or entitlement denial, a 5xx
// outcome, a provider error, an unusable provider result and a request that
// already established a principal are never hostile auth evidence. The address is
// the one the outer gate snapshotted, so this observer can never disagree with
// the gate and never reparses forwarding headers authentication does not trust.
func (h SelfDefenseHooks) recordPrePrincipalFailure(ctx context.Context, hadPrincipal bool, res httpauth.AuthenticationResult) {
	if h.RecordAuthFailure == nil || hadPrincipal || res.EffectiveStatus() != http.StatusUnauthorized {
		return
	}
	if addr, ok := contract.SourceAddr(ctx); ok {
		h.RecordAuthFailure(addr)
	}
}

// clearSource resets the exact-address adaptive state after a full successful
// chain. It is a reset, never a trust cache: a later hostile event starts a fresh
// offense level, and a prior success never exempts a later deterministic refusal.
func (h SelfDefenseHooks) clearSource(ctx context.Context) {
	if h.ClearSource == nil {
		return
	}
	if addr, ok := contract.SourceAddr(ctx); ok {
		h.ClearSource(addr)
	}
}
