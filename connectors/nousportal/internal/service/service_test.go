package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func createTestJWTWithScope(scope string, exp time.Time) string {
	headerJSON := []byte(`{"alg":"none","typ":"JWT"}`)
	claims := map[string]any{
		"sub":   "user-test",
		"scope": scope,
		"exp":   exp.Unix(),
	}
	claimsJSON, _ := json.Marshal(claims)
	b64Header := base64.RawURLEncoding.EncodeToString(headerJSON)
	b64Claims := base64.RawURLEncoding.EncodeToString(claimsJSON)
	return b64Header + "." + b64Claims + ".signature"
}

func TestValidateInvokeJWT(t *testing.T) {
	t.Run("ValidJWTWithInvokeScope", func(t *testing.T) {
		exp := time.Now().Add(1 * time.Hour).Truncate(time.Second)
		jwt := createTestJWTWithScope("inference:invoke other:scope", exp)
		gotExp, isJWT, err := ValidateInvokeJWT(jwt, "")
		if err != nil {
			t.Fatalf("expected valid JWT, got error: %v", err)
		}
		if !isJWT {
			t.Fatalf("expected isJWT true")
		}
		if !gotExp.Equal(exp) {
			t.Fatalf("expected exp %v, got %v", exp, gotExp)
		}
	})

	t.Run("ValidJWTWithResponseScope", func(t *testing.T) {
		exp := time.Now().Add(1 * time.Hour).Truncate(time.Second)
		jwt := createTestJWTWithScope("other:scope", exp)
		_, isJWT, err := ValidateInvokeJWT(jwt, "inference:invoke")
		if err != nil {
			t.Fatalf("expected valid JWT with response scope, got error: %v", err)
		}
		if !isJWT {
			t.Fatalf("expected isJWT true")
		}
	})

	t.Run("JWTMissingInvokeScopeRejected", func(t *testing.T) {
		exp := time.Now().Add(1 * time.Hour)
		jwt := createTestJWTWithScope("billing:manage", exp)
		_, isJWT, err := ValidateInvokeJWT(jwt, "")
		if err == nil {
			t.Fatalf("expected error for missing scope, got nil")
		}
		if !isJWT {
			t.Fatalf("expected isJWT true")
		}
	})

	t.Run("OpaqueSessionKeyAcceptedAsNonJWT", func(t *testing.T) {
		opaque := "opaque-key-not-three-parts"
		_, isJWT, err := ValidateInvokeJWT(opaque, "")
		if err != nil {
			t.Fatalf("expected no error for opaque token, got: %v", err)
		}
		if isJWT {
			t.Fatalf("expected isJWT false for opaque token")
		}
	})
}

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
		if cfg.GetPortalURL() != DefaultPortalURL {
			t.Fatalf("expected default portal URL %q, got %q", DefaultPortalURL, cfg.GetPortalURL())
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

func TestNousOAuthRefresher(t *testing.T) {
	portalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != DefaultUserAgent {
			t.Errorf("expected User-Agent %q, got %q", DefaultUserAgent, r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "mock-access-token",
			"refresh_token": "mock-refresh-token",
			"expires_in":    7200,
		})
	}))
	t.Cleanup(portalSrv.Close)

	refresher := &NousOAuthRefresher{
		PortalURL:  portalSrv.URL,
		ClientID:   "test-client",
		HTTPClient: portalSrv.Client(),
	}

	acc, ref, exp, err := refresher.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
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
