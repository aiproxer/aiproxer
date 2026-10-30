package diag_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/refautoappend"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reftoolpolicy"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reftraffictranscript"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
)

var _ diag.FeatureRegistry = (*pluginreg.Registry)(nil)

func compiledInventoryMetadata(t *testing.T, cfg *config.Config, registry *pluginreg.Registry) []diag.InventoryFeatureMetadata {
	t.Helper()
	merged, err := featurebundle.MergeFeatureSurfacesWithHost(
		registry,
		config.RegistrationsFromConfig(cfg),
		featurebundle.HostContributions{},
	)
	if err != nil {
		t.Fatalf("compile feature diagnostic projections: %v", err)
	}
	return featurehost.InventoryMetadataFromMergeSurface(merged)
}

func TestInventorySnapshot_matchesCurrentCompatibilityGolden(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends:  []config.PluginConfig{{ID: "anthropic", Enabled: false}},
			Features:  []config.PluginConfig{{ID: "submit-noop", Enabled: true}},
		},
	}
	snapshot, err := diag.InventorySnapshotForConfig(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	root := diagRepoRoot(t)
	want, err := os.ReadFile(filepath.Join(root, "internal", "core", "diag", "testdata", "current_inventory_snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("current diagnostic JSON changed; update the golden only with an intentional diagnostic contract change\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestInventorySnapshot_preservesPhase1DiagnosticBaseline(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "fe-1", Kind: "openai-responses", Enabled: true}},
			Backends:  []config.PluginConfig{{ID: "be-1", Kind: "openai-responses", Enabled: true}},
		},
	}
	snapshot, err := diag.InventorySnapshotForConfig(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := diagRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "internal", "core", "diag", "testdata", "phase1_inventory_baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Frontends []diag.PluginRow `json:"frontends"`
		Backends  []diag.PluginRow `json:"backends"`
	}
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if len(want.Frontends) == 0 || len(want.Backends) == 0 {
		t.Fatal("Phase 1 diagnostic baseline must contain frontend and backend rows")
	}
	if len(snapshot.Frontends) != len(want.Frontends) || len(snapshot.Backends) != len(want.Backends) {
		t.Fatalf("Phase 1 diagnostic row counts changed: got %d/%d want %d/%d", len(snapshot.Frontends), len(snapshot.Backends), len(want.Frontends), len(want.Backends))
	}
	for i, row := range want.Frontends {
		if snapshot.Frontends[i] != row {
			t.Fatalf("Phase 1 frontend diagnostic row %d changed: got %+v want %+v", i, snapshot.Frontends[i], row)
		}
	}
	for i, row := range want.Backends {
		if snapshot.Backends[i] != row {
			t.Fatalf("Phase 1 backend diagnostic row %d changed: got %+v want %+v", i, snapshot.Backends[i], row)
		}
	}
}

func diagRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func TestInventoryHandler_returnsPluginRows(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends:  []config.PluginConfig{{ID: "anthropic", Enabled: false}},
			Features:  []config.PluginConfig{{ID: "submit-noop", Enabled: true}},
		},
	}
	ih, err := diag.InventoryHandler(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ih)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var snap diag.InventorySnapshot
	if err := json.NewDecoder(res.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Frontends) != 1 || snap.Frontends[0].ID != "openai-responses" {
		t.Fatalf("frontends: %+v", snap.Frontends)
	}
	if len(snap.Extensions.LegalPipeline) != 17 {
		t.Fatalf("extensions.legal_pipeline: want 17 got %d", len(snap.Extensions.LegalPipeline))
	}
	if len(snap.Extensions.Stages) != 17 {
		t.Fatalf("extensions.stages: want 17 got %d", len(snap.Extensions.Stages))
	}
	for _, st := range snap.Extensions.Stages {
		if strings.TrimSpace(st.ID) == "" || strings.TrimSpace(st.DefaultFailure) == "" {
			t.Fatalf("stage missing id or default_failure: %+v", st)
		}
	}
	pipeline := strings.Join(snap.Extensions.LegalPipeline, " ")
	for _, needle := range []string{"tool_catalog_filter", "pre_request_admission", "completion_gating", "traffic_observation", "session_open", "secret_guard", "session_classification"} {
		if !strings.Contains(pipeline, needle) {
			t.Fatalf("legal_pipeline missing %q: %s", needle, pipeline)
		}
	}
}

