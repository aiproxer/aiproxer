package archtest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
	"github.com/aiproxer/aiproxer/internal/infra/hostcredentials"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

// This file closes the api-key-backend-credential-contract negative-contract
// census (task 12, requirements 5.1–5.5). It extends the existing generic
// architecture gates without adding connector-specific switches: every check
// below drives the same host validators production uses (manifest admission,
// config credential scanner, descriptor comparison, resolver, slot decoder,
// quota coordinator, replay factory), so a future connector that repeats any
// audited violation fails here before it can ship.

// censusMapEnv is an injected host-environment snapshot for resolver proofs.
// It proves sparse discovery runs against the authorized snapshot, never the
// ambient process environment.
type censusMapEnv map[string]string

func (m censusMapEnv) Lookup(name string) (string, bool) {
	v, ok := m[name]
	return v, ok
}

func censusYAMLNode(t *testing.T, doc string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &n); err != nil {
		t.Fatalf("bad fixture yaml: %v", err)
	}
	return n
}

// futureStaticContract is a well-formed v2 contract for a kind that has no
// host registration. Admission must reject it: shape validity alone never
// admits an unregistered namespace.
func futureStaticContract() *backendplugin.CredentialContract {
	return &backendplugin.CredentialContract{
		Version:     backendplugin.CredentialContractVersion,
		Class:       backendplugin.CredentialClassAPIKey,
		Source:      backendplugin.CredentialSourceHostEnv,
		EnvRoot:     "FUTURE_CONNECTOR_API_KEY",
		Slot:        backendplugin.CredentialSlotAPIKey,
		Selection:   backendplugin.CredentialSelectionQuotaPreOutput,
		QuotaDomain: "future-connector",
	}
}

func censusOpenrouterManifest(t *testing.T) sdkmanifest.Manifest {
	t.Helper()
	contract, ok := manifest.LookupCanonicalContract("openrouter")
	if !ok {
		t.Fatal("openrouter must be registered")
	}
	return sdkmanifest.Manifest{
		Schema:           sdkmanifest.SchemaV1,
		PluginID:         "com.aiproxer.openrouter",
		Version:          "0.0.1",
		Executable:       "connector",
		SHA256:           "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		ProtocolMajor:    1,
		ProtocolMinMinor: 0,
		ProtocolMaxMinor: 1,
		Platforms:        []sdkmanifest.Platform{{OS: "linux", Arch: "amd64"}},
		Exports: []sdkmanifest.Export{{
			Kind:               "openrouter",
			CredentialMode:     backendplugin.CredentialModeStatic,
			AccessScope:        backendplugin.AccessScopeAny,
			ProcessSharing:     backendplugin.ProcessSharingPerInstance,
			ExecutionClass:     "inference",
			CredentialContract: &contract,
		}},
	}
}

func censusDescriptor(kinds ...backendplugin.FactoryDescriptor) backendplugin.PluginDescriptor {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1,
		PluginID:      "com.aiproxer.openrouter",
		Version:       "0.0.1",
		Factories:     kinds,
	}
}

func censusStaticFactory(kind string, mode backendplugin.CredentialMode, scope backendplugin.AccessScope) backendplugin.FactoryDescriptor {
	return backendplugin.FactoryDescriptor{
		Kind:           kind,
		CredentialMode: mode,
		AccessScope:    scope,
		ProcessSharing: backendplugin.ProcessSharingPerInstance,
	}
}

