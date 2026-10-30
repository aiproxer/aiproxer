package selfdefense

import (
	"net/http"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	geoipingress "github.com/aiproxer/aiproxer/internal/stdhttp/geoip"
)

// Observer receives bounded self-defense observations. Every reason is a member
// of the closed [ingressdefense.AllReasons] vocabulary; no request path, source
// address, prefix text, query, header, User-Agent, credential or principal ever
// reaches it, so it can only ever produce finite metric labels. Observer absence
// or failure is not authoritative: metrics never decide or change a security
// outcome.
type Observer interface {
	// Denial records one generic refusal for a finite reason.
	Denial(reason ingressdefense.Reason)
	// QuarantineTransition records one started or escalated quarantine for a
	// finite reason. entryCount is the bounded number of tracked source entries
	// after the transition, used for the current-entry gauge.
	QuarantineTransition(reason ingressdefense.Reason, entryCount int)
}

// CredentialProbe is the conservative private credential-presence probe.
//
// It reports whether the active transport-auth provider chain might still
// authenticate the request as presented. Only a proven false is a license to
// refuse a quarantined source before authentication: a credential-bearing
// request, a credential-free provider, an unknown/custom provider, or a provider
// without the private capability must all report true, so a legitimate user
// behind shared NAT, VPN or corporate egress is never locked out by another actor
// on the same public address. A nil probe fails open to the normal auth chain.
type CredentialProbe func(r *http.Request) bool

// Input is the generation-scoped, non-owning self-defense gate projection.
//
// The gate never owns adaptive state: State is the process-lifetime
// [ingressdefense.State] capability shared by every generation, and Policy is the
// immutable reloadable policy of this generation.
type Input struct {
	// Policy is the immutable generation policy. A nil or disabled policy is a
	// structural fast path: the wrapper is absent and no request performs any
	// self-defense work at all.
	Policy *ingressdefense.Policy
	// State is the process-owned bounded adaptive state. A nil State means the
	// process resource was not projected: deterministic impossible-path
	// rejection still runs and adaptive state is simply not consulted.
	State *ingressdefense.State
	// Resolver is the already-compiled GeoIP client-address trust configuration.
	// Self-defense reuses it verbatim, so it creates no second forwarding parser
	// and no second trust boundary; the same parser bounds and fail-closed
	// behavior apply here.
	Resolver geoipingress.ResolverConfig
	// ImpossiblePaths toggles the fixed matcher. Disabling it bypasses only the
	// matcher; adaptive quarantine behavior is retained.
	ImpossiblePaths bool
	// OwnedRoutes are the data-plane routes the RUNNING ROUTER owns, already
	// resolved with the router's own match semantics: an exact registration owns
	// one method/path pair, a trailing-slash registration owns its subtree, and
	// comparison is case-sensitive exactly as the router's is.
	//
	// A published route is authoritative, so the deterministic matcher never
	// refuses one. The configuration surface for a frontend base path and for a
	// diagnostics, metrics or operator-mount path is any normalized absolute
	// non-root path, which includes paths inside the frozen impossible families, so
	// without this inventory a default-on security layer would make a valid
	// configuration's route unreachable.
	//
	// This is the router's answer, not a path-shaped guess: a configured path whose
	// feature is disabled is not mounted and therefore carves nothing, and an exact
	// registration does not carve its own subtree. An empty or absent inventory
	// keeps the full pre-carve behavior, so a generation that projects no inventory
	// still refuses every frozen family.
	OwnedRoutes []httpcontract.OwnedRoute

	// Probe is the optional conservative credential-presence probe consulted for
	// a quarantined source.
	Probe CredentialProbe
	// Observer is the optional bounded metrics seam.
	Observer Observer
	// Now supplies the request-time instant passed to the adaptive state. A nil
	// Now uses time.Now; tests inject a fake clock.
	Now func() time.Time
}

// Middleware returns the early ingress self-defense gate.
//
// Per request the order is normative:
//  1. resolve the source address with the existing GeoIP resolver semantics; a
//     resolver failure returns a generic 403 and stops;
//  2. attach the normalized source address to the request context;
//  3. when impossible-path matching is enabled and the path matches and is not a
//     route this generation publishes, record one probe offense for a
//     non-adaptively-exempt source, record finite metrics and return a generic
//     404;
//  4. an adaptively-exempt source delegates immediately, without inspecting
//     adaptive quarantine state;
//  5. a source that is not quarantined delegates;
//  6. a quarantined source is refused with a generic 429 only when the
//     conservative credential probe proves it cannot authenticate as presented,
//     and otherwise reaches the normal transport-auth chain.
//
// Adaptive exemption is applied before both the quarantine lookup and the probe
// offense, and never overrides fixed network policy or the deterministic
// impossible-path refusal.
func Middleware(in Input, next http.Handler) http.Handler {
	policy := in.Policy
	if policy == nil || !policy.Enabled {
		return next
	}
	now := in.Now
	if now == nil {
		now = time.Now
	}
	// The owned-route inventory is compiled ONCE, here, so the request path is a
	// slice comparison with no normalization and no allocation.
	owned := NewOwnedRoutes(in.OwnedRoutes)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr, err := geoipingress.ResolveClientIP(r, in.Resolver)
		if err != nil {
			recordDenial(in.Observer, ingressdefense.ReasonClientIPError)
			forbidden(w)
			return
		}
		r = r.WithContext(httpcontract.WithSourceAddr(r.Context(), addr))
		exempt := policy.AdaptiveExempt(addr)

		if in.ImpossiblePaths && r.URL != nil && ImpossiblePathExcept(r.URL.Path, r.Method, owned) {
			if !exempt && in.State != nil {
				recordTransition(in.Observer, in.State.RecordProbe(addr, now(), *policy))
			}
			recordDenial(in.Observer, ingressdefense.ReasonImpossiblePath)
			notFound(w)
			return
		}
		if exempt {
			next.ServeHTTP(w, r)
			return
		}
		if in.State == nil || !in.State.IsQuarantined(addr, now()) {
			next.ServeHTTP(w, r)
			return
		}
		if in.Probe != nil && !in.Probe(r) {
			recordDenial(in.Observer, ingressdefense.ReasonActiveQuarantine)
			tooManyRequests(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func recordDenial(observer Observer, reason ingressdefense.Reason) {
	if observer != nil {
		observer.Denial(reason)
	}
}

// recordTransition emits a quarantine transition only when the state actually
// started or escalated one. A counted auth failure below the threshold reports
// the zero Transition with an empty reason, so its reason is never read here.
func recordTransition(observer Observer, transition ingressdefense.Transition) {
	if observer == nil || !transition.QuarantineStarted {
		return
	}
	observer.QuarantineTransition(transition.Reason, transition.EntryCount)
}

func forbidden(w http.ResponseWriter) {
	generic(w, http.StatusForbidden, "Forbidden\n")
}

func notFound(w http.ResponseWriter) {
	generic(w, http.StatusNotFound, "Not Found\n")
}

func tooManyRequests(w http.ResponseWriter) {
	generic(w, http.StatusTooManyRequests, "Too Many Requests\n")
}

// generic writes a refusal that is identical for every triggering request, so it
// discloses no matched rule, source address, quarantine deadline, offense level
// or any other security internal, and it sets no Retry-After hint.
func generic(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
