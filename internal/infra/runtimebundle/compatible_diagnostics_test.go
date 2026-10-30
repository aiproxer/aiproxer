package runtimebundle_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

func TestCompatibleDiagnostics_inventoryAndRoutesExposeLiveHealth(t *testing.T) {
	t.Parallel()
	path := writeCompatibleDiagnosticsConfig(t)

	ctx := context.Background()
	regInput := runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	}
	inv, err := runtimebundle.InspectInventory(ctx, regInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.CompatibleBackends) != 1 {
		t.Fatalf("inventory compatible_backends=%d", len(inv.CompatibleBackends))
	}
	health := inv.CompatibleBackends[0].InventoryHealth
	if health == nil {
		t.Fatal("expected live inventory_health")
	}
	if health.ModelCount != 1 {
		t.Fatalf("model_count=%d health=%+v", health.ModelCount, health)
	}
	if health.Source != "static_inline" {
		t.Fatalf("source=%q", health.Source)
	}
	if len(health.SampleModels) != 1 || health.SampleModels[0].Prefix != "compat-diag" {
		t.Fatalf("samples=%+v", health.SampleModels)
	}

	routes, err := runtimebundle.InspectRoutes(ctx, regInput)
	if err != nil {
		t.Fatal(err)
	}
	if routes.CompatibleBackends[0].InventoryHealth == nil {
		t.Fatal("routes missing live inventory_health")
	}
}

func TestCompatibleDiagnostics_inventoryAndRoutesExposeBuiltInOrigin(t *testing.T) {
	t.Parallel()
	path := writeCompatibleDiagnosticsConfig(t)

	ctx := context.Background()
	regInput := runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	}
	routes, err := runtimebundle.InspectRoutes(ctx, regInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes.CompatibleBackends) != 1 {
		t.Fatalf("routes compatible_backends=%d", len(routes.CompatibleBackends))
	}
	if routes.CompatibleBackends[0].Origin != "built_in_compatible" {
		t.Fatalf("origin=%q", routes.CompatibleBackends[0].Origin)
	}

	inv, err := runtimebundle.InspectInventory(ctx, regInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.CompatibleBackends) != 1 {
		t.Fatalf("inventory compatible_backends=%d", len(inv.CompatibleBackends))
	}
	if inv.CompatibleBackends[0].Prefix != "compat-diag" {
		t.Fatalf("prefix=%q", inv.CompatibleBackends[0].Prefix)
	}
}

func TestCompatibleDiagnostics_inspectDistinguishesFromExternalPlugins(t *testing.T) {
	t.Parallel()
	path := writeCompatibleDiagnosticsConfig(t)
	prep, err := runtimebundle.PrepareInspect(context.Background(), runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prep.Close() })

	rep, err := runtimebundle.InspectBackendPlugins(prep.Config, prep.Registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.CompatibleBackends) != 1 {
		t.Fatalf("compatible_backends=%d", len(rep.CompatibleBackends))
	}
	var factoryEntry, instanceEntry bool
	for _, e := range rep.Entries {
		switch {
		case e.Kind == "custom-openai-legacy-compatible" && e.Source == "built_in_compatible":
			switch e.InstanceID {
			case "":
				factoryEntry = true
			case "compat-diag":
				instanceEntry = true
				if e.ActivationRequired {
					t.Fatal("compatible instance must not require plugin activation")
				}
			}
		}
		if e.Source == "discovered" && strings.Contains(e.Kind, "custom-openai") {
			t.Fatalf("compatible kind must not appear as discovered plugin: %+v", e)
		}
	}
	if !factoryEntry || !instanceEntry {
		t.Fatalf("inspect entries missing factory=%v instance=%v entries=%+v", factoryEntry, instanceEntry, rep.Entries)
	}
}

func TestCompatibleDiagnostics_checkConfigNoProviderRequest(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "must not be called", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)

	path := writeCompatibleDiagnosticsConfig(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "https://example.test/v1", srv.URL+"/v1", 1)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatalf("check-config validation: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("provider requests during check-config = %d", hits.Load())
	}
}

