package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCompatibleCLI_inventoryRoutesInspectBuiltInOrigin(t *testing.T) {
	t.Parallel()
	cfgPath := writeCompatibleExampleConfig(t)

	t.Run("inventory", func(t *testing.T) {
		t.Parallel()
		var out, errb bytes.Buffer
		code := RunCommand(context.Background(), CommandOptions{
			Name:       CommandInventory,
			ConfigPath: cfgPath,
			Output:     &out,
			ErrorOut:   &errb,
		})
		if code != 0 {
			t.Fatalf("inventory exit %d stderr=%s", code, errb.String())
		}
		assertCompatibleProjection(t, out.Bytes())
	})

	t.Run("routes", func(t *testing.T) {
		t.Parallel()
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
		assertCompatibleProjection(t, out.Bytes())
	})

	t.Run("inspect", func(t *testing.T) {
		t.Parallel()
		var out, errb bytes.Buffer
		code := RunCommand(context.Background(), CommandOptions{
			Name:       CommandInspect,
			ConfigPath: cfgPath,
			Output:     &out,
			ErrorOut:   &errb,
		})
		if code != 0 {
			t.Fatalf("inspect exit %d stderr=%s", code, errb.String())
		}
		var rep struct {
			Entries            []json.RawMessage `json:"entries"`
			CompatibleBackends []struct {
				Origin     string `json:"origin"`
				InstanceID string `json:"instance_id"`
			} `json:"compatible_backends"`
		}
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatal(err)
		}
		if len(rep.CompatibleBackends) != 1 || rep.CompatibleBackends[0].Origin != "built_in_compatible" {
			t.Fatalf("inspect compatible_backends=%+v", rep.CompatibleBackends)
		}
	})
}

func TestCompatibleCLI_checkConfigNoNetwork(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "must not be called", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)

	cfgPath := writeCompatibleExampleConfig(t)
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "https://127.0.0.1:1/v1", srv.URL+"/v1", 1)
	if err := os.WriteFile(cfgPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

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
	if hits.Load() != 0 {
		t.Fatalf("provider requests during check-config = %d", hits.Load())
	}
}

func TestCompatibleCLI_multiInstanceLiveInventoryProjection(t *testing.T) {
	t.Parallel()
	cfgPath := writeCompatibleMultiExampleConfig(t)
	var out, errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandInventory,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("inventory exit %d stderr=%s", code, errb.String())
	}
	var snap struct {
		CompatibleBackends []struct {
			InstanceID        string `json:"instance_id"`
			Origin            string `json:"origin"`
			Tokenizer         string `json:"tokenizer"`
			ConcurrencyPolicy string `json:"concurrency_policy"`
			InventoryHealth   *struct {
				ModelCount int `json:"model_count"`
			} `json:"inventory_health"`
		} `json:"compatible_backends"`
	}
	if err := json.Unmarshal(out.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.CompatibleBackends) != 2 {
		t.Fatalf("compatible_backends=%+v", snap.CompatibleBackends)
	}
	seen := map[string]bool{}
	for _, row := range snap.CompatibleBackends {
		seen[row.InstanceID] = true
		if row.Origin != "built_in_compatible" {
			t.Fatalf("origin=%q", row.Origin)
		}
		if row.InventoryHealth == nil || row.InventoryHealth.ModelCount != 1 {
			t.Fatalf("inventory_health=%+v instance=%q", row.InventoryHealth, row.InstanceID)
		}
	}
	if !seen["compat-a"] || !seen["compat-b"] {
		t.Fatalf("missing instances: %+v", snap.CompatibleBackends)
	}
}

func TestCompatibleExampleConfigs_checkConfig(t *testing.T) {
	t.Parallel()
	examples := []string{
		"custom-openai-legacy-compatible.yaml",
		"custom-openai-responses-compatible.yaml",
		"custom-anthropic-compatible.yaml",
		"custom-compatible-no-auth.yaml",
	}
	for _, name := range examples {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join("..", "..", "config", "examples", name)
			var out, errb bytes.Buffer
			code := RunCommand(context.Background(), CommandOptions{
				Name:       CommandCheckConfig,
				ConfigPath: path,
				Output:     &out,
				ErrorOut:   &errb,
			})
			if code != 0 {
				t.Fatalf("check-config exit %d stderr=%s", code, errb.String())
			}
		})
	}
}

func assertCompatibleProjection(t *testing.T, raw []byte) {
	t.Helper()
	var snap struct {
		CompatibleBackends []struct {
			Origin            string `json:"origin"`
			InstanceID        string `json:"instance_id"`
			Prefix            string `json:"prefix"`
			AuthConfigured    bool   `json:"auth_configured"`
			ConcurrencyPolicy string `json:"concurrency_policy"`
			InventoryHealth   *struct {
				ModelCount int `json:"model_count"`
			} `json:"inventory_health"`
		} `json:"compatible_backends"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.CompatibleBackends) != 1 {
		t.Fatalf("compatible_backends=%+v", snap.CompatibleBackends)
	}
	row := snap.CompatibleBackends[0]
	if row.Origin != "built_in_compatible" || row.InstanceID != "compat-example" || row.Prefix != "compat-example" {
		t.Fatalf("row=%+v", row)
	}
	if !row.AuthConfigured || row.ConcurrencyPolicy != "limit:2" {
		t.Fatalf("auth/concurrency=%+v", row)
	}
	if row.InventoryHealth == nil || row.InventoryHealth.ModelCount != 1 {
		t.Fatalf("inventory_health=%+v", row.InventoryHealth)
	}
}

func writeCompatibleExampleConfig(t *testing.T) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rows := `    - id: compat-example
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: compat-example
        base_url: https://127.0.0.1:1/v1
        api_key_env_var_root: COMPAT_EXAMPLE_KEY
        max_concurrent_requests: 2
        models:
          source: inline
          items:
            - canonical_id: compat-example/model-a
              native_id: model-a
`
	text := strings.Replace(string(base), "  features:\n", rows+"  features:\n", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeCompatibleMultiExampleConfig(t *testing.T) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rows := `    - id: compat-a
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: compat-a
        base_url: https://127.0.0.1:1/v1
        api_key_env_var_root: COMPAT_A_KEY
        tokenizer: cl100k_base
        max_concurrent_requests: 1
        models:
          source: inline
          items:
            - canonical_id: compat-a/model-a
              native_id: model-a
    - id: compat-b
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: compat-b
        base_url: https://127.0.0.1:2/v1
        api_key_env_var_root: COMPAT_B_KEY
        tokenizer: o200k_base
        max_concurrent_requests: 2
        models:
          source: inline
          items:
            - canonical_id: compat-b/model-b
              native_id: model-b
`
	text := strings.Replace(string(base), "  features:\n", rows+"  features:\n", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
