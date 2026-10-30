// Package auth integrates transport-layer [httpauth.Provider] chains into stdhttp (R4, design §13).
package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// annotateResponseHeaderNames allow-lists [httpauth.TypeAnnotate] ResponseHeaders on the success path.
var annotateResponseHeaderNames = map[string]struct{}{
	"Cache-Control":                       {},
	"Content-Security-Policy":             {},
	"Content-Security-Policy-Report-Only": {},
	"Cross-Origin-Embedder-Policy":        {},
	"Cross-Origin-Opener-Policy":          {},
	"Cross-Origin-Resource-Policy":        {},
	"Expires":                             {},
	"Permissions-Policy":                  {},
	"Pragma":                              {},
	"Referrer-Policy":                     {},
	"Strict-Transport-Security":           {},
	"Vary":                                {},
	"X-Content-Type-Options":              {},
	"X-Frame-Options":                     {},
}

// terminalResponseHeaderNames allow-lists headers on TypeReject/TypeChallenge responses.
var terminalResponseHeaderNames = buildTerminalResponseHeaderNames()

func buildTerminalResponseHeaderNames() map[string]struct{} {
	names := make(map[string]struct{}, len(annotateResponseHeaderNames)+3)
	for k := range annotateResponseHeaderNames {
		names[k] = struct{}{}
	}
	names["Www-Authenticate"] = struct{}{}
	names["Retry-After"] = struct{}{}
	return names
}

// Middleware returns an HTTP handler that runs providers in order before delegating to next.
// Provider errors are fail-closed (HTTP 500). Empty provider list is a no-op passthrough here
// only; product wiring must supply providers so anonymous pass-through never replaces configured
// authentication (auth-architecture 1.7 / 5.6). A non-empty nil-only list fails closed (HTTP 500).
// When log is non-nil, provider failures emit one structured log line (trace via request context).
// It is the zero-hook compatibility wrapper around [SelfDefenseMiddleware] and observes nothing.
func Middleware(log *slog.Logger, providers []httpauth.Provider, next http.Handler) http.Handler {
	return middleware(log, providers, SelfDefenseHooks{}, next)
}

