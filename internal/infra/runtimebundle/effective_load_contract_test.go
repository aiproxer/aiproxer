package runtimebundle_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestEffectiveLoadContract_LoadEffectiveHelperMatchesBuildHost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// Example YAML expects a packaged discovery layout; tests stage connectors/localstub.
	path := bpkit.WriteDogfoodLocalStubConfig(t)

	eff, _, owner, _, err := runtimebundle.LoadBootstrapEffectiveWithSource(ctx, path, config.StreamRecoveryOverrides{}, nil)
	if err != nil {
		t.Fatalf("LoadBootstrapEffectiveWithSource: %v", err)
	}
	if err := owner.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if eff == nil || eff.Config == nil {
		t.Fatal("expected effective config")
	}
	if eff.Identity.PublicFingerprint == "" {
		t.Fatal("expected public fingerprint")
	}

	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	hostServeCleanup(t, host)

	if host.Config() == nil {
		t.Fatal("expected host config")
	}
	// Shared pipeline must inject standard feature defaults before validation.
	if !hasFeatureID(host.Config(), standardplugins.ToolCallRepairFeatureID) {
		t.Fatal("BuildHost must inject tool-call-repair via shared effective load")
	}
	if !hasFeatureID(host.Config(), standardplugins.ReasoningOutputPreservationFeatureID) {
		t.Fatal("BuildHost must preserve the explicit reasoning-output-preservation feature row")
	}
	id, err := config.ComputeEffectiveIdentity(host.Config())
	if err != nil {
		t.Fatal(err)
	}
	if id.PublicFingerprint != eff.Identity.PublicFingerprint {
		t.Fatalf("fingerprint mismatch: BuildHost=%q helper=%q", id.PublicFingerprint, eff.Identity.PublicFingerprint)
	}
}

func TestEffectiveLoadContract_StrictFixtures_SecretSafeCategories(t *testing.T) {
	t.Parallel()
	secret := "redacted-secret-token-value"
	cases := []struct {
		name    string
		raw     []byte
		wantCat configsource.Category
	}{
		{
			name:    "malformed_yaml",
			raw:     []byte("server: [\napi_key: " + secret + "\n"),
			wantCat: configsource.CategoryMalformedYAML,
		},
		{
			name: "multiple_documents",
			raw: []byte(`server:
  address: "127.0.0.1:0"
---
server:
  address: "127.0.0.1:1"
`),
			wantCat: configsource.CategoryMultipleDocuments,
		},
		{
			name: "unknown_core_field",
			raw: []byte(`server:
  address: "127.0.0.1:0"
not_a_core_field: true
`),
			wantCat: configsource.CategoryUnknownCoreField,
		},
		{
			name:    "empty",
			raw:     []byte{},
			wantCat: configsource.CategoryEmpty,
		},
		{
			name:    "whitespace",
			raw:     []byte("  \n\t\n"),
			wantCat: configsource.CategoryWhitespace,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "cfg.yaml")
			if err := os.WriteFile(path, tc.raw, 0o600); err != nil {
				t.Fatal(err)
			}

			_, _, _, _, err := runtimebundle.LoadBootstrapEffectiveWithSource(context.Background(), path, config.StreamRecoveryOverrides{}, nil)
			if err == nil {
				t.Fatal("LoadBootstrapEffective must reject strict fixture")
			}
			assertSecretSafeCategory(t, err, tc.wantCat, secret)

			_, err = runtimebundle.InspectRoutes(context.Background(), runtimebundle.InspectInput{
				ConfigPath: path,
				Mandatory:  aipsdk.StandardDistributionRequirements(),
			})
			if err == nil {
				t.Fatal("InspectRoutes must reject strict fixture")
			}
			assertSecretSafeCategory(t, err, tc.wantCat, secret)
		})
	}
}

