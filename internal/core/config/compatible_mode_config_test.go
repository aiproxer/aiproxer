package config_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"gopkg.in/yaml.v3"
)

func mustYAMLNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return doc
}

func TestDecodeCompatibleModeConfig_validEnvRootAndNoAuth(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want config.CompatibleModeConfig
	}{
		{
			name: "env_root_only",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
api_key_env_var_root: PROVIDER_A_API_KEY
`,
			want: config.CompatibleModeConfig{
				BackendPrefix:    "provider-a",
				BaseURL:          "https://api.example.com/v1",
				APIKeyEnvVarRoot: "PROVIDER_A_API_KEY",
			},
		},
		{
			name: "no_auth_omits_env_root",
			raw: `backend_prefix: local
base_url: http://127.0.0.1:8080/v1
`,
			want: config.CompatibleModeConfig{
				BackendPrefix: "local",
				BaseURL:       "http://127.0.0.1:8080/v1",
			},
		},
		{
			name: "optional_tokenizer_concurrency_models",
			raw: `backend_prefix: provider-b
base_url: https://api.example.com
tokenizer: cl100k_base
max_concurrent_requests: 4
models:
  source: inline
  items:
    - canonical_id: provider-b/m1
      native_id: m1
`,
			want: config.CompatibleModeConfig{
				BackendPrefix:         "provider-b",
				BaseURL:               "https://api.example.com",
				TokenizerID:           "cl100k_base",
				MaxConcurrentRequests: 4,
				Models: config.CompatibleModeModelsConfig{
					Source: "inline",
					Items: []config.CompatibleModeModelItem{
						{CanonicalID: "provider-b/m1", NativeID: "m1"},
					},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := config.DecodeCompatibleModeConfig("inst-1", "custom-openai-legacy-compatible", mustYAMLNode(t, tc.raw))
			if err != nil {
				t.Fatalf("DecodeCompatibleModeConfig: %v", err)
			}
			if got.BackendPrefix != tc.want.BackendPrefix {
				t.Fatalf("BackendPrefix=%q want %q", got.BackendPrefix, tc.want.BackendPrefix)
			}
			if got.BaseURL != tc.want.BaseURL {
				t.Fatalf("BaseURL=%q want %q", got.BaseURL, tc.want.BaseURL)
			}
			if got.APIKeyEnvVarRoot != tc.want.APIKeyEnvVarRoot {
				t.Fatalf("APIKeyEnvVarRoot=%q want %q", got.APIKeyEnvVarRoot, tc.want.APIKeyEnvVarRoot)
			}
			if got.TokenizerID != tc.want.TokenizerID {
				t.Fatalf("TokenizerID=%q want %q", got.TokenizerID, tc.want.TokenizerID)
			}
			if got.MaxConcurrentRequests != tc.want.MaxConcurrentRequests {
				t.Fatalf("MaxConcurrentRequests=%d want %d", got.MaxConcurrentRequests, tc.want.MaxConcurrentRequests)
			}
			if got.Models.Source != tc.want.Models.Source {
				t.Fatalf("Models.Source=%q want %q", got.Models.Source, tc.want.Models.Source)
			}
			if len(got.Models.Items) != len(tc.want.Models.Items) {
				t.Fatalf("Models.Items len=%d want %d", len(got.Models.Items), len(tc.want.Models.Items))
			}
			for i := range tc.want.Models.Items {
				if got.Models.Items[i] != tc.want.Models.Items[i] {
					t.Fatalf("Models.Items[%d]=%+v want %+v", i, got.Models.Items[i], tc.want.Models.Items[i])
				}
			}
		})
	}
}

func TestDecodeCompatibleModeConfig_rejectsRequiredMissing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     string
		wantSub string
	}{
		{
			name: "missing_backend_prefix",
			raw: `base_url: https://api.example.com/v1
`,
			wantSub: "backend_prefix",
		},
		{
			name: "missing_base_url",
			raw: `backend_prefix: provider-a
`,
			wantSub: "base_url",
		},
		{
			name: "blank_backend_prefix",
			raw: `backend_prefix: "  "
base_url: https://api.example.com/v1
`,
			wantSub: "backend_prefix",
		},
		{
			name: "blank_base_url",
			raw: `backend_prefix: provider-a
base_url: " "
`,
			wantSub: "base_url",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.DecodeCompatibleModeConfig("inst-req", "custom-anthropic-compatible", mustYAMLNode(t, tc.raw))
			if err == nil {
				t.Fatal("expected error")
			}
			msg := err.Error()
			if !strings.Contains(msg, `instance "inst-req"`) || !strings.Contains(msg, `factory "custom-anthropic-compatible"`) {
				t.Fatalf("want instance-scoped error, got %v", err)
			}
			if !strings.Contains(msg, tc.wantSub) {
				t.Fatalf("want substring %q in %v", tc.wantSub, err)
			}
		})
	}
}

func TestDecodeCompatibleModeConfig_rejectsUnknownFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     string
		wantKey string
	}{
		{
			name: "top_level",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
temperature: 0.2
`,
			wantKey: "temperature",
		},
		{
			name: "models",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
models:
  unexpected: value
`,
			wantKey: "models.unexpected",
		},
		{
			name: "model_item",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
models:
  items:
    - native_id: m1
      unexpected: value
`,
			wantKey: "models.items[0].unexpected",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.DecodeCompatibleModeConfig("inst-u", "custom-openai-responses-compatible", mustYAMLNode(t, tc.raw))
			if err == nil || !strings.Contains(err.Error(), tc.wantKey) {
				t.Fatalf("want unknown field %q rejection, got %v", tc.wantKey, err)
			}
			if !strings.Contains(err.Error(), `instance "inst-u"`) {
				t.Fatalf("want instance-scoped error, got %v", err)
			}
		})
	}
}

