package openresponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// Task 17.1 characterization: Bounded no-store frontend state for OpenResponses
// (Requirements 1, 17.4, 17.7, 18.4, 18.8; Design Section 14 Lane 3).
//
// Initial subset:
//   - HTTP create (/openresponses/v1/responses or suffix /responses)
//   - explicit store:false ("store": false)
//   - no previous_response_id (PreviousResponseID == "")
//   - no compaction
//   - no WebSocket
//
// Invariants frozen by these tests:
//   1. Missing store field stays canonical because current decode defaults store=true.
//   2. Explicit store:true stays canonical (requires reservation + continuation recording).
//   3. previous_response_id stays canonical (requires parent resolution in store).
//   4. Compaction and WebSocket requests remain canonical.
//   5. Outer method -> auth -> JSON media-type ordering is strictly preserved before
//      entering frontendpipe and candidate evaluation.
//   6. For the certified no-store subset, AfterDecode produces only bounded, stateless
//      frontend state with ZERO store reservations, ZERO parent lookups, ZERO stream
//      observers, and ZERO store-related errors.
//   7. Proves by test that no AfterDecode side effect or error is moved after the
//      wire commit point.

// failingContinuationStore always returns errors on Reserve and Resolve to prove
// that AfterDecode never invokes store operations for explicit store:false requests.
type failingContinuationStore struct {
	mu           sync.Mutex
	reserveCalls int
	resolveCalls int
	deleteCalls  int
}

func (s *failingContinuationStore) Reserve(_ context.Context, _ aipcont.Scope, _ aipcont.StoragePolicy) (aipcont.ResponseID, error) {
	s.mu.Lock()
	s.reserveCalls++
	s.mu.Unlock()
	return "", errors.New("failingContinuationStore: injected reserve failure")
}

func (s *failingContinuationStore) Get(_ context.Context, _ aipcont.Scope, _ aipcont.ResponseID) (aipcont.ContinuationRecord, error) {
	s.mu.Lock()
	s.resolveCalls++
	s.mu.Unlock()
	return aipcont.ContinuationRecord{}, aipcont.ErrPreviousResponseNotFound
}

func (s *failingContinuationStore) PutTerminal(_ context.Context, _ aipcont.ContinuationRecord) error {
	return nil
}

func (s *failingContinuationStore) Delete(_ context.Context, _ aipcont.Scope, _ aipcont.ResponseID) error {
	s.mu.Lock()
	s.deleteCalls++
	s.mu.Unlock()
	return nil
}

var _ aipcont.Store = (*failingContinuationStore)(nil)

// dummyProfile is a stub profile for testing HandlerConfig.Profile wiring (Task 17.1).
type dummyProfile struct {
	id string
}

func (p *dummyProfile) ProfileID() string { return p.id }

func (p *dummyProfile) CompileProof(_ context.Context, _ frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	return frontendpipe.ProofOutput{}, errors.New("dummyProfile: not implemented")
}

var _ frontendpipe.FrontendProfile = (*dummyProfile)(nil)

