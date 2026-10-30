package geoip

import (
	"net/http"

	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
)

// Observer receives finite-label decision outcomes without request-controlled data.
type Observer interface {
	Decision(reason coregeoip.Reason, allow bool)
}

// Input is the generation-scoped, non-owning GeoIP gate projection.
type Input struct {
	Policy   *coregeoip.Policy
	Lookup   coregeoip.CountryLookup
	Resolver ResolverConfig
	Observer Observer
}

// Middleware returns the early ingress gate. A nil policy is a structural
// disabled fast path: no client resolution, lookup, or policy evaluation occurs.
func Middleware(in Input, next http.Handler) http.Handler {
	if in.Policy == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr, err := ResolveClientIP(r, in.Resolver)
		if err != nil {
			record(in.Observer, coregeoip.ReasonClientIPError, false)
			forbidden(w)
			return
		}
		decision := in.Policy.Evaluate(addr, in.Lookup)
		record(in.Observer, decision.Reason, decision.Allow)
		if !decision.Allow {
			forbidden(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func record(observer Observer, reason coregeoip.Reason, allow bool) {
	if observer != nil {
		observer.Decision(reason, allow)
	}
}

func forbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte("Forbidden\n"))
}
