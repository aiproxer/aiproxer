package execerr_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
)

func TestClassifyExecute_UnsafeExecutionComposition(t *testing.T) {
	t.Parallel()
	err := &routing.UnsafeExecutionCompositionError{
		Composition: "failover",
		BackendID:   "acp",
		Class:       aipsdk.BackendExecutionAgentRuntime,
	}
	wrapped := fmt.Errorf("executor: %w", err)
	out := execerr.ClassifyExecute(wrapped)
	if out.Kind != execerr.KindClientReject {
		t.Fatalf("kind: want KindClientReject, got %v", out.Kind)
	}
	if out.Status != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", out.Status)
	}
	if !strings.Contains(out.Message, "unsafe backend execution composition") {
		t.Fatalf("message: want unsafe backend execution composition, got %q", out.Message)
	}
	if !errors.Is(out.Err, routing.ErrUnsafeExecutionComposition) {
		t.Fatal("expected Err to unwrap to ErrUnsafeExecutionComposition")
	}
}

func TestClassifyExecute_reject(t *testing.T) {
	t.Parallel()
	err := &aipapi.RejectError{Reason: "missing thing"}
	out := execerr.ClassifyExecute(err)
	if out.Kind != execerr.KindClientReject {
		t.Fatalf("kind: %v", out.Kind)
	}
	if out.Status != http.StatusBadRequest {
		t.Fatalf("status: %d", out.Status)
	}
	if out.Message != "missing thing" {
		t.Fatalf("message: %q", out.Message)
	}
	if out.Err != err {
		t.Fatalf("Err: want same reject pointer")
	}
}

func TestClassifyExecute_rejectWrappedUpstreamDetailNotOnWire(t *testing.T) {
	t.Parallel()
	rej := &aipapi.RejectError{Reason: "missing required capabilities: vision"}
	err := fmt.Errorf("executor submit upstream=http://internal-host:9090/v1 token=sekret: %w", rej)
	out := execerr.ClassifyExecute(err)
	if out.Kind != execerr.KindClientReject || out.Status != http.StatusBadRequest {
		t.Fatalf("kind=%v status=%d", out.Kind, out.Status)
	}
	if strings.Contains(out.Message, "internal-host") || strings.Contains(out.Message, "sekret") ||
		strings.Contains(out.Message, "executor submit") {
		t.Fatalf("message must not leak wrapped upstream detail: %q", out.Message)
	}
	if out.Message != "missing required capabilities: vision" {
		t.Fatalf("message: %q", out.Message)
	}
	if !errors.Is(out.Err, err) {
		t.Fatalf("Err: want original wrapped error for logging")
	}
}

func TestClassifyExecute_rejectUnsafeReasonFallsBack(t *testing.T) {
	t.Parallel()
	// Reason carrying only control characters normalizes to empty: the wire must
	// get the stable fallback, never raw control-laden text.
	err := &aipapi.RejectError{Reason: "\n\r\t"}
	out := execerr.ClassifyExecute(err)
	if out.Message != execerr.ClientRejectWireMessage {
		t.Fatalf("message: %q want fallback %q", out.Message, execerr.ClientRejectWireMessage)
	}
}

func TestClassifyExecute_rejectReasonNormalized(t *testing.T) {
	t.Parallel()
	err := &aipapi.RejectError{Reason: "bad\ninput\t" + strings.Repeat("x", 400)}
	out := execerr.ClassifyExecute(err)
	if strings.ContainsAny(out.Message, "\n\t\r") {
		t.Fatalf("message must be control-free: %q", out.Message)
	}
	if len(out.Message) > aipapi.MaxClientMessageBytes {
		t.Fatalf("message len=%d exceeds %d", len(out.Message), aipapi.MaxClientMessageBytes)
	}
}

