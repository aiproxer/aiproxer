package product

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestClaudeSpec_AuthenticationRemediationAndOutputBoundary(t *testing.T) {
	for _, tc := range []struct {
		source  string
		secrets map[string]string
	}{
		{"api_key", map[string]string{"api_key": "test-only-credential"}},
		{"subscription_token", map[string]string{"claude_oauth_token": "test-only-credential"}},
		{"host_login", nil},
		{"gateway", nil},
	} {
		t.Run(tc.source, func(t *testing.T) {
			auth, err := NewAuthEnv(AuthConfig{CredentialSource: tc.source, Gateway: GatewayConfig{BaseURL: "https://gw.example.com"}}, tc.secrets, []string{"HOME=/host"})
			if err != nil {
				t.Fatal(err)
			}
			spec, err := NewClaudeSpec("test-adapter", "/workspace", auth)
			if err != nil {
				t.Fatal(err)
			}
			cause := &acp.RPCError{Code: -32000, Method: "session/prompt", Message: "test-only-credential private-account-data"}
			for _, phase := range []aipapi.OutputPhase{aipapi.PhasePreOutput, aipapi.PhasePostOutput} {
				got := spec.ClassifyError(fmt.Errorf("unsafe-wrapper: %w", cause), phase)
				if aipapi.IsRecoverablePreOutput(got) != (phase == aipapi.PhasePreOutput) {
					t.Fatalf("%s: recovery=%v", phase, got)
				}
				remediation := map[string]string{"api_key": "api_key secret", "subscription_token": "claude setup-token", "host_login": "claude auth login", "gateway": "verify the gateway endpoint and provisioned header secrets"}[tc.source]
				if !strings.Contains(got.Error(), "missing or expired") || !strings.Contains(got.Error(), remediation) {
					t.Fatalf("%s: missing remediation: %v", phase, got)
				}
				if !errors.Is(got, aipapi.ErrStreamTerminal) {
					t.Fatalf("%s: lost auth failure identity: %v", phase, got)
				}
				var streamErr *aipapi.StreamError
				if !errors.As(got, &streamErr) || streamErr.Code != "authentication_required" || !strings.Contains(streamErr.Message, remediation) {
					t.Fatalf("%s: missing safe stream detail: %v", phase, got)
				}
				assertSafeError(t, got)
			}
		})
	}
}

// An unconfirmed lifecycle cleanup must survive classification: callers have
// to observe the cleanup sentinel instead of a generic recoverable failure.
func TestClaudeSpec_CleanupIncompleteKeepsItsIdentity(t *testing.T) {
	auth, err := NewAuthEnv(AuthConfig{CredentialSource: "api_key", Gateway: GatewayConfig{BaseURL: "https://gw.example.com"}}, map[string]string{"api_key": "test-only-credential"}, []string{"HOME=/host"})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := NewClaudeSpec("test-adapter", "/workspace", auth)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []aipapi.OutputPhase{aipapi.PhasePreOutput, aipapi.PhasePostOutput} {
		got := spec.ClassifyError(fmt.Errorf("joined: %w", acp.ErrCleanupIncomplete), phase)
		if !errors.Is(got, acp.ErrCleanupIncomplete) {
			t.Errorf("%s: cleanup identity lost: %v", phase, got)
		}
		if aipapi.IsRecoverablePreOutput(got) {
			t.Errorf("%s: cleanup failure classified as recoverable: %v", phase, got)
		}
	}
}

func TestClaudeSpec_OptionAndGenericFailuresKeepSafeIdentity(t *testing.T) {
	spec := claudeTestSpec(t, nil)
	rpc := &acp.RPCError{Code: -32603, Method: "session/prompt", Message: "test-only-credential private-account-data"}
	for _, tc := range []struct {
		name                    string
		cause                   error
		capability, recoverable bool
	}{
		{"model", fmt.Errorf("private-account-data model: %w", aipapi.ErrCapabilityReject), true, false},
		{"effort", &acp.RPCError{Code: -32602, Method: "session/set_config_option", Message: "test-only-credential"}, true, false},
		{"generic_rpc", rpc, false, false},
		{"invalid_prompt", &acp.RPCError{Code: -32602, Method: "session/prompt", Message: "private-account-data"}, false, false},
		{"transport", errors.New("private-account-data"), false, true},
		{"cancel", context.Canceled, false, false},
		{"deadline", context.DeadlineExceeded, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := spec.ClassifyError(tc.cause, aipapi.PhasePreOutput)
			if errors.Is(got, aipapi.ErrCapabilityReject) != tc.capability || aipapi.IsRecoverablePreOutput(got) != tc.recoverable {
				t.Fatalf("wrong kind/cause: %v", got)
			}
			if tc.capability && !strings.Contains(got.Error(), "model or effort") {
				t.Fatalf("missing capability diagnostic: %v", got)
			}
			if tc.name == "generic_rpc" {
				var preserved *acp.RPCError
				if !errors.As(got, &preserved) || preserved == rpc || preserved.Code != rpc.Code || preserved.Method != rpc.Method || got.Error() != "claudeagentacp: agent RPC failed" {
					t.Fatalf("generic RPC fallback lost: %v", got)
				}
			}
			assertSafeError(t, got)
		})
	}
	if got := spec.ClassifyError(nil, aipapi.PhasePreOutput); got != nil {
		t.Fatalf("nil classified as %v", got)
	}
}

