package product

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func planExhaustionCause(code int64, kind, message string) error {
	return fmt.Errorf("unsafe-wrapper: %w", &acp.RPCError{
		Code: code, Method: "session/prompt", Message: message, ErrorKind: kind,
	})
}

func TestClaudeSpec_PlanExhaustionKind(t *testing.T) {
	for _, source := range []string{"api_key", "subscription_token", "host_login"} {
		auth, err := NewAuthEnv(AuthConfig{CredentialSource: source}, map[string]string{"api_key": "test-only-credential", "claude_oauth_token": "test-only-credential"}, []string{"HOME=/host"})
		if err != nil {
			t.Fatal(err)
		}
		spec, err := NewClaudeSpec("test-adapter", "/workspace", auth)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name              string
			cause             error
			preRecoverable    bool
			postRecoverable   bool
			remediationSubstr string
		}{
			{"billing_error", planExhaustionCause(-32603, "billing_error", "Credit balance is too low private-account-data"), true, false, ""},
			{"account_on_hold", planExhaustionCause(-32603, "account_on_hold", "Account on hold private-account-data"), true, false, ""},
			{"synthetic_prefix_no_kind", planExhaustionCause(-32603, "", "You've hit your weekly limits private-account-data"), true, false, ""},
			{"transient_rate_limit_not_plan", planExhaustionCause(-32603, "rate_limit", "You've hit your rate limit, retry private-account-data"), false, false, ""},
			{"generic_internal", planExhaustionCause(-32603, "", "Internal error private-account-data"), false, false, ""},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				pre := spec.ClassifyError(tc.cause, aipapi.PhasePreOutput)
				var streamErr *aipapi.StreamError
				hasStreamErr := errors.As(pre, &streamErr)
				isPlan := tc.name == "billing_error" || tc.name == "account_on_hold" || tc.name == "synthetic_prefix_no_kind"
				if isPlan && (!hasStreamErr || streamErr.Code != "plan_exhausted") {
					t.Fatalf("want plan_exhausted, got %#v", pre)
				}
				if !isPlan && hasStreamErr && streamErr.Code == "plan_exhausted" {
					t.Fatalf("must not classify as plan exhaustion: %v", pre)
				}
				if aipapi.IsRecoverablePreOutput(pre) != tc.preRecoverable {
					t.Fatalf("pre recoverable = %v, want %v", aipapi.IsRecoverablePreOutput(pre), tc.preRecoverable)
				}
				post := spec.ClassifyError(tc.cause, aipapi.PhasePostOutput)
				if aipapi.IsRecoverablePreOutput(post) {
					t.Fatal("post-output plan exhaustion must be terminal")
				}
				if !errors.Is(post, aipapi.ErrStreamTerminal) {
					t.Fatalf("post must stay terminal: %v", post)
				}
				assertSafeError(t, pre)
				assertSafeError(t, post)
			})
		}
	}
}

func TestClaudeSpec_PlanExhaustionMessageNamesRemediation(t *testing.T) {
	spec := claudeTestSpec(t, nil)
	got := spec.ClassifyError(planExhaustionCause(-32603, "billing_error", "x"), aipapi.PhasePreOutput)
	if !strings.Contains(got.Error(), "plan") {
		t.Fatalf("message must name plan exhaustion: %v", got)
	}
}
