package stdhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionpolicy"
	policyhttp "github.com/aiproxer/aiproxer/internal/stdhttp/terminalpolicy"
)

// RED contract for Task 7.1. The endpoint adapter is deliberately a thin
// provider-neutral HTTP seam over the process-owned policy store. It receives
// secure-session/operator authority that existing middleware has already
// established; it does not resolve credentials, inspect request bodies for
// identity, or know any concrete feature implementation.
//
// Proposed API:
//
//   policyhttp.NewHandler(policyhttp.Options{
//       Store:                    *sessionpolicy.Store,
//       FeatureStatus:            func(context.Context, string) (known, available bool, err error),
//       ResolveClientScope:       func(context.Context, *http.Request, string) (Key, Authority, error),
//       AuthorizeOperatorTarget:  func(context.Context, *http.Request, string, string) (Key, Authority, error),
//       GenerationDefault:        func(string) bool,
//       MaxBodyBytes:             int64,
//   })
//
// The adapter owns only route matching, exact body decoding, status/error
// mapping, and response projection. Policy truth, scope authorization, and
// provider lifecycle remain owned by core/composition.

const (
	policyFeature   = "terminal-decision"
	clientSession   = "secure-session-incarnation-1"
	clientALeg      = "a-leg-1"
	operatorSession = "operator-target-session"
)

type endpointHarness struct {
	store              *sessionpolicy.Store
	key                sessionpolicy.Key
	auth               sessionpolicy.Authority
	clientResolvedPath string

	known       bool
	available   bool
	clientErr   error
	operatorErr error
}

func newEndpointHarness(t *testing.T, maxKeys int) *endpointHarness {
	t.Helper()
	key := sessionpolicy.Key{
		SecureSessionIncarnation: clientSession,
		ALegID:                   clientALeg,
		FeatureID:                policyFeature,
	}
	store := sessionpolicy.NewStore(sessionpolicy.Config{
		MaxKeys:       maxKeys,
		MaxKeyBytes:   128,
		MaxValueBytes: 128,
	})
	if store == nil {
		t.Fatal("NewStore returned nil")
	}
	t.Cleanup(func() { _ = store.Close() })
	return &endpointHarness{
		store:     store,
		key:       key,
		auth:      sessionpolicy.Authority{SecureSessionIncarnation: clientSession, ALegID: clientALeg, Authorized: true},
		known:     true,
		available: true,
	}
}

func (h *endpointHarness) handler(t *testing.T) http.Handler {
	t.Helper()
	handler, err := policyhttp.NewHandler(policyhttp.Options{
		Store: h.store,
		FeatureStatus: func(_ context.Context, featureID string) (bool, bool, error) {
			return h.known && featureID != "missing-feature", h.available, nil
		},
		ResolveClientScope: func(_ context.Context, r *http.Request, _ string) (sessionpolicy.Key, sessionpolicy.Authority, error) {
			h.clientResolvedPath = r.URL.Path
			if h.clientErr != nil {
				return sessionpolicy.Key{}, sessionpolicy.Authority{}, h.clientErr
			}
			return h.key, h.auth, nil
		},
		AuthorizeOperatorTarget: func(context.Context, *http.Request, string, string) (sessionpolicy.Key, sessionpolicy.Authority, error) {
			if h.operatorErr != nil {
				return sessionpolicy.Key{}, sessionpolicy.Authority{}, h.operatorErr
			}
			key := h.key
			key.SecureSessionIncarnation = operatorSession
			key.ALegID = "operator-target-a-leg"
			return key, sessionpolicy.Authority{
				SecureSessionIncarnation: operatorSession,
				ALegID:                   "operator-target-a-leg",
				Authorized:               true,
			}, nil
		},
		GenerationDefault: func(string) bool { return false },
		MaxBodyBytes:      64,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return handler
}

func doEndpointRequest(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	contentType := ""
	if body != "" {
		contentType = "application/json"
	}
	return doEndpointRequestWithContentType(handler, method, path, body, contentType)
}

func doEndpointRequestWithContentType(handler http.Handler, method, path, body, contentType string) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeObject(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response status=%d body=%q: %v", rec.Code, rec.Body.String(), err)
	}
	return body
}

func assertNoSensitiveEndpointText(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	body := strings.ToLower(rec.Body.String())
	for _, forbidden := range []string{"provider-secret", "alg-secret", "credential-secret", "raw-body", "expected_revision", "agent-loop-guard", "openai", "anthropic"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, rec.Body.String())
		}
	}
}