func TestClaudeSpec_ExposedCausesContainOnlySafeMetadata(t *testing.T) {
	spec := claudeTestSpec(t, nil)
	for _, phase := range []aipapi.OutputPhase{aipapi.PhasePreOutput, aipapi.PhasePostOutput} {
		for _, code := range []int64{-32000, -32603, -32602} {
			t.Run(fmt.Sprintf("%s/%d", phase, code), func(t *testing.T) {
				cause := &acp.RPCError{Code: code, Method: "session/set_config_option", Message: "test-only-credential private-account-data"}
				got := spec.ClassifyError(fmt.Errorf("unsafe-wrapper test-only-credential: %w", cause), phase)
				var exposed *acp.RPCError
				if !errors.As(got, &exposed) || exposed == nil || exposed.Code != code || exposed.Method != cause.Method {
					t.Fatalf("lost structured RPC metadata: %v", got)
				}
				if exposed == cause || exposed.Message != "" || errors.Is(got, cause) {
					t.Error("original untrusted RPC cause retained")
				}
				assertSafeError(t, got)
			})
		}
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded, aipapi.ErrCapabilityReject, errors.New("test-only-credential private-account-data")} {
			got := spec.ClassifyError(fmt.Errorf("unsafe-wrapper test-only-credential: %w", cause), phase)
			if cause == context.Canceled || cause == context.DeadlineExceeded || cause == aipapi.ErrCapabilityReject {
				if !errors.Is(got, cause) || aipapi.IsRecoverablePreOutput(got) {
					t.Errorf("lost terminal sentinel classification: %v", got)
				}
			}
			assertSafeError(t, got)
		}
	}
}

// Requirement 3.3: the gateway source never inherits the Anthropic plan
// taxonomy. Its quota and billing failures stay generic upstream RPC
// failures with safe metadata and unchanged output-commitment semantics.
func TestClaudeSpec_GatewayQuotaErrorsStayGeneric(t *testing.T) {
	spec := claudeTestSpecForSource(t, "gateway")
	for _, tc := range []struct {
		name    string
		kind    string
		message string
	}{
		{"gateway-billing_error", "billing_error", "Credit balance is too low private-account-data"},
		{"gateway-account_on_hold", "account_on_hold", "Account on hold private-account-data"},
		{"gateway-prefix-no-kind", "", "You've hit your weekly limits private-account-data"},
		{"gateway-seat-prefix", "", "Your seat type doesn't include usage private-account-data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := planExhaustionCause(-32603, tc.kind, tc.message)
			pre := spec.ClassifyError(cause, aipapi.PhasePreOutput)
			if aipapi.IsRecoverablePreOutput(pre) {
				t.Fatalf("%s: gateway quota failure must stay terminal pre-output: %v", tc.name, pre)
			}
			var streamErr *aipapi.StreamError
			if errors.As(pre, &streamErr) && streamErr.Code == "plan_exhausted" {
				t.Fatalf("%s: gateway must not claim plan exhaustion", tc.name)
			}
			if !strings.Contains(pre.Error(), "agent RPC failed") || strings.Contains(pre.Error(), "Anthropic") || strings.Contains(pre.Error(), "plan") {
				t.Fatalf("%s: wrong generic wording: %v", tc.name, pre)
			}
			post := spec.ClassifyError(cause, aipapi.PhasePostOutput)
			if !errors.Is(post, aipapi.ErrStreamTerminal) || aipapi.IsRecoverablePreOutput(post) {
				t.Fatalf("%s: post-output must stay terminal: %v", tc.name, post)
			}
			exposed, ok := errors.AsType[*acp.RPCError](pre)
			if !ok || exposed.Code != -32603 || exposed.Method != "session/prompt" || exposed.ErrorKind != tc.kind {
				t.Fatalf("%s: safe RPC metadata lost: %#v", tc.name, pre)
			}
			assertSafeError(t, pre)
			assertSafeError(t, post)
		})
	}
}