// TestBackendPluginCredentialCensusAdmitsEveryManifestExport discovers every
// first-party connector template export and proves host admission agrees with
// the closed registry: static exports carry exactly their registered
// contract, non-static exports carry none. A new static connector without its
// registration fails here. Every intended connector module is accounted for:
// unreadable templates and empty exports fail instead of being silently
// skipped, so an incompletely registered connector cannot escape the census.
func TestBackendPluginCredentialCensusAdmitsEveryManifestExport(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(firstPartyConnectorsDir)))
	if err != nil {
		t.Fatalf("read connectors dir: %v", err)
	}
	var checked, static, nonstatic int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		tmpl := filepath.Join(root, filepath.FromSlash(firstPartyConnectorsDir), e.Name(), "manifest", "template.backendplugin.json")
		raw, err := os.ReadFile(tmpl)
		if err != nil {
			t.Fatalf("%s: connector manifest template must be readable; incompletely registered connectors fail here: %v", tmpl, err)
		}
		var decoded manifestTemplateWithContract
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("%s: %v", tmpl, err)
		}
		if len(decoded.Exports) == 0 {
			t.Fatalf("%s: connector manifest carries no exports; every intended module must declare its credential posture", tmpl)
		}
		checked++
		for _, te := range decoded.Exports {
			exp := sdkmanifest.Export{
				Kind:               te.Kind,
				CredentialMode:     backendplugin.CredentialMode(te.CredentialMode),
				AccessScope:        backendplugin.AccessScope(te.AccessScope),
				ProcessSharing:     backendplugin.ProcessSharing(te.ProcessSharing),
				CredentialContract: te.CredentialContract,
			}
			if exp.CredentialMode == backendplugin.CredentialModeStatic {
				static++
			} else {
				nonstatic++
				if exp.CredentialContract != nil {
					t.Fatalf("%s: non-static export %q must not carry a credential contract", tmpl, exp.Kind)
				}
			}
			if err := manifest.AdmitStaticExport(exp); err != nil {
				t.Fatalf("%s: export %q admission: %v", tmpl, exp.Kind, err)
			}
		}
	}
	if checked == 0 || static == 0 {
		t.Fatalf("census must cover templates and static exports, got %d templates %d static %d non-static", checked, static, nonstatic)
	}
	t.Logf("census admitted %d templates (%d static, %d non-static exports)", checked, static, nonstatic)
}

// TestBackendPluginCredentialCensusRejectsFutureViolations is the deliberately
// violating future-connector fixture: each of the four audited violation
// shapes is rejected by the same host validator production enforces.
func TestBackendPluginCredentialCensusRejectsFutureViolations(t *testing.T) {
	t.Parallel()
	t.Run("missing namespace", func(t *testing.T) {
		t.Parallel()
		bare := sdkmanifest.Export{Kind: "future-connector", CredentialMode: backendplugin.CredentialModeStatic}
		if err := manifest.AdmitStaticExport(bare); err == nil {
			t.Fatal("static export without a credential contract must fail admission")
		}
		unregistered := sdkmanifest.Export{
			Kind:               "future-connector",
			CredentialMode:     backendplugin.CredentialModeStatic,
			AccessScope:        backendplugin.AccessScopeAny,
			ProcessSharing:     backendplugin.ProcessSharingPerInstance,
			CredentialContract: futureStaticContract(),
		}
		if err := manifest.AdmitStaticExport(unregistered); err == nil {
			t.Fatal("well-formed contract for an unregistered kind must fail admission")
		}
	})
	t.Run("literal YAML key", func(t *testing.T) {
		t.Parallel()
		n := censusYAMLNode(t, "base_url: https://api.example.example/v1\napi_key: sentinel-secret-value\n")
		err := config.RejectBackendStaticCredentials("future-connector backend config", "FUTURE_CONNECTOR_API_KEY", n)
		if err == nil {
			t.Fatal("literal api_key in backend config must be rejected")
		}
		if !strings.Contains(err.Error(), "config.api_key") || !strings.Contains(err.Error(), "FUTURE_CONNECTOR_API_KEY") {
			t.Fatalf("rejection must name field path + env root, got: %v", err)
		}
		if strings.Contains(err.Error(), "sentinel-secret-value") {
			t.Fatalf("rejection leaked the secret value: %v", err)
		}
	})
	t.Run("unauthorized file fallback", func(t *testing.T) {
		t.Parallel()
		n := censusYAMLNode(t, "base_url: https://api.example.example/v1\nendpoint: file:/etc/secrets/key\n")
		if err := config.RejectBackendStaticCredentials("future-connector backend config", "FUTURE_CONNECTOR_API_KEY", n); err == nil {
			t.Fatal("file: credential reference in backend config must be rejected")
		}
		bad := futureStaticContract()
		bad.Source = "file"
		if err := bad.Validate(); err == nil {
			t.Fatal("non-host_env contract source must fail validation")
		}
	})
	t.Run("manifest Describe mismatch", func(t *testing.T) {
		t.Parallel()
		m := censusOpenrouterManifest(t)
		good := censusStaticFactory("openrouter", backendplugin.CredentialModeStatic, backendplugin.AccessScopeAny)
		rogue := censusStaticFactory("undeclared-kind", backendplugin.CredentialModeStatic, backendplugin.AccessScopeAny)
		if err := manifest.CompareDescriptorToManifest(m, censusDescriptor(good, rogue)); err == nil {
			t.Fatal("descriptor exporting an undeclared kind must fail comparison")
		}
		drifted := censusStaticFactory("openrouter", backendplugin.CredentialModeNone, backendplugin.AccessScopeAny)
		if err := manifest.CompareDescriptorToManifest(m, censusDescriptor(drifted)); err == nil {
			t.Fatal("descriptor credential_mode drift must fail comparison")
		}
		if err := manifest.CompareDescriptorToManifest(m, censusDescriptor(good)); err != nil {
			t.Fatalf("agreeing descriptor must compare clean: %v", err)
		}
	})
}

