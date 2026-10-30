package standardplugins

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/modeldiscover"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func TestCompatibleInventory_provenanceCompleteOnBuild(t *testing.T) {
	t.Parallel()
	reg := customCompatibleRegistry(t)
	be := buildCompatibleFromYAML(t, reg, CustomOpenAILegacyCompatibleID, "prov-a", `backend_prefix: prov-a
base_url: http://127.0.0.1:9/v1
models:
  source: inline
  items:
    - canonical_id: prov-a/model-a
      native_id: model-a
`)
	inv := modelregistry.BackendInventory{
		BackendID: "prov-a", Kind: CustomOpenAILegacyCompatibleID,
		BackendPrefixes: []string{"prov-a"}, Provider: be.ModelInventory,
	}
	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{inv}, nil)
	if err != nil {
		t.Fatal(err)
	}
	models := built.Registry.All()
	if len(models) != 1 {
		t.Fatalf("models=%+v", models)
	}
	m := models[0]
	if m.Prefix != "prov-a" || m.CapabilitySource != modelregistry.CapabilitySourceStaticConfig {
		t.Fatalf("provenance=%+v", m)
	}
	if m.CanonicalID != "prov-a/model-a" || m.NativeID != "model-a" {
		t.Fatalf("ids=%+v", m)
	}
}

func TestCompatibleInventory_remoteResultsBounded(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[`)
		for i := range modeldiscover.MaxInventoryModels + 1 {
			if i > 0 {
				_, _ = io.WriteString(w, ",")
			}
			_, _ = io.WriteString(w, fmt.Sprintf(`{"id":"m%d"}`, i))
		}
		_, _ = io.WriteString(w, `]}`)
	}))
	t.Cleanup(srv.Close)

	reg := customCompatibleRegistry(t)
	be := buildCompatibleFromYAML(t, reg, CustomOpenAILegacyCompatibleID, "bound", fmt.Sprintf(`backend_prefix: bound
base_url: %s/v1
`, srv.URL), srv.Client())
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != modeldiscover.MaxInventoryModels {
		t.Fatalf("models=%d want %d warnings=%v", len(snap.Models), modeldiscover.MaxInventoryModels, snap.Warnings)
	}
}

func TestCompatibleInventory_sameKindStaticRowsDoNotCross(t *testing.T) {
	t.Parallel()
	reg := customCompatibleRegistry(t)
	beA := buildCompatibleFromYAML(t, reg, CustomOpenAILegacyCompatibleID, "inv-a", `backend_prefix: inv-a
base_url: http://127.0.0.1:9/v1
models:
  source: inline
  items:
    - canonical_id: inv-a/model-a
      native_id: model-a
`)
	beB := buildCompatibleFromYAML(t, reg, CustomOpenAILegacyCompatibleID, "inv-b", `backend_prefix: inv-b
base_url: http://127.0.0.1:9/v1
models:
  source: inline
  items:
    - canonical_id: inv-b/model-b
      native_id: model-b
`)

	assertStaticNative(t, beA, "model-a")
	assertStaticNative(t, beB, "model-b")
	assertNoNative(t, beA, "model-b")
	assertNoNative(t, beB, "model-a")
}

func TestCompatibleInventory_remoteDiscoveryUsesEndpointJoin(t *testing.T) {
	root := "COMPAT_INV_JOIN_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "yaml-key")

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"gpt-join"}]}`)
	}))
	t.Cleanup(srv.Close)

	reg := customCompatibleRegistry(t)
	raw := fmt.Sprintf(`backend_prefix: join-test
base_url: %s/provider/v1/
api_key_env_var_root: %s
`, srv.URL, root)
	be := buildCompatibleFromYAML(t, reg, CustomOpenAILegacyCompatibleID, "join-test", raw, srv.Client())

	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/provider/v1/models" {
		t.Fatalf("discovery path = %q, want /provider/v1/models", gotPath)
	}
	if snap.Source != modelinventory.SourceRemote {
		t.Fatalf("Source = %q, want remote", snap.Source)
	}
	if len(snap.Models) != 1 || snap.Models[0].CanonicalID != "join-test/gpt-join" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}

func TestCompatibleInventory_anthropicRemoteUsesV1ModelsJoin(t *testing.T) {
	root := "COMPAT_INV_ANTH_JOIN"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "yaml-key")

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-join","display_name":"Join"}]}`)
	}))
	t.Cleanup(srv.Close)

	reg := customCompatibleRegistry(t)
	raw := fmt.Sprintf(`backend_prefix: anth-join
base_url: %s
api_key_env_var_root: %s
`, srv.URL, root)
	be := buildCompatibleFromYAML(t, reg, CustomAnthropicCompatibleID, "anth-join", raw, srv.Client())

	if _, err := be.ModelInventory.LoadModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("discovery path = %q, want /v1/models", gotPath)
	}
}

func buildCompatibleFromYAML(t *testing.T, reg *pluginreg.Registry, factory, instanceID, raw string, client ...*http.Client) execbackend.Backend {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	var hc *http.Client
	if len(client) > 0 {
		hc = client[0]
	}
	res, err := reg.BuildBackendWithLifecycle(factory, instanceID, node, hc, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle(%s): %v", factory, err)
	}
	return res.Backend
}

func assertStaticNative(t *testing.T, be execbackend.Backend, nativeID string) {
	t.Helper()
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceStaticInline {
		t.Fatalf("Source = %q", snap.Source)
	}
	found := false
	for _, m := range snap.Models {
		if m.NativeID == nativeID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("native_id %q not in %+v", nativeID, snap.Models)
	}
}

func assertNoNative(t *testing.T, be execbackend.Backend, nativeID string) {
	t.Helper()
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range snap.Models {
		if m.NativeID == nativeID {
			t.Fatalf("unexpected native_id %q in %+v", nativeID, snap.Models)
		}
	}
}
