package openresponsescompat

import (
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

const minimalYAML = `backend_prefix: my-or
base_url: https://api.example.com/openresponses/v1
`

func mustConfig(t *testing.T, instanceID, raw string) Config {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := DecodeConfig(instanceID, ID, n)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func decodeConfigErr(t *testing.T, instanceID, raw string) error {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	_, err := DecodeConfig(instanceID, ID, n)
	return err
}

func configHasCapability(caps []aipapi.Capability, targetCap aipapi.Capability) bool {
	return slices.Contains(caps, targetCap)
}

func TestConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg := mustConfig(t, "inst", minimalYAML)
	if cfg.BackendPrefix != "my-or" {
		t.Fatalf("backend_prefix = %q, want my-or", cfg.BackendPrefix)
	}
	if cfg.Profile != DefaultProfile {
		t.Fatalf("profile = %q, want %q", cfg.Profile, DefaultProfile)
	}
	if cfg.BaseURL != "https://api.example.com/openresponses/v1" {
		t.Fatalf("base_url = %q", cfg.BaseURL)
	}
	if cfg.APIKeyEnvVarRoot != "" {
		t.Fatalf("api_key_env_var_root = %q, want explicit no-auth", cfg.APIKeyEnvVarRoot)
	}
	if len(cfg.Capabilities) == 0 {
		t.Fatal("expected default capability set")
	}
	if !configHasCapability(cfg.Capabilities, aipapi.CapabilityOrderedItems) {
		t.Fatal("default capabilities must include ordered_items")
	}
	if !configHasCapability(cfg.Capabilities, aipapi.CapabilityAssistantPhase) {
		t.Fatal("default capabilities must include assistant_phase")
	}
	if len(cfg.Dialects.Item) != 2 || cfg.Dialects.Item[0].Dialect != DefaultItemDialect || cfg.Dialects.Item[1].Dialect != "item_reference" {
		t.Fatalf("item dialects = %+v, want [%s item_reference]", cfg.Dialects.Item, DefaultItemDialect)
	}
	if len(cfg.Dialects.Compaction) != 1 || cfg.Dialects.Compaction[0].Dialect != DefaultCompactionDialect {
		t.Fatalf("compaction dialects = %+v, want [%s]", cfg.Dialects.Compaction, DefaultCompactionDialect)
	}
	if cfg.RequestLimits.MaxItems != DefaultMaxRequestItems {
		t.Fatalf("request_limits.max_items = %d, want %d", cfg.RequestLimits.MaxItems, DefaultMaxRequestItems)
	}
	if cfg.ResponseLimits.MaxEventBytes != DefaultMaxResponseEventBytes {
		t.Fatalf("response_limits.max_event_bytes = %d, want %d", cfg.ResponseLimits.MaxEventBytes, DefaultMaxResponseEventBytes)
	}
	if cfg.ResponseLimits.MaxResourceBytes != DefaultMaxResponseResourceBytes {
		t.Fatalf("response_limits.max_resource_bytes = %d, want %d", cfg.ResponseLimits.MaxResourceBytes, DefaultMaxResponseResourceBytes)
	}
}

func TestConfig_ProfileAccepted(t *testing.T) {
	t.Parallel()
	cfg := mustConfig(t, "inst", minimalYAML+"profile: "+DefaultProfile+"\n")
	if cfg.Profile != DefaultProfile {
		t.Fatalf("profile = %q", cfg.Profile)
	}
}

func TestConfig_ProfileRejected(t *testing.T) {
	t.Parallel()
	err := decodeConfigErr(t, "inst", minimalYAML+"profile: 2025-01-01\n")
	if err == nil {
		t.Fatal("expected unsupported profile rejection")
	}
	if !strings.Contains(err.Error(), "2025-01-01") || !strings.Contains(err.Error(), DefaultProfile) {
		t.Fatalf("error = %v, want profile and supported profile listed", err)
	}
}

func TestConfig_UnknownFieldRejectedWithInstanceIdentity(t *testing.T) {
	t.Parallel()
	err := decodeConfigErr(t, "or-inst-7", minimalYAML+"proprietary_control: on\n")
	if err == nil {
		t.Fatal("expected unknown config key rejection")
	}
	msg := err.Error()
	if !strings.Contains(msg, "proprietary_control") {
		t.Fatalf("error must name unknown key, got %q", msg)
	}
	if !strings.Contains(msg, "or-inst-7") {
		t.Fatalf("error must carry instance identity, got %q", msg)
	}
}

func TestConfig_ForbiddenSecretKeysRejectedWithoutEcho(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "api_key", raw: minimalYAML + "api_key: sk-or-literal\n"},
		{name: "api_keys", raw: minimalYAML + "api_keys: [sk-or-list]\n"},
		{name: "credentials", raw: minimalYAML + "credentials:\n  - id: c1\n    api_key: sk-or-cred\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := decodeConfigErr(t, "inst", tc.raw)
			if err == nil {
				t.Fatal("expected forbidden secret key rejection")
			}
			if !strings.Contains(err.Error(), "forbidden") {
				t.Fatalf("error = %q, want forbidden marker", err)
			}
			for _, secret := range []string{"sk-or-literal", "sk-or-list", "sk-or-cred"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error echoed literal secret: %q", err)
				}
			}
		})
	}
}