func TestDecodeCompatibleModeConfig_rejectsNegativeConcurrency(t *testing.T) {
	t.Parallel()
	raw := `backend_prefix: provider-a
base_url: https://api.example.com/v1
max_concurrent_requests: -1
`
	_, err := config.DecodeCompatibleModeConfig("inst-c", "custom-openai-legacy-compatible", mustYAMLNode(t, raw))
	if err == nil || !strings.Contains(err.Error(), "max_concurrent_requests") {
		t.Fatalf("want negative concurrency rejection, got %v", err)
	}
	if !strings.Contains(err.Error(), `instance "inst-c"`) {
		t.Fatalf("want instance-scoped error, got %v", err)
	}
}

func TestDecodeCompatibleModeConfig_rejectsMalformedEnvRoot(t *testing.T) {
	t.Parallel()
	for _, root := range []string{"lower_api_key", "OPENAI_API_KEY_2", "OPENAI_API_KEY_1", "${OPENAI_API_KEY}"} {
		raw := "backend_prefix: provider-a\nbase_url: https://api.example.com/v1\napi_key_env_var_root: " + root + "\n"
		_, err := config.DecodeCompatibleModeConfig("inst-root", "custom-openai-legacy-compatible", mustYAMLNode(t, raw))
		if err == nil {
			t.Fatalf("root %q: expected rejection", root)
		}
		if !strings.Contains(err.Error(), "api_key_env_var_root") {
			t.Fatalf("root %q: error %v must name api_key_env_var_root", root, err)
		}
	}
}

func TestUpstreamCredentialsYAMLBlockDecodes(t *testing.T) {
	t.Parallel()
	raw := []byte("upstream_credentials:\n  max_env_index: 7\n  quota_switch_retry_after: 30m\n")
	cfg, _, err := config.StrictDecode(raw)
	if err != nil {
		t.Fatalf("StrictDecode: %v", err)
	}
	if cfg.UpstreamCredentials.MaxEnvIndex != 7 {
		t.Fatalf("max_env_index = %d want 7", cfg.UpstreamCredentials.MaxEnvIndex)
	}
	if cfg.UpstreamCredentials.QuotaSwitchRetryAfter != "30m" {
		t.Fatalf("quota_switch_retry_after = %q want 30m", cfg.UpstreamCredentials.QuotaSwitchRetryAfter)
	}
}

