package openresponses_test

// Task 1.6 characterization: freeze OpenResponses response-state + keepalive
// behavior for the future large-payload lane (requirements 17, 18; design
// sections 1, 13 read-only, 16).
//
// What this proves with real existing seams only:
//   - AfterDecode/prepareCreateState response-ID sourcing: explicit store:false
//     uses the ephemeral ResponseIDSource ("resp_" prefix) with zero
//     reservation and zero recorder terminal writes; store:true echoes the
//     reserved continuation ID in the response resource and completes exactly
//     one recorder PutTerminal.
//   - previous_response_id without a store/resolver fails in AfterDecode with
//     previous_response_not_found before executor entry (no continuation read,
//     no reservation).
//   - Nil-store graceful degradation: store:true without a store still answers
//     200 with an ephemeral ID instead of failing the create.
//   - Outer JSON media-type check stays before frontendpipe (415 precedence pin
//     so the future bridge cannot reorder outer auth/media handling).
//
// Reused, not duplicated here:
//   - store true/false/default reservation matrix + reserve-failure mapping +
//     pre-output-failure single cleanup (continuation_policy_test.go).
//   - Recorder completion for stream/non-stream (TestContinuation_Recorder).
//   - Continuation materialization/parent echo (TestHTTPContinuationStorePolicyAndMaterialization).
//   - Compaction Extra routing end to end (compact_test.go).
//   - Holdalive/stream-keepalive pipe mechanics (frontendpipe/keepalive_freeze_test.go);
//     this handler only forwards PreRequestKeepalive/StreamKeepaliveInterval
//     into its pipe spec (handler_pipe.go).
//
// What this explicitly does NOT claim (out of scope, needs future tasks):
//   - Storage/continuation certification (previous_response_id, store:true
//     lineage, compaction) stays canonical-only per tasks.md 17.4; the future
//     wire lane starts at explicit store:false with no previous_response_id.
//     No ExecuteLargeBody/ResponseFacts bridge is constructed here.
//
// Test-only, no production diff.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/continuation"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

type freezeReserveCaptureStore struct {
	aipcont.Store
	mu       sync.Mutex
	reserved []aipcont.ResponseID
}

func (s *freezeReserveCaptureStore) Reserve(ctx context.Context, scope aipcont.Scope, policy aipcont.StoragePolicy) (aipcont.ResponseID, error) {
	id, err := s.Store.Reserve(ctx, scope, policy)
	if err == nil {
		s.mu.Lock()
		s.reserved = append(s.reserved, id)
		s.mu.Unlock()
	}
	return id, err
}

func (s *freezeReserveCaptureStore) lastReserved() aipcont.ResponseID {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reserved) == 0 {
		return ""
	}
	return s.reserved[len(s.reserved)-1]
}

func freezeResponseResource(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var res map[string]any
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("invalid json: %v body=%s", err, string(body))
	}
	return res
}

func TestResponseStateFreeze_StoreFalseUsesEphemeralIDWithoutReservation(t *testing.T) {
	t.Parallel()
	memStore := continuation.NewMemoryStore()
	tStore := newTrackingTestStore(memStore)
	executor := &scriptedContinuationExecutor{stream: func() aipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           continuationAuth{},
		Executor:             executor,
		ContinuationStore:    tStore,
	})

	rec := serveContinuationRequest(t, handler, `{"model":"gpt-4o","input":"hello","store":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	res := freezeResponseResource(t, rec.Body.Bytes())
	respID, _ := res["id"].(string)
	if !strings.HasPrefix(respID, "resp_") {
		t.Fatalf("ephemeral id=%q want resp_ prefix", respID)
	}
	if storeVal, ok := res["store"].(bool); !ok || storeVal {
		t.Fatalf("store=%v want false", res["store"])
	}
	if tStore.reserveCalls != 0 {
		t.Fatalf("reserveCalls=%d want 0 for store:false", tStore.reserveCalls)
	}
	if tStore.putCalls != 0 {
		t.Fatalf("putCalls=%d want 0 (no recorder observer for store:false)", tStore.putCalls)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("executor calls=%d want 1", len(executor.calls))
	}
}

func TestResponseStateFreeze_StoreTrueResponseIDEchoesReservation(t *testing.T) {
	t.Parallel()
	memStore := continuation.NewMemoryStore()
	capture := &freezeReserveCaptureStore{Store: newTrackingTestStore(memStore)}
	executor := &scriptedContinuationExecutor{stream: func() aipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           continuationAuth{},
		Executor:             executor,
		ContinuationStore:    capture,
	})

	rec := serveContinuationRequest(t, handler, `{"model":"gpt-4o","input":"hello","store":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	res := freezeResponseResource(t, rec.Body.Bytes())
	respID, _ := res["id"].(string)
	if respID == "" {
		t.Fatal("missing response id")
	}
	if got := capture.lastReserved(); string(got) != respID {
		t.Fatalf("response id=%q reserved id=%q: non-stream envelope must echo the reservation", respID, string(got))
	}
	if storeVal, ok := res["store"].(bool); !ok || !storeVal {
		t.Fatalf("store=%v want true", res["store"])
	}
	inner, ok := capture.Store.(*trackingTestStore)
	if !ok {
		t.Fatal("capture store does not wrap tracking store")
	}
	if inner.putCalls != 1 {
		t.Fatalf("putCalls=%d want exactly 1 recorder terminal write", inner.putCalls)
	}
}

func TestResponseStateFreeze_PreviousResponseIDWithoutStoreFailsBeforeExecute(t *testing.T) {
	t.Parallel()
	executor := &scriptedContinuationExecutor{stream: func() aipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           continuationAuth{},
		Executor:             executor,
	})

	rec := serveContinuationRequest(t, handler, `{"previous_response_id":"resp_missing","input":"hi"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s want 400", rec.Code, rec.Body.String())
	}
	var errEnv map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &errEnv); err != nil {
		t.Fatal(err)
	}
	details, _ := errEnv["error"].(map[string]any)
	if details["code"] != "previous_response_not_found" {
		t.Fatalf("error code=%v want previous_response_not_found", details["code"])
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls=%d want 0 (AfterDecode must fail first)", len(executor.calls))
	}
}

func TestResponseStateFreeze_NilStoreDegradesGracefullyForStoreTrue(t *testing.T) {
	t.Parallel()
	executor := &scriptedContinuationExecutor{stream: func() aipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           continuationAuth{},
		Executor:             executor,
	})

	rec := serveContinuationRequest(t, handler, `{"model":"gpt-4o","input":"hello","store":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s want 200 graceful degradation", rec.Code, rec.Body.String())
	}
	res := freezeResponseResource(t, rec.Body.Bytes())
	respID, _ := res["id"].(string)
	if !strings.HasPrefix(respID, "resp_") {
		t.Fatalf("ephemeral id=%q want resp_ prefix", respID)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("executor calls=%d want 1", len(executor.calls))
	}
}

func TestResponseStateFreeze_OuterMediaTypeCheckedBeforePipe(t *testing.T) {
	t.Parallel()
	executor := &scriptedContinuationExecutor{stream: func() aipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           continuationAuth{},
		Executor:             executor,
	})

	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(`{"model":"gpt-4o","input":"hi"}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d want 415", rec.Code)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls=%d want 0 (outer media check precedes pipe)", len(executor.calls))
	}
}