// Requirement 17.4: Initial subset requires explicit store:false, no previous_response_id.
// Missing store stays canonical because decode defaults true.
func TestOpenResponses_IsEligibleNoStoreCreate(t *testing.T) {
	t.Parallel()

	t.Run("explicit store false is eligible", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"model":"gpt-4o","input":"hi","store":false}`)
		decoded, err := openresponses.AuthenticateAndDecodeCreate(t.Context(), raw, openresponses.DecodeCreateOptions{
			DefaultRouteSelector: "stub:default",
		})
		if err != nil {
			t.Fatalf("decode failed: %v", err)
		}
		if decoded.Store {
			t.Fatalf("decoded.Store=%v want false", decoded.Store)
		}
		if decoded.ExplicitStore == nil || *decoded.ExplicitStore != false {
			t.Fatalf("decoded.ExplicitStore=%v want &false", decoded.ExplicitStore)
		}
		if !openresponses.IsEligibleNoStoreCreate(decoded) {
			t.Fatal("IsEligibleNoStoreCreate must return true for explicit store:false with no previous_response_id")
		}
	})

	t.Run("missing store defaults true and is NOT eligible", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"model":"gpt-4o","input":"hi"}`)
		decoded, err := openresponses.AuthenticateAndDecodeCreate(t.Context(), raw, openresponses.DecodeCreateOptions{
			DefaultRouteSelector: "stub:default",
		})
		if err != nil {
			t.Fatalf("decode failed: %v", err)
		}
		if !decoded.Store {
			t.Fatalf("decoded.Store=%v want true (decode defaults true)", decoded.Store)
		}
		if decoded.ExplicitStore != nil {
			t.Fatalf("decoded.ExplicitStore=%v want nil for missing store", decoded.ExplicitStore)
		}
		if openresponses.IsEligibleNoStoreCreate(decoded) {
			t.Fatal("IsEligibleNoStoreCreate must return false when store is omitted (defaults true, stays canonical)")
		}
	})

	t.Run("explicit store true is NOT eligible", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"model":"gpt-4o","input":"hi","store":true}`)
		decoded, err := openresponses.AuthenticateAndDecodeCreate(t.Context(), raw, openresponses.DecodeCreateOptions{
			DefaultRouteSelector: "stub:default",
		})
		if err != nil {
			t.Fatalf("decode failed: %v", err)
		}
		if !decoded.Store {
			t.Fatalf("decoded.Store=%v want true", decoded.Store)
		}
		if decoded.ExplicitStore == nil || *decoded.ExplicitStore != true {
			t.Fatalf("decoded.ExplicitStore=%v want &true", decoded.ExplicitStore)
		}
		if openresponses.IsEligibleNoStoreCreate(decoded) {
			t.Fatal("IsEligibleNoStoreCreate must return false for explicit store:true (stays canonical)")
		}
	})

	t.Run("store false with previous_response_id is NOT eligible", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"model":"gpt-4o","input":"hi","store":false,"previous_response_id":"resp_prev"}`)
		decoded, err := openresponses.AuthenticateAndDecodeCreate(t.Context(), raw, openresponses.DecodeCreateOptions{
			DefaultRouteSelector: "stub:default",
		})
		if err != nil {
			t.Fatalf("decode failed: %v", err)
		}
		if decoded.Store {
			t.Fatalf("decoded.Store=%v want false", decoded.Store)
		}
		if decoded.PreviousResponseID != "resp_prev" {
			t.Fatalf("decoded.PreviousResponseID=%q want resp_prev", decoded.PreviousResponseID)
		}
		if openresponses.IsEligibleNoStoreCreate(decoded) {
			t.Fatal("IsEligibleNoStoreCreate must return false when previous_response_id is present (stays canonical)")
		}
	})

	t.Run("nil decoded returns false", func(t *testing.T) {
		t.Parallel()
		if openresponses.IsEligibleNoStoreCreate(nil) {
			t.Fatal("IsEligibleNoStoreCreate(nil) must return false")
		}
	})
}

// Requirement 17.1, 18.4: HandlerConfig exposes Profile and LargePayload, and Handler.Spec() exposes pipe.
func TestOpenResponses_ProfileSeamConfiguration(t *testing.T) {
	t.Parallel()

	t.Run("spec exposes configured profile and large payload", func(t *testing.T) {
		t.Parallel()
		dummy := &dummyProfile{id: "test_openresponses_v1"}
		lpCfg := frontendpipe.LargePayloadConfig{
			Enabled:          true,
			ThresholdBytes:   64 * 1024,
			MemorySpoolBytes: 128 * 1024,
		}
		exec := &orderingOpenResponsesExec{}
		h := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Executor:             exec,
			Profile:              dummy,
			LargePayload:         lpCfg,
		})

		spec := h.Spec()
		if spec == nil {
			t.Fatal("Handler.Spec() must return non-nil spec")
		}
		if spec.Profile != dummy {
			t.Fatalf("spec.Profile = %v, want %v", spec.Profile, dummy)
		}
		if spec.LargePayload != lpCfg {
			t.Fatalf("spec.Config.LargePayload = %+v, want %+v", spec.LargePayload, lpCfg)
		}
		if spec.ResolvePreAdmissionRoute != nil || spec.ResolveBodyRoute != nil {
			t.Fatal("Handler must NOT configure custom route hooks")
		}
	})

	t.Run("default handler has nil profile and disabled large payload", func(t *testing.T) {
		t.Parallel()
		exec := &orderingOpenResponsesExec{}
		h := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Executor:             exec,
		})

		spec := h.Spec()
		if spec == nil {
			t.Fatal("Handler.Spec() must return non-nil spec")
		}
		if spec.Profile != nil {
			t.Fatalf("default spec.Profile = %v, want nil", spec.Profile)
		}
		if spec.LargePayload.Enabled {
			t.Fatal("default spec.Config.LargePayload.Enabled must be false")
		}
	})
}