// TestBackendPluginCredentialCensusRejectsEveryForbiddenFieldAndSource proves
// the config scanner discovers each secret-bearing schema field and each
// static-key source path: every forbidden key at any nesting depth and every
// secret-reference shape fails closed with field path + env root and never
// echoes the value.
func TestBackendPluginCredentialCensusRejectsEveryForbiddenFieldAndSource(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"api_key", "api_keys", "credentials", "credential", "access_token", "api_token", "auth_token", "bearer_token", "personal_access_token", "pat", "private_key", "client_secret"} {
		t.Run("field/"+key, func(t *testing.T) {
			t.Parallel()
			sentinel := "sentinel-" + key + "-value"
			n := censusYAMLNode(t, "base_url: https://api.example.example/v1\nnested:\n  "+key+": "+sentinel+"\n")
			err := config.RejectBackendStaticCredentials("future-connector backend config", "FUTURE_CONNECTOR_API_KEY", n)
			if err == nil {
				t.Fatalf("forbidden field %q must be rejected", key)
			}
			if !strings.Contains(err.Error(), "config.nested."+key) {
				t.Fatalf("rejection must name field path config.nested.%s, got: %v", key, err)
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Fatalf("rejection leaked the secret value for %q", key)
			}
		})
	}
	for _, tc := range []struct{ name, value string }{
		{"file", "file:/etc/secrets/key"},
		{"vault", "vault:/secret/api-key"},
		{"dotenv", "dotenv:/run/key.env"},
		{"env", "env:PROVIDER_API_KEY"},
		{"interpolation", "${PROVIDER_API_KEY}"},
	} {
		t.Run("source/"+tc.name, func(t *testing.T) {
			t.Parallel()
			n := censusYAMLNode(t, "base_url: https://api.example.example/v1\ntoken_hint: "+tc.value+"\n")
			if err := config.RejectBackendStaticCredentials("future-connector backend config", "FUTURE_CONNECTOR_API_KEY", n); err == nil {
				t.Fatalf("secret reference shape %q must be rejected", tc.name)
			}
		})
	}
}

