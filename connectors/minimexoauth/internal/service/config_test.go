package service_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/minimexoauth/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestConfigValidation_SecretsInYAMLRejected(t *testing.T) {
	for _, forbidden := range []string{"api_key", "access_token", "refresh_token", "token"} {
		cfgYAML := []byte(forbidden + ": leaked-secret\n")
		_, err := service.ParseAndValidateConfig(cfgYAML, backendplugin.SecretBundle{})
		if err == nil {
			t.Fatalf("expected error for forbidden secret %q in YAML, got nil", forbidden)
		}
		if !strings.Contains(err.Error(), "literal secret") {
			t.Fatalf("expected literal secret error for %q, got: %v", forbidden, err)
		}
	}
}

func TestConfigValidation_MinimaxAPIKeyRejected(t *testing.T) {
	cfgYAML := []byte("oauth_token_file: /path/to/tokens.json\noauth_client_id: test-client\n")
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{
			"MINIMAX_API_KEY": []byte("sk-cp-12345"),
		},
	}
	_, err := service.ParseAndValidateConfig(cfgYAML, secrets)
	if err == nil {
		t.Fatalf("expected error when MINIMAX_API_KEY is supplied, got nil")
	}
	if !strings.Contains(err.Error(), "MINIMAX_API_KEY is not supported") {
		t.Fatalf("expected MINIMAX_API_KEY rejection error, got: %v", err)
	}
}

func TestConfigValidation_DefaultsGlobal(t *testing.T) {
	cfgYAML := []byte("oauth_token_file: /path/to/tokens.json\noauth_client_id: my-client\n")
	cfg, err := service.ParseAndValidateConfig(cfgYAML, backendplugin.SecretBundle{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PortalBaseURL != service.DefaultPortalBaseURL {
		t.Fatalf("expected portal base %q, got %q", service.DefaultPortalBaseURL, cfg.PortalBaseURL)
	}
	if cfg.InferenceBaseURL != service.DefaultInferenceBaseURL {
		t.Fatalf("expected inference base %q, got %q", service.DefaultInferenceBaseURL, cfg.InferenceBaseURL)
	}
	if cfg.Region != "global" {
		t.Fatalf("expected region global, got %q", cfg.Region)
	}
	if cfg.OAuthScope != service.DefaultScope {
		t.Fatalf("expected default scope %q, got %q", service.DefaultScope, cfg.OAuthScope)
	}
	if cfg.HTTPTimeout != service.DefaultHTTPTimeout {
		t.Fatalf("expected default timeout %v, got %v", service.DefaultHTTPTimeout, cfg.HTTPTimeout)
	}
}

func TestConfigValidation_RegionResolution(t *testing.T) {
	tests := []struct {
		name                  string
		region                string
		expectedPortalBase    string
		expectedInferenceBase string
		expectedRegion        string
	}{
		{
			name:                  "cn alias",
			region:                "cn",
			expectedPortalBase:    service.CNPortalBaseURL,
			expectedInferenceBase: service.CNInferenceBaseURL,
			expectedRegion:        "cn",
		},
		{
			name:                  "china alias",
			region:                "china",
			expectedPortalBase:    service.CNPortalBaseURL,
			expectedInferenceBase: service.CNInferenceBaseURL,
			expectedRegion:        "china",
		},
		{
			name:                  "minimax-cn alias",
			region:                "minimax-cn",
			expectedPortalBase:    service.CNPortalBaseURL,
			expectedInferenceBase: service.CNInferenceBaseURL,
			expectedRegion:        "minimax-cn",
		},
		{
			name:                  "global default",
			region:                "global",
			expectedPortalBase:    service.DefaultPortalBaseURL,
			expectedInferenceBase: service.DefaultInferenceBaseURL,
			expectedRegion:        "global",
		},
		{
			name:                  "empty defaults to global",
			region:                "",
			expectedPortalBase:    service.DefaultPortalBaseURL,
			expectedInferenceBase: service.DefaultInferenceBaseURL,
			expectedRegion:        "global",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfgYAML := []byte("region: " + tc.region + "\noauth_token_file: /path/to/tokens.json\noauth_client_id: my-client\n")
			cfg, err := service.ParseAndValidateConfig(cfgYAML, backendplugin.SecretBundle{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.PortalBaseURL != tc.expectedPortalBase {
				t.Fatalf("expected portal base %q, got %q", tc.expectedPortalBase, cfg.PortalBaseURL)
			}
			if cfg.InferenceBaseURL != tc.expectedInferenceBase {
				t.Fatalf("expected inference base %q, got %q", tc.expectedInferenceBase, cfg.InferenceBaseURL)
			}
			if cfg.Region != tc.expectedRegion {
				t.Fatalf("expected region %q, got %q", tc.expectedRegion, cfg.Region)
			}
		})
	}
}

func TestConfigValidation_BaseURLOverride(t *testing.T) {
	cfgYAML := []byte("base_url: https://custom-proxy.internal/anthropic\noauth_token_file: /path/to/tokens.json\noauth_client_id: my-client\n")
	cfg, err := service.ParseAndValidateConfig(cfgYAML, backendplugin.SecretBundle{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.InferenceBaseURL != "https://custom-proxy.internal/anthropic" {
		t.Fatalf("expected overridden inference base URL, got %q", cfg.InferenceBaseURL)
	}
}

func TestConfigValidation_MissingClientID(t *testing.T) {
	cfgYAML := []byte("oauth_token_file: /path/to/tokens.json\n")
	_, err := service.ParseAndValidateConfig(cfgYAML, backendplugin.SecretBundle{})
	if err == nil {
		t.Fatalf("expected error when oauth_client_id is missing, got nil")
	}
	if !strings.Contains(err.Error(), "oauth_client_id is required") {
		t.Fatalf("expected oauth_client_id required error, got: %v", err)
	}
}

func TestConfigValidation_StaticTokenInSecret(t *testing.T) {
	cfgYAML := []byte("inference_url: https://api.minimax.io/anthropic\n")
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{
			"access_token": []byte("static-bearer-tok"),
		},
	}
	cfg, err := service.ParseAndValidateConfig(cfgYAML, secrets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DirectToken != "static-bearer-tok" {
		t.Fatalf("expected direct token 'static-bearer-tok', got %q", cfg.DirectToken)
	}
}
