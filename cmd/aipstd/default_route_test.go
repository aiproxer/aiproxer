package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckConfig_connectorExampleIsValid proves the shipped connector example
// passes the operator check. The example names the route it serves, so the
// check has nothing to refuse.
func TestCheckConfig_connectorExampleIsValid(t *testing.T) {
	t.Parallel()
	cfgPath := filepath.Join("..", "..", "config", "examples", "claude-agent-acp.yaml")
	var out, errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandCheckConfig,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("check-config exit %d stderr=%s", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("configuration is valid")) {
		t.Fatalf("stdout=%q", out.String())
	}
}

// TestCheckConfig_connectorBackendWithoutDefaultRouteWarns proves a
// configuration whose enabled backend has no canonical default model is
// accepted, with the advisory the operator needs. The configuration serves the
// clients that route explicitly, so refusing it would report a working
// configuration as invalid.
func TestCheckConfig_connectorBackendWithoutDefaultRouteWarns(t *testing.T) {
	t.Parallel()
	cfgPath := writeCompatibleBackendConfig(t)
	var out, errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandCheckConfig,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("check-config exit %d stderr=%s", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("configuration is valid")) {
		t.Fatalf("stdout=%q", out.String())
	}
	if !strings.Contains(errb.String(), "no default route") {
		t.Fatalf("stderr must carry the advisory: %q", errb.String())
	}
	if !strings.Contains(errb.String(), "routing.default_route") {
		t.Fatalf("stderr must name the remedy: %q", errb.String())
	}
}

// writeCompatibleBackendConfig writes a configuration whose only enabled backend
// is a compatible-mode instance: its kind has no canonical default model, so
// the configuration resolves to no default route.
func writeCompatibleBackendConfig(t *testing.T) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rows := `    - id: compat-check-config
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: compat-check-config
        base_url: https://example.test/v1
        api_key_env_var_root: COMPAT_CHECK_CONFIG_KEY
        tokenizer: o200k_base
        max_concurrent_requests: 2
        models:
          source: inline
          items:
            - canonical_id: compat-check-config/model-a
              native_id: model-a
`
	text := strings.Replace(string(base), "  features:\n", rows+"  features:\n", 1)
	const configured = "  default_route: \"openai-responses:gpt-4o-mini\"\n"
	if !strings.Contains(text, configured) {
		t.Fatal("config/config.yaml must carry the documented default route")
	}
	text = strings.Replace(text, configured, "", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRoutesCommand_connectorExampleNamesItsRoute proves the operator read
// model publishes the route the example names. A connector backend discovers
// its models at runtime, so there is no default model to synthesize: the
// published route is the one the configuration names, never a placeholder.
func TestRoutesCommand_connectorExampleNamesItsRoute(t *testing.T) {
	t.Parallel()
	cfgPath := filepath.Join("..", "..", "config", "examples", "claude-agent-acp.yaml")
	var out, errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandRoutes,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("routes exit %d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "claude-acp:anthropic/claude-sonnet-5.5") {
		t.Fatalf("stdout=%q", out.String())
	}
	if strings.Contains(out.String(), ":model\"") {
		t.Fatalf("the placeholder model must never be published: %q", out.String())
	}
}

// TestRoutesCommand_publishesTheDiscoveredDefaultRoute proves the operator
// read model derives the default route from the inventory the backend instance
// published. A deployment that discovers its models serves clients that omit
// a route without the operator copying a model id into the configuration.
func TestRoutesCommand_publishesTheDiscoveredDefaultRoute(t *testing.T) {
	t.Parallel()
	cfgPath := writeCompatibleBackendConfig(t)
	var out, errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandRoutes,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("routes exit %d stderr=%s", code, errb.String())
	}
	var snap struct {
		EffectiveDefaultRoute       string `json:"effective_default_route"`
		EffectiveDefaultRouteModel  string `json:"effective_default_route_model"`
		EffectiveDefaultRouteSource string `json:"effective_default_route_source"`
		EffectiveDefaultRouteNote   string `json:"effective_default_route_note"`
	}
	if err := json.Unmarshal(out.Bytes(), &snap); err != nil {
		t.Fatalf("decode routes: %v (%s)", err, out.String())
	}
	want := "compat-check-config:compat-check-config/model-a"
	if snap.EffectiveDefaultRoute != want {
		t.Fatalf("effective default route = %q want %q", snap.EffectiveDefaultRoute, want)
	}
	if snap.EffectiveDefaultRouteModel != "compat-check-config/model-a" {
		t.Fatalf("effective default route model = %q", snap.EffectiveDefaultRouteModel)
	}
	if snap.EffectiveDefaultRouteSource != "discovered_inventory" {
		t.Fatalf("effective default route source = %q", snap.EffectiveDefaultRouteSource)
	}
	if snap.EffectiveDefaultRouteNote != "" {
		t.Fatalf("note = %q want none", snap.EffectiveDefaultRouteNote)
	}
}