func TestConfig_EndpointPolicy(t *testing.T) {
	t.Parallel()
	valid := []string{
		"https://api.example.com/openresponses/v1",
		"https://api.example.com",
		"http://127.0.0.1:8080/openresponses/v1",
		"http://localhost:9/openresponses/v1",
		"http://[::1]:9/v1",
	}
	for _, base := range valid {
		t.Run("accept_"+base, func(t *testing.T) {
			t.Parallel()
			cfg := mustConfig(t, "inst", "backend_prefix: my-or\nbase_url: "+base+"\n")
			if cfg.BaseURL == "" {
				t.Fatal("expected base_url")
			}
		})
	}

	invalid := []struct {
		name string
		base string
	}{
		{name: "http_remote_host", base: "http://api.insecure.example/v1"},
		{name: "relative", base: "/openresponses/v1"},
		{name: "missing_host", base: "https:///openresponses/v1"},
		{name: "empty", base: ""},
		{name: "userinfo", base: "https://user:pass@api.example.com/v1"},
		{name: "fragment", base: "https://api.example.com/v1#frag"},
		{name: "not_http_scheme", base: "ftp://api.example.com/v1"},
	}
	for _, tc := range invalid {
		t.Run("reject_"+tc.name, func(t *testing.T) {
			t.Parallel()
			err := decodeConfigErr(t, "inst", "backend_prefix: my-or\nbase_url: "+tc.base+"\n")
			if err == nil {
				t.Fatalf("expected base_url %q rejection", tc.base)
			}
		})
	}
}

func TestConfig_BaseURLRequired(t *testing.T) {
	t.Parallel()
	err := decodeConfigErr(t, "inst", "backend_prefix: my-or\n")
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("error = %v, want base_url required", err)
	}
}

func TestConfig_BackendPrefixRequired(t *testing.T) {
	t.Parallel()
	err := decodeConfigErr(t, "inst", "base_url: https://api.example.com/v1\n")
	if err == nil || !strings.Contains(err.Error(), "backend_prefix") {
		t.Fatalf("error = %v, want backend_prefix required", err)
	}
}