func TestClassifyExecute_contextLimitExceeded(t *testing.T) {
	t.Parallel()
	err := aipapi.ErrAllCandidatesContextLimitExceeded
	out := execerr.ClassifyExecute(err)
	if out.Kind != execerr.KindClientReject {
		t.Fatalf("kind: %v", out.Kind)
	}
	if out.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d", out.Status)
	}
	if out.Message != execerr.ContextLimitExceededWireMessage {
		t.Fatalf("message: %q", out.Message)
	}
	if !aipapi.IsAllCandidatesContextLimitExceeded(out.Err) {
		t.Fatalf("Err should wrap sentinel, got %v", out.Err)
	}
}

func TestClassifyExecute_preRequestReject(t *testing.T) {
	t.Parallel()
	err := prerequest.NewRejectError("policy", "blocked by policy")
	out := execerr.ClassifyExecute(err)
	if out.Kind != execerr.KindClientReject {
		t.Fatalf("kind: %v", out.Kind)
	}
	if out.Status != http.StatusForbidden {
		t.Fatalf("status: %d", out.Status)
	}
	if out.Message != "blocked by policy" {
		t.Fatalf("message: %q", out.Message)
	}
	if out.Err != err {
		t.Fatalf("Err: want original reject")
	}
}

func TestClassifyExecute_preRequestRejectDefaultMessage(t *testing.T) {
	t.Parallel()
	err := prerequest.NewRejectError("policy", "")
	out := execerr.ClassifyExecute(err)
	if out.Status != http.StatusForbidden {
		t.Fatalf("status: %d", out.Status)
	}
	if out.Message == "" || strings.Contains(out.Message, "policy") {
		t.Fatalf("message should be client-safe default, got %q", out.Message)
	}
}

func TestClassifyExecute_contextLimitExceeded_wrapped(t *testing.T) {
	t.Parallel()
	out := execerr.ClassifyExecute(fmt.Errorf("plan: %w", aipapi.ErrAllCandidatesContextLimitExceeded))
	if out.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d", out.Status)
	}
	if out.Message != execerr.ContextLimitExceededWireMessage {
		t.Fatalf("message: %q", out.Message)
	}
}

