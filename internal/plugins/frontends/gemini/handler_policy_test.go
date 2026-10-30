package gemini_test

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
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type policyErrExecutor struct{ err error }

func (e *policyErrExecutor) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return nil, e.err
}

func (e *policyErrExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }

func (e *policyErrExecutor) WallClock() func() time.Time { return nil }

func newPolicyHandler(err error) *gemini.Handler {
	return &gemini.Handler{
		Exec:                 &policyErrExecutor{err: err},
		DefaultRouteSelector: "stub:gemini-2.0-flash",
	}
}

func policyBody(t *testing.T) []byte {
	t.Helper()
	return []byte(`{
  "contents": [{"role":"user","parts":[{"text":"ping"}]}],
  "generationConfig": {"maxOutputTokens": 128, "temperature": 0.5, "topP": 0.9}
}`)
}

func decodeGeminiAPIError(t *testing.T, body []byte) (code int, message, gstatus string) {
	t.Helper()
	var payload struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode gemini error body: %v body=%s", err, body)
	}
	return payload.Error.Code, payload.Error.Message, payload.Error.Status
}

func TestHandlerPolicyErrorsRenderClientSafeMessageAndDistinctStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		err         error
		wantCode    int
		wantMsg     string
		wantGStatus string
		wantKind    execerr.Kind
	}{
		{
			name:        "denied_with_client_message",
			err:         aipapi.NewPolicyDeniedError("pre_request", "secret-provider-id", "policy_denied", "policy_denied", "no tool access", errors.New("raw secret cause with prompt text")),
			wantCode:    http.StatusForbidden,
			wantMsg:     "no tool access",
			wantGStatus: "PERMISSION_DENIED",
			wantKind:    execerr.KindPolicyDenied,
		},
		{
			name:        "denied_default_message",
			err:         aipapi.NewPolicyDeniedError("pre_request", "secret-provider-id", "policy_denied", "policy_denied", "", nil),
			wantCode:    http.StatusForbidden,
			wantMsg:     execerr.PolicyDeniedWireMessage,
			wantGStatus: "PERMISSION_DENIED",
			wantKind:    execerr.KindPolicyDenied,
		},
		{
			name:        "failure_default_message",
			err:         aipapi.NewPolicyFailureError("tool_policy", "secret-provider-id", "policy_timeout", "policy_failure", "", nil),
			wantCode:    http.StatusServiceUnavailable,
			wantMsg:     execerr.PolicyFailureWireMessage,
			wantGStatus: "UNAVAILABLE",
			wantKind:    execerr.KindPolicyFailure,
		},
		{
			name:        "malformed_default_message",
			err:         aipapi.NewPolicyMalformedError("completion", "secret-provider-id", "policy_malformed", "policy_malformed", "", nil),
			wantCode:    http.StatusInternalServerError,
			wantMsg:     execerr.PolicyMalformedWireMessage,
			wantGStatus: "INTERNAL",
			wantKind:    execerr.KindPolicyMalformed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newPolicyHandler(tc.err)
			req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", bytes.NewReader(policyBody(t)))
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
			code, msg, gstatus := decodeGeminiAPIError(t, rr.Body.Bytes())
			if code != tc.wantCode {
				t.Fatalf("error.code: got %d want %d", code, tc.wantCode)
			}
			if msg != tc.wantMsg {
				t.Fatalf("message: got %q want %q", msg, tc.wantMsg)
			}
			if gstatus != tc.wantGStatus {
				t.Fatalf("error.status: got %q want %q", gstatus, tc.wantGStatus)
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
	type triple struct{ code, msg, gstatus string }
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
		req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		_, msg, gstatus := decodeGeminiAPIError(t, rr.Body.Bytes())
		results[tc.name] = triple{http.StatusText(rr.Code), msg, gstatus}
	}
	denied := results["policy_denied"]
	internal := results["generic_internal"]
	if denied.code == internal.code && denied.msg == internal.msg {
		t.Fatalf("policy denied not distinct from internal: %+v", denied)
	}
	if denied.gstatus == internal.gstatus && denied.msg == internal.msg {
		t.Fatalf("policy denied not distinct from internal by status+message: %+v", denied)
	}
	if denied.msg == execerr.InternalWireMessage {
		t.Fatalf("policy denied rendered internal wire message: %q", denied.msg)
	}
	if denied.gstatus != "PERMISSION_DENIED" {
		t.Fatalf("policy denied google status: got %q want PERMISSION_DENIED", denied.gstatus)
	}
	if results["capability_reject"].gstatus == denied.gstatus && results["capability_reject"].msg == denied.msg {
		t.Fatalf("policy denied not distinct from capability reject: %+v", denied)
	}
}

func TestHandlerSuccessShapeUnchangedWithoutPolicy(t *testing.T) {
	t.Parallel()
	exec := &recordingExecutor{}
	h := &gemini.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:gemini-2.0-flash",
	}
	body := policyBody(t)
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", bytes.NewReader(body))
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
		Candidates []json.RawMessage `json:"candidates"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode success body: %v body=%s", err, rr.Body.String())
	}
	if len(resp.Candidates) == 0 {
		t.Fatalf("success body missing candidates: %s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), execerr.PolicyDeniedWireMessage) {
		t.Fatalf("success body leaked policy denial message: %s", rr.Body.String())
	}
}