func TestConfig_CapabilitiesDeclared(t *testing.T) {
	t.Parallel()
	cfg := mustConfig(t, "inst", minimalYAML+`capabilities: [ordered_items, streaming, tools, compaction]
`)
	if len(cfg.Capabilities) != 4 {
		t.Fatalf("capabilities = %+v", cfg.Capabilities)
	}
	for _, want := range []aipapi.Capability{
		aipapi.CapabilityOrderedItems,
		aipapi.CapabilityStreaming,
		aipapi.CapabilityTools,
		aipapi.CapabilityCompaction,
	} {
		if !configHasCapability(cfg.Capabilities, want) {
			t.Fatalf("capabilities missing %q: %+v", want, cfg.Capabilities)
		}
	}
	if configHasCapability(cfg.Capabilities, aipapi.CapabilityVision) {
		t.Fatal("declared capabilities must replace defaults, vision must be absent")
	}
}

func TestConfig_CapabilitiesUnknownRejected(t *testing.T) {
	t.Parallel()
	err := decodeConfigErr(t, "inst", minimalYAML+"capabilities: [ordered_items, sky_writing]\n")
	if err == nil {
		t.Fatal("expected unknown capability rejection")
	}
	if !strings.Contains(err.Error(), "sky_writing") {
		t.Fatalf("error = %v, want unknown capability named", err)
	}
}

func TestConfig_CapabilitiesNotSequenceRejected(t *testing.T) {
	t.Parallel()
	if err := decodeConfigErr(t, "inst", minimalYAML+"capabilities: ordered_items\n"); err == nil {
		t.Fatal("expected capabilities sequence requirement")
	}
}

func TestConfig_DialectsDeclared(t *testing.T) {
	t.Parallel()
	raw := minimalYAML + `dialects:
  item:
    - dialect: openresponses.2026-04-24
      implementor: provider-x
  reasoning:
    - dialect: openai.chat.text.v1
  compaction:
    - dialect: openresponses.2026-04-24
  extensions:
    - namespace: acme
      type: widget
      implementor: acme-vendor
`
	cfg := mustConfig(t, "inst", raw)
	if len(cfg.Dialects.Item) != 1 || cfg.Dialects.Item[0].Dialect != "openresponses.2026-04-24" || cfg.Dialects.Item[0].Implementor != "provider-x" {
		t.Fatalf("item dialects = %+v", cfg.Dialects.Item)
	}
	if len(cfg.Dialects.Reasoning) != 1 || cfg.Dialects.Reasoning[0].Dialect != "openai.chat.text.v1" {
		t.Fatalf("reasoning dialects = %+v", cfg.Dialects.Reasoning)
	}
	if len(cfg.Dialects.Compaction) != 1 || cfg.Dialects.Compaction[0].Dialect != "openresponses.2026-04-24" {
		t.Fatalf("compaction dialects = %+v", cfg.Dialects.Compaction)
	}
	if len(cfg.Dialects.Extensions) != 1 || cfg.Dialects.Extensions[0].Namespace != "acme" || cfg.Dialects.Extensions[0].Type != "widget" {
		t.Fatalf("extensions = %+v", cfg.Dialects.Extensions)
	}
}

func TestConfig_DialectsUnknownKeyRejected(t *testing.T) {
	t.Parallel()
	err := decodeConfigErr(t, "inst", minimalYAML+"dialects:\n  item:\n    - dialect: openresponses.2026-04-24\n      vendor: nope\n")
	if err == nil {
		t.Fatal("expected unknown dialects key rejection")
	}
	if !strings.Contains(err.Error(), "vendor") {
		t.Fatalf("error = %v, want unknown dialects key named", err)
	}
}

func TestConfig_DialectsEmptyDialectRejected(t *testing.T) {
	t.Parallel()
	err := decodeConfigErr(t, "inst", minimalYAML+"dialects:\n  compaction:\n    - implementor: x\n")
	if err == nil {
		t.Fatal("expected missing dialect rejection")
	}
}

