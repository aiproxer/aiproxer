package openresponses_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

type mockAuthorizer struct {
	authenticated bool
	err           error
	calls         int
}

func (m *mockAuthorizer) Authenticate(ctx context.Context, meta sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	m.calls++
	if m.err != nil {
		return sdkauth.Decision{}, m.err
	}
	if !m.authenticated {
		return sdkauth.Decision{
			Outcome:    sdkauth.OutcomeDeny,
			ReasonCode: "unauthorized",
		}, nil
	}
	return sdkauth.Decision{
		Outcome:   sdkauth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: "test-user"},
	}, nil
}

type mockExecutor struct {
	executeCalls int
	lastCall     *aipapi.Call
	err          error
}

func (m *mockExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	m.executeCalls++
	m.lastCall = call
	return nil, m.err
}

type mockContinuationStore struct {
	getCalls  int
	lastScope aipcont.Scope
}

func (m *mockContinuationStore) Reserve(ctx context.Context, scope aipcont.Scope, policy aipcont.StoragePolicy) (aipcont.ResponseID, error) {
	return aipcont.ResponseID(""), nil
}

func (m *mockContinuationStore) PutTerminal(ctx context.Context, record aipcont.ContinuationRecord) error {
	return nil
}

func (m *mockContinuationStore) Get(ctx context.Context, scope aipcont.Scope, id aipcont.ResponseID) (aipcont.ContinuationRecord, error) {
	m.getCalls++
	m.lastScope = scope
	return aipcont.ContinuationRecord{}, errors.New("not found")
}

func (m *mockContinuationStore) Delete(ctx context.Context, scope aipcont.Scope, id aipcont.ResponseID) error {
	return nil
}

type countingReadCloser struct {
	reader io.Reader
	reads  int
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	r.reads++
	return r.reader.Read(p)
}

func (r *countingReadCloser) Close() error { return nil }