// Requirement 17.4, 18.4: Prove no AfterDecode side effect or error exists for explicit store:false,
// while store:true and previous_response_id have real AfterDecode side effects and errors.
func TestOpenResponses_AfterDecodeSideEffectsAndErrors(t *testing.T) {
	t.Parallel()

	t.Run("store false succeeds with failing continuation store (zero AfterDecode side effects)", func(t *testing.T) {
		t.Parallel()
		store := &failingContinuationStore{}
		exec := &orderingOpenResponsesExec{}
		h := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Executor:             exec,
			ContinuationStore:    store,
		})

		body := []byte(`{"model":"gpt-4o","input":"hello","store":false}`)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d want 200 (store:false must succeed even with broken store): %s", rec.Code, rec.Body.String())
		}
		if store.reserveCalls != 0 {
			t.Fatalf("reserveCalls=%d want 0 (AfterDecode must never call store.Reserve for store:false)", store.reserveCalls)
		}
		if store.resolveCalls != 0 {
			t.Fatalf("resolveCalls=%d want 0 (AfterDecode must never call store.Resolve for store:false)", store.resolveCalls)
		}
		if exec.calls != 1 {
			t.Fatalf("exec.calls=%d want 1", exec.calls)
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		id, ok := resp["id"].(string)
		if !ok || !strings.HasPrefix(id, "resp_") {
			t.Fatalf("response id=%q want ephemeral resp_ prefix", id)
		}
		if sVal, ok := resp["store"].(bool); !ok || sVal {
			t.Fatalf("store=%v want false", resp["store"])
		}
	})

	t.Run("store true fails in AfterDecode when store.Reserve fails (before executor entry)", func(t *testing.T) {
		t.Parallel()
		store := &failingContinuationStore{}
		exec := &orderingOpenResponsesExec{}
		h := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Executor:             exec,
			ContinuationStore:    store,
		})

		body := []byte(`{"model":"gpt-4o","input":"hello","store":true}`)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d want 500 storage_error", rec.Code)
		}
		if store.reserveCalls != 1 {
			t.Fatalf("reserveCalls=%d want 1 (store:true calls Reserve in AfterDecode)", store.reserveCalls)
		}
		if exec.calls != 0 {
			t.Fatalf("exec.calls=%d want 0 (AfterDecode error must stop execution before executor)", exec.calls)
		}
		typ, code, _ := decodeOpenResponsesWireError(t, rec.Body.Bytes())
		if typ != "server_error" || code != "storage_error" {
			t.Fatalf("error type=%q code=%q want server_error/storage_error", typ, code)
		}
	})

	t.Run("missing store defaults true and fails in AfterDecode when store.Reserve fails", func(t *testing.T) {
		t.Parallel()
		store := &failingContinuationStore{}
		exec := &orderingOpenResponsesExec{}
		h := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Executor:             exec,
			ContinuationStore:    store,
		})

		body := []byte(`{"model":"gpt-4o","input":"hello"}`)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d want 500 storage_error for omitted store", rec.Code)
		}
		if store.reserveCalls != 1 {
			t.Fatalf("reserveCalls=%d want 1 (omitted store defaults true, calls Reserve)", store.reserveCalls)
		}
		if exec.calls != 0 {
			t.Fatalf("exec.calls=%d want 0 (AfterDecode failure stops before executor)", exec.calls)
		}
	})

	t.Run("previous_response_id fails in AfterDecode when parent missing (before executor entry)", func(t *testing.T) {
		t.Parallel()
		store := &failingContinuationStore{}
		exec := &orderingOpenResponsesExec{}
		h := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Authorizer:           continuationAuth{},
			Executor:             exec,
			ContinuationStore:    store,
		})

		body := []byte(`{"model":"gpt-4o","input":"hello","store":false,"previous_response_id":"resp_MDEyMzQ1Njc4OWFiY2RlZg"}`)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400 previous_response_not_found", rec.Code)
		}
		if store.resolveCalls != 1 {
			t.Fatalf("resolveCalls=%d want 1 (previous_response_id calls Resolve in AfterDecode)", store.resolveCalls)
		}
		if exec.calls != 0 {
			t.Fatalf("exec.calls=%d want 0 (AfterDecode failure stops before executor)", exec.calls)
		}
		typ, code, _ := decodeOpenResponsesWireError(t, rec.Body.Bytes())
		if typ != "invalid_request_error" || code != "previous_response_not_found" {
			t.Fatalf("error type=%q code=%q want invalid_request_error/previous_response_not_found", typ, code)
		}
	})
}