func TestConfig_LimitsValid(t *testing.T) {
	t.Parallel()
	raw := minimalYAML + `request_limits:
  max_items: 64
  max_item_bytes: 524288
response_limits:
  max_event_bytes: 262144
`
	cfg := mustConfig(t, "inst", raw)
	if cfg.RequestLimits.MaxItems != 64 || cfg.RequestLimits.MaxItemBytes != 524288 {
		t.Fatalf("request limits = %+v", cfg.RequestLimits)
	}
	if cfg.RequestLimits.MaxContentParts != DefaultMaxRequestContentParts {
		t.Fatalf("unspecified request limit must default, got %d", cfg.RequestLimits.MaxContentParts)
	}
	if cfg.ResponseLimits.MaxEventBytes != 262144 || cfg.ResponseLimits.MaxItems != DefaultMaxResponseItems {
		t.Fatalf("response limits = %+v", cfg.ResponseLimits)
	}
}

func TestConfig_LimitsRejected(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "request_zero", raw: minimalYAML + "request_limits:\n  max_items: 0\n"},
		{name: "request_negative", raw: minimalYAML + "request_limits:\n  max_tools: -1\n"},
		{name: "request_over_max", raw: minimalYAML + "request_limits:\n  max_items: 999999\n"},
		{name: "response_over_max", raw: minimalYAML + "response_limits:\n  max_event_bytes: 999999999\n"},
		{name: "unknown_limit_key", raw: minimalYAML + "request_limits:\n  max_frobs: 5\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := decodeConfigErr(t, "inst", tc.raw)
			if err == nil {
				t.Fatal("expected limits rejection")
			}
		})
	}
}

func TestConfig_ModelsInline(t *testing.T) {
	t.Parallel()
	raw := minimalYAML + `models:
  source: inline
  items:
    - canonical_id: my-or/model-a
      native_id: model-a
`
	cfg := mustConfig(t, "inst", raw)
	if len(cfg.Models.Items) != 1 || cfg.Models.Items[0].CanonicalID != "my-or/model-a" || cfg.Models.Items[0].NativeID != "model-a" {
		t.Fatalf("models = %+v", cfg.Models)
	}
	if cfg.Models.Source != "inline" {
		t.Fatalf("source = %q", cfg.Models.Source)
	}
}

func TestConfig_ModelsUnknownKeyRejected(t *testing.T) {
	t.Parallel()
	err := decodeConfigErr(t, "inst", minimalYAML+"models:\n  source: inline\n  refresh_seconds: 10\n")
	if err == nil {
		t.Fatal("expected unknown models key rejection")
	}
}

func TestConfig_UnknownKeyValueNeverEchoed(t *testing.T) {
	t.Parallel()
	secret := "super-secret-openresponses-value"
	err := decodeConfigErr(t, "inst", "backend_prefix: my-or\nbase_url: https://api.example.com/v1\nproprietary_control: "+secret+"\n")
	if err == nil {
		t.Fatal("expected unknown key rejection")
	}
	if !strings.Contains(err.Error(), "proprietary_control") {
		t.Fatalf("error must name unknown key, got %q", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error echoed sensitive value: %q", err)
	}
}

func TestConfig_ProviderBoundary_RejectsOpenRouterAttribution(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"app_url", "static_referer", "static_title", "x_http_referer", "x_title", "openrouter_attribution"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			err := decodeConfigErr(t, "or-bnd", minimalYAML+key+": provider-x\n")
			if err == nil {
				t.Fatalf("expected provider-boundary rejection for key %q", key)
			}
			msg := err.Error()
			if !strings.Contains(msg, key) {
				t.Fatalf("error must name key, got %q", msg)
			}
			if !strings.Contains(msg, "provider-connector owned") {
				t.Fatalf("error must explain connector ownership, got %q", msg)
			}
		})
	}
}

func TestConfig_ProviderBoundary_RejectsOpenRouterRoutingBillingCatalogProprietary(t *testing.T) {
	t.Parallel()
	for _, key := range []string{
		"route", "provider", "billing", "catalog",
		"middleware", "transforms", "integrations",
		"openrouter", "openrouter_route", "provider_options", "provider_controls",
	} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			err := decodeConfigErr(t, "or-bnd", minimalYAML+key+": on\n")
			if err == nil {
				t.Fatalf("expected provider-boundary rejection for key %q", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("error must name key, got %q", err)
			}
		})
	}
}

