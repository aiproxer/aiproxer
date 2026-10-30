package standardplugins

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"gopkg.in/yaml.v3"
)

func TestStandardBackends_exposeInventoryPrefixes(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	for _, id := range standardBackendFactoryIDs(t) {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			var node yaml.Node
			if err := yaml.Unmarshal([]byte(standardBackendBuildYAML(id)), &node); err != nil {
				t.Fatal(err)
			}
			be := buildStandardBackend(t, reg, id, node, nil)
			if len(be.BackendPrefixes) == 0 {
				t.Fatalf("BuildBackend(%q) BackendPrefixes is empty", id)
			}
			wantPrefix := standardBackendWantPrefix(id)
			if !slices.Contains(be.BackendPrefixes, wantPrefix) {
				t.Fatalf("BuildBackend(%q) BackendPrefixes = %#v, want prefix %q", id, be.BackendPrefixes, wantPrefix)
			}
			for _, prefix := range be.BackendPrefixes {
				prefix = strings.TrimSpace(prefix)
				if prefix == "" || strings.Contains(prefix, "/") || strings.Contains(prefix, ":") {
					t.Fatalf("BuildBackend(%q) invalid prefix %q", id, prefix)
				}
			}
		})
	}
}

func TestBuiltInOwnership_coversStandardBackendPrefixes(t *testing.T) {
	t.Parallel()

	owners := CollectBuiltInBackendOwners(nil)
	reserved := make(map[string]struct{}, len(owners))
	for _, owner := range owners {
		reserved[owner.Prefix] = struct{}{}
	}

	for _, id := range standardBackendFactoryIDs(t) {
		if IsCustomCompatibleBackendKind(id) {
			continue
		}
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			var node yaml.Node
			if err := yaml.Unmarshal([]byte(standardBackendBuildYAML(id)), &node); err != nil {
				t.Fatal(err)
			}
			reg := pluginreg.NewRegistry()
			if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
				t.Fatal(err)
			}
			be := buildStandardBackend(t, reg, id, node, nil)
			for _, prefix := range be.BackendPrefixes {
				if _, ok := reserved[prefix]; !ok {
					t.Fatalf("standard backend %q exposes prefix %q not covered by built-in ownership", id, prefix)
				}
			}
		})
	}
}

func standardBackendFactoryIDs(t *testing.T) []string {
	t.Helper()
	be := StandardBackendBundle(UpstreamAPIKeys{})
	out := make([]string, 0, len(be.Backends))
	for _, entry := range be.Backends {
		out = append(out, entry.ID)
	}
	slices.Sort(out)
	return out
}

func standardBackendBuildYAML(id string) string {
	switch id {
	case "anthropic", "openai-legacy", "openai-responses":
		return "base_url: http://127.0.0.1:9\n"
	case "gemini":
		return "base_url: http://127.0.0.1:9\n"
	case "bedrock":
		return "region: us-east-1\n"
	case CustomOpenAILegacyCompatibleID:
		return "backend_prefix: custom-openai-legacy\nbase_url: http://127.0.0.1:9/v1\n"
	case CustomOpenAIResponsesCompatibleID:
		return "backend_prefix: custom-openai-responses\nbase_url: http://127.0.0.1:9/v1\n"
	case CustomAnthropicCompatibleID:
		return "backend_prefix: custom-anthropic\nbase_url: http://127.0.0.1:9/v1\n"
	case CustomOpenResponsesCompatibleID:
		return "backend_prefix: custom-openresponses\nbase_url: http://127.0.0.1:9/openresponses/v1\n"
	case CustomSystemOneCompatibleID:
		return "backend_prefix: custom-systemone\nbase_url: http://127.0.0.1:9/v1\nmodels:\n  source: inline\n  items:\n    - canonical_id: jev\n      native_id: jev\n"
	default:
		return ""
	}
}

func buildStandardBackend(t *testing.T, reg *pluginreg.Registry, id string, node yaml.Node, client *http.Client) execbackend.Backend {
	t.Helper()
	if IsCustomCompatibleBackendKind(id) {
		res, err := reg.BuildBackendWithLifecycle(id, standardBackendWantPrefix(id), node, client, pluginreg.BackendFactoryDeps{})
		if err != nil {
			t.Fatalf("BuildBackendWithLifecycle(%q) error = %v", id, err)
		}
		return res.Backend
	}
	be, err := reg.BuildBackend(id, node, client, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatalf("BuildBackend(%q) error = %v", id, err)
	}
	return be
}

func standardBackendWantPrefix(id string) string {
	switch id {
	case CustomOpenAILegacyCompatibleID:
		return "custom-openai-legacy"
	case CustomOpenAIResponsesCompatibleID:
		return "custom-openai-responses"
	case CustomAnthropicCompatibleID:
		return "custom-anthropic"
	case CustomOpenResponsesCompatibleID:
		return "custom-openresponses"
	case CustomSystemOneCompatibleID:
		return "custom-systemone"
	default:
		return id
	}
}