// Requirement 2.4: a failed gateway handshake keeps naming the gateway
// remediation for every RPC code the adapter may emit, not only -32000.
func TestClaudeSpec_GatewayHandshakeFailureRemediation(t *testing.T) {
	for _, tc := range []struct{ code int64 }{{-32000}, {-32601}, {-32602}, {-32603}} {
		spec := claudeTestSpecForSource(t, "gateway")
		cause := fmt.Errorf("unsafe-wrapper: %w", &acp.RPCError{Code: tc.code, Method: "authenticate", Message: "private-account-data"})
		pre := spec.ClassifyError(cause, aipapi.PhasePreOutput)
		want := "verify the gateway endpoint and provisioned header secrets"
		if !strings.Contains(pre.Error(), want) || strings.Contains(fmt.Sprintf("%+v %#v", pre, pre), "private-account-data") {
			t.Fatalf("code %d: missing gateway remediation or leak: %v", tc.code, pre)
		}
	}
	// Other RPC failures of other sources keep their own remediation text.
	spec := claudeTestSpec(t, nil)
	cause := fmt.Errorf("unsafe-wrapper: %w", &acp.RPCError{Code: -32602, Method: "authenticate", Message: "x"})
	if got := spec.ClassifyError(cause, aipapi.PhasePreOutput); strings.Contains(got.Error(), "gateway") {
		t.Fatalf("non-gateway remediation wording changed: %v", got)
	}
}

// Cross-product of the classifier's locally assigned method, the adapter's
// categorical errorKind and the credential source: the authenticate method
// takes precedence over plan taxonomy for every source, while non-authenticate
// failures keep their own source-specific class (gateway generic, first-party
// plan_exhausted).
func TestClaudeSpec_AuthenticateMethodPrecedesPlanTaxonomy(t *testing.T) {
	planKinds := []struct{ name, kind, message string }{
		{"billing_error", "billing_error", "Credit balance is too low private-account-data"},
		{"account_on_hold", "account_on_hold", "Account on hold private-account-data"},
		{"limit-prefix", "", "You've hit your weekly limits private-account-data"},
	}
	for _, source := range []string{"gateway", "api_key"} {
		for _, kind := range planKinds {
			for _, method := range []string{"authenticate", "session/prompt"} {
				t.Run(source+"/"+kind.name+"/"+method, func(t *testing.T) {
					spec := claudeTestSpecForSource(t, source)
					cause := fmt.Errorf("unsafe-wrapper: %w", &acp.RPCError{
						Code: -32603, Method: method, Message: kind.message, ErrorKind: kind.kind,
					})
					got := spec.ClassifyError(cause, aipapi.PhasePreOutput)
					if method == "authenticate" {
						want := "agent authentication failed; "
						if source == "gateway" {
							want = "gateway authentication failed; "
						}
						if !strings.Contains(got.Error(), want) || strings.Contains(got.Error(), "plan usage exhausted") || strings.Contains(got.Error(), "agent RPC failed") {
							t.Fatalf("authenticate must name remediation: %v", got)
						}
						return
					}
					if source == "gateway" {
						if !strings.Contains(got.Error(), "agent RPC failed") || strings.Contains(got.Error(), "plan usage exhausted") || strings.Contains(got.Error(), "authentication failed") {
							t.Fatalf("gateway non-handshake billing must stay generic: %v", got)
						}
						return
					}
					if !strings.Contains(got.Error(), "plan usage exhausted; check Anthropic billing and quota for the API key") {
						t.Fatalf("first-party non-handshake billing must keep plan taxonomy: %v", got)
					}
					assertSafeError(t, got)
				})
			}
		}
	}
}

func assertSafeError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, text := range []string{fmt.Sprint(err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		if strings.Contains(text, "test-only-credential") || strings.Contains(text, "private-account-data") || strings.Contains(text, "unsafe-wrapper") {
			t.Errorf("untrusted details leaked: %s", text)
		}
	}
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range e.Unwrap() {
			assertSafeError(t, child)
		}
	case interface{ Unwrap() error }:
		assertSafeError(t, e.Unwrap())
	}
}
