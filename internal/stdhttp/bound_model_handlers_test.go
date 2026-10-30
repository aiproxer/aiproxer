package stdhttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func TestModelRegistryHandler_boundViewIgnoresLiveRefresh(t *testing.T) {
	t.Parallel()

	provider := &stdhttpFlipInventory{
		a: []modelinventory.Model{{CanonicalID: "openai/gpt-a", NativeID: "gpt-a"}},
		b: []modelinventory.Model{{CanonicalID: "openai/gpt-b", NativeID: "gpt-b"}},
	}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: &stdhttpUnavailableCache{},
		Now:   func() time.Time { return time.Unix(10, 0).UTC() },
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	bound := rt.BoundView()
	bodyA, genA, ok := bound.ModelsJSON()
	if !ok {
		t.Fatal("bound ModelsJSON")
	}

	provider.useB = true
	rt.RunRefresh(context.Background())

	h := stdhttp.NewModelRegistryHandler(rt)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req = req.WithContext(modelregistry.WithBoundView(req.Context(), bound))
	// Attach a fake config binding via dispatcher plane is heavy; ETag without
	// config gen still uses model generation. Verify body + model gen identity.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	if rr.Body.String() != string(bodyA) {
		t.Fatalf("body changed after refresh")
	}
	if etag := rr.Header().Get("ETag"); etag != `"`+genA+`"` {
		t.Fatalf("ETag=%q want %q", etag, `"`+genA+`"`)
	}

	// Later unbound request observes refresh.
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rr2.Body.String() == string(bodyA) {
		t.Fatal("live request must observe refresh")
	}
}

func TestModelRegistryStatusHandler_boundViewCoherent(t *testing.T) {
	t.Parallel()

	provider := &stdhttpFlipInventory{
		a: []modelinventory.Model{{CanonicalID: "openai/gpt-a", NativeID: "gpt-a"}},
		b: []modelinventory.Model{{CanonicalID: "openai/gpt-b", NativeID: "gpt-b"}},
	}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: &stdhttpUnavailableCache{},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	bound := rt.BoundView()
	genA := bound.Diagnostics().Generation

	provider.useB = true
	rt.RunRefresh(context.Background())

	h := stdhttp.NewModelRegistryStatusHandler(rt)
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req = req.WithContext(modelregistry.WithBoundView(req.Context(), bound))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["generation"] != genA {
		t.Fatalf("generation=%v want %q", got["generation"], genA)
	}
	modelCount, ok := got["model_count"].(float64)
	if !ok {
		t.Fatalf("model_count type %T, want float64", got["model_count"])
	}
	if int(modelCount) != 1 {
		t.Fatalf("model_count=%v", modelCount)
	}
}

func TestCatalogStatusHandler_boundSnapshot(t *testing.T) {
	t.Parallel()

	idxA := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"m/a": {Source: modelcatalog.FactSourceCatalog},
	})
	idxB := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"m/b": {Source: modelcatalog.FactSourceCatalog},
	})
	rt := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{})
	rt.PublishSnapshot(modelcatalog.Snapshot{
		Generation: "cat-a", FetchedAt: time.Unix(1, 0).UTC(), Index: idxA,
	})
	bound := rt.BoundView()
	rt.PublishSnapshot(modelcatalog.Snapshot{
		Generation: "cat-b", FetchedAt: time.Unix(2, 0).UTC(), Index: idxB,
	})

	h := stdhttp.NewCatalogStatusHandler(nil, modelcatalog.CatalogStatusHandlerConfig{
		Runtime:      rt,
		UsageEnabled: true,
	})
	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	req = req.WithContext(modelcatalog.WithBoundView(req.Context(), bound))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	var got modelcatalog.CatalogDiagnosticsJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Snapshot == nil || got.Snapshot.Generation != "cat-a" {
		t.Fatalf("bound catalog status = %+v", got.Snapshot)
	}
}

type stdhttpUnavailableCache struct{}

func (stdhttpUnavailableCache) Load(context.Context) (modelregistry.Snapshot, error) {
	return modelregistry.Snapshot{}, modelregistry.ErrSnapshotUnavailable
}
func (stdhttpUnavailableCache) Save(context.Context, modelregistry.Snapshot) error { return nil }

type stdhttpFlipInventory struct {
	useB bool
	a, b []modelinventory.Model
}

func (p *stdhttpFlipInventory) LoadModels(context.Context) (modelinventory.Snapshot, error) {
	models := p.a
	if p.useB {
		models = p.b
	}
	return modelinventory.Snapshot{
		Source: modelinventory.SourceRemote,
		Models: append([]modelinventory.Model(nil), models...),
	}, nil
}
