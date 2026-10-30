package standardplugins

import (
	"strings"
	"testing"

	coreconfig "github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"gopkg.in/yaml.v3"
)

func featureConfigNode(t *testing.T, configYAML string) yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(configYAML), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if len(doc.Content) > 0 {
		return *doc.Content[0]
	}
	return doc
}

func standardFeatureFactory(t *testing.T, id string) pluginreg.FeatureFactory {
	t.Helper()
	for _, registration := range StandardBundle().Features {
		if registration.ID == id {
			if registration.Factory == nil {
				t.Fatalf("feature %q has no factory", id)
			}
			return registration.Factory
		}
	}
	t.Fatalf("standard bundle does not register feature %q", id)
	return nil
}

// TestStandardBundleRegistersSessionClassificationFactory pins the standard
// feature registration itself (requirement 8.1): the canonical
// plugins.features id resolves to a factory through the generic registry.
func TestStandardBundleRegistersSessionClassificationFactory(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatalf("InstallStandardBundleOn: %v", err)
	}
	if _, err := reg.BuildFeatureBundle(sessionclassification.ID, featureConfigNode(t, "")); err != nil {
		t.Fatalf("BuildFeatureBundle(%q): %v", sessionclassification.ID, err)
	}
}

// TestSessionClassificationFactoryValidatesModeSpecificConfig pins the
// registry-facing factory: it validates and decodes the opaque feature config
// subtree and contributes no plane, because the concrete classifier needs the
// process-owned coordinator bound during generation composition.
func TestSessionClassificationFactoryValidatesModeSpecificConfig(t *testing.T) {
	t.Parallel()

	factory := standardFeatureFactory(t, sessionclassification.ID)
	cases := []struct {
		name       string
		configYAML string
		wantErr    bool
	}{
		{name: "empty config uses the documented heuristic default", configYAML: ""},
		{name: "explicit heuristic mode", configYAML: "mode: heuristic\n"},
		{name: "heuristic exclusions", configYAML: "heuristic:\n  ignored_user_agent_prefixes: [internal-bot]\n"},
		{
			name: "explicit hybrid mode with operational remote values",
			configYAML: "mode: hybrid\nremote:\n  provider: jev\n  api_key_env: TYPESAFE_API_KEY\n" +
				"  timeout: 750ms\n  max_attempts_per_session: 1\n  lease_ttl: 2s\n  retry_backoff: 0s\n  positive_threshold: 0.90\n",
		},
		{name: "unknown mode is rejected", configYAML: "mode: telepathy\n", wantErr: true},
		{name: "unknown top-level key is rejected", configYAML: "not_a_mode: 1\n", wantErr: true},
		{name: "heuristic mode with remote settings is rejected", configYAML: "mode: heuristic\nremote:\n  provider: jev\n  api_key_env: TYPESAFE_API_KEY\n  timeout: 750ms\n  max_attempts_per_session: 1\n  lease_ttl: 2s\n  retry_backoff: 0s\n  positive_threshold: 0.9\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bundle, err := factory(featureConfigNode(t, tc.configYAML))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("factory accepted invalid config %q", tc.configYAML)
				}
				if !strings.Contains(err.Error(), sessionclassification.ID) {
					t.Fatalf("factory error = %v, want it attributed to %q", err, sessionclassification.ID)
				}
				return
			}
			if err != nil {
				t.Fatalf("factory rejected valid config %q: %v", tc.configYAML, err)
			}
			if bundle.SchemaVersion != aipfeature.SchemaVersionV1 {
				t.Fatalf("bundle schema version = %d, want %d", bundle.SchemaVersion, aipfeature.SchemaVersionV1)
			}
			if !bundle.PlaneSet.IsZero() {
				t.Fatalf("registry bundle published planes %+v, want none before generation composition", bundle.PlaneSet)
			}
		})
	}
}

// TestSessionClassificationDecodesCanonicalPluginRow pins requirement 8.1: the
// semantic configuration lives in the canonical plugins.features payload, with
// the outer registration enablement remaining authoritative.
func TestSessionClassificationDecodesCanonicalPluginRow(t *testing.T) {
	t.Parallel()

	const canonicalRow = "id: session-classification\nenabled: true\nconfig:\n  mode: heuristic\n  heuristic:\n    ignored_user_agent_prefixes: [internal-bot]\n"
	var row coreconfig.PluginConfig
	if err := yaml.Unmarshal([]byte(canonicalRow), &row); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if row.InstanceID() != sessionclassification.ID || !row.Enabled {
		t.Fatalf("canonical row = %+v, want an enabled %q registration", row, sessionclassification.ID)
	}
	cfg, err := sessionclassification.DecodeConfig(row.Config)
	if err != nil {
		t.Fatalf("DecodeConfig from the canonical plugins.features payload: %v", err)
	}
	if cfg.Mode != sessionclassification.ModeHeuristic || cfg.Remote != nil {
		t.Fatalf("canonical config = %+v, want heuristic mode without remote settings", cfg)
	}
	if len(cfg.Heuristic.IgnoredUserAgentPrefixes) != 1 || cfg.Heuristic.IgnoredUserAgentPrefixes[0] != "internal-bot" {
		t.Fatalf("canonical exclusions = %#v, want the single normalized literal prefix", cfg.Heuristic.IgnoredUserAgentPrefixes)
	}
}

// TestSessionClassificationFactoryRejectsInvalidConfigBeforeEscaping proves the
// registry factory fails closed on invalid configuration rather than serving a
// partially configured mode (requirement 8.4, design error table).
func TestSessionClassificationFactoryRejectsInvalidConfigBeforeEscaping(t *testing.T) {
	t.Parallel()

	factory := standardFeatureFactory(t, sessionclassification.ID)
	if _, err := factory(featureConfigNode(t, "mode: hybrid\n")); err == nil {
		t.Fatal("hybrid mode without remote settings was accepted")
	}
	bundle, err := factory(featureConfigNode(t, ""))
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if got := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneSessionClassifier); got != nil {
		t.Fatalf("registry bundle published a classifier %v without generation composition", got)
	}
}
