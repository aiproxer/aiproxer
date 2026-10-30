package secretguard_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard"
	sgcompose "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTask51_ComposeLocalDiscoverySwitchKeepsBetterLeaksIndependent(t *testing.T) {
	t.Parallel()

	const opaque = "Opaque-Compose-Secret-2026"
	cases := []struct {
		name        string
		local       string
		betterLeaks string
		wantLocal   bool
		wantBetter  bool
		panicEnv    bool
	}{
		{name: "absent_local_absent_betterleaks", wantLocal: true, wantBetter: true},
		{name: "local_enabled_betterleaks_disabled", local: "true", betterLeaks: "false", wantLocal: true, wantBetter: false},
		{name: "local_disabled_betterleaks_enabled", local: "false", betterLeaks: "true", wantLocal: false, wantBetter: true, panicEnv: true},
		{name: "both_disabled", local: "false", betterLeaks: "false", wantLocal: false, wantBetter: false, panicEnv: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			raw := "action: redact\n"
			if tc.local != "" {
				raw += "auto_discovered_local_keys:\n  enabled: " + tc.local + "\n"
			}
			if tc.betterLeaks != "" {
				raw += "betterleaks:\n  enabled: " + tc.betterLeaks + "\n"
			}
			raw += "single_user:\n  include_popular_env: false\n  include_env: [AIP_TASK51_OPAQUE]\n"
			regs := []aipsdk.Registration{{
				Kind: aipsdk.PluginKindFeature, ID: "task-5-1", FactoryKind: "secrets-guard", Enabled: true,
				Config: aipsdk.ConfigPayload{Node: mustYAMLNode(t, raw)},
			}}

			runtimeCfg, err := secretguard.ComposeRuntimeConfig("single_user", regs)
			require.NoError(t, err)
			assert.Equal(t, tc.wantLocal, runtimeCfg.LocalAutoDiscoveryEnabled)
			assert.Equal(t, tc.wantBetter, runtimeCfg.BetterLeaksEnabled,
				"BetterLeaks policy must remain independent of the exact local switch")

			var env interface {
				Lookup(string) (string, bool)
				Snapshot() []string
			}
			if tc.panicEnv {
				env = &panicEnv{}
			} else {
				env = &mapEnv{vals: map[string]string{"AIP_TASK51_OPAQUE": opaque}}
			}
			out, err := sgcompose.Compose(sgcompose.Input{
				AccessMode:    accessmode.ModeSingleUser,
				Registrations: regs,
				Environment:   env,
				Logger:        discardLogger(),
			})
			require.NoError(t, err)
			require.NotNil(t, out)
			require.NotNil(t, out.Inventory)

			matcher, err := out.Plane.MatcherResolver.Resolve(t.Context())
			require.NoError(t, err)
			require.NotNil(t, matcher)
			findings, err := matcher.ScanString(t.Context(), "secret="+opaque)
			require.NoError(t, err)
			if tc.wantLocal {
				assert.Len(t, findings, 1, "exact local protection must remain active")
				assert.Equal(t, 1, out.Inventory.SecretGuardCatalogEntryCount)
			} else {
				assert.Empty(t, findings)
				assert.Equal(t, 0, out.Inventory.SecretGuardCatalogEntryCount)
			}
		})
	}
}

func TestTask51_ComposeHostOverridesPreserveExactCatalogPrecedence(t *testing.T) {
	t.Parallel()

	const (
		yamlSecret = "yaml-secret-value-2026"
		hostSecret = "host-opaque-secret-value-2026"
	)
	env := &mapEnv{vals: map[string]string{
		"YAML_SECRET":   yamlSecret,
		"HOST_SECRET":   hostSecret,
		"GITHUB_TOKEN":  "popular-host-token-2026",
		"HOST_OPENAI":   "sk-host-openai-secret-2026",
		"EXCLUDED_NAME": "excluded-name-value-2026",
	}}
	runtimeCfg := secretguard.RuntimeConfig{
		Enabled:                   true,
		LocalAutoDiscoveryEnabled: true,
		IncludePopularEnv:         false,
		IncludeEnv:                []string{"YAML_SECRET"},
		MinSecretBytes:            8,
		PreserveKnownPrefixes:     false,
		MaskByte:                  '*',
	}
	hostInputs := &sgcompose.SecretGuardInputs{SingleUser: sgcompose.SingleUserOptions{
		IncludePopularEnv: true,
		IncludeEnv:        []string{"HOST_SECRET", "HOST_OPENAI"},
		ExcludeEnv:        []string{"YAML_SECRET", "EXCLUDED_NAME"},
		MinSecretBytes:    16,
		MatcherConfigured: true,
		Matcher: sgcompose.MatcherOptions{
			PreserveKnownPrefixes: true,
			MaskByte:              '#',
		},
	}}

	out, err := sgcompose.Compose(sgcompose.Input{
		AccessMode:    accessmode.ModeSingleUser,
		RuntimeConfig: &runtimeCfg,
		Environment:   env,
		HostInputs:    hostInputs,
		Logger:        discardLogger(),
	})
	require.NoError(t, err)
	require.NotNil(t, out.Inventory)
	assert.Equal(t, 3, out.Inventory.SecretGuardCatalogEntryCount,
		"host include/exclude/popular/minimum options must drive the exact catalog")

	matcher, err := out.Plane.MatcherResolver.Resolve(t.Context())
	require.NoError(t, err)
	require.NotNil(t, matcher)

	for _, value := range []string{hostSecret, "popular-host-token-2026", "sk-host-openai-secret-2026"} {
		findings, scanErr := matcher.ScanString(t.Context(), "secret="+value)
		require.NoError(t, scanErr)
		assert.Len(t, findings, 1, "host-controlled catalog value must remain exact-protected")
	}
	for _, value := range []string{yamlSecret, "excluded-name-value-2026"} {
		findings, scanErr := matcher.ScanString(t.Context(), "secret="+value)
		require.NoError(t, scanErr)
		assert.Empty(t, findings, "YAML values replaced by host options must not remain in the catalog")
	}

	redacted, findings, err := matcher.RedactString(t.Context(), "sk-host-openai-secret-2026")
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, "sk-"+strings.Repeat("#", len("sk-host-openai-secret-2026")-len("sk-")), redacted)

	redacted, findings, err = matcher.RedactString(t.Context(), hostSecret)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, strings.Repeat("#", len(hostSecret)), redacted)
}
