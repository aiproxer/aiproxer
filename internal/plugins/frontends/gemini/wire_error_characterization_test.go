package gemini_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
)

type charErrExecutor struct{ err error }

func (e *charErrExecutor) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return nil, e.err
}
func (e *charErrExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }
func (e *charErrExecutor) WallClock() func() time.Time                                { return nil }

func TestWireErrorCharacterization_executeErrors(t *testing.T) {
	t.Parallel()
	body := policyBody(t)
	cases := []struct {
		name        string
		err         error
		wantStatus  int
		wantGStatus string
		wantMsg     string
	}{
		{
			name:        "policy_denied",
			err:         aipapi.NewPolicyDeniedError("pre_request", "p", "policy_denied", "policy_denied", execerr.PolicyDeniedWireMessage, nil),
			wantStatus:  http.StatusForbidden,
			wantGStatus: "PERMISSION_DENIED",
			wantMsg:     execerr.PolicyDeniedWireMessage,
		},
		{
			name:        "policy_failure_overloaded",
			err:         aipapi.NewPolicyFailureError("tool_policy", "p", "policy_timeout", "policy_failure", "", nil),
			wantStatus:  http.StatusServiceUnavailable,
			wantGStatus: "UNAVAILABLE",
			wantMsg:     execerr.PolicyFailureWireMessage,
		},
		{
			name:        "policy_malformed",
			err:         aipapi.NewPolicyMalformedError("completion", "p", "policy_malformed", "policy_malformed", "", nil),
			wantStatus:  http.StatusInternalServerError,
			wantGStatus: "INTERNAL",
			wantMsg:     execerr.PolicyMalformedWireMessage,
		},
		{
			name:        "capability_reject",
			err:         &aipapi.RejectError{Reason: "missing capability"},
			wantStatus:  http.StatusBadRequest,
			wantGStatus: "INVALID_ARGUMENT",
			wantMsg:     "missing capability",
		},
		{
			name:        "backend_permission_denied",
			err:         &prerequest.RejectError{Message: "backend permission denied"},
			wantStatus:  http.StatusForbidden,
			wantGStatus: "PERMISSION_DENIED",
			wantMsg:     "backend permission denied",
		},
		{
			name:        "internal_error",
			err:         errors.New("boom"),
			wantStatus:  http.StatusInternalServerError,
			wantGStatus: "INTERNAL",
			wantMsg:     execerr.InternalWireMessage,
		},
		{
			name:        "billing_denied_insufficient_credit",
			err:         fmt.Errorf("%w: %w", runtime.ErrBillingAdmissionDenied, billingport.ErrInsufficientSpendable),
			wantStatus:  http.StatusTooManyRequests,
			wantGStatus: "RESOURCE_EXHAUSTED",
			wantMsg:     execerr.InsufficientCreditWireMessage,
		},
		{
			name:        "billing_unavailable",
			err:         fmt.Errorf("%w: %w", runtime.ErrBillingAdmissionDenied, billingport.ErrBillingStoreUnavailable),
			wantStatus:  http.StatusServiceUnavailable,
			wantGStatus: "UNAVAILABLE",
			wantMsg:     execerr.BillingUnavailableWireMessage,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &gemini.Handler{
				Exec:                 &charErrExecutor{err: tc.err},
				DefaultRouteSelector: "stub:gemini-2.0-flash",
			}
			req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status: got %d want %d body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			code, msg, gstatus := decodeGeminiAPIError(t, rr.Body.Bytes())
			if code != tc.wantStatus || msg != tc.wantMsg || gstatus != tc.wantGStatus {
				t.Fatalf("got code=%d msg=%q status=%q want code=%d msg=%q status=%q", code, msg, gstatus, tc.wantStatus, tc.wantMsg, tc.wantGStatus)
			}
			_, msg2, gs2 := frontendpipe.MapGeminiExecuteError(execerr.ClassifyExecute(tc.err))
			if msg2 != tc.wantMsg || gs2 != tc.wantGStatus {
				t.Fatalf("mapper drift: msg=%q status=%q", msg2, gs2)
			}
		})
	}
}

func TestWireErrorCharacterization_malformedRequestJSON(t *testing.T) {
	t.Parallel()
	h := &gemini.Handler{Exec: &charErrExecutor{}, DefaultRouteSelector: "stub:gemini-2.0-flash"}
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", bytes.NewReader([]byte("{")))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: %d body=%s", rr.Code, rr.Body.String())
	}
	code, msg, gstatus := decodeGeminiAPIError(t, rr.Body.Bytes())
	if code != 400 || msg != "invalid request JSON" || gstatus != "INVALID_ARGUMENT" {
		t.Fatalf("got code=%d msg=%q status=%q", code, msg, gstatus)
	}
	var envelope struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != 400 {
		t.Fatalf("error.code: %d", envelope.Error.Code)
	}
}
