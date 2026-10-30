package openresponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/continuation"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

func TestClientSelectedResponseIDRejection(t *testing.T) {
	t.Parallel()
	store := continuation.NewMemoryStore()
	executor := &scriptedContinuationExecutor{stream: func() aipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           staticAuth{tenant: "tenant-1", principal: "principal-1", allow: true},
		Executor:             executor,
		ContinuationStore:    store,
	})

	// Client passes custom "response_id" attempting response ID fixation
	body := `{"response_id":"resp_custom_fixed_123","model":"gpt-4o","input":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400 when client specifies response_id, got %d: %s", rec.Code, rec.Body.String())
	}

	var errEnv struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errEnv); err != nil {
		t.Fatalf("failed to parse wire error: %v", err)
	}
	if errEnv.Error.Code != "bad_request" {
		t.Errorf("expected code 'bad_request', got %q", errEnv.Error.Code)
	}
}

func TestUniformErrorShapeForParentResolutionFailures(t *testing.T) {
	t.Parallel()
	store := continuation.NewMemoryStore()
	sc := aipcont.Scope{TenantID: "tenant-1", PrincipalID: "principal-1"}
	ctx := context.Background()

	// Seed parent under wrong scope
	wrongScope := aipcont.Scope{TenantID: "tenant-2", PrincipalID: "principal-2"}
	wrongScopeParentID := seedParentRecord(t, store, wrongScope, aipcont.RecordStatusCompleted)

	// Seed failed parent record
	// Failed terminal records are ineligible for persistence, so model this
	// failure as an unresolved parent ID rather than bypassing the store contract.
	failedParentID := aipcont.ResponseID("resp_failed_999")

	// Seed incomplete parent record without allow_incomplete
	incompletePolicy := aipcont.StoragePolicy{TTL: time.Hour, AllowIncomplete: true}
	incompleteID, err := store.Reserve(ctx, sc, incompletePolicy)
	if err != nil {
		t.Fatal(err)
	}
	incompleteRec := aipcont.ContinuationRecord{
		ID:          incompleteID,
		Scope:       sc,
		Status:      aipcont.RecordStatusIncomplete,
		Terminal:    true,
		InputItems:  []aipapi.Item{{ID: "in1"}},
		OutputItems: []aipapi.Item{{ID: "out1"}},
		Policy:      incompletePolicy,
	}
	if err := store.PutTerminal(ctx, incompleteRec); err != nil {
		t.Fatal(err)
	}

	executor := &scriptedContinuationExecutor{stream: func() aipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           staticAuth{tenant: "tenant-1", principal: "principal-1", allow: true},
		Executor:             executor,
		ContinuationStore:    store,
	})

	cases := []struct {
		name     string
		parentID string
	}{
		{"missing_id", "resp_missing_9999999999999999"},
		{"malformed_id_no_prefix", "invalid_id_format"},
		{"malformed_id_short_entropy", "resp_short"},
		{"wrong_scope_id", wrongScopeParentID.String()},
		{"failed_parent_status", failedParentID.String()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"previous_response_id":"` + tc.parentID + `","input":"new-input"}`
			req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400 for %s, got %d: %s", tc.name, rec.Code, rec.Body.String())
			}

			var errEnv struct {
				Error struct {
					Message string `json:"message"`
					Code    string `json:"code"`
					Type    string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &errEnv); err != nil {
				t.Fatalf("failed to parse error JSON for %s: %v", tc.name, err)
			}
			if errEnv.Error.Code != "previous_response_not_found" {
				t.Errorf("%s: expected code 'previous_response_not_found', got %q", tc.name, errEnv.Error.Code)
			}
			if errEnv.Error.Message != "Previous response was not found" {
				t.Errorf("%s: expected message 'Previous response was not found', got %q", tc.name, errEnv.Error.Message)
			}
			if errEnv.Error.Type != "invalid_request_error" {
				t.Errorf("%s: expected type 'invalid_request_error', got %q", tc.name, errEnv.Error.Type)
			}
		})
	}
}