func TestInventoryHandler_withRegistry_refAutoappend_hasStageOccupancyAndPrivileges(t *testing.T) {
	t.Parallel()
	reg := &pluginreg.Registry{}
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{
				ID:      refautoappend.ID,
				Enabled: true,
			}},
		},
	}
	extras := &diag.InventoryExtras{
		FeatureMetadata: compiledInventoryMetadata(t, cfg, reg),
	}
	ih, err := diag.InventoryHandler(cfg, extras)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ih)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var snap diag.InventorySnapshot
	if err := json.NewDecoder(res.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Extensions.Features) != 1 {
		t.Fatalf("features: %d", len(snap.Extensions.Features))
	}
	f0 := snap.Extensions.Features[0]
	if f0.BundleError != "" {
		t.Fatalf("bundle_error: %s", f0.BundleError)
	}
	if len(f0.StageOccupancy) == 0 {
		t.Fatal("want non-empty stage_occupancy for ref-autoappend-file with live registry")
	}
	seenSession := false
	for _, occ := range f0.StageOccupancy {
		if occ.StageID == "session_open" {
			seenSession = true
		}
		if occ.Count != len(occ.HandlerIDs) {
			t.Fatalf("occupancy count mismatch: %+v", occ)
		}
	}
	if !seenSession {
		t.Fatalf("expected session_open occupancy, got %#v", f0.StageOccupancy)
	}
}

func TestInventoryHandler_withRegistry_refToolPolicy_listsCatalogFilterAndToolPolicy(t *testing.T) {
	t.Parallel()
	reg := &pluginreg.Registry{}
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{
				ID:      reftoolpolicy.ID,
				Enabled: true,
			}},
		},
	}
	extras := &diag.InventoryExtras{
		FeatureMetadata: compiledInventoryMetadata(t, cfg, reg),
	}
	ih, err := diag.InventoryHandler(cfg, extras)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ih)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var snap diag.InventorySnapshot
	if err := json.NewDecoder(res.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Extensions.Features) != 1 {
		t.Fatalf("features: %d", len(snap.Extensions.Features))
	}
	f0 := snap.Extensions.Features[0]
	if f0.BundleError != "" {
		t.Fatalf("bundle_error: %s", f0.BundleError)
	}
	var toolCat, toolReact []string
	for _, occ := range f0.StageOccupancy {
		switch occ.StageID {
		case "tool_catalog_filter":
			toolCat = append(toolCat, occ.HandlerIDs...)
		case "tool_event_reaction":
			toolReact = append(toolReact, occ.HandlerIDs...)
		}
	}
	hasPolicy := false
	hasFilter := false
	hasReactor := false
	for _, id := range toolCat {
		if strings.Contains(id, "tool_catalog:"+reftoolpolicy.ID+"-filter") {
			hasFilter = true
		}
	}
	for _, id := range toolReact {
		if strings.Contains(id, "tool_policy:"+reftoolpolicy.ID+"-tool-policy") {
			hasPolicy = true
		}
		if id == reftoolpolicy.ID+"-reactor" {
			hasReactor = true
		}
	}
	if !hasFilter {
		t.Fatalf("want catalog filter in occupancy, got tool_catalog %#v", toolCat)
	}
	if !hasPolicy {
		t.Fatalf("want tool_policy in occupancy, got tool_event_reaction %#v", toolReact)
	}
	if !hasReactor {
		t.Fatalf("want reactor id in occupancy, got tool_event_reaction %#v", toolReact)
	}
	if !f0.Privileges.AuxiliaryRequests {
		t.Fatal("want auxiliary_requests for catalog shaping")
	}
}