// TestBackendPluginCredentialCensusG01G19Matrix maps every research gap to its
// landed validator. Each entry drives production code, so a regression in any
// slice breaks this matrix.
func TestBackendPluginCredentialCensusG01G19Matrix(t *testing.T) {
	t.Parallel()
	t.Run("G01 bounded default scan", func(t *testing.T) {
		t.Parallel()
		got, err := config.UpstreamCredentialsConfig{}.EffectiveMaxEnvIndex()
		if err != nil || got != 20 {
			t.Fatalf("default max_env_index = %d, %v; want 20", got, err)
		}
		for _, bad := range []int{-1, 257} {
			if _, err := (config.UpstreamCredentialsConfig{MaxEnvIndex: bad}.EffectiveMaxEnvIndex()); err == nil {
				t.Fatalf("max_env_index %d must be rejected", bad)
			}
		}
	})
	t.Run("G02 sparse discovery", func(t *testing.T) {
		t.Parallel()
		env := censusMapEnv{"G02_ROOT": "one", "G02_ROOT_3": "three"}
		slots, err := hostcredentials.Resolve("G02_ROOT", env, 20)
		if err != nil {
			t.Fatalf("sparse resolve: %v", err)
		}
		if len(slots) != 2 || slots[0].Index != 1 || slots[1].Index != 3 {
			t.Fatalf("sparse resolve must skip the gap, got %+v", slots)
		}
	})
	t.Run("G03 no YAML precedence", func(t *testing.T) {
		t.Parallel()
		n := censusYAMLNode(t, "api_key: sentinel-g03\n")
		if err := config.RejectBackendStaticCredentials("probe", "PROBE_API_KEY", n); err == nil {
			t.Fatal("top-level api_key must be rejected")
		}
	})
	t.Run("G04 uniform posture", func(t *testing.T) {
		t.Parallel()
		n := censusYAMLNode(t, "items:\n  - auth_token: sentinel-g04\n")
		if err := config.RejectBackendStaticCredentials("probe", "PROBE_API_KEY", n); err == nil {
			t.Fatal("structured auth_token must be rejected like every other secret field")
		}
	})
	t.Run("G05 snapshot authority", func(t *testing.T) {
		t.Parallel()
		// os.Setenv (not t.Setenv: the matrix parent is parallel) with a
		// unique probe name no other test observes.
		if err := os.Setenv("G05_CENSUS_PROBE_ROOT", "process-value-must-be-ignored"); err != nil {
			t.Fatalf("set probe env: %v", err)
		}
		t.Cleanup(func() { _ = os.Unsetenv("G05_CENSUS_PROBE_ROOT") })
		slots, err := hostcredentials.Resolve("G05_CENSUS_PROBE_ROOT", censusMapEnv{}, 20)
		if err != nil || len(slots) != 0 {
			t.Fatalf("resolver must see only the injected snapshot, got %+v, %v", slots, err)
		}
	})
	t.Run("G06 external inference admitted", func(t *testing.T) {
		t.Parallel()
		contract, ok := manifest.LookupCanonicalContract("openrouter")
		if !ok || contract.Selection != backendplugin.CredentialSelectionQuotaPreOutput {
			t.Fatalf("openrouter must be registered for quota_pre_output inference, got %+v, %v", contract, ok)
		}
	})
	t.Run("G07 no child env source", func(t *testing.T) {
		t.Parallel()
		bad := futureStaticContract()
		bad.Source = "child_env"
		if err := bad.Validate(); err == nil {
			t.Fatal("only host_env is an approved static-key source")
		}
	})
	t.Run("G08 ordered slots", func(t *testing.T) {
		t.Parallel()
		slots, err := backendplugin.DecodeOrderedSecretSlots(backendplugin.SecretBundle{Values: map[string][]byte{
			"api_key_2": []byte("second"), "api_key": []byte("first"),
		}}, backendplugin.CredentialSlotAPIKey)
		if err != nil || len(slots) != 2 || slots[0].Index != 1 || slots[1].Index != 2 {
			t.Fatalf("slot decoder must order sparse slots, got %+v, %v", slots, err)
		}
	})
	t.Run("G09 no file fallback", func(t *testing.T) {
		t.Parallel()
		n := censusYAMLNode(t, "token_file: file:/run/key.json\n")
		if err := config.RejectBackendStaticCredentials("probe", "PROBE_API_TOKEN", n); err == nil {
			t.Fatal("file: credential reference must be rejected")
		}
	})
	t.Run("G10 distinct commandcode roots", func(t *testing.T) {
		t.Parallel()
		openai, ok := manifest.LookupCanonicalContract("commandcode-openai")
		if !ok {
			t.Fatal("commandcode-openai must be registered")
		}
		anthropic, ok := manifest.LookupCanonicalContract("commandcode-anthropic")
		if !ok {
			t.Fatal("commandcode-anthropic must be registered")
		}
		if openai.EnvRoot == anthropic.EnvRoot {
			t.Fatal("commandcode exports must not share one environment root")
		}
		if !manifest.IsDeclaredSameProviderAlias("openai-responses", "openai-legacy") {
			t.Fatal("openai chat/responses must be a declared same-provider alias")
		}
		if manifest.IsDeclaredSameProviderAlias("commandcode-openai", "commandcode-anthropic") {
			t.Fatal("commandcode exports must not be an alias group")
		}
	})
	t.Run("G11 shared selector binding", func(t *testing.T) {
		t.Parallel()
		custom := []string{
			standardplugins.CustomOpenAILegacyCompatibleID,
			standardplugins.CustomOpenAIResponsesCompatibleID,
			standardplugins.CustomAnthropicCompatibleID,
			standardplugins.CustomOpenResponsesCompatibleID,
			standardplugins.CustomSystemOneCompatibleID,
		}
		for _, kind := range custom {
			if manifest.IsRegisteredCredentialKind(kind) {
				t.Fatalf("%s must bind its namespace per instance, not fixed in the registry", kind)
			}
		}
	})
	t.Run("G12 short throttle pins primary", func(t *testing.T) {
		t.Parallel()
		pool, err := credpool.New([]credpool.Credential{{Secret: "s0"}, {Secret: "s1"}})
		if err != nil {
			t.Fatalf("pool: %v", err)
		}
		now := time.Now()
		coord, err := credpool.NewCoordinator(pool, time.Hour, func() time.Time { return now })
		if err != nil {
			t.Fatalf("coordinator: %v", err)
		}
		probe := errors.New("probe 429 with 30s hint")
		var attempted []string
		_, err = credpool.Do(context.Background(), coord,
			func(error) (credpool.FailureClass, string) { return credpool.FailureRateLimited, "30" },
			func(_ context.Context, c credpool.Credential) (struct{}, error) {
				attempted = append(attempted, c.ID)
				return struct{}{}, probe
			})
		if err == nil {
			t.Fatal("short-throttle attempt must surface the throttle, not succeed")
		}
		if len(attempted) != 1 || attempted[0] != "c0" {
			t.Fatalf("short 429 must not consume the secondary key, attempted %v", attempted)
		}
	})
	t.Run("G13 no replay without proof", func(t *testing.T) {
		t.Parallel()
		if (largebody.WireReplayFactory{}.Valid()) {
			t.Fatal("zero replay factory must be invalid")
		}
		factory, err := largebody.NewWireReplayFromBytes([]byte("probe-bytes"), 1<<20)
		if err != nil || !factory.Valid() {
			t.Fatalf("byte-backed replay factory must be valid, got %v", err)
		}
	})
	t.Run("G14 descriptor agreement", func(t *testing.T) {
		t.Parallel()
		m := censusOpenrouterManifest(t)
		good := censusStaticFactory("openrouter", backendplugin.CredentialModeStatic, backendplugin.AccessScopeAny)
		if err := manifest.CompareDescriptorToManifest(m, censusDescriptor(good)); err != nil {
			t.Fatalf("agreeing descriptor must compare clean: %v", err)
		}
		extra := censusStaticFactory("extra-kind", backendplugin.CredentialModeStatic, backendplugin.AccessScopeAny)
		if err := manifest.CompareDescriptorToManifest(m, censusDescriptor(good, extra)); err == nil {
			t.Fatal("extra descriptor export must fail comparison")
		}
	})
	t.Run("G15 closed credential classes", func(t *testing.T) {
		t.Parallel()
		bad := futureStaticContract()
		bad.Class = "oauth_user"
		if err := bad.Validate(); err == nil {
			t.Fatal("unknown credential class must fail validation")
		}
		nonrot := futureStaticContract()
		nonrot.Class = backendplugin.CredentialClassStaticNonrotatable
		if err := nonrot.Validate(); err != nil {
			t.Fatalf("static_nonrotatable is the explicit non-poolable class: %v", err)
		}
	})
	t.Run("G16 closed registration", func(t *testing.T) {
		t.Parallel()
		if err := manifest.ValidateCredentialNamespaceRegistry(); err != nil {
			t.Fatalf("compiled registry must be self-consistent: %v", err)
		}
	})
	t.Run("G17 explicit quota identity", func(t *testing.T) {
		t.Parallel()
		openrouter, _ := manifest.LookupCanonicalContract("openrouter")
		anthropic, _ := manifest.LookupCanonicalContract("anthropic")
		if openrouter.QuotaDomain == anthropic.QuotaDomain {
			t.Fatal("unrelated providers must not share a quota domain")
		}
		bad := futureStaticContract()
		bad.QuotaDomain = "has space"
		if err := bad.Validate(); err == nil {
			t.Fatal("malformed quota_domain must fail validation")
		}
	})
	t.Run("G18 token shapes removed", func(t *testing.T) {
		t.Parallel()
		for _, key := range []string{"access_token", "private_key"} {
			n := censusYAMLNode(t, key+": sentinel-g18\n")
			if err := config.RejectBackendStaticCredentials("probe", "PROBE_API_KEY", n); err == nil {
				t.Fatalf("static %s field must be rejected", key)
			}
		}
	})
	t.Run("G19 auto-onboarding contract", func(t *testing.T) {
		t.Parallel()
		if ok := manifest.IsRegisteredCredentialKind("openrouter"); !ok {
			t.Fatal("registered inference kinds must resolve through the same admission registry the census drives")
		}
	})
}
