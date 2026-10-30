package vertex_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/vertex/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// vertexAuthCapture serves a minimal :generateContent endpoint and records
// the presented Authorization header.
func vertexAuthCapture(t *testing.T, auth *atomic.Pointer[string]) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		auth.Store(&h)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"candidates": [{"content": {"parts": [{"text": "hi"}], "role": "model"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestCredentialContract_RejectsSecretAliases proves every secret-bearing
// YAML spelling fails closed — not just the historically known singular
// fields. List/plural, token and authorization aliases must never be
// silently ignored.
func TestCredentialContract_RejectsSecretAliases(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"api_keys: [hunter2]\n",
		"api_tokens: hunter2\n",
		"authorization: Bearer hunter2\n",
		"password: hunter2\n",
		"tokens: [hunter2]\n",
		"secrets: {a: hunter2}\n",
	} {
		_, err := service.ParseConfigYAML([]byte("project: p\nlocation: us-central1\n" + raw))
		if err == nil {
			t.Fatalf("expected rejection of secret YAML alias (yaml=%q)", raw)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("rejection leaks secret value: %v", err)
		}
		if !strings.Contains(err.Error(), "literal secrets in configuration YAML are forbidden") {
			t.Fatalf("rejection must use the closed secret-free error: %v", err)
		}
	}
}

// TestCredentialContract_StrayAPIKeySecretIgnored proves the workload
// classification: the connector consumes only the service_account_json
// host secret (or the provider-managed ADC chain). A stray api_key secret
// is never pooled as a bearer credential — the injected provider token is
// the sole wire identity.
func TestCredentialContract_StrayAPIKeySecretIgnored(t *testing.T) {
	t.Parallel()
	var auth atomic.Pointer[string]
	srv := vertexAuthCapture(t, &auth)
	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("workload-token")))
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "cred-contract",
		ConfigYAML:  []byte("project: p\nlocation: us-central1\napi_origin: " + srv.URL + "\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("stray-key")}},
	})
	if err != nil {
		t.Fatalf("stray secrets must not break workload admission: %v", err)
	}
	stream := newTestExecuteStream(context.Background(), "vertex/model", aipapi.OperationGeminiGenerateContent, "hi", true)
	if err := inst.Execute(stream); err != nil {
		t.Fatal(err)
	}
	if got := auth.Load(); got == nil || *got != "Bearer workload-token" {
		t.Fatalf("wire Authorization=%v, want the provider token only", got)
	}
}

// TestCredentialContract_ManifestWorkloadClassification pins the packaged
// manifest classification for the Vertex export: static_nonrotatable with
// no_rotation selection over the host namespace. The runtime descriptor
// must agree; Google workload identity (service-account secret or ADC)
// stays provider-managed behind this classification.
func TestCredentialContract_ManifestWorkloadClassification(t *testing.T) {
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
			Kind               string `json:"kind"`
			CredentialMode     string `json:"credential_mode"`
			AccessScope        string `json:"access_scope"`
			CredentialContract *struct {
				Version     int    `json:"version"`
				Class       string `json:"class"`
				Source      string `json:"source"`
				EnvRoot     string `json:"env_root"`
				Slot        string `json:"slot"`
				Selection   string `json:"selection"`
				QuotaDomain string `json:"quota_domain"`
			} `json:"credential_contract"`
		} `json:"exports"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Exports) != 1 || doc.Exports[0].Kind != service.FactoryKind {
		t.Fatalf("exports=%+v", doc.Exports)
	}
	ex := doc.Exports[0]
	if ex.CredentialMode != "static" {
		t.Fatalf("credential_mode=%q, want static", ex.CredentialMode)
	}
	if ex.CredentialContract == nil {
		t.Fatal("missing credential_contract for the static export")
	}
	cc := ex.CredentialContract
	if cc.Version != 2 || cc.Class != "static_nonrotatable" || cc.Source != "host_env" ||
		cc.EnvRoot != "VERTEX_API_KEY" || cc.Selection != "no_rotation" ||
		cc.QuotaDomain != "vertex-ai" {
		t.Fatalf("credential_contract=%+v", cc)
	}
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Factories) != 1 || d.Factories[0].CredentialMode != backendplugin.CredentialModeStatic {
		t.Fatalf("descriptor disagrees with manifest posture: %+v", d.Factories)
	}
}