func TestClassifyExecute_billingAdmission(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		err        error
		wantKind   execerr.Kind
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "insufficient_spendable_execute_wrap",
			err:        fmt.Errorf("%w: %w", runtime.ErrBillingAdmissionDenied, billingport.ErrInsufficientSpendable),
			wantKind:   execerr.KindBillingDenied,
			wantStatus: http.StatusTooManyRequests,
			wantMsg:    execerr.InsufficientCreditWireMessage,
		},
		{
			name:       "account_not_ready_execute_wrap",
			err:        fmt.Errorf("%w: %w", runtime.ErrBillingAdmissionDenied, billingport.ErrAccountNotReady),
			wantKind:   execerr.KindBillingDenied,
			wantStatus: http.StatusTooManyRequests,
			wantMsg:    execerr.InsufficientCreditWireMessage,
		},
		{
			name:       "authorization_unavailable_execute_wrap",
			err:        fmt.Errorf("%w: %w", runtime.ErrBillingAdmissionDenied, billingport.ErrBillingStoreUnavailable),
			wantKind:   execerr.KindBillingUnavailable,
			wantStatus: http.StatusServiceUnavailable,
			wantMsg:    execerr.BillingUnavailableWireMessage,
		},
		{
			name:       "admission_denied_without_billing_sentinel_stays_internal",
			err:        fmt.Errorf("%w: missing prepared route plan", runtime.ErrBillingAdmissionDenied),
			wantKind:   execerr.KindInternalError,
			wantStatus: http.StatusInternalServerError,
			wantMsg:    execerr.InternalWireMessage,
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

func TestClassifyExecute_internal(t *testing.T) {
	t.Parallel()
	err := errors.New("backend unavailable")
	out := execerr.ClassifyExecute(err)
	if out.Kind != execerr.KindInternalError {
		t.Fatalf("kind: %v", out.Kind)
	}
	if out.Status != http.StatusInternalServerError {
		t.Fatalf("status: %d", out.Status)
	}
	if out.Message != execerr.InternalWireMessage {
		t.Fatalf("message: %q (want non-revealing wire text)", out.Message)
	}
	if out.Err != err {
		t.Fatalf("Err: want original for logging")
	}
}

func TestClassifyExecute_nil(t *testing.T) {
	t.Parallel()
	out := execerr.ClassifyExecute(nil)
	if out.Kind != execerr.KindInternalError {
		t.Fatalf("kind: %v", out.Kind)
	}
	if out.Status != http.StatusInternalServerError {
		t.Fatalf("status: %d", out.Status)
	}
	if out.Message != execerr.UnknownExecuteErrorMessage {
		t.Fatalf("message: %q", out.Message)
	}
	if out.Err != nil {
		t.Fatalf("Err: %v", out.Err)
	}
}

func TestClassifyExecute_sessionDenial_matrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		err           error
		wantStatus    int
		wantKind      execerr.Kind
		wantCode      string
		wantMsgSubstr string
	}{
		{"missing_principal", aipapi.NewSessionDenialMissingPrincipal("internal"), http.StatusUnauthorized, execerr.KindSessionDenial, string(aipapi.SessionDeniedMissingPrincipal), "identity"},
		{"invalid_authority", aipapi.NewSessionDenialInvalidAuthority("internal"), http.StatusBadRequest, execerr.KindSessionDenial, string(aipapi.SessionDeniedInvalidAuthority), "resumed"},
		{"owner_mismatch", aipapi.NewSessionDenialOwnerMismatch("internal"), http.StatusBadRequest, execerr.KindSessionDenial, string(aipapi.SessionDeniedOwnerMismatch), "resumed"},
		{"resume_expired", aipapi.NewSessionDenialResumeExpired("internal"), http.StatusBadRequest, execerr.KindSessionDenial, string(aipapi.SessionDeniedResumeExpired), "longer"},
		{"workspace", aipapi.NewSessionDenialWorkspace("internal"), http.StatusBadRequest, execerr.KindSessionDenial, string(aipapi.SessionDeniedWorkspace), "workspace"},
		{"policy_unavailable", aipapi.NewSessionDenialPolicyUnavailable("internal"), http.StatusServiceUnavailable, execerr.KindSessionDenial, string(aipapi.SessionDeniedPolicyUnavailable), "policy"},
		{"storage_unavailable", aipapi.NewSessionDenialStorageUnavailable("internal"), http.StatusServiceUnavailable, execerr.KindSessionDenial, string(aipapi.SessionDeniedStorageUnavailable), "storage"},
		{"mandatory_audit", aipapi.NewSessionDenialMandatoryAuditFailure("internal"), http.StatusServiceUnavailable, execerr.KindSessionDenial, string(aipapi.SessionDeniedMandatoryAuditFailure), "recorded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := execerr.ClassifyExecute(tc.err)
			if out.Kind != tc.wantKind {
				t.Fatalf("kind for %v: got %v want %v", tc.wantCode, out.Kind, tc.wantKind)
			}
			if out.Status != tc.wantStatus {
				t.Fatalf("status for %v: got %d want %d", tc.wantCode, out.Status, tc.wantStatus)
			}
			if out.SessionPublicCode != tc.wantCode {
				t.Fatalf("SessionPublicCode: got %q want %q", out.SessionPublicCode, tc.wantCode)
			}
			if tc.wantMsgSubstr != "" && !strings.Contains(out.Message, tc.wantMsgSubstr) {
				t.Fatalf("message for %v: got %q want substring %q", tc.wantCode, out.Message, tc.wantMsgSubstr)
			}
			var sd *aipapi.SessionDenialError
			if !aipapi.IsSessionDenial(out.Err) || !errors.As(out.Err, &sd) {
				t.Fatalf("expected session denial in Err")
			}
			if strings.Contains(out.Message, "internal") {
				t.Fatalf("message must not leak internal reason: %q", out.Message)
			}
		})
	}
}
