package openresponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/continuation"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type staticAuth struct {
	tenant    string
	principal string
	allow     bool
}

func (a staticAuth) Authenticate(ctx context.Context, meta sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	if !a.allow {
		return sdkauth.Decision{
			Outcome:    sdkauth.OutcomeDeny,
			ReasonCode: "unauthorized",
		}, nil
	}
	return sdkauth.Decision{
		Outcome:   sdkauth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: a.principal},
		Scope: &scope.PrincipalScopeView{
			PrincipalID: scope.Known(a.principal),
			TenantID:    scope.Known(a.tenant),
			Origin:      scope.OriginClient,
		},
	}, nil
}

type trackingBodyReader struct {
	r         io.Reader
	bytesRead int
}

func (t *trackingBodyReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	t.bytesRead += n
	return n, err
}

func (t *trackingBodyReader) Close() error {
	if rc, ok := t.r.(io.Closer); ok {
		return rc.Close()
	}
	return nil
}

type trackingResolver struct {
	inner      openresponses.ContinuationResolver
	calls      int
	lastScope  aipcont.Scope
	lastParent string
}

func (tr *trackingResolver) ResolveParent(ctx context.Context, sc aipcont.Scope, parentID string, baseCall aipapi.Call) (aipapi.Call, aipcont.ContinuationRecord, error) {
	tr.calls++
	tr.lastScope = sc
	tr.lastParent = parentID
	if tr.inner != nil {
		return tr.inner.ResolveParent(ctx, sc, parentID, baseCall)
	}
	return aipapi.Call{}, aipcont.ContinuationRecord{}, aipcont.ErrPreviousResponseNotFound
}

func seedParentRecord(t *testing.T, store aipcont.Store, sc aipcont.Scope, status aipcont.RecordStatus) aipcont.ResponseID {
	t.Helper()
	ctx := context.Background()
	policy := aipcont.StoragePolicy{
		Mode: aipcont.PersistencePersistent,
		TTL:  24 * time.Hour,
	}
	id, err := store.Reserve(ctx, sc, policy)
	if err != nil {
		t.Fatalf("failed to reserve id: %v", err)
	}
	rec := aipcont.ContinuationRecord{
		ID:    id,
		Scope: sc,
		InputItems: []aipapi.Item{
			{
				ID:      "item-in-1",
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleUser,
				Status:  aipapi.ItemStatusCompleted,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "parent-input"}},
			},
		},
		OutputItems: []aipapi.Item{
			{
				ID:      "item-out-1",
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleAssistant,
				Status:  aipapi.ItemStatusCompleted,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "parent-output"}},
			},
		},
		Lineage: aipcont.Lineage{
			ProfileID:     "openresponses",
			Model:         "gpt-4o",
			RouteSelector: "gpt-4o",
		},
		Terminal: status == aipcont.RecordStatusCompleted,
		Policy:   policy,
	}
	if status == aipcont.RecordStatusFailed {
		rec.Terminal = true
		rec.Status = aipcont.RecordStatusFailed
	}
	if err := store.PutTerminal(ctx, rec); err != nil {
		t.Fatalf("failed to seed parent record: %v", err)
	}
	return id
}

func TestPreviousResponseParentResolveSuccess(t *testing.T) {
	store := continuation.NewMemoryStore()
	sc := aipcont.Scope{TenantID: "tenant-1", PrincipalID: "principal-1"}
	parentID := seedParentRecord(t, store, sc, aipcont.RecordStatusCompleted)

	executor := &scriptedContinuationExecutor{stream: func() aipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           staticAuth{tenant: "tenant-1", principal: "principal-1", allow: true},
		Executor:             executor,
		ContinuationStore:    store,
	})

	body := `{"previous_response_id":"` + parentID.String() + `","input":"new-input"}`
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(executor.calls) != 1 {
		t.Fatalf("expected 1 executor call, got %d", len(executor.calls))
	}
	call := executor.calls[0]
	if len(call.Items) != 3 {
		t.Fatalf("expected 3 items (parent-in, parent-out, new-in), got %d", len(call.Items))
	}
	if call.Items[0].Content[0].Text != "parent-input" {
		t.Errorf("item 0 text mismatch: got %q, want %q", call.Items[0].Content[0].Text, "parent-input")
	}
	if call.Items[1].Content[0].Text != "parent-output" {
		t.Errorf("item 1 text mismatch: got %q, want %q", call.Items[1].Content[0].Text, "parent-output")
	}
	if call.Items[2].Content[0].Text != "new-input" {
		t.Errorf("item 2 text mismatch: got %q, want %q", call.Items[2].Content[0].Text, "new-input")
	}
	if call.Session.ClientSessionID != "" || call.Session.ContinuityKey != "" || call.Session.ResumeToken != "" {
		t.Errorf("client proxy ID or session token leaked into canonical call: %+v", call.Session)
	}
}

