package stdhttp

import (
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	corehttp "github.com/aiproxer/aiproxer/internal/core/http"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/infra/tracing"
	stdauth "github.com/aiproxer/aiproxer/internal/stdhttp/auth"
	geoipingress "github.com/aiproxer/aiproxer/internal/stdhttp/geoip"
	"github.com/aiproxer/aiproxer/internal/stdhttp/selfdefense"
)

// stackHTTPInput carries dependencies for [stackHTTPHandler] (same stack as [ComposeStandardHTTP] / generation host).
type stackHTTPInput struct {
	Cfg      *config.Config
	Log      *slog.Logger
	Security HTTPSecurityInput
	TraceGen *diag.TraceIDGenerator
	Inner    http.Handler
	HTTPProm *metrics.HTTPMetrics

	// testOuterWrap, if non-nil, wraps the composed handler before the final outer recovery
	// middleware. Used only from stdhttp tests to simulate panics above inner recovery.
	testOuterWrap func(http.Handler) http.Handler
}

// stackHTTPHandler assembles the same middleware stack as [ComposeStandardHTTP] (outer→inner:
// security headers, DownstreamServerMiddleware, final outer recovery, optional GeoIP ingress gate,
// optional ingress self-defense gate, optional OpenTelemetry HTTP, optional Prometheus, trace +
// request ID, access log, inner recovery, transport auth with the optional self-defense outcome
// observer, route mux). Innermost is the shared [http.ServeMux] from mounting.
//
// The two ingress security gates are the cheapest layers in the graph and the only ones outside
// tracing, metrics, request ID and the access log, so a refused request costs no general
// observability work. GeoIP stays outside self-defense and therefore wins on a hard denial
// (requirement 8.4), while a self-defense refusal never reaches the inner layers at all. Both are
// absent for a generation that does not project them, so a disabled generation performs no
// self-defense request-side work (requirement 1.2).
//
// The self-defense gate also publishes the resolved source address on the request context, so its
// paired transport-auth observer consumes exactly the identity the gate snapshotted instead of
// reparsing forwarding headers.
//
// Panic containment: [RecoveryMiddleware] remains between access logging and transport auth so
// access logs and HTTP metrics still observe inner handler panics as 5xx. [outerRecoveryMiddleware]
// wraps the full composed stack as a last resort for panics in outer layers (access log, metrics,
// tracing, or future outer wrappers). [DownstreamServerMiddleware] uses a thin commit-time
// ResponseWriter wrapper so Server policy wins on WriteHeader/Write/Flush (including HTTP 102
// hold-alive) while preserving Flusher and ResponseController Unwrap. [corehttp.SecurityHeadersMiddleware]
// is outermost so every response — including panic-generated 500s written by [outerRecoveryMiddleware]
// before it delegates — carries the security headers.
func stackHTTPHandler(in stackHTTPInput) http.Handler {
	cfg, log, sec, traceGen, inner, httpProm := in.Cfg, in.Log, in.Security, in.TraceGen, in.Inner, in.HTTPProm
	h := selfDefenseAuthHandler(log, sec, inner)
	h = RecoveryMiddleware(log, h)
	h = accessLogMiddleware(cfg, log, h)
	traceNames := []string{corehttp.HeaderTraceID}
	if cfg != nil {
		if names := cfg.HTTPHeaders.Effective().Trace; len(names) > 0 {
			traceNames = names
		}
	}
	h = corehttp.TraceMiddlewareHeaders(traceNames, corehttp.RequestIDMiddlewareHeaders(traceGen, traceNames, h))
	if httpProm != nil {
		h = httpProm.Middleware(h)
	}
	if cfg != nil && cfg.Observability.Tracing.Enabled {
		h = tracing.HTTPMiddleware(true, h)
	}
	if selfDefense := sec.SelfDefense; selfDefense.SelfDefenseEnabled() {
		h = selfdefense.Middleware(selfdefense.Input{
			Policy:          selfDefense.Policy,
			State:           selfDefense.State,
			Resolver:        geoipingress.ResolverConfig{Source: geoipingress.Source(selfDefense.Resolver.Source), TrustedProxies: append([]netip.Prefix(nil), selfDefense.Resolver.TrustedProxies...)},
			ImpossiblePaths: selfDefense.ImpossiblePaths,
			OwnedRoutes:     selfDefense.OwnedRoutes,
			Probe:           selfDefense.CredentialGateProbe(),
			Observer:        selfDefense.Observer,
			Now:             selfDefense.Now,
		}, h)
	}
	if sec.GeoIP.Policy != nil {
		geo := sec.GeoIP
		h = geoipingress.Middleware(geoipingress.Input{
			Policy:   geo.Policy,
			Lookup:   geo.Lookup,
			Resolver: geoipingress.ResolverConfig{Source: geoipingress.Source(geo.Resolver.Source), TrustedProxies: append([]netip.Prefix(nil), geo.Resolver.TrustedProxies...)},
			Observer: geo.Observer,
		}, h)
	}
	if in.testOuterWrap != nil {
		h = in.testOuterWrap(h)
	}
	h = outerRecoveryMiddleware(log, h)
	h = DownstreamServerMiddleware(cfg, h)
	return corehttp.SecurityHeadersMiddleware(h)
}

// selfDefenseAuthHandler selects the transport-auth variant for this generation. With ingress
// self-defense enabled it builds the observed variant against the borrowed process state and the
// immutable generation policy; with self-defense absent or disabled it is exactly the pre-existing
// [stdauth.Middleware] call, so switching the feature on can never add per-request work to a
// generation that has it off.
func selfDefenseAuthHandler(log *slog.Logger, sec HTTPSecurityInput, inner http.Handler) http.Handler {
	selfDefense := sec.SelfDefense
	if !selfDefense.SelfDefenseEnabled() {
		return stdauth.Middleware(log, sec.HTTPAuthProviders, inner)
	}
	return stdauth.SelfDefenseMiddleware(log, sec.HTTPAuthProviders, selfDefenseAuthHooks(selfDefense), inner)
}

// selfDefenseAuthHooks drives the borrowed process state from the authoritative transport-auth
// outcomes of this generation. It never reads the source address itself: the hooks consume only
// the identity the outer gate snapshotted, so the observer and the gate can never disagree. An
// adaptively exempt source is filtered here exactly as the gate filters it, so no counted
// authentication failure from an exempt address creates state. The transition reason is read only
// when a quarantine actually started, so no below-threshold label can escape the closed reason
// vocabulary, and a nil observer leaves the decision untouched.
func selfDefenseAuthHooks(selfDefense SelfDefenseSecurityInput) stdauth.SelfDefenseHooks {
	state, policy := selfDefense.State, selfDefense.Policy
	if state == nil || policy == nil {
		return stdauth.SelfDefenseHooks{}
	}
	now := selfDefense.Now
	if now == nil {
		now = time.Now
	}
	observer := selfDefense.Observer
	return stdauth.SelfDefenseHooks{
		RecordAuthFailure: func(addr netip.Addr) {
			// An adaptively exempt source accrues no state here, matching the gate: it
			// delegates before the quarantine lookup, so counting its auth failures
			// would only inflate the bounded entry gauge and the transition counter.
			if policy.AdaptiveExempt(addr) {
				return
			}
			transition := state.RecordAuthFailure(addr, now(), *policy)
			if transition.QuarantineStarted && observer != nil {
				observer.QuarantineTransition(transition.Reason, transition.EntryCount)
			}
		},
		ClearSource: func(addr netip.Addr) { state.Clear(addr) },
	}
}
