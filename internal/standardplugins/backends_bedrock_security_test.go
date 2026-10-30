package standardplugins

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/identity"
	"gopkg.in/yaml.v3"
)

func buildBedrockFromYAML(t *testing.T, raw string) error {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	_, err := backendBedrock(root, nil, identity.Config{})
	return err
}

const bedrockTestCredsYAML = "region: us-east-1\naccess_key_id: AKIDTEST\nsecret_access_key: SECRETTEST\n"

func TestBackendBedrock_rejectsDisableHTTPSWithoutBaseEndpoint(t *testing.T) {
	t.Parallel()
	err := buildBedrockFromYAML(t, bedrockTestCredsYAML+"disable_https: true\n")
	if err == nil || !strings.Contains(err.Error(), "base_endpoint") {
		t.Fatalf("expected base_endpoint error, got %v", err)
	}
}

func TestBackendBedrock_rejectsNonLoopbackInsecureEndpointWithoutFlag(t *testing.T) {
	t.Parallel()
	err := buildBedrockFromYAML(t, bedrockTestCredsYAML+"disable_https: true\nbase_endpoint: http://example.com\n")
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected loopback policy error, got %v", err)
	}
}

func TestBackendBedrock_allowsNonLoopbackInsecureEndpointWithFlag(t *testing.T) {
	t.Parallel()
	err := buildBedrockFromYAML(t, bedrockTestCredsYAML+"disable_https: true\nbase_endpoint: http://example.com\nallow_insecure_non_loopback: true\n")
	if err != nil {
		t.Fatal(err)
	}
}

func TestBackendBedrock_allowsLoopbackInsecureEndpoints(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"http://127.0.0.1:9", "http://localhost:8080", "http://[::1]:1234"} {
		err := buildBedrockFromYAML(t, bedrockTestCredsYAML+"disable_https: true\nbase_endpoint: "+base+"\n")
		if err != nil {
			t.Fatalf("base %q: %v", base, err)
		}
	}
}
