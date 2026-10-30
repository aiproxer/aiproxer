package openairesponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// policyErrExecutor returns a fixed error from Execute so policy error rendering can be
// exercised end-to-end through the handler (decode -> executor -> execerr -> WriteErrorJSON).
type policyErrExecutor struct{ err error }

func (e *policyErrExecutor) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return nil, e.err
}

func (e *policyErrExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }

func (e *policyErrExecutor) WallClock() func() time.Time { return nil }

func newPolicyHandler(err error) *openairesponses.Handler {
	return &openairesponses.Handler{
		Exec:                 &policyErrExecutor{err: err},
		DefaultRouteSelector: "stub:gpt-4o-mini",
	}
}

func policyBody(t *testing.T) []byte {
	t.Helper()
	return readGolden(t, "create_text_nonstream.json")
}

func decodeOpenAIAPIError(t *testing.T, body []byte) (message, errType string) {
	t.Helper()
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode openai error body: %v body=%s", err, body)
	}
	return payload.Error.Message, payload.Error.Type
}

func TestHandlerPolicyDeniedRendersClientSafeMessageAndDistinctType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
		wantType string
		wantKind execerr.Kind
	}{
		{
			name:     "denied_with_client_message",
			err:      aipapi.NewPolicyDeniedError("pre_request", "secret-provider-id", "policy_denied", "policy_denied", "no tool access", errors.New("raw secret cause with prompt text")),
			wantCode: http.StatusForbidden,
			wantMsg:  "no tool access",
			wantType: "invalid_request_error",
			wantKind: execerr.KindPolicyDenied,
		},
		{
			name:     "denied_default_message",
			err:      aipapi.NewPolicyDeniedError("pre_request", "secret-provider-id", "policy_denied", "policy_denied", "", nil),
			wantCode: http.StatusForbidden,
			wantMsg:  execerr.PolicyDeniedWireMessage,
			wantType: "invalid_request_error",
			wantKind: execerr.KindPolicyDenied,
		},
		{
			name:     "failure_default_message",
			err:      aipapi.NewPolicyFailureError("tool_policy", "secret-provider-id", "policy_timeout", "policy_failure", "", nil),
			wantCode: http.StatusServiceUnavailable,
			wantMsg:  execerr.PolicyFailureWireMessage,
			wantType: "api_error",
			wantKind: execerr.KindPolicyFailure,
		},
		{
			name:     "malformed_default_message",
			err:      aipapi.NewPolicyMalformedError("completion", "secret-provider-id", "policy_malformed", "policy_malformed", "", nil),
			wantCode: http.StatusInternalServerError,
			wantMsg:  execerr.PolicyMalformedWireMessage,
			wantType: "api_error",
			wantKind: execerr.KindPolicyMalformed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newPolicyHandler(tc.err)
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(policyBody(t)))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if rr.Code != tc.wantCode {
				t.Fatalf("status: got %d want %d body=%s", rr.Code, tc.wantCode, rr.Body.String())
			}
			out := execerr.ClassifyExecute(tc.err)
			if out.Kind != tc.wantKind {
				t.Fatalf("classify kind: got %v want %v", out.Kind, tc.wantKind)
			}
			msg, etype := decodeOpenAIAPIError(t, rr.Body.Bytes())
			if msg != tc.wantMsg {
				t.Fatalf("message: got %q want %q", msg, tc.wantMsg)
			}
			if etype != tc.wantType {
				t.Fatalf("error.type: got %q want %q", etype, tc.wantType)
			}
			if strings.Contains(msg, "secret-provider-id") {
				t.Fatalf("message leaked provider id: %q", msg)
			}
			if strings.Contains(msg, "raw secret cause") {
				t.Fatalf("message leaked raw cause: %q", msg)
			}
		})
	}
}

func TestHandlerPolicyDeniedRendersDistinctFromInternalAndCapabilityErrors(t *testing.T) {
	t.Parallel()
	body := policyBody(t)
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
		wantType string
	}{
		{
			name:     "policy_denied",
			err:      aipapi.NewPolicyDeniedError("pre_request", "p1", "policy_denied", "policy_denied", "request denied by policy", nil),
			wantCode: http.StatusForbidden,
			wantMsg:  "request denied by policy",
			wantType: "invalid_request_error",
		},
		{
			name:     "generic_internal",
			err:      errors.New("boom from downstream backend"),
			wantCode: http.StatusInternalServerError,
			wantMsg:  execerr.InternalWireMessage,
			wantType: "api_error",
		},
		{
			name:     "capability_reject",
			err:      &aipapi.RejectError{Reason: "missing capability"},
			wantCode: http.StatusBadRequest,
			wantMsg:  "missing capability",
			wantType: "invalid_request_error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newPolicyHandler(tc.err)
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("status: got %d want %d body=%s", rr.Code, tc.wantCode, rr.Body.String())
			}
			msg, etype := decodeOpenAIAPIError(t, rr.Body.Bytes())
			if msg != tc.wantMsg || etype != tc.wantType {
				t.Fatalf("got (msg=%q type=%q) want (msg=%q type=%q)", msg, etype, tc.wantMsg, tc.wantType)
			}
		})
	}
}

func TestHandlerSuccessShapeUnchangedWithoutPolicy(t *testing.T) {
	t.Parallel()
	exec := &recordingExecutor{}
	h := &openairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:gpt-4o-mini",
	}
	body := policyBody(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type: %q", ct)
	}
	var resp struct {
		Object string `json:"object"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode success body: %v body=%s", err, rr.Body.String())
	}
	if resp.Object != "response" {
		t.Fatalf("success object: got %q want %q", resp.Object, "response")
	}
	if strings.Contains(rr.Body.String(), execerr.PolicyDeniedWireMessage) {
		t.Fatalf("success body leaked policy denial message: %s", rr.Body.String())
	}
}