func TestEffectiveLoadContract_MissingPath_SourceMissingCategory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	_, _, _, _, err := runtimebundle.LoadBootstrapEffectiveWithSource(context.Background(), path, config.StreamRecoveryOverrides{}, nil)
	if err == nil {
		t.Fatal("expected missing source error")
	}
	cat, ok := configsource.CategoryOf(err)
	if !ok || cat != configsource.CategoryMissing {
		t.Fatalf("LoadBootstrapEffective category=%q ok=%v err=%v want %q", cat, ok, err, configsource.CategoryMissing)
	}

	_, err = runtimebundle.InspectInventory(context.Background(), runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err == nil {
		t.Fatal("expected missing source error")
	}
	cat, ok = configsource.CategoryOf(err)
	if !ok || cat != configsource.CategoryMissing {
		t.Fatalf("InspectInventory category=%q ok=%v err=%v want %q", cat, ok, err, configsource.CategoryMissing)
	}
}

func TestEffectiveLoadContract_FixedStreamRecoveryCLIWins(t *testing.T) {
	t.Parallel()
	path := bpkit.WriteDogfoodLocalStubConfig(t)
	cliOff := false
	cliIdle := 12 * time.Second

	eff, _, owner, _, err := runtimebundle.LoadBootstrapEffectiveWithSource(context.Background(), path, config.StreamRecoveryOverrides{
		CLIEnabled:     &cliOff,
		CLIIdleTimeout: cliIdle,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if eff.Config.StreamRecovery.AutoResume.Enabled == nil || *eff.Config.StreamRecovery.AutoResume.Enabled {
		t.Fatal("CLI disabled must materialize into effective config")
	}
	if eff.Config.StreamRecovery.AutoResume.IdleTimeout != cliIdle.String() {
		t.Fatalf("CLI idle timeout: got %q want %q", eff.Config.StreamRecovery.AutoResume.IdleTimeout, cliIdle)
	}

	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		StreamRecoveryOverrides: config.StreamRecoveryOverrides{
			CLIEnabled:     &cliOff,
			CLIIdleTimeout: cliIdle,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hostServeCleanup(t, host)
	if host.Config().StreamRecovery.AutoResume.Enabled == nil || *host.Config().StreamRecovery.AutoResume.Enabled {
		t.Fatal("BuildHost must apply fixed CLI stream-recovery overrides")
	}
	if host.Config().StreamRecovery.AutoResume.IdleTimeout != cliIdle.String() {
		t.Fatalf("BuildHost idle timeout: got %q", host.Config().StreamRecovery.AutoResume.IdleTimeout)
	}
}

func TestEffectiveLoadContract_ExplicitToolCallRepairOptOutSurvivesEffectiveLoad(t *testing.T) {
	t.Parallel()
	path := bpkit.WriteDogfoodLocalStubConfig(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const enabledRow = `    - id: tool-call-repair
      enabled: true
      config: {}
`
	const disabledRow = `    - id: tool-call-repair
      enabled: false
      config: {}
`
	updated := strings.Replace(string(raw), enabledRow, disabledRow, 1)
	if updated == string(raw) {
		t.Fatal("test fixture did not contain the expected tool-call-repair row")
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}

	eff, _, owner, _, err := runtimebundle.LoadBootstrapEffectiveWithSource(context.Background(), path, config.StreamRecoveryOverrides{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var matches int
	for _, feature := range eff.Config.Plugins.Features {
		if feature.FactoryID() != standardplugins.ToolCallRepairFeatureID && feature.InstanceID() != standardplugins.ToolCallRepairFeatureID {
			continue
		}
		matches++
		if feature.Enabled {
			t.Fatalf("explicit tool-call-repair opt-out was re-enabled: %+v", feature)
		}
	}
	if matches != 1 {
		t.Fatalf("expected exactly one preserved opt-out row, got %d: %+v", matches, eff.Config.Plugins.Features)
	}
}

func TestEffectiveLoadContract_InjectsStandardFeaturesWhenAbsent(t *testing.T) {
	t.Parallel()
	const body = `
server:
  address: "127.0.0.1:0"
continuity:
  in_memory: true
routing:
  default_route: "local-stub:stub-default"
plugins:
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
    - id: openai-legacy
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
    - id: gemini
      enabled: true
      config: {}
  backends:
    - kind: openai-codex
      id: codex-primary
      enabled: true
      config: {}
    - id: openai-responses
      enabled: false
      config: {}
    - id: openai-legacy
      enabled: false
      config: {}
    - id: anthropic
      enabled: false
      config: {}
    - id: gemini
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}
    - id: acp
      enabled: false
      config: {}
    - id: openrouter
      enabled: false
      config: {}
    - id: nvidia
      enabled: false
      config: {}
    - id: opencode-go
      enabled: false
      config: {}
    - id: opencode-zen
      enabled: false
      config: {}
    - id: ollama
      enabled: false
      config: {}
    - id: ollama-cloud
      enabled: false
      config: {}
    - id: llamacpp
      enabled: false
      config: {}
    - id: lmstudio
      enabled: false
      config: {}
    - id: vllm
      enabled: false
      config: {}
    - kind: local-stub
      id: local-stub
      enabled: true
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	eff, _, owner, _, err := runtimebundle.LoadBootstrapEffectiveWithSource(context.Background(), path, config.StreamRecoveryOverrides{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !hasFeatureID(eff.Config, standardplugins.ToolCallRepairFeatureID) {
		t.Fatal("expected tool-call-repair injection")
	}
	if !hasFeatureID(eff.Config, standardplugins.ReasoningOutputPreservationFeatureID) {
		t.Fatal("expected reasoning-output-preservation injection")
	}
}

func TestBuildHostContract_BuildPartialCleanupOnCandidateFailure(t *testing.T) {
	t.Parallel()
	// Invalid backend kind fails during candidate compile after process services
	// may have opened resources; BuildHost must dispose everything internally
	// and return a nil Host (no partial ownership handed to the caller).
	path := writeConfigWithUnknownBackendKind(t)
	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err == nil {
		hostServeCleanup(t, host)
		t.Fatal("expected candidate compile failure")
	}
	if host != nil {
		t.Fatal("failed BuildHost must not return a partial Host")
	}
	if !strings.Contains(err.Error(), "runtime assembly") && !strings.Contains(err.Error(), "backend") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func hasFeatureID(cfg *config.Config, id string) bool {
	if cfg == nil {
		return false
	}
	for _, p := range cfg.Plugins.Features {
		if p.FactoryID() == id || p.InstanceID() == id {
			return true
		}
	}
	return false
}

func assertSecretSafeCategory(t *testing.T, err error, want configsource.Category, secret string) {
	t.Helper()
	cat, ok := configsource.CategoryOf(err)
	if !ok || cat != want {
		t.Fatalf("category=%q ok=%v err=%v want %q", cat, ok, err, want)
	}
	msg := err.Error()
	if secret != "" && strings.Contains(msg, secret) {
		t.Fatalf("error leaked secret: %q", msg)
	}
	if strings.Contains(msg, "not_a_core_field: true") {
		t.Fatalf("error leaked raw field: %q", msg)
	}
}

func writeConfigWithUnknownBackendKind(t *testing.T) string {
	t.Helper()
	base, err := os.ReadFile(bpkit.WriteDogfoodLocalStubConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(base), "kind: local-stub", "kind: not-a-real-backend-kind", 1)
	if text == string(base) {
		// dogfood may identify local-stub by id only; force an unknown kind row.
		text = strings.Replace(string(base), "id: dogfood-local\n      enabled: true", "id: dogfood-local\n      kind: not-a-real-backend-kind\n      enabled: true", 1)
	}
	if !strings.Contains(text, "not-a-real-backend-kind") {
		t.Fatal("failed to inject unknown backend kind into dogfood fixture")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
