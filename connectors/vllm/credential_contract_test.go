package vllm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/vllm/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// TestCredentialContract_RejectsYAMLAPIKey proves the no-auth local export
// accepts no static YAML keys: any literal key field fails closed with a
// secret-free error.
func TestCredentialContract_RejectsYAMLAPIKey(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"api_key: hunter2\n",
		"api_keys: [hunter2]\n",
		"API_KEY: hunter2\n",
	} {
		_, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind:   service.FactoryKind,
			InstanceID:    "cred-contract",
			ConfigYAML:    []byte(raw),
			Negotiation:   backendplugin.Negotiation{Compatible: true},
			RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
		})
		if err == nil {
			t.Fatalf("expected literal YAML key to be rejected (yaml=%q)", raw)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("rejection leaks secret value: %v", err)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "api_key") {
			t.Fatalf("rejection must name the field path: %v", err)
		}
	}
}

// TestCredentialContract_NoAuthNeedsNoSecrets proves the local no-auth
// export configures and lists models without any credentials.
func TestCredentialContract_NoAuthNeedsNoSecrets(t *testing.T) {
	t.Parallel()
	var bearer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	t.Cleanup(srv.Close)
	inst, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind:   service.FactoryKind,
		InstanceID:    "local",
		ConfigYAML:    []byte("base_url: " + srv.URL + "\n"),
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatalf("no-auth export must configure without secrets: %v", err)
	}
	if _, err := inst.ListModels(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if bearer != "Bearer vllm" {
		t.Fatalf("no-auth wire bearer=%q, want the constant dummy", bearer)
	}
}

// TestCredentialContract_HostSecretsNeverBecomeKeys proves Configure
// secrets can never smuggle a static key into the no-auth transport: even
// when secrets are supplied, the wire carries only the constant dummy
// bearer and the secret value is never presented.
func TestCredentialContract_HostSecretsNeverBecomeKeys(t *testing.T) {
	t.Parallel()
	var bearer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	t.Cleanup(srv.Close)
	inst, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind:   service.FactoryKind,
		InstanceID:    "local",
		ConfigYAML:    []byte("base_url: " + srv.URL + "\n"),
		Secrets:       backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("smuggled-secret")}},
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatalf("no-auth export must stay up with stray secrets: %v", err)
	}
	if _, err := inst.ListModels(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if bearer != "Bearer vllm" {
		t.Fatalf("wire bearer=%q, want the constant dummy only", bearer)
	}
	if strings.Contains(bearer, "smuggled-secret") {
		t.Fatalf("smuggled secret reached the wire: %q", bearer)
	}
}

// TestCredentialContract_ManifestNoAuth pins the packaged manifest posture:
// a credential-free local_only inference export with no credential contract.
// The runtime descriptor must agree.
func TestCredentialContract_ManifestNoAuth(t *testing.T) {
	t.Parallel()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(wd, "manifest", "template.backendplugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Exports []struct {
			Kind               string         `json:"kind"`
			CredentialMode     string         `json:"credential_mode"`
			AccessScope        string         `json:"access_scope"`
			CredentialContract map[string]any `json:"credential_contract"`
		} `json:"exports"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Exports) != 1 || doc.Exports[0].Kind != service.FactoryKind {
		t.Fatalf("exports=%+v", doc.Exports)
	}
	ex := doc.Exports[0]
	if ex.CredentialMode != "none" || ex.AccessScope != "local_only" {
		t.Fatalf("posture=%q/%q, want none/local_only", ex.CredentialMode, ex.AccessScope)
	}
	if len(ex.CredentialContract) != 0 {
		t.Fatalf("no-auth export must carry no credential contract: %+v", ex.CredentialContract)
	}
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Factories) != 1 || d.Factories[0].CredentialMode != backendplugin.CredentialModeNone ||
		d.Factories[0].AccessScope != backendplugin.AccessScopeLocalOnly {
		t.Fatalf("descriptor disagrees with manifest posture: %+v", d.Factories)
	}
}
