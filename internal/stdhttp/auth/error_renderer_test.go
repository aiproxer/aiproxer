package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

func TestDefaultAuthErrorRenderer_reasonCodes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var r DefaultAuthErrorRenderer
	cases := []struct {
		name   string
		in     httpauth.AuthErrorRenderInput
		status int
		subs   []string
		neg    []string
	}{
		{
			name: "missing key",
			in: httpauth.AuthErrorRenderInput{
				Decision:      sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "missing_api_key"},
				DefaultStatus: http.StatusUnauthorized,
			},
			status: http.StatusUnauthorized,
			subs:   []string{`"code"`, "unauthorized", "missing"},
		},
		{
			name: "invalid key",
			in: httpauth.AuthErrorRenderInput{
				Decision:      sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "invalid_api_key"},
				DefaultStatus: 0,
			},
			status: http.StatusUnauthorized,
			subs:   []string{"invalid_key"},
		},
		{
			name: "sso challenge",
			in: httpauth.AuthErrorRenderInput{
				Decision: sdkauth.Decision{
					Outcome:    sdkauth.OutcomeChallenge,
					ReasonCode: "challenge",
					Challenge:  sdkauth.Challenge{Kind: sdkauth.ChallengeSSORequired, Summary: "Sign in to continue."},
				},
				DefaultStatus: http.StatusUnauthorized,
			},
			status: http.StatusUnauthorized,
			subs:   []string{"challenge", "Sign in"},
		},
		{
			name: "sso challenge summary with credential-like text uses safe default",
			in: httpauth.AuthErrorRenderInput{
				Decision: sdkauth.Decision{
					Outcome:    sdkauth.OutcomeChallenge,
					ReasonCode: "challenge",
					Challenge: sdkauth.Challenge{
						Kind:    sdkauth.ChallengeSSORequired,
						Summary: "Open this link: bearer super-secret-token",
					},
				},
				DefaultStatus: http.StatusUnauthorized,
			},
			status: http.StatusUnauthorized,
			subs:   []string{"challenge_sso", sdkauth.DefaultChallengeSSOSummary},
			neg:    []string{"super-secret", "bearer super"},
		},
		{
			name: "remote_unavailable",
			in: httpauth.AuthErrorRenderInput{
				Decision:      sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "remote_unavailable"},
				DefaultStatus: 0,
			},
			status: http.StatusServiceUnavailable,
			subs:   []string{"unavailable"},
		},
		{
			name: "remote_denied_aligns_403_and_forbidden_code",
			in: httpauth.AuthErrorRenderInput{
				Decision:      sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "remote_denied"},
				DefaultStatus: http.StatusForbidden,
			},
			status: http.StatusForbidden,
			subs:   []string{`"code":"forbidden"`, "access denied"},
		},
		{
			name: "no secret key material in body",
			in: httpauth.AuthErrorRenderInput{
				Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "invalid_api_key"},
			},
			neg: []string{"sk-", "secret", "bearer "},
		},
		{
			name: "api_key_sso_misconfigured",
			in: httpauth.AuthErrorRenderInput{
				Decision:      sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "api_key_sso_misconfigured"},
				DefaultStatus: 0,
			},
			status: http.StatusServiceUnavailable,
			subs:   []string{`"code":"misconfigured"`, "not configured"},
		},
		{
			name: "event_delivery_failed",
			in: httpauth.AuthErrorRenderInput{
				Decision:      sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "event_delivery_failed"},
				DefaultStatus: 0,
			},
			status: http.StatusServiceUnavailable,
			subs:   []string{"event_unavailable", "could not be processed"},
		},
		{
			name: "forbidden_reason_code",
			in: httpauth.AuthErrorRenderInput{
				Decision:      sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "forbidden"},
				DefaultStatus: 0,
			},
			status: http.StatusForbidden,
			subs:   []string{`"code":"forbidden"`, "access denied"},
		},
		{
			name: "deny_empty_reason_generic_unauthorized",
			in: httpauth.AuthErrorRenderInput{
				Decision:      sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: ""},
				DefaultStatus: http.StatusPaymentRequired,
			},
			status: http.StatusPaymentRequired,
			subs:   []string{`"code":"unauthorized"`, "not authorized"},
		},
		{
			name: "challenge_non_sso",
			in: httpauth.AuthErrorRenderInput{
				Decision: sdkauth.Decision{
					Outcome:    sdkauth.OutcomeChallenge,
					ReasonCode: "mfa",
					Challenge:  sdkauth.Challenge{Kind: sdkauth.ChallengeKind("step_up"), Summary: "step up"},
				},
				DefaultStatus: 0,
			},
			status: http.StatusUnauthorized,
			subs:   []string{"auth_challenge", "additional authentication"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := r.RenderAuthError(ctx, tc.in)
			if tc.status != 0 && res.Status != tc.status {
				t.Fatalf("status %d want %d", res.Status, tc.status)
			}
			body := string(res.Body)
			for _, s := range tc.subs {
				if !strings.Contains(body, s) {
					t.Fatalf("body %q should contain %q", body, s)
				}
			}
			for _, s := range tc.neg {
				if strings.Contains(body, s) {
					t.Fatalf("body should not contain %q", s)
				}
			}
		})
	}
}

func TestDefaultAuthErrorRenderer_WWWAuthenticate_challenge(t *testing.T) {
	t.Parallel()
	var r DefaultAuthErrorRenderer
	res := r.RenderAuthError(context.Background(), httpauth.AuthErrorRenderInput{
		Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeChallenge, Challenge: sdkauth.Challenge{Kind: sdkauth.ChallengeSSORequired, Summary: "s"}},
	})
	if res.Headers.Get("WWW-Authenticate") == "" {
		t.Fatal("expected WWW-Authenticate")
	}
}
