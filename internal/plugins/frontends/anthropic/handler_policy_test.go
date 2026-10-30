package anthropic_test

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

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type policyErrExecutor struct{ err error }

func (e *policyErrExecutor) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return nil, e.err
}

func (e *policyErrExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }

func (e *policyErrExecutor) WallClock() func() time.Time { return nil }

func newPolicyHandler(err error) *anthropic.Handler {
	return &anthropic.Handler{
		Exec:                 &policyErrExecutor{err: err},
		DefaultRouteSelector: "stub:claude-3-5-haiku-20241022",
	}
}

func policyBody(t *testing.T) []byte {
	t.Helper()
	return []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"ping"}]
}`)
}

func decodeAnthropicAPIError(t *testing.T, body []byte) (message, errType string) {
	t.Helper()
	var payload struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode anthropic error body: %v body=%s", err, body)
	}
	if payload.Type != "error" {
		t.Fatalf("error envelope type: got %q want %q", payload.Type, "error")
	}
	return payload.Error.Message, payload.Error.Type
}

func TestHandlerPolicyErrorsRenderClientSafeMessageAndNoLeak(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
		wantKind execerr.Kind
	}{
		{
			name:     "denied_with_client_message",
			err:      aipapi.NewPolicyDeniedError("pre_request", "secret-provider-id", "policy_denied", "policy_denied", "no tool access", errors.New("raw secret cause with prompt text")),
			wantCode: http.StatusForbidden,
			wantMsg:  "no tool access",
			wantKind: execerr.KindPolicyDenied,
		},
		{
			name:     "denied_default_message",
			err:      aipapi.NewPolicyDeniedError("pre_request", "secret-provider-id", "policy_denied", "policy_denied", "", nil),
			wantCode: http.StatusForbidden,
			wantMsg:  execerr.PolicyDeniedWireMessage,
			wantKind: execerr.KindPolicyDenied,
		},
		{
			name:     "failure_default_message",
			err:      aipapi.NewPolicyFailureError("tool_policy", "secret-provider-id", "policy_timeout", "policy_failure", "", nil),
			wantCode: http.StatusServiceUnavailable,
			wantMsg:  execerr.PolicyFailureWireMessage,
			wantKind: execerr.KindPolicyFailure,
		},
		{
			name:     "malformed_default_message",
			err:      aipapi.NewPolicyMalformedError("completion", "secret-provider-id", "policy_malformed", "policy_malformed", "", nil),
			wantCode: http.StatusInternalServerError,
			wantMsg:  execerr.PolicyMalformedWireMessage,
			wantKind: execerr.KindPolicyMalformed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newPolicyHandler(tc.err)
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(policyBody(t)))
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
			msg, _ := decodeAnthropicAPIError(t, rr.Body.Bytes())
			if msg != tc.wantMsg {
				t.Fatalf("message: got %q want %q", msg, tc.wantMsg)
			}
			if strings.Contains(msg, "secret-provider-id") {
				t.Fatalf("message leaked provider id: %q", msg)
			}
			if strings.Contains(msg, "raw secret cause") {
				t.Fatalf("message leaked raw cause: %q", msg)
			}
			if strings.Contains(rr.Body.String(), "pre_request") {
				t.Fatalf("body leaked stage: %s", rr.Body.String())
			}
		})
	}
}

func TestHandlerPolicyDeniedRendersDistinctFromInternalAndCapabilityErrors(t *testing.T) {
	t.Parallel()
	body := policyBody(t)
	type triple struct{ code, msg, etype string }
	results := map[string]triple{}
	cases := []struct {
		name string
		err  error
	}{
		{name: "policy_denied", err: aipapi.NewPolicyDeniedError("pre_request", "p1", "policy_denied", "policy_denied", "request denied by policy", nil)},
		{name: "generic_internal", err: errors.New("boom from downstream backend")},
		{name: "capability_reject", err: &aipapi.RejectError{Reason: "missing capability"}},
	}
	for _, tc := range cases {
		h := newPolicyHandler(tc.err)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		msg, etype := decodeAnthropicAPIError(t, rr.Body.Bytes())
		results[tc.name] = triple{http.StatusText(rr.Code), msg, etype}
	}
	// Assert policy denied differs from generic internal and capability reject by code+message
	// (the spec requires distinct rendering, not a protocol-specific type rename; the existing
	// anthropic renderer already surfaces the client-safe message and correct status).
	denied := results["policy_denied"]
	if denied.code == results["generic_internal"].code && denied.msg == results["generic_internal"].msg {
		t.Fatalf("policy denied not distinct from internal: %+v", denied)
	}
	if denied.code == results["capability_reject"].code && denied.msg == results["capability_reject"].msg {
		t.Fatalf("policy denied not distinct from capability reject: %+v", denied)
	}
	if denied.msg == execerr.InternalWireMessage {
		t.Fatalf("policy denied rendered internal wire message: %q", denied.msg)
	}
}

func TestHandlerSuccessShapeUnchangedWithoutPolicy(t *testing.T) {
	t.Parallel()
	exec := &recordingExecutor{}
	h := &anthropic.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:claude-3-5-haiku-20241022",
	}
	body := policyBody(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
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
		Type string `json:"type"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode success body: %v body=%s", err, rr.Body.String())
	}
	if resp.Type != "message" {
		t.Fatalf("success type: got %q want %q", resp.Type, "message")
	}
	if strings.Contains(rr.Body.String(), execerr.PolicyDeniedWireMessage) {
		t.Fatalf("success body leaked policy denial message: %s", rr.Body.String())
	}
}
