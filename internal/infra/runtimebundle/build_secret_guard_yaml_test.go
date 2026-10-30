package runtimebundle

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguardhost"
	"gopkg.in/yaml.v3"
)

func TestBuildSecretGuardRuntime_multiUserZeroEnvEvenWithMalformedSingleUser(t *testing.T) {
	t.Parallel()
	// Composition security boundary: multi-user source construction must never call Environment.
	env := &panicSGEnv{}
	opts := &BuildOptions{
		Production: ProductionOptions{
			FeatureHostRegistrations: []featurehost.Registration{
				(&secretguardhost.Binding{
					Environment: env,
					SingleUser: secretguardhost.SingleUserOptions{
						IncludePopularEnv: true,
						IncludeEnv:        []string{"OPENAI_API_KEY"},
						MinSecretBytes:    8,
					},
				}).Registration(),
			},
		},
	}
	regs := []aipsdk.Registration{{
		Kind:        aipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: mustYAMLNode(t, "action: block\n")},
	}}
	cfg := &config.Config{Access: config.AccessConfig{Mode: "multi_user"}}
	if _, err := testBuildSecretGuardRuntime(cfg, nilDiscardLogger(), opts, regs); err != nil {
		t.Fatal(err)
	}
	if env.calls != 0 {
		t.Fatalf("env calls=%d want 0", env.calls)
	}
}

func TestBuildSecretGuardRuntime_decodedIncludeEnvAffectsCatalog(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticDuplicateValueAliasA
	env := &mapSGEnv{vals: map[string]string{
		"AIP_TEST_SECRETGUARD_INCLUDE": secret,
		"OPENAI_API_KEY":               testkit.SyntheticOpenAIAPIKey,
	}}
	raw := mustYAMLNode(t, `
action: log
min_secret_bytes: 8
single_user:
  include_popular_env: false
  include_env: [AIP_TEST_SECRETGUARD_INCLUDE]
  exclude_env: [OPENAI_API_KEY]
`)
	regs := []aipsdk.Registration{{
		Kind:        aipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: raw},
	}}
	opts := &BuildOptions{
		Production: ProductionOptions{
			FeatureHostRegistrations: []featurehost.Registration{
				(&secretguardhost.Binding{
					Environment: env,
				}).Registration(),
			},
		},
	}
	rt, err := testBuildSecretGuardRuntime(&config.Config{}, nilDiscardLogger(), opts, regs)
	if err != nil {
		t.Fatal(err)
	}
	m, err := rt.Plane.MatcherResolver.Resolve(t.Context())
	if err != nil || m == nil {
		t.Fatalf("matcher: m=%v err=%v", m, err)
	}
	findInclude, err := m.ScanString(t.Context(), "x="+secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(findInclude) == 0 {
		t.Fatal("include_env secret must be in catalog")
	}
	findExcluded, err := m.ScanString(t.Context(), "y="+testkit.SyntheticOpenAIAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(findExcluded) != 0 {
		t.Fatal("exclude_env secret must not be in catalog")
	}
	if rt.Inventory == nil || rt.Inventory.CatalogEntryCount < 1 {
		t.Fatalf("catalog entry count: %#v", rt.Inventory)
	}
	if len(rt.Inventory.SourceCategories) == 0 {
		t.Fatal("source categories must be reported for inventory")
	}
	for _, s := range testkit.AllSyntheticSecretGuardValues() {
		for _, cat := range rt.Inventory.SourceCategories {
			if strings.Contains(cat, s) {
				t.Fatalf("source category leaked secret material (len=%d)", len(cat))
			}
		}
	}
}

func nilDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
}

func mustYAMLNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}