// middleware is the single provider-chain chokepoint. With no hooks the only added
// request work is one local flag plus one bool store inside the already existing
// principal case and the nil-checked hooks on the already existing terminal and
// delegation paths: no allocation, no additional provider invocation, no
// source-address read.
func middleware(log *slog.Logger, providers []httpauth.Provider, hooks SelfDefenseHooks, next http.Handler) http.Handler {
	observe := hooks.RecordAuthFailure != nil || hooks.ClearSource != nil
	nonNil := compactNonNilHTTPAuthProviders(providers)
	if len(nonNil) == 0 {
		if len(providers) > 0 {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if log != nil {
					ctx := context.Background()
					if r != nil {
						ctx = r.Context()
					}
					log.ErrorContext(
						ctx, "stdhttp: auth middleware has non-empty provider list but every entry is nil",
						slog.String("component", "stdhttp.auth"),
						slog.String("reason", "all_httpauth_providers_nil"),
					)
				}
				http.Error(w, "authentication misconfigured", http.StatusInternalServerError)
			})
		}
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r == nil {
			if log != nil {
				log.WarnContext(
					context.Background(), "stdhttp: nil request in auth middleware",
					slog.String("component", "stdhttp.auth"),
				)
			}
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		// Capture credential matchers at each Principal success while header state is current,
		// but defer attaching the pending matcher until the full provider chain succeeds.
		// Principal, scope, and ingress attribution still propagate during the chain.
		// hadPrincipal only records that a provider accepted the request; the adaptive
		// entry is cleared only once the whole chain completes without a terminal rejection.
		var pendingMatcher secretguard.Matcher
		var hadPrincipal bool
		for _, p := range nonNil {
			res, err := p.Authenticate(ctx, w, r)
			if err != nil {
				if log != nil {
					// Provider errors are logged without the raw error string so request material
					// wrapped by providers cannot leak into logs.
					log.ErrorContext(
						ctx, "stdhttp: auth provider authenticate failed",
						slog.String("component", "stdhttp.auth"),
						slog.String("error_kind", authProviderErrorKind(err)),
					)
				}
				http.Error(w, "authentication failed", http.StatusInternalServerError)
				return
			}
			switch res.Type {
			case httpauth.TypeContinue:
				continue
			case httpauth.TypePrincipal:
				hadPrincipal = true
				ctx = httpauth.WithPrincipal(ctx, res.Principal)
				if res.Scope != nil {
					ctx = httpauth.WithScope(ctx, *res.Scope)
				}
				if res.IngressAttribution != (httpauth.IngressAttribution{}) {
					ctx = httpauth.WithIngressAttribution(ctx, res.IngressAttribution)
				}
				if attacher, ok := p.(authSuccessContextAttacher); ok {
					pendingMatcher = attacher.captureAuthSuccessMatcher(r, res)
				}
				r = r.WithContext(ctx)
			case httpauth.TypeAnnotate:
				mergeAnnotateResponseHeaders(ctx, log, w.Header(), res.ResponseHeaders)
			case httpauth.TypeReject, httpauth.TypeChallenge:
				// Score a qualifying unauthenticated failure before the existing
				// termination response is written, so recorded state never depends
				// on how that response renders.
				if observe {
					hooks.recordPrePrincipalFailure(ctx, hadPrincipal, res)
				}
				writeTermination(ctx, log, w, res)
				return
			default:
				if log != nil {
					log.WarnContext(ctx, "stdhttp: auth provider returned unknown result type", "type", res.Type)
				}
				http.Error(w, "authentication failed", http.StatusInternalServerError)
				return
			}
		}
		if pendingMatcher != nil {
			ctx = httpauth.WithCredentialMatcher(ctx, pendingMatcher)
		}
		// Align with [PolicyProvider.frontendID] when [PolicyProvider.FrontendID] is nil (path-derived wire id).
		ctx = execview.WithFrontendID(ctx, DefaultFrontendIDFromRequest(r))
		// The full chain completed with no terminal rejection and at least one
		// accepted principal: clear the exact-address adaptive state immediately
		// before the route mux runs.
		if observe && hadPrincipal {
			hooks.clearSource(ctx)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func compactNonNilHTTPAuthProviders(providers []httpauth.Provider) []httpauth.Provider {
	out := make([]httpauth.Provider, 0, len(providers))
	for _, p := range providers {
		if p != nil {
			out = append(out, p)
		}
	}
	return out
}

func authProviderErrorKind(err error) string {
	if err == nil {
		return "unknown"
	}
	return strings.TrimPrefix(fmt.Sprintf("%T", err), "*")
}

func mergeAnnotateResponseHeaders(ctx context.Context, log *slog.Logger, dst, src http.Header) {
	if len(src) == 0 {
		return
	}
	for rawKey, vs := range src {
		canon := http.CanonicalHeaderKey(strings.TrimSpace(rawKey))
		if canon == "" {
			continue
		}
		if _, ok := annotateResponseHeaderNames[canon]; !ok {
			if log != nil {
				log.WarnContext(ctx, "stdhttp: auth annotate dropped disallowed response header", "header", canon)
			}
			continue
		}
		for _, v := range vs {
			dst.Add(canon, v)
		}
	}
}

func mergeTerminalResponseHeaders(ctx context.Context, log *slog.Logger, dst, src http.Header) {
	if len(src) == 0 {
		return
	}
	for rawKey, vs := range src {
		canon := http.CanonicalHeaderKey(strings.TrimSpace(rawKey))
		if canon == "" {
			continue
		}
		if _, ok := terminalResponseHeaderNames[canon]; !ok {
			if log != nil {
				log.WarnContext(ctx, "stdhttp: auth termination dropped disallowed response header", "header", canon)
			}
			continue
		}
		for _, v := range vs {
			dst.Add(canon, v)
		}
	}
}

func writeTermination(ctx context.Context, log *slog.Logger, w http.ResponseWriter, res httpauth.AuthenticationResult) {
	h := w.Header()
	mergeTerminalResponseHeaders(ctx, log, h, res.Headers)
	switch ct := strings.TrimSpace(res.ContentType); {
	case ct != "":
		h.Set("Content-Type", ct)
	case h.Get("Content-Type") == "" && len(res.Body) > 0:
		h.Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(res.EffectiveStatus())
	if len(res.Body) > 0 {
		if _, err := w.Write(res.Body); err != nil && log != nil {
			log.WarnContext(
				ctx, "stdhttp: auth termination response write failed",
				slog.String("component", "stdhttp.auth"),
				"error", err,
			)
		}
	}
}