func TestCompatibleDiagnostics_inventoryJSONSecretSafe(t *testing.T) {
	t.Parallel()
	path := writeCompatibleDiagnosticsConfig(t)
	inv, err := runtimebundle.InspectInventory(context.Background(), runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if strings.Contains(body, "sk-") {
		t.Fatalf("inventory json leaked secret-like value: %s", body)
	}
}

func writeCompatibleDiagnosticsConfig(t *testing.T) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rows := `    - id: compat-diag
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: compat-diag
        base_url: https://example.test/v1
        api_key_env_var_root: COMPAT_DIAG_KEY
        tokenizer: o200k_base
        max_concurrent_requests: 3
        models:
          source: inline
          items:
            - canonical_id: compat-diag/model-a
              native_id: model-a
`
	text := strings.Replace(string(base), "  features:\n", rows+"  features:\n", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeOpenResponsesFrontendConfig(t *testing.T, enabled bool, extra string) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rows := fmt.Sprintf(`    - id: or-diag
      kind: openresponses
      enabled: %v
      config:
        profile: 2026-04-24
        base_path: /openresponses/v1
        continuation:
          persistent_store: standard
          ttl: 24h
        websocket:
          enabled: true
          max_connection_age: 60m
          idle_timeout: 5m
          max_queued_turns: 1
          allowed_origins:
            - https://app.example.test
%s`, enabled, extra)
	text := strings.Replace(string(base), "  backends:\n", rows+"  backends:\n", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenResponsesFrontendDiagnostics_routesAndInventoryExposeSanitizedRow(t *testing.T) {
	t.Parallel()
	path := writeOpenResponsesFrontendConfig(t, true, "")

	ctx := context.Background()
	regInput := runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	}

	routes, err := runtimebundle.InspectRoutes(ctx, regInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes.OpenResponsesFrontends) != 1 {
		t.Fatalf("routes openresponses_frontends=%d", len(routes.OpenResponsesFrontends))
	}
	fe := routes.OpenResponsesFrontends[0]
	if fe.Origin != "client_facing" || fe.InstanceID != "or-diag" {
		t.Fatalf("origin/instance = %+v", fe)
	}
	if fe.Profile != "2026-04-24" || fe.BasePath != "/openresponses/v1" {
		t.Fatalf("profile/base_path = %+v", fe)
	}
	if !fe.WebSocketEnabled || fe.ContinuationStore != "standard" {
		t.Fatalf("ws/continuation = %+v", fe)
	}
	if fe.Conformance != "profile:2026-04-24" {
		t.Fatalf("conformance=%q", fe.Conformance)
	}
	if len(fe.RouteClaims) != 3 {
		t.Fatalf("route_claims=%v", fe.RouteClaims)
	}
	if fe.ConfigError != "" {
		t.Fatalf("config_error=%q", fe.ConfigError)
	}

	inv, err := runtimebundle.InspectInventory(ctx, regInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.OpenResponsesFrontends) != 1 {
		t.Fatalf("inventory openresponses_frontends=%d", len(inv.OpenResponsesFrontends))
	}
	if inv.OpenResponsesFrontends[0].InstanceID != "or-diag" {
		t.Fatalf("inventory row instance=%q", inv.OpenResponsesFrontends[0].InstanceID)
	}
	raw, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-") {
		t.Fatalf("inventory json leaked secret-like value: %s", raw)
	}
	var shape struct {
		InstanceDiagnostics []struct {
			FactoryKind string `json:"factory_kind"`
		} `json:"instance_diagnostics"`
		OpenResponsesFrontends []struct {
			FactoryKind string `json:"factory_kind"`
		} `json:"openresponses_frontends"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	if len(shape.InstanceDiagnostics) == 0 {
		t.Fatal("generic instance_diagnostics field must contain catalog/profile diagnostics")
	}
	for _, row := range shape.OpenResponsesFrontends {
		if row.FactoryKind != "openresponses" {
			t.Fatalf("legacy openresponses_frontends leaked non-OpenResponses row: %+v", shape.OpenResponsesFrontends)
		}
	}
}

func TestOpenResponsesFrontendDiagnostics_unknownFieldFailsStructuralValidation(t *testing.T) {
	t.Parallel()
	path := writeOpenResponsesFrontendConfig(t, true, "        sniffing: enabled\n")
	_, err := runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err == nil {
		t.Fatal("expected structural validation failure for unknown openresponses frontend field")
	}
	if !strings.Contains(err.Error(), "or-diag") || !strings.Contains(err.Error(), "sniffing") {
		t.Fatalf("structural error must name instance + unknown field: %v", err)
	}
}

func TestProviderProfile_ReloadPublishesExpandedFamilyRow(t *testing.T) {
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	row := func(enabled bool) string {
		return fmt.Sprintf(`    - id: profile-reload
      kind: provider-profile
      enabled: %v
      config:
        profile: groq
`, enabled)
	}
	initial := strings.Replace(string(base), "  backends:\n", "  backends:\n"+row(false), 1)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath: cfgPath, Mandatory: aipsdk.StandardDistributionRequirements(), LogWriter: io.Discard, HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	next := strings.Replace(string(base), "  backends:\n", "  backends:\n"+row(true), 1)
	tmp := cfgPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}
	// The fixed source uses the repository's atomic sibling-temp rename contract;
	// on supported platforms this replaces the destination without exposing a
	// partially written YAML document to the reload reader.
	if err := replaceTestFile(tmp, cfgPath); err != nil {
		t.Fatal(err)
	}
	result := host.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI, SafeActor: "profile-reload-test"})
	if result.Category != sdkreload.ResultPublished {
		t.Fatalf("reload category=%q reason=%q", result.Category, result.ReasonCategory)
	}
	active := runtimebundle.HostManager(host).Active()
	provider, ok := active.RequestPlane().(runtimehost.ExecutorProvider)
	if !ok || provider == nil {
		t.Fatal("active generation missing executor provider")
	}
	executor, ok := provider.ExecutorView().(*coreruntime.Executor)
	if !ok || executor == nil {
		t.Fatal("active generation missing core executor")
	}
	if _, ok := executor.Backends["profile-reload"]; !ok {
		t.Fatalf("reloaded generation missing expanded provider-profile backend: %v", executor.Backends)
	}
}

func TestProviderProfile_CompileAndDiagnosticsEndToEnd(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "mock-key-value")
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	row := `    - id: profile-end-to-end
      kind: provider-profile
      enabled: true
      config:
        profile: groq
`
	text := strings.Replace(string(base), "  backends:\n", "  backends:\n"+row, 1)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	reqs := aipsdk.StandardDistributionRequirements()

	if _, err := runtimebundle.ValidateStructural(ctx, runtimebundle.ValidateStructuralInput{ConfigPath: cfgPath, Mandatory: reqs}); err != nil {
		t.Fatalf("structural validation failed for provider profile config: %v", err)
	}

	rawCfg, err := config.LoadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	snap, err := runtimebundle.InventorySnapshotForOperator(ctx, rawCfg, reg, config.RegistrationsFromConfig(rawCfg))
	if err != nil {
		t.Fatalf("operator snapshot failed: %v", err)
	}
	if len(snap.CompatibleBackends) == 0 {
		t.Fatal("expected expanded provider profile in compatible_backends snapshot")
	}

	foundProfile := false
	for _, b := range snap.CompatibleBackends {
		if b.InstanceID == "profile-end-to-end" || b.Profile == "groq" {
			foundProfile = true
			break
		}
	}
	if !foundProfile {
		t.Fatalf("provider profile instance not found in snapshot: %+v", snap.CompatibleBackends)
	}
}