func TestDecode_AuthBeforeBodyOrStore(t *testing.T) {
	t.Parallel()

	auth := &mockAuthorizer{authenticated: false}
	exec := &mockExecutor{}
	store := &mockContinuationStore{}

	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           auth,
		Executor:             exec,
		ContinuationStore:    store,
	})

	body := []byte(`{"model":"gpt-4o","input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
	req.Body = &countingReadCloser{reader: bytes.NewReader(body)}
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}

	if auth.calls != 1 {
		t.Errorf("expected 1 auth call, got %d", auth.calls)
	}
	if exec.executeCalls != 0 {
		t.Errorf("expected 0 executor calls when auth fails, got %d", exec.executeCalls)
	}
	if store.getCalls != 0 {
		t.Errorf("expected 0 continuation store calls when auth fails, got %d", store.getCalls)
	}
	countedBody, ok := req.Body.(*countingReadCloser)
	if !ok {
		t.Fatalf("request body type = %T", req.Body)
	}
	if countedBody.reads != 0 {
		t.Errorf("expected 0 body reads when auth fails, got %d", countedBody.reads)
	}
}

func TestDecode_InvalidUnauthorizedCausesNoWork(t *testing.T) {
	t.Parallel()

	auth := &mockAuthorizer{authenticated: false}
	exec := &mockExecutor{}
	store := &mockContinuationStore{}

	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           auth,
		Executor:             exec,
		ContinuationStore:    store,
	})

	// Malformed JSON payload + unauthenticated
	body := []byte(`{invalid json payload`)
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401 (auth failure before body parse), got %d", rec.Code)
	}
	if exec.executeCalls != 0 {
		t.Fatalf("executor should receive 0 work, got %d", exec.executeCalls)
	}
	if store.getCalls != 0 {
		t.Fatalf("store should receive 0 work, got %d", store.getCalls)
	}
}

func TestDecode_CreateRequest_StringInput(t *testing.T) {
	t.Parallel()

	auth := &mockAuthorizer{authenticated: true}
	exec := &mockExecutor{}

	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           auth,
		Executor:             exec,
	})

	body := []byte(`{
		"model": "gpt-4o",
		"input": "Hello OpenResponses",
		"temperature": 0.7
	}`)
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if exec.executeCalls != 1 {
		t.Fatalf("expected 1 executor call, got %d (status=%d body=%s)", exec.executeCalls, rec.Code, rec.Body.String())
	}

	call := exec.lastCall
	if call == nil {
		t.Fatal("call is nil")
	}

	if len(call.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(call.Items))
	}

	item := call.Items[0]
	if item.Kind != aipapi.ItemKindMessage {
		t.Errorf("got item kind %v, want message", item.Kind)
	}
	if item.Role != aipapi.RoleUser {
		t.Errorf("got role %v, want user", item.Role)
	}
	if len(item.Content) != 1 || item.Content[0].Text != "Hello OpenResponses" {
		t.Errorf("unexpected content: %+v", item.Content)
	}

	if call.Invocation.Operation != aipapi.OperationOpenResponsesCreate {
		t.Errorf("got operation %q, want %q", call.Invocation.Operation, aipapi.OperationOpenResponsesCreate)
	}
}

func TestDecode_CreateRequest_ToolsControlsExtensions(t *testing.T) {
	t.Parallel()

	auth := &mockAuthorizer{authenticated: true}
	exec := &mockExecutor{}

	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           auth,
		Executor:             exec,
	})

	body := []byte(`{
		"model": "gpt-4o",
		"input": "Test tools",
		"tools": [
			{
				"type": "function",
				"name": "get_weather",
				"description": "Get current weather",
				"parameters": {"type": "object"}
			}
		],
		"tool_choice": "auto",
		"parallel_tool_calls": true,
		"top_p": 0.9,
		"vendor:custom_ext": {"key": "val"}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if exec.executeCalls != 1 {
		t.Fatalf("expected 1 executor call, got %d", exec.executeCalls)
	}

	call := exec.lastCall
	if len(call.Tools) != 1 || call.Tools[0].Name != "get_weather" {
		t.Errorf("unexpected tools: %+v", call.Tools)
	}
	if call.ToolChoice.Mode != aipapi.ToolChoiceAuto {
		t.Errorf("unexpected tool choice mode: %v", call.ToolChoice.Mode)
	}
	if call.Options.ParallelToolCalls == nil || !*call.Options.ParallelToolCalls {
		t.Errorf("expected parallel_tool_calls=true")
	}
	if len(call.Extensions) == 0 {
		t.Errorf("expected extension vendor:custom_ext")
	}
}

func TestDecode_CreateRequest_ReturnsCanonicalRequirements(t *testing.T) {
	t.Parallel()

	decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), []byte(`{
		"model": "gpt-4o",
		"input": [{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],
		"vendor:item": {"value": true}
	}`), openresponses.DecodeCreateOptions{
		Auth: &mockAuthorizer{authenticated: true},
	})
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if decoded.Call == nil || !decoded.Call.HasItemAuthority() {
		t.Fatal("expected ordered item authority")
	}
	want := aipapi.DeriveProtocolRequirements(*decoded.Call)
	if got := aipapi.NormalizeProtocolRequirements(decoded.Requirements); !reflect.DeepEqual(got, want) {
		t.Fatalf("requirements do not match canonical call: got=%+v want=%+v", got, want)
	}
}

func TestDecode_ConditionalModelInput(t *testing.T) {
	t.Parallel()

	auth := &mockAuthorizer{authenticated: true}
	exec := &mockExecutor{}

	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           auth,
		Executor:             exec,
	})

	// Missing input without previous_response_id must fail decode with 400
	bodyNoInput := []byte(`{"model": "gpt-4o"}`)
	req1 := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(bodyNoInput))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()

	handler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for missing input, got %d", rec1.Code)
	}
	if exec.executeCalls != 0 {
		t.Errorf("executor should receive 0 work when input missing without previous_response_id, got %d", exec.executeCalls)
	}
}

