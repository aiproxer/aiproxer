package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// DefaultAuthErrorRenderer maps authentication denials and challenges to generic, safe JSON
// (or empty body) and stable status codes. It does not include secrets or per-key material.
type DefaultAuthErrorRenderer struct{}

type authErrorWire struct {
	Error *authErrorPayload `json:"error,omitempty"`
}

type authErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RenderAuthError implements [httpauth.AuthErrorRenderer].
func (DefaultAuthErrorRenderer) RenderAuthError(
	ctx context.Context,
	in httpauth.AuthErrorRenderInput,
) httpauth.AuthErrorRenderResult {
	_ = ctx
	status, code, msg := mapDecisionToHTTPStatusAndMessage(in)
	out := cloneHeader(in.ChallengeHeaders)
	typ := in.Decision.Outcome
	if typ == "" {
		typ = sdkauth.OutcomeDeny
	}
	// For challenges, add WWW-Authenticate if none supplied (SSO or bearer policy).
	if typ == sdkauth.OutcomeChallenge {
		if out == nil {
			out = make(http.Header, 1)
		}
		if out.Get("WWW-Authenticate") == "" {
			realm := "aip"
			if in.Decision.Challenge.Kind == sdkauth.ChallengeSSORequired {
				realm = "aip-sso"
			}
			out.Set("WWW-Authenticate", `Bearer realm="`+realm+`"`)
		}
	}
	wire := authErrorWire{Error: &authErrorPayload{Code: code, Message: msg}}
	b, err := json.Marshal(&wire)
	if err != nil || len(b) == 0 {
		b = []byte(`{"error":{"code":"auth_failed","message":"unauthorized"}}`)
	}
	ct := "application/json; charset=utf-8"
	if status == 0 {
		status = http.StatusUnauthorized
	}
	if out == nil {
		out = make(http.Header, 0)
	}
	return httpauth.AuthErrorRenderResult{Status: status, Headers: out, ContentType: ct, Body: b}
}

// cloneHeader returns a shallow copy of h suitable for mutation, or nil when empty.
func cloneHeader(h http.Header) http.Header {
	if len(h) == 0 {
		return nil
	}
	return h.Clone()
}

// mapDecisionToHTTPStatusAndMessage returns HTTP status, stable error code, and a safe public message.
func mapDecisionToHTTPStatusAndMessage(in httpauth.AuthErrorRenderInput) (int, string, string) {
	d := in.Decision
	rc := strings.TrimSpace(strings.ToLower(d.ReasonCode))
	st := in.DefaultStatus
	// reason-first overrides
	switch rc {
	case "remote_unavailable":
		return http.StatusServiceUnavailable, "service_unavailable", "authentication service unavailable"
	case "api_key_sso_misconfigured", "remote_misconfigured", "local_noop_misconfigured", "local_api_key_misconfigured":
		if st == 0 {
			st = http.StatusServiceUnavailable
		}
		return st, "misconfigured", "server authentication is not configured"
	case "remote_unusable_decision":
		if st == 0 {
			st = http.StatusUnauthorized
		}
		return st, "invalid_response", "authentication could not be completed"
	case "event_delivery_failed", "aip_event_delivery_failed":
		if st == 0 {
			st = http.StatusServiceUnavailable
		}
		return st, "event_unavailable", "the request could not be processed"
	case "forbidden", "insufficient", "insufficient_privilege":
		if st == 0 {
			st = http.StatusForbidden
		}
		return st, "forbidden", "access denied"
	}
	// common API-key
	switch rc {
	case "invalid_api_key", "unknown_api_key", "key_mismatch", "mismatched_key":
		if st == 0 {
			st = http.StatusUnauthorized
		}
		return st, "invalid_key", "invalid or missing credentials"
	case "missing_api_key", "no_api_key", "bearer_required":
		if st == 0 {
			st = http.StatusUnauthorized
		}
		return st, "unauthorized", "missing credentials"
	}
	// challenge / SSO
	if d.Outcome == sdkauth.OutcomeChallenge {
		if d.Challenge.Kind == sdkauth.ChallengeSSORequired {
			if st == 0 {
				st = http.StatusUnauthorized
			}
			summary := sdkauth.SanitizePublicChallengeSummary(
				d.Challenge.Summary,
				sdkauth.DefaultChallengeSSOSummary,
				sdkauth.PublicChallengeSummaryMaxRunes,
			)
			return st, "challenge_sso", summary
		}
		if st == 0 {
			st = http.StatusUnauthorized
		}
		return st, "auth_challenge", "additional authentication is required"
	}
	if d.Outcome == sdkauth.OutcomeDeny {
		// Remote deny: align JSON "forbidden" with HTTP 403 (adapter default status).
		if rc == "remote_denied" || rc == "deny" {
			if st == 0 {
				st = http.StatusForbidden
			}
			return st, "forbidden", "access denied"
		}
		if st == 0 {
			st = http.StatusUnauthorized
		}
		return st, "unauthorized", "the request is not authorized"
	}
	if st == 0 {
		st = http.StatusUnauthorized
	}
	return st, "unauthorized", "the request is not authorized"
}