func TestTerminalDecisionPolicyEndpoints_ClientLifecycleAndShape_RED(t *testing.T) {
	t.Parallel()
	h := newEndpointHarness(t, 2)
	handler := h.handler(t)

	initial := doEndpointRequest(handler, http.MethodGet, "/v1/aip/session/features/"+policyFeature, "")
	if initial.Code != http.StatusOK {
		t.Fatalf("client GET status=%d body=%s", initial.Code, initial.Body.String())
	}
	initialBody := decodeObject(t, initial)
	for _, field := range []string{"feature_id", "available", "client_state", "effective_enabled", "revision"} {
		if _, ok := initialBody[field]; !ok {
			t.Fatalf("client GET missing %q: %s", field, initial.Body.String())
		}
	}
	if _, ok := initialBody["applies_from"]; ok {
		t.Fatalf("client GET must not contain applies_from: %s", initial.Body.String())
	}

	put := doEndpointRequest(handler, http.MethodPut, "/v1/aip/session/features/"+policyFeature, `{"enabled":true}`)
	if put.Code != http.StatusOK {
		t.Fatalf("client PUT status=%d body=%s", put.Code, put.Body.String())
	}
	putBody := decodeObject(t, put)
	if putBody["applies_from"] != "next_request" {
		t.Fatalf("client PUT applies_from=%v, want next_request", putBody["applies_from"])
	}
	if _, ok := putBody["revision"]; !ok {
		t.Fatalf("client PUT missing revision: %s", put.Body.String())
	}

	get := doEndpointRequest(handler, http.MethodGet, "/v1/aip/session/features/"+policyFeature, "")
	if get.Code != http.StatusOK || decodeObject(t, get)["client_state"] != "enabled" {
		t.Fatalf("client GET after PUT status=%d body=%s", get.Code, get.Body.String())
	}

	delete := doEndpointRequest(handler, http.MethodDelete, "/v1/aip/session/features/"+policyFeature, "")
	if delete.Code != http.StatusOK {
		t.Fatalf("client DELETE status=%d body=%s", delete.Code, delete.Body.String())
	}
	deleteBody := decodeObject(t, delete)
	if deleteBody["client_state"] != "unset" || deleteBody["applies_from"] != "next_request" {
		t.Fatalf("client DELETE must mean actor inherit: %s", delete.Body.String())
	}
	if _, ok := deleteBody["operator_state"]; ok {
		t.Fatalf("client response leaked operator_state: %s", delete.Body.String())
	}
}