func TestDecode_MetadataCannotBecomeSessionAuthority(t *testing.T) {
	t.Parallel()

	decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), []byte(`{
		"model": "gpt-4o",
		"input": "hello",
		"metadata": {"aip_session_id": "client-controlled"}
	}`), openresponses.DecodeCreateOptions{
		Auth: &mockAuthorizer{authenticated: true},
	})
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if decoded.Call.Session.AuthoritativeSessionID != "" {
		t.Fatalf("client metadata became authoritative session identity: %q", decoded.Call.Session.AuthoritativeSessionID)
	}
}

func TestDecode_UsesConfiguredDefaultRouteForPlainModel(t *testing.T) {
	t.Parallel()

	decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), []byte(`{
		"model": "plain-model",
		"input": "hello"
	}`), openresponses.DecodeCreateOptions{
		Auth:                 &mockAuthorizer{authenticated: true},
		DefaultRouteSelector: "backend:default",
		RoutePrefixes:        []string{"backend"},
	})
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if got := decoded.Call.Route.Selector; got != "backend:default" {
		t.Fatalf("route selector=%q, want configured default", got)
	}
}

func TestDecode_StreamSetsCanonicalTransport(t *testing.T) {
	t.Parallel()

	decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), []byte(`{
		"model": "backend:model",
		"input": "hello",
		"stream": true
	}`), openresponses.DecodeCreateOptions{
		Auth:          &mockAuthorizer{authenticated: true},
		RoutePrefixes: []string{"backend"},
	})
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if !decoded.Stream {
		t.Fatal("stream flag was not decoded")
	}
	if decoded.Call.Invocation.DeliveryMode != aipapi.DeliveryModeStreaming {
		t.Fatalf("delivery mode=%q, want streaming", decoded.Call.Invocation.DeliveryMode)
	}
	if decoded.Call.Invocation.TransportMode != aipapi.TransportModeStreaming {
		t.Fatalf("transport mode=%q, want streaming", decoded.Call.Invocation.TransportMode)
	}
}

func TestDecode_MaxBodyBytesIsEnforced(t *testing.T) {
	t.Parallel()

	_, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), []byte(`{
		"model": "backend:model",
		"input": "hello"
	}`), openresponses.DecodeCreateOptions{
		Auth:         &mockAuthorizer{authenticated: true},
		MaxBodyBytes: 8,
	})
	if err == nil {
		t.Fatal("expected body limit error")
	}
}

func TestDecode_DuplicateStreamFieldIsRejected(t *testing.T) {
	t.Parallel()

	_, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), []byte(`{"model":"gpt-4o","input":"hello","stream":true,"stream":false}`), openresponses.DecodeCreateOptions{
		Auth: &mockAuthorizer{authenticated: true},
	})
	if err == nil {
		t.Fatal("expected duplicate stream field to be rejected")
	}
}

func TestHandler_ExecutorErrorIsSanitized(t *testing.T) {
	t.Parallel()

	exec := &mockExecutor{err: errors.New("provider secret: token=abc")}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true, Executor: exec,
	})
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(`{"model":"gpt-4o","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("provider secret")) || bytes.Contains(rec.Body.Bytes(), []byte("token=abc")) {
		t.Fatalf("executor error leaked in response: %s", rec.Body.String())
	}
}

func TestCompact_AuthBeforeBodyOrWork(t *testing.T) {
	t.Parallel()

	auth := &mockAuthorizer{authenticated: false}
	exec := &mockExecutor{}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           auth,
		Executor:             exec,
	})

	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBufferString(`{"model":"gpt-4o","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if auth.calls != 1 {
		t.Fatalf("auth calls=%d, want 1", auth.calls)
	}
	if exec.executeCalls != 0 {
		t.Fatalf("unauthorized compact request must cause zero executor calls: calls=%d", exec.executeCalls)
	}
}

func TestCompact_MissingModel_CausesNoWork(t *testing.T) {
	t.Parallel()

	exec := &mockExecutor{}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true, Executor: exec,
	})

	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBufferString(`{"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusBadRequest)
	}
	if exec.executeCalls != 0 {
		t.Fatalf("missing model must cause zero executor calls: calls=%d", exec.executeCalls)
	}
}

func TestCompact_MissingInput_CausesNoWork(t *testing.T) {
	t.Parallel()

	exec := &mockExecutor{}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true, Executor: exec,
	})

	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBufferString(`{"model":"gpt-4o"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusBadRequest)
	}
	if exec.executeCalls != 0 {
		t.Fatalf("missing input must cause zero executor calls: calls=%d", exec.executeCalls)
	}
}

