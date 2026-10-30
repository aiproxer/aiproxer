package standardplugins

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/bedrock"
	"gopkg.in/yaml.v3"
)

func Test_decodeBedrockBackendYAML_regression(t *testing.T) {
	t.Parallel()
	raw := `region: us-east-1
access_key_id: AKIDTEST
secret_access_key: SECRETTEST
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	var y bedrockBackendYAML
	if err := config.DecodeYAMLNode(root, &y); err != nil {
		t.Fatal(err)
	}
	if y.Region != "us-east-1" || y.AccessKeyID != "AKIDTEST" || y.SecretAccessKey != "SECRETTEST" {
		t.Fatalf("unexpected decode: %+v", y)
	}
}

func Test_registryBuildBedrock_afterHostedKeyChanges(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.BackendSecurityProfile("acp"); ok {
		t.Fatal("acp must not remain a static registry factory after Phase 6 cutover")
	}

	brRaw := `region: us-east-1
access_key_id: AKIDTEST
secret_access_key: SECRETTEST
`
	var brNode yaml.Node
	if err := yaml.Unmarshal([]byte(brRaw), &brNode); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.BuildBackend(bedrock.ID, brNode, nil, pluginreg.BackendFactoryDeps{}); err != nil {
		t.Fatalf("bedrock BuildBackend: %v", err)
	}
}