func TestPreviousResponseParentResolveNotFound(t *testing.T) {
	store := continuation.NewMemoryStore()
	executor := &scriptedContinuationExecutor{stream: func() aipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           staticAuth{tenant: "tenant-1", principal: "principal-1", allow: true},
		Executor:             executor,
		ContinuationStore:    store,
	})

	body := `{"previous_response_id":"resp_missing_999","input":"new-input"}`
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(executor.calls) != 0 {
		t.Fatalf("expected 0 executor calls when parent not found, got %d", len(executor.calls))
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
	if errEnv.Error.Code != "previous_response_not_found" {
		t.Errorf("expected code 'previous_response_not_found', got %q", errEnv.Error.Code)
	}
}

func TestPreviousResponseParentResolveExpired(t *testing.T) {
	store := continuation.NewMemoryStore()
	// Failed terminal records are rejected by the store; an expired/missing
	// parent is represented by an ID that is not present in the store.
	parentID := aipcont.ResponseID("resp_expired_999")

	executor := &scriptedContinuationExecutor{stream: func() aipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           staticAuth{tenant: "tenant-1", principal: "principal-1", allow: true},
		Executor:             executor,
		ContinuationStore:    store,
	})

	body := `{"previous_response_id":"` + parentID.String() + `","input":"new-input"}`
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(executor.calls) != 0 {
		t.Fatalf("expected 0 executor calls for failed parent, got %d", len(executor.calls))
	}
}

func TestPreviousResponseParentResolveWrongScope(t *testing.T) {
	store := continuation.NewMemoryStore()
	// Seed parent under tenant-1 / principal-1
	sc1 := aipcont.Scope{TenantID: "tenant-1", PrincipalID: "principal-1"}
	parentID := seedParentRecord(t, store, sc1, aipcont.RecordStatusCompleted)

	executor := &scriptedContinuationExecutor{stream: func() aipapi.EventStream { return &responseStream{} }}
	// Request authenticated under tenant-1 / principal-OTHER
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           staticAuth{tenant: "tenant-1", principal: "principal-other", allow: true},
		Executor:             executor,
		ContinuationStore:    store,
	})

	body := `{"previous_response_id":"` + parentID.String() + `","input":"new-input"}`
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(executor.calls) != 0 {
		t.Fatalf("expected 0 executor calls on wrong scope, got %d", len(executor.calls))
	}
}

func TestPreviousResponseUnauthorizedNoWork(t *testing.T) {
	store := continuation.NewMemoryStore()
	executor := &scriptedContinuationExecutor{stream: func() aipapi.EventStream { return &responseStream{} }}

	resolver := &trackingResolver{}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           staticAuth{allow: false},
		Executor:             executor,
		ContinuationStore:    store,
		ContinuationResolver: resolver,
	})

	rawBody := []byte(`{"previous_response_id":"resp_123","input":"test"}`)
	tb := &trackingBodyReader{r: bytes.NewReader(rawBody)}

	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", tb)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d: %s", rec.Code, rec.Body.String())
	}
	if tb.bytesRead != 0 {
		t.Errorf("expected 0 bytes read from body on unauthorized request, got %d", tb.bytesRead)
	}
	if resolver.calls != 0 {
		t.Errorf("expected 0 resolver calls on unauthorized request, got %d", resolver.calls)
	}
	if len(executor.calls) != 0 {
		t.Errorf("expected 0 executor calls on unauthorized request, got %d", len(executor.calls))
	}
}

func TestNoPreviousResponseNoStoreLookup(t *testing.T) {
	store := continuation.NewMemoryStore()
	executor := &scriptedContinuationExecutor{stream: func() aipapi.EventStream { return &responseStream{} }}

	resolver := &trackingResolver{}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           staticAuth{tenant: "tenant-1", principal: "principal-1", allow: true},
		Executor:             executor,
		ContinuationStore:    store,
		ContinuationResolver: resolver,
	})

	body := `{"model":"gpt-4o","input":"standalone-input"}`
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if resolver.calls != 0 {
		t.Errorf("expected 0 resolver calls when previous_response_id is absent, got %d", resolver.calls)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("expected 1 executor call, got %d", len(executor.calls))
	}
}
