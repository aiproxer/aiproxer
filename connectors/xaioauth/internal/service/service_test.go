package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestConfigValidation(t *testing.T) {
	t.Run("SecretsInYAMLRejected", func(t *testing.T) {
		badYAML := `
api_key: secret-123
oauth_client_id: client-1
`
		_, err := ParseConfigYAML([]byte(badYAML))
		if err == nil {
			t.Fatalf("expected secret in YAML rejection, got nil")
		}
	})

	t.Run("ValidConfigDefaults", func(t *testing.T) {
		cfgYAML := `
oauth_client_id: client-1
`
		cfg, err := ParseConfigYAML([]byte(cfgYAML))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.GetIssuerURL() != DefaultIssuerURL {
			t.Fatalf("expected default issuer URL %q, got %q", DefaultIssuerURL, cfg.GetIssuerURL())
		}
		if cfg.GetInferenceURL() != DefaultInferenceURL {
			t.Fatalf("expected default inference URL %q, got %q", DefaultInferenceURL, cfg.GetInferenceURL())
		}
		if cfg.DefaultModel() != DefaultModel {
			t.Fatalf("expected default model %q, got %q", DefaultModel, cfg.DefaultModel())
		}
	})
}

func TestDescribe(t *testing.T) {
	svc := New()
	desc, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.PluginID != PluginID {
		t.Fatalf("expected PluginID %q, got %q", PluginID, desc.PluginID)
	}
	if len(desc.Factories) != 1 || desc.Factories[0].Kind != FactoryKind {
		t.Fatalf("expected factory %q, got %+v", FactoryKind, desc.Factories)
	}
}

func TestStaticTokenProvider(t *testing.T) {
	tp := NewStaticTokenProvider("my-static-token")
	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "my-static-token" {
		t.Fatalf("expected my-static-token, got %q", tok)
	}
	refreshed, err := tp.ForceRefresh(context.Background())
	if err != nil {
		t.Fatalf("ForceRefresh: %v", err)
	}
	if refreshed != "my-static-token" {
		t.Fatalf("expected my-static-token, got %q", refreshed)
	}
}

func TestOIDCDiscoveryAndRefresh(t *testing.T) {
	var tokenEndpointHit, discoveryHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != DefaultUserAgent {
			t.Errorf("expected User-Agent %q, got %q", DefaultUserAgent, r.Header.Get("User-Agent"))
		}

		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			discoveryHit = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":         "http://" + r.Host,
				"token_endpoint": "http://" + r.Host + "/oauth2/token",
			})
		case "/oauth2/token":
			tokenEndpointHit = true
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "refresh_token" {
				http.Error(w, "invalid grant_type", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "mock-access-token",
				"refresh_token": "mock-refresh-token",
				"expires_in":    7200,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	refresher := &XAIOAuthRefresher{
		IssuerURL:  srv.URL,
		ClientID:   "test-client",
		HTTPClient: srv.Client(),
	}

	acc, ref, exp, err := refresher.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !discoveryHit {
		t.Fatalf("expected discovery endpoint to be hit")
	}
	if !tokenEndpointHit {
		t.Fatalf("expected token endpoint to be hit")
	}
	if acc != "mock-access-token" || ref != "mock-refresh-token" {
		t.Fatalf("unexpected tokens: acc=%q ref=%q", acc, ref)
	}
	if exp.Before(time.Now().Add(1 * time.Hour)) {
		t.Fatalf("unexpected expiry: %v", exp)
	}
}

func TestConfigureWithStaticToken(t *testing.T) {
	svc := NewProduction()
	cfgYAML := []byte("inference_url: https://custom.inference.com/v1\n")
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{"api_key": []byte("static-pat-123")},
		},
	})
	if err != nil {
		t.Fatalf("Configure with static api_key failed: %v", err)
	}
	if inst == nil {
		t.Fatalf("expected non-nil instance")
	}
}
