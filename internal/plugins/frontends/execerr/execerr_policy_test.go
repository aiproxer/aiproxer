package execerr_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
)

func TestClassifyExecutePolicyErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		err        error
		wantKind   execerr.Kind
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "denied_with_client_message",
			err:        aipapi.NewPolicyDeniedError("pre_request", "p1", "policy_denied", "policy_denied", "no tool access", nil),
			wantKind:   execerr.KindPolicyDenied,
			wantStatus: http.StatusForbidden,
			wantMsg:    "no tool access",
		},
		{
			name:       "denied_without_client_message_uses_default",
			err:        aipapi.NewPolicyDeniedError("pre_request", "p1", "policy_denied", "policy_denied", "", nil),
			wantKind:   execerr.KindPolicyDenied,
			wantStatus: http.StatusForbidden,
			wantMsg:    execerr.PolicyDeniedWireMessage,
		},
		{
			name:       "failure_default_message",
			err:        aipapi.NewPolicyFailureError("tool_policy", "p2", "policy_timeout", "policy_failure", "", nil),
			wantKind:   execerr.KindPolicyFailure,
			wantStatus: http.StatusServiceUnavailable,
			wantMsg:    execerr.PolicyFailureWireMessage,
		},
		{
			name:       "malformed_default_message",
			err:        aipapi.NewPolicyMalformedError("completion", "p3", "policy_malformed", "policy_malformed", "", nil),
			wantKind:   execerr.KindPolicyMalformed,
			wantStatus: http.StatusInternalServerError,
			wantMsg:    execerr.PolicyMalformedWireMessage,
		},
		{
			name:       "denied_wrapped_propagates_through_errors_is",
			err:        fmt.Errorf("wrap: %w", aipapi.NewPolicyDeniedError("pre_request", "p1", "policy_denied", "policy_denied", "wrapped msg", nil)),
			wantKind:   execerr.KindPolicyDenied,
			wantStatus: http.StatusForbidden,
			wantMsg:    "wrapped msg",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := execerr.ClassifyExecute(tc.err)
			if out.Kind != tc.wantKind {
				t.Fatalf("kind: got %v want %v", out.Kind, tc.wantKind)
			}
			if out.Status != tc.wantStatus {
				t.Fatalf("status: got %d want %d", out.Status, tc.wantStatus)
			}
			if out.Message != tc.wantMsg {
				t.Fatalf("message: got %q want %q", out.Message, tc.wantMsg)
			}
			if out.Err == nil || !errors.Is(out.Err, tc.err) {
				t.Fatalf("Err must preserve original error, got %v", out.Err)
			}
		})
	}
}

func TestClassifyExecutePolicyDistinctFromOtherErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		err      error
		wantKind execerr.Kind
	}{
		{
			name:     "session_denial_is_not_policy",
			err:      aipapi.NewSessionDenialMissingPrincipal("internal reason"),
			wantKind: execerr.KindSessionDenial,
		},
		{
			name:     "capability_reject_is_not_policy",
			err:      &aipapi.RejectError{Reason: "missing capability"},
			wantKind: execerr.KindClientReject,
		},
		{
			name:     "prerequest_reject_is_not_policy",
			err:      prerequest.NewRejectError("h1", "no"),
			wantKind: execerr.KindClientReject,
		},
		{
			name:     "plain_internal_error_is_not_policy",
			err:      errors.New("boom"),
			wantKind: execerr.KindInternalError,
		},
		{
			name:     "policy_denied_is_distinct",
			err:      aipapi.NewPolicyDeniedError("pre_request", "p1", "policy_denied", "policy_denied", "denied", nil),
			wantKind: execerr.KindPolicyDenied,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := execerr.ClassifyExecute(tc.err)
			if out.Kind != tc.wantKind {
				t.Fatalf("%s: kind got %v want %v", tc.name, out.Kind, tc.wantKind)
			}
		})
	}
}

func TestClassifyExecutePolicyMessageIsClientSafe(t *testing.T) {
	t.Parallel()
	err := aipapi.NewPolicyDeniedError(
		"pre_request",
		"secret-provider-id",
		"policy_denied",
		"policy_denied",
		"safe client msg",
		errors.New("raw secret cause with prompt text"),
	)
	out := execerr.ClassifyExecute(err)
	if out.Message != "safe client msg" {
		t.Fatalf("message: got %q want %q", out.Message, "safe client msg")
	}
	if strings.Contains(out.Message, "secret-provider-id") {
		t.Fatalf("message leaked provider id: %q", out.Message)
	}
	if strings.Contains(out.Message, "raw secret cause") {
		t.Fatalf("message leaked cause text: %q", out.Message)
	}
}

func TestClassifyExecutePolicyMessageBoundedToMaxBytes(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 600)
	err := aipapi.NewPolicyDeniedError("pre_request", "p1", "policy_denied", "policy_denied", long, nil)
	out := execerr.ClassifyExecute(err)
	if out.Kind != execerr.KindPolicyDenied {
		t.Fatalf("kind: got %v want %v", out.Kind, execerr.KindPolicyDenied)
	}
	if len(out.Message) > aipapi.MaxClientMessageBytes {
		t.Fatalf("wire message not bounded: len=%d", len(out.Message))
	}
	if out.Message != long[:aipapi.MaxClientMessageBytes] {
		t.Fatalf("wire message = %q, want first %d bytes", out.Message, aipapi.MaxClientMessageBytes)
	}
}

func TestClassifyExecutePolicyMessageStripsControlChars(t *testing.T) {
	t.Parallel()
	err := aipapi.NewPolicyDeniedError("pre_request", "p1", "policy_denied", "policy_denied", "no\x07access\x00now", nil)
	out := execerr.ClassifyExecute(err)
	if strings.ContainsAny(out.Message, "\x00\x07") {
		t.Fatalf("control characters survived on wire: %q", out.Message)
	}
}

func TestClassifyExecutePrerequestRejectMessageBounded(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("y", 600)
	out := execerr.ClassifyExecute(prerequest.NewRejectError("h1", long))
	if out.Kind != execerr.KindClientReject {
		t.Fatalf("kind: got %v want %v", out.Kind, execerr.KindClientReject)
	}
	if len(out.Message) > aipapi.MaxClientMessageBytes {
		t.Fatalf("prerequest reject wire message not bounded: len=%d", len(out.Message))
	}
	if out.Message != long[:aipapi.MaxClientMessageBytes] {
		t.Fatalf("wire message = %q, want first %d bytes", out.Message, aipapi.MaxClientMessageBytes)
	}
}

func TestClassifyExecutePrerequestRejectControlCharsStripped(t *testing.T) {
	t.Parallel()
	out := execerr.ClassifyExecute(prerequest.NewRejectError("h1", "no\x07access\x00now"))
	if strings.ContainsAny(out.Message, "\x00\x07") {
		t.Fatalf("control characters survived on wire: %q", out.Message)
	}
}