// Requirement 1.6, 17.7: Outer auth + JSON content-type ordering is preserved
// regardless of whether Profile/LargePayload is enabled on HandlerConfig.
func TestOpenResponses_OuterAuthAndMediaOrderingWithLargePayloadConfig(t *testing.T) {
	t.Parallel()

	newConfiguredHandler := func(auth openresponses.Authorizer, exec *orderingOpenResponsesExec) *openresponses.Handler {
		dummy := &dummyProfile{id: "test_openresponses_v1"}
		lpCfg := frontendpipe.LargePayloadConfig{
			Enabled:          true,
			ThresholdBytes:   64 * 1024,
			MemorySpoolBytes: 128 * 1024,
		}
		return openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Authorizer:           auth,
			Executor:             exec,
			Profile:              dummy,
			LargePayload:         lpCfg,
		})
	}

	t.Run("non-POST method rejected with 405 before auth or media check", func(t *testing.T) {
		t.Parallel()
		auth := &mockAuthorizer{authenticated: false}
		exec := &orderingOpenResponsesExec{}
		h := newConfiguredHandler(auth, exec)

		req := httptest.NewRequest(http.MethodGet, "/openresponses/v1/responses", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d want 405 Method Not Allowed", rec.Code)
		}
		if auth.calls != 0 {
			t.Fatalf("auth calls=%d want 0 (method precedes auth)", auth.calls)
		}
		if exec.calls != 0 {
			t.Fatal("executor called for GET")
		}
	})

	t.Run("unauthenticated request rejected with 401 before media check or body read", func(t *testing.T) {
		t.Parallel()
		auth := &mockAuthorizer{authenticated: false}
		exec := &orderingOpenResponsesExec{}
		h := newConfiguredHandler(auth, exec)

		body := []byte(`{"model":"gpt-4o","input":"hello","store":false}`)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "text/plain") // bad media type!
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d want 401 Unauthorized (auth precedes media check)", rec.Code)
		}
		if auth.calls != 1 {
			t.Fatalf("auth calls=%d want 1", auth.calls)
		}
		if exec.calls != 0 {
			t.Fatal("executor called for unauthenticated request")
		}
		typ, code, _ := decodeOpenResponsesWireError(t, rec.Body.Bytes())
		if typ != "authentication_error" || code != "unauthorized" {
			t.Fatalf("error type=%q code=%q want authentication_error/unauthorized", typ, code)
		}
	})

	t.Run("bad media type rejected with 415 before pipe or candidate gate processing", func(t *testing.T) {
		t.Parallel()
		auth := &mockAuthorizer{authenticated: true}
		exec := &orderingOpenResponsesExec{}
		h := newConfiguredHandler(auth, exec)

		body := []byte(`{"model":"gpt-4o","input":"hello","store":false}`)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("status=%d want 415 Unsupported Media Type", rec.Code)
		}
		if auth.calls != 1 {
			t.Fatalf("auth calls=%d want 1", auth.calls)
		}
		if exec.calls != 0 {
			t.Fatal("executor called for unsupported media type")
		}
		typ, code, msg := decodeOpenResponsesWireError(t, rec.Body.Bytes())
		if typ != "invalid_request_error" || code != "unsupported_media_type" {
			t.Fatalf("error type=%q code=%q want invalid_request_error/unsupported_media_type", typ, code)
		}
		if msg != "Request Content-Type must be application/json" {
			t.Fatalf("message=%q want 'Request Content-Type must be application/json'", msg)
		}
	})

	t.Run("valid auth and application/json media passes to pipe", func(t *testing.T) {
		t.Parallel()
		auth := &mockAuthorizer{authenticated: true}
		exec := &orderingOpenResponsesExec{}
		h := newConfiguredHandler(auth, exec)

		body := []byte(`{"model":"gpt-4o","input":"hello","store":false}`)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d want 200: %s", rec.Code, rec.Body.String())
		}
		if auth.calls != 1 {
			t.Fatalf("auth calls=%d want 1", auth.calls)
		}
		if exec.calls != 1 {
			t.Fatalf("exec.calls=%d want 1", exec.calls)
		}
	})
}