func TestTerminalDecisionPolicyEndpoints_OperatorLifecycleAndTargetAuth_RED(t *testing.T) {
	t.Parallel()
	h := newEndpointHarness(t, 2)
	handler := h.handler(t)
	path := "/admin/session-features/" + operatorSession + "/" + policyFeature

	put := doEndpointRequest(handler, http.MethodPut, path, `{"enabled":false}`)
	if put.Code != http.StatusOK {
		t.Fatalf("operator PUT status=%d body=%s", put.Code, put.Body.String())
	}
	putBody := decodeObject(t, put)
	for _, field := range []string{"feature_id", "available", "client_state", "operator_state", "effective_enabled", "revision", "applies_from"} {
		if _, ok := putBody[field]; !ok {
			t.Fatalf("operator PUT missing %q: %s", field, put.Body.String())
		}
	}
	if putBody["applies_from"] != "next_request" {
		t.Fatalf("operator PUT applies_from=%v, want next_request", putBody["applies_from"])
	}

	get := doEndpointRequest(handler, http.MethodGet, path, "")
	if get.Code != http.StatusOK {
		t.Fatalf("operator GET status=%d body=%s", get.Code, get.Body.String())
	}
	getBody := decodeObject(t, get)
	if _, ok := getBody["applies_from"]; ok {
		t.Fatalf("operator GET must not contain applies_from: %s", get.Body.String())
	}
	if getBody["operator_state"] != "disabled" {
		t.Fatalf("operator state=%v, want disabled", getBody["operator_state"])
	}

	delete := doEndpointRequest(handler, http.MethodDelete, path, "")
	if delete.Code != http.StatusOK || decodeObject(t, delete)["operator_state"] != "unset" {
		t.Fatalf("operator DELETE must inherit: status=%d body=%s", delete.Code, delete.Body.String())
	}

	h.operatorErr = policyhttp.ErrForbidden
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rec := doEndpointRequest(handler, method, path, `{"enabled":true}`)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"forbidden"`) {
			t.Fatalf("unauthorized operator %s status=%d body=%s", method, rec.Code, rec.Body.String())
		}
	}
}

func TestTerminalDecisionPolicyEndpoints_ErrorMatrixAndNoMutation_RED(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		configure  func(*endpointHarness)
		method     string
		path       string
		body       string
		wantStatus int
		wantCode   string
		wantAllow  string
	}{
		{name: "method", method: http.MethodPatch, path: "/v1/aip/session/features/" + policyFeature, wantStatus: http.StatusMethodNotAllowed, wantCode: "method_not_allowed", wantAllow: "GET, PUT, DELETE"},
		{name: "operator-method", method: http.MethodPatch, path: "/admin/session-features/" + operatorSession + "/" + policyFeature, wantStatus: http.StatusMethodNotAllowed, wantCode: "method_not_allowed", wantAllow: "GET, PUT, DELETE"},
		{name: "media", method: http.MethodPut, path: "/v1/aip/session/features/" + policyFeature, body: `{"enabled":true}`, wantStatus: http.StatusUnsupportedMediaType, wantCode: "unsupported_media_type"},
		{name: "body-too-large", method: http.MethodPut, path: "/v1/aip/session/features/" + policyFeature, body: `{"enabled":true,"padding":"` + strings.Repeat("x", 96) + `"}`, wantStatus: http.StatusRequestEntityTooLarge, wantCode: "body_too_large"},
		{name: "malformed", method: http.MethodPut, path: "/v1/aip/session/features/" + policyFeature, body: `{`, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "wrong-shape", method: http.MethodPut, path: "/v1/aip/session/features/" + policyFeature, body: `{"enabled":"yes"}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "empty-object", method: http.MethodPut, path: "/v1/aip/session/features/" + policyFeature, body: `{}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "unknown-field", method: http.MethodPut, path: "/v1/aip/session/features/" + policyFeature, body: `{"enabled":true,"expected_revision":7}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "credential-body-never-echoed", method: http.MethodPut, path: "/v1/aip/session/features/" + policyFeature, body: `{"enabled":true,"credential":"credential-secret"}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "empty-body", method: http.MethodPut, path: "/v1/aip/session/features/" + policyFeature, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "unknown-feature", method: http.MethodGet, path: "/v1/aip/session/features/missing-feature", wantStatus: http.StatusNotFound, wantCode: "feature_not_found"},
		{name: "unauthenticated-client", configure: func(h *endpointHarness) { h.clientErr = policyhttp.ErrUnauthenticated }, method: http.MethodGet, path: "/v1/aip/session/features/" + policyFeature, wantStatus: http.StatusUnauthorized, wantCode: "unauthorized"},
		{name: "missing-secure-session", configure: func(h *endpointHarness) { h.clientErr = policyhttp.ErrSecureSessionRequired }, method: http.MethodGet, path: "/v1/aip/session/features/" + policyFeature, wantStatus: http.StatusForbidden, wantCode: "secure_session_required"},
		{name: "operator-unauthenticated", configure: func(h *endpointHarness) { h.operatorErr = policyhttp.ErrUnauthenticated }, method: http.MethodGet, path: "/admin/session-features/" + operatorSession + "/" + policyFeature, wantStatus: http.StatusUnauthorized, wantCode: "unauthorized"},
		{name: "operator-invalid-target", configure: func(h *endpointHarness) { h.operatorErr = policyhttp.ErrSessionNotFound }, method: http.MethodGet, path: "/admin/session-features/" + operatorSession + "/" + policyFeature, wantStatus: http.StatusNotFound, wantCode: "session_not_found"},
		{name: "capacity", configure: func(h *endpointHarness) {
			for _, aLegID := range []string{"already-at-capacity-1", "already-at-capacity-2"} {
				other := h.key
				other.ALegID = aLegID
				otherAuth := h.auth
				otherAuth.ALegID = aLegID
				if _, err := h.store.Set(context.Background(), otherAuth, other, sessionpolicy.ActorClient, sessionpolicy.TriStateEnabled); err != nil {
					panic(err)
				}
			}
		}, method: http.MethodPut, path: "/v1/aip/session/features/" + policyFeature, body: `{"enabled":true}`, wantStatus: http.StatusConflict, wantCode: "policy_capacity"},
		{name: "store-closing", configure: func(h *endpointHarness) { _ = h.store.Close() }, method: http.MethodGet, path: "/v1/aip/session/features/" + policyFeature, wantStatus: http.StatusServiceUnavailable, wantCode: "policy_unavailable"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newEndpointHarness(t, 2)
			if tc.configure != nil {
				tc.configure(h)
			}
			before, err := h.store.Snapshot(t.Context(), h.auth, h.key, false)
			if err != nil && !errors.Is(err, sessionpolicy.ErrClosed) {
				t.Fatalf("baseline snapshot: %v", err)
			}
			request := doEndpointRequest
			if tc.name == "media" {
				request = func(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
					return doEndpointRequestWithContentType(handler, method, path, body, "text/plain")
				}
			}
			rec := request(h.handler(t), tc.method, tc.path, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"`+tc.wantCode+`"`) {
				t.Fatalf("body=%s missing error code %q", rec.Body.String(), tc.wantCode)
			}
			if tc.wantAllow != "" && rec.Header().Get("Allow") != tc.wantAllow {
				t.Fatalf("Allow=%q want %q", rec.Header().Get("Allow"), tc.wantAllow)
			}
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("Content-Type=%q, want application/json", rec.Header().Get("Content-Type"))
			}
			assertNoSensitiveEndpointText(t, rec)
			if tc.name == "store-closing" {
				return
			}
			after, err := h.store.Snapshot(t.Context(), h.auth, h.key, false)
			if err != nil {
				t.Fatalf("post-error snapshot: %v", err)
			}
			if after != before {
				t.Fatalf("error mutated policy: before=%+v after=%+v", before, after)
			}
		})
	}

	// Existing diagnostics/shared-secret middleware remains the operator
	// authority boundary; this feature must not introduce a second auth system.
	h := newEndpointHarness(t, 2)
	protected := diag.WrapDiagnosticsProtect("operator-secret", h.handler(t))
	wrongSecret := httptest.NewRequest(http.MethodGet, "/admin/session-features/"+operatorSession+"/"+policyFeature, nil)
	wrongSecret.Header.Set(diag.HeaderDiagnosticsSecret, "wrong-secret")
	wrongRec := httptest.NewRecorder()
	protected.ServeHTTP(wrongRec, wrongSecret)
	if wrongRec.Code != http.StatusForbidden || !strings.Contains(strings.ToLower(wrongRec.Body.String()), "forbidden") {
		t.Fatalf("shared-secret mismatch status=%d body=%s", wrongRec.Code, wrongRec.Body.String())
	}
	assertNoSensitiveEndpointText(t, wrongRec)
}

func TestTerminalDecisionPolicyEndpoints_ClientUsesAuthoritativeSelfScope_RED(t *testing.T) {
	t.Parallel()
	h := newEndpointHarness(t, 2)
	path := "/v1/aip/session/features/" + policyFeature
	rec := doEndpointRequest(h.handler(t), http.MethodGet, path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("client self-scope GET status=%d body=%s", rec.Code, rec.Body.String())
	}
	if h.clientResolvedPath != path || strings.Contains(h.clientResolvedPath, clientSession) {
		t.Fatalf("client resolver saw non-self route path=%q; session identity must come from existing authority", h.clientResolvedPath)
	}
}

func TestTerminalDecisionPolicyEndpoints_AvailableFalseStillMountsAndIsProviderNeutral_RED(t *testing.T) {
	t.Parallel()
	h := newEndpointHarness(t, 2)
	h.available = false
	rec := doEndpointRequest(h.handler(t), http.MethodGet, "/v1/aip/session/features/"+policyFeature, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("known feature without active provider must remain mounted: status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeObject(t, rec)
	if body["available"] != false {
		t.Fatalf("available=%v, want false: %s", body["available"], rec.Body.String())
	}
	assertNoSensitiveEndpointText(t, rec)
}
