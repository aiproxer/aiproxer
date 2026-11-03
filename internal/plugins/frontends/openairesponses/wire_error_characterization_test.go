package openairesponses_test

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
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
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
	quotaCode := "insufficient_quota"
	permissionCode := "unsupported_parameter"
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantType   string
		wantCode   *string
		wantMsg    string
	}{
		{
			name:       "policy_denied",
			err:        aipapi.NewPolicyDeniedError("pre_request", "p", "policy_denied", "policy_denied", execerr.PolicyDeniedWireMessage, nil),
			wantStatus: http.StatusForbidden,
			wantType:   "invalid_request_error",
			wantMsg:    execerr.PolicyDeniedWireMessage,
		},
		{
			name:       "policy_failure_overloaded",
			err:        aipapi.NewPolicyFailureError("tool_policy", "p", "policy_timeout", "policy_failure", "", nil),
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "api_error",
			wantMsg:    execerr.PolicyFailureWireMessage,
		},
		{
			name:       "policy_malformed",
			err:        aipapi.NewPolicyMalformedError("completion", "p", "policy_malformed", "policy_malformed", "", nil),
			wantStatus: http.StatusInternalServerError,
			wantType:   "api_error",
			wantMsg:    execerr.PolicyMalformedWireMessage,
		},
		{
			name:       "capability_reject",
			err:        &aipapi.RejectError{Reason: "missing capability"},
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
			wantMsg:    "missing capability",
		},
		{
			name:       "backend_permission_denied",
			err:        &prerequest.RejectError{Message: "backend permission denied"},
			wantStatus: http.StatusForbidden,
			wantType:   "invalid_request_error",
			wantCode:   &permissionCode,
			wantMsg:    "backend permission denied",
		},
		{
			name:       "billing_denied_insufficient_credit",
			err:        fmt.Errorf("%w: %w", runtime.ErrBillingAdmissionDenied, billingport.ErrInsufficientSpendable),
			wantStatus: http.StatusTooManyRequests,
			wantType:   "insufficient_quota",
			wantCode:   &quotaCode,
			wantMsg:    execerr.InsufficientCreditWireMessage,
		},
		{
			name:       "billing_unavailable",
			err:        fmt.Errorf("%w: %w", runtime.ErrBillingAdmissionDenied, billingport.ErrBillingStoreUnavailable),
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "api_error",
			wantMsg:    execerr.BillingUnavailableWireMessage,
		},
		{
			name:       "internal_error",
			err:        errors.New("boom"),
			wantStatus: http.StatusInternalServerError,
			wantType:   "api_error",
			wantMsg:    execerr.InternalWireMessage,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &openairesponses.Handler{
				Exec:                 &charErrExecutor{err: tc.err},
				DefaultRouteSelector: "stub:gpt-4o-mini",
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status: got %d want %d body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			var payload struct {
				Error struct {
					Message string  `json:"message"`
					Type    string  `json:"type"`
					Param   any     `json:"param"`
					Code    *string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode: %v body=%s", err, rr.Body.Bytes())
			}
			if payload.Error.Message != tc.wantMsg || payload.Error.Type != tc.wantType {
				t.Fatalf("got msg=%q type=%q want msg=%q type=%q", payload.Error.Message, payload.Error.Type, tc.wantMsg, tc.wantType)
			}
			if payload.Error.Param != nil {
				t.Fatalf("param must be null, got %v", payload.Error.Param)
			}
			if tc.wantCode != nil {
				if payload.Error.Code == nil || *payload.Error.Code != *tc.wantCode {
					t.Fatalf("code: got %v want %v", payload.Error.Code, tc.wantCode)
				}
			} else if tc.name == "capability_reject" {
				code := "unsupported_parameter"
				if payload.Error.Code == nil || *payload.Error.Code != code {
					t.Fatalf("code: got %v want %q", payload.Error.Code, code)
				}
			} else if payload.Error.Code != nil {
				t.Fatalf("unexpected code pointer: %v", *payload.Error.Code)
			}
			st, msg, et, _ := frontendpipe.MapOpenAIExecuteError(execerr.ClassifyExecute(tc.err))
			if st != tc.wantStatus || msg != tc.wantMsg || et != tc.wantType {
				t.Fatalf("mapper drift: (%d,%q,%q)", st, msg, et)
			}
		})
	}
}

func TestWireErrorCharacterization_malformedRequestJSON(t *testing.T) {
	t.Parallel()
	h := &openairesponses.Handler{
		Exec:                 &charErrExecutor{},
		DefaultRouteSelector: "stub:gpt-4o-mini",
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte("{")))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: %d body=%s", rr.Code, rr.Body.String())
	}
	msg, typ := decodeOpenAIAPIError(t, rr.Body.Bytes())
	if msg != "invalid request JSON" || typ != "invalid_request_error" {
		t.Fatalf("got msg=%q type=%q", msg, typ)
	}
}