func TestCompact_ForbiddenFields_CausesNoWork(t *testing.T) {
	t.Parallel()

	forbiddenPayloads := []string{
		`{"model":"gpt-4o","input":"hello","stream":true}`,
		`{"model":"gpt-4o","input":"hello","store":true}`,
		`{"model":"gpt-4o","input":"hello","previous_response_id":"resp_123"}`,
		`{"model":"gpt-4o","input":"hello","background":true}`,
		`{"model":"gpt-4o","input":"hello","unknown_field":123}`,
	}

	for _, payload := range forbiddenPayloads {
		exec := &mockExecutor{}
		handler := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true, Executor: exec,
		})

		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("payload %s got status=%d, want %d", payload, rec.Code, http.StatusBadRequest)
		}
		if exec.executeCalls != 0 {
			t.Fatalf("forbidden field payload %s must cause zero executor calls: calls=%d", payload, exec.executeCalls)
		}
	}
}

func TestCompact_DecodeAndDispatchContract(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","input":"Compress this text"}`)
	opts := openresponses.DecodeCompactOptions{
		DefaultRouteSelector: "gpt-4o",
		Headers:              http.Header{"User-Agent": []string{"TestClient/1.0"}},
	}

	decoded, err := openresponses.AuthenticateAndDecodeCompact(context.Background(), body, opts)
	if err != nil {
		t.Fatalf("AuthenticateAndDecodeCompact failed: %v", err)
	}

	if decoded.Call.Invocation.Operation != aipapi.OperationContextCompaction {
		t.Errorf("operation=%s, want %s", decoded.Call.Invocation.Operation, aipapi.OperationContextCompaction)
	}
	if decoded.Call.Invocation.TransportMode != aipapi.TransportModeNonStreaming {
		t.Errorf("transportMode=%s, want %s", decoded.Call.Invocation.TransportMode, aipapi.TransportModeNonStreaming)
	}
	if decoded.Call.Invocation.ClientUserAgent != "TestClient/1.0" {
		t.Errorf("userAgent=%s, want TestClient/1.0", decoded.Call.Invocation.ClientUserAgent)
	}
	if decoded.Call.Route.Selector != "gpt-4o" {
		t.Errorf("route selector=%s, want gpt-4o", decoded.Call.Route.Selector)
	}
	if !decoded.Call.HasItemAuthority() {
		t.Errorf("call must be item-authoritative")
	}

	hasCompaction := slices.Contains(decoded.Requirements.Capabilities, aipapi.CapabilityCompaction)
	if !hasCompaction {
		t.Errorf("decoded requirements must contain CapabilityCompaction: %v", decoded.Requirements.Capabilities)
	}

	// Now verify Handler dispatch
	compactExec := &mockExecutor{}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true, Executor: compactExec,
	})

	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "TestClient/1.0")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if compactExec.executeCalls != 1 {
		t.Fatalf("compact route must reach executor exactly once: calls=%d", compactExec.executeCalls)
	}
	if compactExec.lastCall.Invocation.Operation != aipapi.OperationContextCompaction {
		t.Errorf("handler dispatched operation=%s, want %s", compactExec.lastCall.Invocation.Operation, aipapi.OperationContextCompaction)
	}
}