func TestConfig_ProviderBoundary_DoesNotRejectGenericKeys(t *testing.T) {
	t.Parallel()
	cfg := mustConfig(t, "inst", minimalYAML+`profile: `+DefaultProfile+`
capabilities: [ordered_items]
models:
  source: inline
  items:
    - canonical_id: my-or/model-a
      native_id: model-a
`)
	if cfg.Profile != DefaultProfile || len(cfg.Capabilities) == 0 || len(cfg.Models.Items) != 1 {
		t.Fatalf("generic keys must decode under provider boundary: %+v", cfg)
	}
}

func TestConfig_ProviderBoundary_PreservesProfileProvenance(t *testing.T) {
	t.Parallel()
	cfg := mustConfig(t, "inst", minimalYAML+"profile: "+DefaultProfile+"\n")
	if cfg.Profile != DefaultProfile {
		t.Fatalf("profile = %q, want %q", cfg.Profile, DefaultProfile)
	}
}

func TestConfig_ExtendedStaticCredentialFieldsRejected(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
		path string
	}{
		{name: "access_token", raw: minimalYAML + "access_token: tok-live-value\n", path: "config.access_token"},
		{name: "api_token", raw: minimalYAML + "api_token: tok-live-value\n", path: "config.api_token"},
		{name: "personal_access_token", raw: minimalYAML + "personal_access_token: pat-live-value\n", path: "config.personal_access_token"},
		{name: "pat", raw: minimalYAML + "pat: pat-live-value\n", path: "config.pat"},
		{name: "private_key", raw: minimalYAML + "private_key: -----BEGIN PRIVATE KEY-----\n", path: "config.private_key"},
		{name: "client_secret", raw: minimalYAML + "client_secret: shh-live-value\n", path: "config.client_secret"},
		{name: "nested_token", raw: minimalYAML + "credentials:\n  - id: c1\n    access_token: tok-live-nested\n", path: "config.credentials"},
		{name: "interpolated", raw: minimalYAML + "api_token: ${PROVIDER_API_TOKEN}\n", path: "config.api_token"},
		{name: "file_scheme", raw: minimalYAML + "api_key: file:/run/secrets/provider\n", path: "config.api_key"},
		{name: "auth_token", raw: minimalYAML + "auth_token: tok-live-value\n", path: "config.auth_token"},
		{name: "bearer_token", raw: minimalYAML + "bearer_token: tok-live-value\n", path: "config.bearer_token"},
		{name: "credential", raw: minimalYAML + "credential: sk-live-cred\n", path: "config.credential"},
		{name: "env_scheme", raw: minimalYAML + "tokenizer: env:PROVIDER_TOKENIZER\n", path: "config.tokenizer"},
		{name: "deep_nesting", raw: minimalYAML + "outer:\n  middle:\n    inner:\n      api_key: sk-live-deep\n", path: "config.outer.middle.inner.api_key"},
		{name: "merge_alias", raw: minimalYAML + "shared: &shared\n  api_key: sk-live-merged\nextra:\n  <<: *shared\n", path: "config.shared.api_key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := decodeConfigErr(t, "inst", tc.raw)
			if err == nil {
				t.Fatal("expected extended static-credential rejection")
			}
			msg := err.Error()
			if !strings.Contains(msg, "forbidden") {
				t.Fatalf("error = %q, want forbidden marker", msg)
			}
			if !strings.Contains(msg, tc.path) {
				t.Fatalf("error = %q, want field path %q", msg, tc.path)
			}
			for _, secret := range []string{"tok-live-value", "tok-live-nested", "pat-live-value", "shh-live-value", "sk-live-cred", "sk-live-deep", "sk-live-merged", "PROVIDER_TOKENIZER"} {
				if strings.Contains(msg, secret) {
					t.Fatalf("error echoed literal secret: %q", msg)
				}
			}
		})
	}
}
