package openailegacy_test

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
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
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
		name       string
		err        error
		wantStatus int
		wantType   string
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
			name:       "internal_error",
			err:        errors.New("boom"),
			wantStatus: http.StatusInternalServerError,
			wantType:   "api_error",
			wantMsg:    execerr.InternalWireMessage,
		},
		{
			name:       "billing_denied_insufficient_credit",
			err:        fmt.Errorf("%w: %w", runtime.ErrBillingAdmissionDenied, billingport.ErrInsufficientSpendable),
			wantStatus: http.StatusTooManyRequests,
			wantType:   "insufficient_quota",
			wantMsg:    execerr.InsufficientCreditWireMessage,
		},
		{
			name:       "billing_unavailable",
			err:        fmt.Errorf("%w: %w", runtime.ErrBillingAdmissionDenied, billingport.ErrBillingStoreUnavailable),
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "api_error",
			wantMsg:    execerr.BillingUnavailableWireMessage,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &openailegacy.Handler{
				Exec:                 &charErrExecutor{err: tc.err},
				DefaultRouteSelector: "stub:gpt-4o-mini",
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status: got %d want %d body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			msg, typ := decodeOpenAIAPIError(t, rr.Body.Bytes())
			if msg != tc.wantMsg || typ != tc.wantType {
				t.Fatalf("got msg=%q type=%q want msg=%q type=%q", msg, typ, tc.wantMsg, tc.wantType)
			}
			_, msg2, typ2, _ := frontendpipe.MapOpenAIExecuteError(execerr.ClassifyExecute(tc.err))
			if msg2 != tc.wantMsg || typ2 != tc.wantType {
				t.Fatalf("mapper drift: msg=%q type=%q", msg2, typ2)
			}
		})
	}
}

func TestWireErrorCharacterization_malformedRequestJSON(t *testing.T) {
	t.Parallel()
	h := &openailegacy.Handler{Exec: &charErrExecutor{}, DefaultRouteSelector: "stub:gpt-4o-mini"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte("{")))
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
	var payload struct {
		Error struct {
			Param any `json:"param"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Param != nil {
		t.Fatalf("param must be null")
	}
}