func TestCompact_UsesNormalExecutorStreamPort(t *testing.T) {
	t.Parallel()

	exec := &mockExecutor{}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Executor:             exec,
	})

	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBufferString(`{"model":"gpt-4o","input":"Compress this"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if exec.executeCalls != 1 {
		t.Fatalf("executor calls=%d, want 1", exec.executeCalls)
	}
	if exec.lastCall == nil || exec.lastCall.Invocation.Operation != aipapi.OperationContextCompaction {
		t.Fatalf("executor did not receive compaction call: %+v", exec.lastCall)
	}
}

func TestCompactDecode_OperationIsCompleteBeforeExecutor(t *testing.T) {
	t.Parallel()

	decoded, err := openresponses.DecodeCompactRequest(context.Background(), []byte(`{
		"model":"gpt-4o",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second"}]}
		]
	}`), openresponses.DecodeCompactOptions{
		Auth: &mockAuthorizer{authenticated: true},
	})
	if err != nil {
		t.Fatalf("compact decode failed: %v", err)
	}
	if decoded.Call == nil || !decoded.Call.HasItemAuthority() {
		t.Fatal("compact call must retain ordered item authority")
	}
	if got := len(decoded.Call.Items); got != 2 {
		t.Fatalf("item count=%d, want 2", got)
	}
	if got := decoded.Call.Invocation.Operation; got != aipapi.OperationContextCompaction {
		t.Fatalf("operation=%q, want %q", got, aipapi.OperationContextCompaction)
	}
	if got := decoded.Call.Invocation.TransportMode; got != aipapi.TransportModeNonStreaming {
		t.Fatalf("transport=%q, want %q", got, aipapi.TransportModeNonStreaming)
	}
	if !containsCapability(decoded.Requirements.Capabilities, aipapi.CapabilityCompaction) {
		t.Fatalf("requirements=%v do not include compaction", decoded.Requirements.Capabilities)
	}
	wantRequirements := aipapi.DeriveProtocolRequirements(*decoded.Call)
	wantRequirements.Capabilities = append(wantRequirements.Capabilities, aipapi.CapabilityCompaction)
	wantRequirements = aipapi.NormalizeProtocolRequirements(wantRequirements)
	if got := aipapi.NormalizeProtocolRequirements(decoded.Requirements); !reflect.DeepEqual(got, wantRequirements) {
		t.Fatalf("requirements do not match canonical call: got=%+v want=%+v", got, wantRequirements)
	}
}

func TestCompactDecode_StrictJSONRejectsDuplicateAndTrailingData(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		`{"model":"gpt-4o","input":"x","model":"other"}`,
		`{"model":"gpt-4o","input":"x"} trailing`,
	} {
		_, err := openresponses.DecodeCompactRequest(context.Background(), []byte(body), openresponses.DecodeCompactOptions{
			Auth: &mockAuthorizer{authenticated: true},
		})
		if err == nil {
			t.Fatalf("body %q unexpectedly decoded", body)
		}
	}
}

func TestCompactDecode_ForbiddenCreateOnlyFieldsAreRejected(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"stream", "store", "previous_response_id", "background"} {
		body := []byte(`{"model":"gpt-4o","input":"x","` + field + `":true}`)
		_, err := openresponses.DecodeCompactRequest(context.Background(), body, openresponses.DecodeCompactOptions{
			Auth: &mockAuthorizer{authenticated: true},
		})
		if err == nil {
			t.Fatalf("forbidden field %q unexpectedly decoded", field)
		}
	}
}

func TestDecode_ExplicitRouteSelectorMustMatchConfiguredPrefix(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"backend:model","input":"hello"}`)
	_, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, openresponses.DecodeCreateOptions{
		Auth:                 &mockAuthorizer{authenticated: true},
		RouteSelector:        "forbidden:model",
		RoutePrefixes:        []string{"backend"},
		DefaultRouteSelector: "backend:default",
	})
	if err == nil {
		t.Fatal("forbidden explicit route selector unexpectedly decoded")
	}
}

func TestCompactDecode_ExplicitRouteSelectorMustMatchConfiguredPrefix(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"backend:model","input":"hello"}`)
	_, err := openresponses.AuthenticateAndDecodeCompact(context.Background(), body, openresponses.DecodeCompactOptions{
		Auth:                 &mockAuthorizer{authenticated: true},
		RouteSelector:        "forbidden:model",
		RoutePrefixes:        []string{"backend"},
		DefaultRouteSelector: "backend:default",
	})
	if err == nil {
		t.Fatal("forbidden explicit compact route selector unexpectedly decoded")
	}
}

func containsCapability(capabilities []aipapi.Capability, want aipapi.Capability) bool {
	return slices.Contains(capabilities, want)
}