func TestDecodeCompatibleModeConfig_rejectsForbiddenSecrets(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     string
		wantKey string
	}{
		{
			name: "api_key",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
api_key: literal-secret
`,
			wantKey: "api_key",
		},
		{
			name: "api_keys",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
api_keys:
  - literal-one
`,
			wantKey: "api_keys",
		},
		{
			name: "credentials",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
credentials:
  - id: primary
    api_key: literal-secret
`,
			wantKey: "credentials",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.DecodeCompatibleModeConfig("inst-s", "custom-openai-legacy-compatible", mustYAMLNode(t, tc.raw))
			if err == nil {
				t.Fatal("expected forbidden secret rejection")
			}
			msg := err.Error()
			if !strings.Contains(msg, `instance "inst-s"`) {
				t.Fatalf("want instance-scoped error, got %v", err)
			}
			if !strings.Contains(msg, tc.wantKey) {
				t.Fatalf("want forbidden key %q mentioned in %v", tc.wantKey, err)
			}
			if strings.Contains(msg, "literal-secret") || strings.Contains(msg, "literal-one") {
				t.Fatalf("error must not echo secret values: %v", err)
			}
		})
	}
}

func TestCompatibleModeConfig_hasNoLiteralSecretFields(t *testing.T) {
	t.Parallel()
	// Compile-time / reflect-free structural lock: successful decode type cannot carry secrets.
	var cfg config.CompatibleModeConfig
	_ = cfg.BackendPrefix
	_ = cfg.BaseURL
	_ = cfg.APIKeyEnvVarRoot
	_ = cfg.TokenizerID
	_ = cfg.MaxConcurrentRequests
	_ = cfg.Models
}

func TestDecodeCompatibleModeConfig_rejectsExtendedStaticCredentialFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     string
		wantKey string
	}{
		{
			name: "access_token",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
access_token: tok-live-value
`,
			wantKey: "config.access_token",
		},
		{
			name: "api_token",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
api_token: tok-live-value
`,
			wantKey: "config.api_token",
		},
		{
			name: "personal_access_token",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
personal_access_token: pat-live-value
`,
			wantKey: "config.personal_access_token",
		},
		{
			name: "pat",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
pat: pat-live-value
`,
			wantKey: "config.pat",
		},
		{
			name: "private_key",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
private_key: -----BEGIN PRIVATE KEY-----
`,
			wantKey: "config.private_key",
		},
		{
			name: "client_secret",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
client_secret: shh-live-value
`,
			wantKey: "config.client_secret",
		},
		{
			name: "nested_structured_token",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
credentials:
  - id: primary
    access_token: tok-live-nested
`,
			wantKey: "config.credentials",
		},
		{
			name: "interpolated_alias",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
api_token: ${PROVIDER_API_TOKEN}
`,
			wantKey: "config.api_token",
		},
		{
			name: "file_scheme",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
api_key: file:/run/secrets/provider
`,
			wantKey: "config.api_key",
		},
		{
			name: "auth_token",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
auth_token: tok-live-value
`,
			wantKey: "config.auth_token",
		},
		{
			name: "bearer_token",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
bearer_token: tok-live-value
`,
			wantKey: "config.bearer_token",
		},
		{
			name: "credential",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
credential: sk-live-cred
`,
			wantKey: "config.credential",
		},
		{
			name: "env_scheme",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
tokenizer: env:PROVIDER_TOKENIZER
`,
			wantKey: "config.tokenizer",
		},
		{
			name: "deep_nesting",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
outer:
  middle:
    inner:
      api_key: sk-live-deep
`,
			wantKey: "config.outer.middle.inner.api_key",
		},
		{
			name: "merge_alias",
			raw: `backend_prefix: provider-a
base_url: https://api.example.com/v1
shared: &shared
  api_key: sk-live-merged
extra:
  <<: *shared
`,
			wantKey: "config.shared.api_key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.DecodeCompatibleModeConfig("inst-x", "custom-openai-legacy-compatible", mustYAMLNode(t, tc.raw))
			if err == nil {
				t.Fatal("expected extended static-credential rejection")
			}
			msg := err.Error()
			if !strings.Contains(msg, "forbidden") {
				t.Fatalf("error = %q, want forbidden marker", msg)
			}
			if !strings.Contains(msg, tc.wantKey) {
				t.Fatalf("error = %q, want field path %q", msg, tc.wantKey)
			}
			for _, secret := range []string{"tok-live-value", "tok-live-nested", "pat-live-value", "shh-live-value", "sk-live-cred", "sk-live-deep", "sk-live-merged", "PROVIDER_TOKENIZER"} {
				if strings.Contains(msg, secret) {
					t.Fatalf("error leaked secret material: %q", msg)
				}
			}
		})
	}
}