func TestInventoryHandler_refTrafficTranscript_inventoryJSONOmitsDefaultRedactSubstring(t *testing.T) {
	t.Parallel()
	reg := &pluginreg.Registry{}
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{
				ID:      reftraffictranscript.ID,
				Enabled: true,
			}},
		},
	}
	extras := &diag.InventoryExtras{
		FeatureMetadata: compiledInventoryMetadata(t, cfg, reg),
	}
	ih, err := diag.InventoryHandler(cfg, extras)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ih)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	// Default proof config redacts this substring on traffic observer path; inventory must not echo it.
	if strings.Contains(string(body), "REF_SECRET") {
		t.Fatalf("inventory leaked default redaction substring")
	}
	var snap diag.InventorySnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Extensions.Features) != 1 {
		t.Fatalf("features: %d", len(snap.Extensions.Features))
	}
	f0 := snap.Extensions.Features[0]
	if f0.BundleError != "" {
		t.Fatalf("bundle_error: %s", f0.BundleError)
	}
	if !f0.Privileges.RawCapture {
		t.Fatal("want raw_capture privilege flagged")
	}
	seenUsageObserver := false
	for _, occ := range f0.StageOccupancy {
		if occ.StageID != "traffic_observation" {
			continue
		}
		for _, id := range occ.HandlerIDs {
			if strings.HasPrefix(id, "usage_observer:") {
				seenUsageObserver = true
			}
		}
	}
	if !seenUsageObserver {
		t.Fatalf("want usage_observer in traffic_observation occupancy: %#v", f0.StageOccupancy)
	}
}

func TestInventoryHandler_refVerifierPrivilegesExposeCompletionGate(t *testing.T) {
	t.Parallel()
	const refVerifierID = "ref-verifier-stub"
	reg := &pluginreg.Registry{}
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{
				ID:      refVerifierID,
				Enabled: true,
			}},
		},
	}
	extras := &diag.InventoryExtras{
		FeatureMetadata: compiledInventoryMetadata(t, cfg, reg),
	}
	ih, err := diag.InventoryHandler(cfg, extras)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ih)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var snap diag.InventorySnapshot
	if err := json.NewDecoder(res.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	for _, f := range snap.Extensions.Features {
		if f.InstanceID != refVerifierID {
			continue
		}
		if !f.Privileges.CompletionGate {
			t.Fatal("want completion_gate privilege true for ref-verifier-stub bundle")
		}
		if !f.Privileges.AuxiliaryRequests {
			t.Fatal("want auxiliary_requests privilege true for ref-verifier-stub bundle")
		}
		return
	}
	t.Fatal("ref-verifier feature row missing")
}

func TestInventorySnapshotForConfig_matchesInventoryHandlerJSON(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends:  []config.PluginConfig{{ID: "anthropic", Enabled: false}},
			Features:  []config.PluginConfig{{ID: "submit-noop", Enabled: true}},
		},
	}
	ctx := context.Background()
	direct, err := diag.InventorySnapshotForConfig(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ih, err := diag.InventoryHandler(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	ih.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("handler status %d body %s", rr.Code, rr.Body.String())
	}
	var viaHTTP diag.InventorySnapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &viaHTTP); err != nil {
		t.Fatal(err)
	}
	dj, err := json.Marshal(direct)
	if err != nil {
		t.Fatal(err)
	}
	hj, err := json.Marshal(viaHTTP)
	if err != nil {
		t.Fatal(err)
	}
	if string(dj) != string(hj) {
		t.Fatalf("JSON mismatch\ndirect=%s\nhttp  =%s", dj, hj)
	}
}

func TestInventorySnapshotForConfig_nilContext(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
		},
	}
	//nolint:staticcheck // explicit nil context: contract must reject nil without panicking
	_, err := diag.InventorySnapshotForConfig(nil, cfg, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if want := "diag: inventory snapshot for config: nil context"; err.Error() != want {
		t.Fatalf("got %q want %q", err.Error(), want)
	}
}

func TestInventorySnapshotForConfig_nilConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, err := diag.InventorySnapshotForConfig(ctx, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if want := "diag: inventory snapshot for config: nil config"; err.Error() != want {
		t.Fatalf("got %q want %q", err.Error(), want)
	}
}
