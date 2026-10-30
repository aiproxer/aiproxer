package stdhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

type mutableInventoryProvider struct {
	mu     sync.Mutex
	models []modelinventory.Model
	err    error
}

func (p *mutableInventoryProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return modelinventory.Snapshot{}, p.err
	}
	return modelinventory.Snapshot{
		Source: modelinventory.SourceRemote,
		Models: append([]modelinventory.Model(nil), p.models...),
	}, nil
}

func (p *mutableInventoryProvider) SetModels(models []modelinventory.Model) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.models = append([]modelinventory.Model(nil), models...)
}

func startTestModelRegistryRuntime(t *testing.T, provider *mutableInventoryProvider, backendID, kind string) *modelregistry.Runtime {
	t.Helper()
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       backendID,
			Kind:            kind,
			BackendPrefixes: []string{kind},
			Provider:        provider,
		}},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return rt
}

func TestModelRegistryHandler_etagNotModified(t *testing.T) {
	t.Parallel()
	provider := &mutableInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-4o",
		NativeID:    "gpt-4o",
	}}}
	rt := startTestModelRegistryRuntime(t, provider, "openai-a", "openai-responses")
	h := NewModelRegistryHandler(rt)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, openAIModelsPath, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d", rr.Code)
	}
	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag")
	}
	body := rr.Body.String()
	if !strings.Contains(body, "openai-a:openai/gpt-4o") {
		t.Fatalf("body=%s", body)
	}

	req := httptest.NewRequest(http.MethodGet, openAIModelsPath, nil)
	req.Header.Set("If-None-Match", etag)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotModified {
		t.Fatalf("code=%d want 304 body=%s", rr.Code, rr.Body.String())
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("304 body should be empty, got %q", rr.Body.String())
	}
}

func TestModelRegistryHandler_emptyAndNil(t *testing.T) {
	t.Parallel()
	provider := &mutableInventoryProvider{models: nil}
	rt := startTestModelRegistryRuntime(t, provider, "openai-a", "openai-responses")
	for _, tc := range []struct {
		name string
		h    http.Handler
	}{
		{name: "empty", h: NewModelRegistryHandler(rt)},
		{name: "nil", h: NewModelRegistryHandler(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rr := httptest.NewRecorder()
			tc.h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, openAIModelsPath, nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
			}
			var got openAIModelList
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Object != "list" || len(got.Data) != 0 {
				t.Fatalf("got=%+v", got)
			}
		})
	}
}

func TestModelRegistryHandler_methodsNoNativeLeakStableOrder(t *testing.T) {
	t.Parallel()
	p1 := &mutableInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "google/gemini-3.5-flash-high",
		NativeID:    "pretty-secret-a",
	}}}
	p2 := &mutableInventoryProvider{models: []modelinventory.Model{
		{CanonicalID: "google/gemini-3.5-flash-high", NativeID: "pretty-secret-b"},
		{CanonicalID: "openai/gpt-4o", NativeID: "native-secret"},
		{CanonicalID: "openai/gpt-4o", NativeID: "native-secret"}, // identical duplicate; harmless
	}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{
			{BackendID: "agycliacp.z", Kind: "agycliacp", BackendPrefixes: []string{"agycliacp"}, Provider: p1},
			{BackendID: "agycliacp.a", Kind: "agycliacp", BackendPrefixes: []string{"agycliacp"}, Provider: p2},
		},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := NewModelRegistryHandler(rt)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, openAIModelsPath, nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST code=%d", rr.Code)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, openAIModelsPath, nil))
	body := rr.Body.String()
	for _, leak := range []string{"pretty-secret", "native-secret", "NativeID"} {
		if strings.Contains(body, leak) {
			t.Fatalf("leak %q in %s", leak, body)
		}
	}
	var got openAIModelList
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"agycliacp.a:google/gemini-3.5-flash-high",
		"agycliacp.a:openai/gpt-4o",
		"agycliacp.z:google/gemini-3.5-flash-high",
	}
	if len(got.Data) != len(want) {
		t.Fatalf("got=%+v", got.Data)
	}
	for i, id := range want {
		if got.Data[i].ID != id || got.Data[i].Object != "model" {
			t.Fatalf("data[%d]=%+v want id %q", i, got.Data[i], id)
		}
	}
	if got.Data[1].OwnedBy != "agycliacp" {
		t.Fatalf("owned_by=%q", got.Data[1].OwnedBy)
	}
}

