package standardplugins

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// TestProxyMemoryRegistrationValidatesRoutes pins design §14.5: the standard
// bundle registers proxy-memory with a factory that decodes and validates the
// subtree before an empty bundle escapes, so a row missing a required route is
// rejected at generation compile instead of publishing a half-configured
// feature (Requirement 1.8). The concrete capture/worker/service wiring needs
// process-owned resources, so the factory contributes no plane (design §14.3).
func TestProxyMemoryRegistrationValidatesRoutes(t *testing.T) {
	t.Parallel()

	factory := standardFeatureFactory(t, proxymemory.ID)
	cases := []struct {
		name       string
		configYAML string
		wantErr    bool
	}{
		{
			name:       "missing consolidate route is rejected",
			configYAML: "principals: [alice]\nextract_route: \"openai:gpt-5-mini\"\n",
			wantErr:    true,
		},
		{
			name:       "missing extract route is rejected",
			configYAML: "principals: [alice]\nconsolidate_route: \"openai:gpt-5\"\n",
			wantErr:    true,
		},
		{
			name:       "blank route is rejected",
			configYAML: "principals: [alice]\nextract_route: \"   \"\nconsolidate_route: \"openai:gpt-5\"\n",
			wantErr:    true,
		},
		{
			name:       "valid row validates and publishes no plane",
			configYAML: "principals: [alice, bob]\nextract_route: \"openai:gpt-5-mini\"\nconsolidate_route: \"openai:gpt-5\"\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bundle, err := factory(featureConfigNode(t, tc.configYAML))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("factory accepted invalid config %q", tc.configYAML)
				}
				if !strings.Contains(err.Error(), proxymemory.ID) {
					t.Fatalf("factory error = %v, want it attributed to %q", err, proxymemory.ID)
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
