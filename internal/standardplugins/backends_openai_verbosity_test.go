package standardplugins

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/identity"
	"gopkg.in/yaml.v3"
)

func TestOpenAIHostedYAML_acceptsDefaultVerbosity(t *testing.T) {
	t.Parallel()
	var root yaml.Node
	if err := yaml.Unmarshal([]byte("base_url: http://127.0.0.1:9/v1\ndefault_verbosity: medium\n"), &root); err != nil {
		t.Fatal(err)
	}
	if _, err := backendOpenAIResponses(root, nil, UpstreamAPIKeys{}, identity.Config{}); err != nil {
		t.Fatalf("valid default_verbosity should be accepted: %v", err)
	}
}

func TestOpenAIHostedYAML_rejectsInvalidDefaultVerbosity(t *testing.T) {
	t.Parallel()
	var root yaml.Node
	if err := yaml.Unmarshal([]byte("base_url: http://127.0.0.1:9/v1\ndefault_verbosity: extreme\n"), &root); err != nil {
		t.Fatal(err)
	}
	if _, err := backendOpenAILegacy(root, nil, UpstreamAPIKeys{}, identity.Config{}); err == nil {
		t.Fatal("invalid default_verbosity should be rejected")
	}
}