func TestModelRegistryHandler_refreshVisibility(t *testing.T) {
	t.Parallel()
	provider := &mutableInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-4o",
		NativeID:    "gpt-4o",
	}}}
	rt := startTestModelRegistryRuntime(t, provider, "openai-a", "openai-responses")
	h := NewModelRegistryHandler(rt)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, openAIModelsPath, nil))
	var before openAIModelList
	if err := json.Unmarshal(rr.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if len(before.Data) != 1 || before.Data[0].ID != "openai-a:openai/gpt-4o" {
		t.Fatalf("before=%+v", before)
	}

	provider.SetModels([]modelinventory.Model{{
		CanonicalID: "openai/gpt-4o-mini",
		NativeID:    "gpt-4o-mini",
	}})
	rt.RunRefresh(context.Background())

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, openAIModelsPath, nil))
	var after openAIModelList
	if err := json.Unmarshal(rr.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Data) != 1 || after.Data[0].ID != "openai-a:openai/gpt-4o-mini" {
		t.Fatalf("after=%+v", after)
	}
}

func TestModelRegistryStatusHandler_methodsProtectShape(t *testing.T) {
	t.Parallel()
	provider := &mutableInventoryProvider{err: errors.New("spawn failed: native-secret stderr")}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "cursor.main",
			Kind:            "cursorcliacp",
			BackendPrefixes: []string{"cursorcliacp"},
			Provider:        provider,
		}},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := NewModelRegistryStatusHandler(rt)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST code=%d", rr.Code)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET code=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, leak := range []string{"native-secret", "spawn failed", "stderr"} {
		if strings.Contains(body, leak) {
			t.Fatalf("leak %q in %s", leak, body)
		}
	}
	var got modelRegistryStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "active" && got.Status != "unavailable" {
		t.Fatalf("status=%q", got.Status)
	}
	if got.BackendModelCounts == nil || got.Discoveries == nil {
		t.Fatalf("nil maps/slices: %+v", got)
	}
	if len(got.Discoveries) != 1 || got.Discoveries[0].BackendID != "cursor.main" {
		t.Fatalf("discoveries=%+v", got.Discoveries)
	}
	if got.Discoveries[0].ErrorCode == "" {
		t.Fatalf("expected error_code, got=%+v", got.Discoveries[0])
	}
}

func TestModelRegistryStatusHandler_nilRuntime(t *testing.T) {
	t.Parallel()
	h := NewModelRegistryStatusHandler(nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d", rr.Code)
	}
	var got modelRegistryStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "unavailable" || len(got.Discoveries) != 0 {
		t.Fatalf("got=%+v", got)
	}
}

func TestModelRegistryStatusHandler_refreshUpdates(t *testing.T) {
	t.Parallel()
	provider := &mutableInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-4o",
		NativeID:    "gpt-4o",
	}}}
	var nowMu sync.Mutex
	now := time.Unix(1700, 0).UTC()
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "openai-a",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Now: func() time.Time {
			nowMu.Lock()
			defer nowMu.Unlock()
			return now
		},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := NewModelRegistryStatusHandler(rt)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	var before modelRegistryStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if before.ModelCount != 1 || before.Status != "active" {
		t.Fatalf("before=%+v", before)
	}

	provider.SetModels([]modelinventory.Model{
		{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"},
		{CanonicalID: "openai/gpt-4o-mini", NativeID: "gpt-4o-mini"},
	})
	nowMu.Lock()
	now = now.Add(time.Second)
	nowMu.Unlock()
	rt.RunRefresh(context.Background())

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	var after modelRegistryStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if after.ModelCount != 2 || after.Generation == before.Generation {
		t.Fatalf("after=%+v beforeGen=%q", after, before.Generation)
	}
}
