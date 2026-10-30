package sapaicore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/sapaicore/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func sapValidYAML(srvURL string) string {
	return fmt.Sprintf("resource_group: rg\ninference_contract: openai-chat\ndeployment_id: dep-1\napi_origin: %s\noauth_origin: %s\n", srvURL, srvURL)
}

func sapSecrets(srvURL string) backendplugin.SecretBundle {
	return backendplugin.SecretBundle{
		Values: map[string][]byte{"service_key": validServiceKeyJSON(srvURL, srvURL)},
	}
}

// TestCredentialContract_RejectsSecretAliases proves every secret-bearing
// YAML spelling fails closed — not just the historically known singular
// fields. List/plural, token and authorization aliases must never be
// silently ignored.
func TestCredentialContract_RejectsSecretAliases(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	for _, raw := range []string{
		"api_keys: [hunter2]\n",
		"api_tokens: hunter2\n",
		"authorization: Bearer hunter2\n",
		"password: hunter2\n",
		"bearer_token: hunter2\n",
		"servicekey: hunter2\n",
	} {
		_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			ConfigYAML:  []byte("resource_group: rg\ninference_contract: openai-chat\ndeployment_id: dep-1\n" + raw),
			Secrets:     sapSecrets("https://api.ai.example.com"),
		})
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

// TestCredentialContract_SingleServiceKeyNoPooling proves the workload
// classification: one admitted service_key, one OAuth token identity, no
// rotation across calls. Two sequential executes share a single cached
// token fetch; both inference calls carry it.
func TestCredentialContract_SingleServiceKeyNoPooling(t *testing.T) {
	t.Parallel()
	var tokenHits atomic.Int64
	var authHeaders [2]string
	var authIdx atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth/token"):
			tokenHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_token": "single-workload-token", "token_type": "bearer", "expires_in": 3600}`))
		case strings.HasSuffix(r.URL.Path, "/v2/inference/deployments/dep-1/chat/completions"):
			if i := int(authIdx.Add(1)) - 1; i < len(authHeaders) {
				authHeaders[i] = r.Header.Get("Authorization")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	svc := service.NewProduction()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "cred-contract",
		ConfigYAML:  []byte(sapValidYAML(srv.URL)),
		Secrets:     sapSecrets(srv.URL),
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		stream := newTestExecuteStream(context.Background(), "sapaicore/dep-1", aipapi.OperationOpenAIChatCompletions, "hi", true)
		if err := inst.Execute(stream); err != nil {
			t.Fatal(err)
		}
	}
	if got := tokenHits.Load(); got != 1 {
		t.Fatalf("oauth token fetches=%d, want exactly 1 (single identity, no rotation)", got)
	}
	for i, h := range authHeaders {
		if h != "Bearer single-workload-token" {
			t.Fatalf("call %d Authorization=%q", i, h)
		}
	}
}

// TestCredentialContract_ManifestWorkloadClassification pins the packaged
// manifest classification for the SAP AI Core export: static_nonrotatable
// over the host service-key namespace with no_rotation selection. The
// runtime descriptor must agree; the OAuth token lifecycle stays behind the
// single admitted service key.
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
		cc.EnvRoot != "SAP_AI_CORE_SERVICE_KEY" || cc.Selection != "no_rotation" ||
		cc.QuotaDomain != "sap-ai-core" {
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
