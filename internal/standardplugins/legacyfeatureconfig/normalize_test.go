package legacyfeatureconfig_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/internal/standardplugins/legacyfeatureconfig"
	"gopkg.in/yaml.v3"
)

func extractFeatureConfigNode(t *testing.T, rawYAML []byte, featureID string) yaml.Node {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal(rawYAML, &root); err != nil {
		t.Fatalf("unmarshal yaml: %v", err)
	}
	doc := &root
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			t.Fatal("empty document")
		}
		doc = doc.Content[0]
	}
	for i := 0; i < len(doc.Content); i += 2 {
		if doc.Content[i].Value == "plugins" {
			pluginsNode := doc.Content[i+1]
			for j := 0; j < len(pluginsNode.Content); j += 2 {
				if pluginsNode.Content[j].Value == "features" {
					featuresNode := pluginsNode.Content[j+1]
					for _, item := range featuresNode.Content {
						for k := 0; k < len(item.Content); k += 2 {
							if item.Content[k].Value == "id" && item.Content[k+1].Value == featureID {
								for c := 0; c < len(item.Content); c += 2 {
									if item.Content[c].Value == "config" {
										return *item.Content[c+1]
									}
								}
							}
						}
					}
				}
			}
		}
	}
	t.Fatalf("feature %q config node not found", featureID)
	return yaml.Node{}
}

func TestNormalize_Parity_OldAndNew(t *testing.T) {
	t.Parallel()

	oldYAML := `
interleaved:
  enabled: true
  stream_to_client: visible
  regular_turns_remaining: 5
  max_memo_bytes: 8192
  instructions_file: ./custom_thinker.md
`

	newYAML := `
plugins:
  features:
    - id: interleaved-thinking
      enabled: true
      config:
        enabled: true
        stream_to_client: visible
        regular_turns_remaining: 5
        max_memo_bytes: 8192
        instructions_file: ./custom_thinker.md
`

	// 1. Normalize old YAML
	normalizedBytes, err := legacyfeatureconfig.NormalizeYAML([]byte(oldYAML))
	if err != nil {
		t.Fatalf("NormalizeYAML oldYAML: %v", err)
	}

	// 2. Extract feature config node from normalized old YAML and decode
	oldConfigNode := extractFeatureConfigNode(t, normalizedBytes, interleavedthinking.ID)
	oldCfg, err := interleavedthinking.DecodeConfig(oldConfigNode)
	if err != nil {
		t.Fatalf("DecodeConfig old: %v", err)
	}

	// 3. Extract feature config node from canonical new YAML and decode
	newConfigNode := extractFeatureConfigNode(t, []byte(newYAML), interleavedthinking.ID)
	newCfg, err := interleavedthinking.DecodeConfig(newConfigNode)
	if err != nil {
		t.Fatalf("DecodeConfig new: %v", err)
	}

	// 4. Assert strict parity between old normalized and new canonical
	if !reflect.DeepEqual(oldCfg, newCfg) {
		t.Fatalf("config parity mismatch:\n old: %+v\n new: %+v", oldCfg, newCfg)
	}

	// 5. Check specific field values
	if !oldCfg.Enabled {
		t.Error("expected Enabled: true")
	}
	if oldCfg.StreamToClient != "visible" {
		t.Errorf("expected StreamToClient: visible, got %s", oldCfg.StreamToClient)
	}
	if oldCfg.RegularTurnsRemaining != 5 {
		t.Errorf("expected RegularTurnsRemaining: 5, got %d", oldCfg.RegularTurnsRemaining)
	}
	if oldCfg.MaxMemoBytes != 8192 {
		t.Errorf("expected MaxMemoBytes: 8192, got %d", oldCfg.MaxMemoBytes)
	}
	if oldCfg.InstructionsFile != "./custom_thinker.md" {
		t.Errorf("expected InstructionsFile: ./custom_thinker.md, got %s", oldCfg.InstructionsFile)
	}
}

func TestNormalize_Conflict(t *testing.T) {
	t.Parallel()

	conflictYAML := `
interleaved:
  enabled: true
  stream_to_client: hidden
plugins:
  features:
    - id: interleaved-thinking
      enabled: true
      config:
        enabled: true
        stream_to_client: visible
`

	_, err := legacyfeatureconfig.NormalizeYAML([]byte(conflictYAML))
	if err == nil {
		t.Fatal("expected conflict error when both old and new interleaved configurations exist")
	}
	if !strings.Contains(err.Error(), "both legacy top-level 'interleaved' and canonical 'plugins.features' entry") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestNormalize_Defaults(t *testing.T) {
	t.Parallel()

	minimalOldYAML := `
interleaved:
  enabled: true
`

	normalizedBytes, err := legacyfeatureconfig.NormalizeYAML([]byte(minimalOldYAML))
	if err != nil {
		t.Fatalf("NormalizeYAML minimalOldYAML: %v", err)
	}

	cfgNode := extractFeatureConfigNode(t, normalizedBytes, interleavedthinking.ID)
	cfg, err := interleavedthinking.DecodeConfig(cfgNode)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}

	if !cfg.Enabled {
		t.Error("expected Enabled: true")
	}
	if cfg.StreamToClient != interleavedthinking.DefaultStreamToClient {
		t.Errorf("StreamToClient = %q, want %q", cfg.StreamToClient, interleavedthinking.DefaultStreamToClient)
	}
	if cfg.RegularTurnsRemaining != interleavedthinking.DefaultRegularTurns {
		t.Errorf("RegularTurnsRemaining = %d, want %d", cfg.RegularTurnsRemaining, interleavedthinking.DefaultRegularTurns)
	}
	if cfg.MaxMemoBytes != interleavedthinking.DefaultMaxMemoBytes {
		t.Errorf("MaxMemoBytes = %d, want %d", cfg.MaxMemoBytes, interleavedthinking.DefaultMaxMemoBytes)
	}
}

func TestNormalize_NewUnchanged(t *testing.T) {
	t.Parallel()

	canonicalYAML := `
plugins:
  features:
    - id: interleaved-thinking
      enabled: true
      config:
        enabled: true
        stream_to_client: visible
`

	normalizedBytes, err := legacyfeatureconfig.NormalizeYAML([]byte(canonicalYAML))
	if err != nil {
		t.Fatalf("NormalizeYAML: %v", err)
	}

	cfgNode := extractFeatureConfigNode(t, normalizedBytes, interleavedthinking.ID)
	cfg, err := interleavedthinking.DecodeConfig(cfgNode)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}

	if cfg.StreamToClient != "visible" {
		t.Errorf("StreamToClient = %q, want visible", cfg.StreamToClient)
	}
}

func TestNormalize_NullNode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "explicit null",
			yaml: `
server:
  address: "127.0.0.1:0"
interleaved: null
`,
		},
		{
			name: "tilde null",
			yaml: `
server:
  address: "127.0.0.1:0"
interleaved: ~
`,
		},
		{
			name: "empty scalar null",
			yaml: `
server:
  address: "127.0.0.1:0"
interleaved:
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			normalizedBytes, err := legacyfeatureconfig.NormalizeYAML([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("unexpected error normalizing null node: %v", err)
			}

			var root yaml.Node
			if err := yaml.Unmarshal(normalizedBytes, &root); err != nil {
				t.Fatalf("unmarshal normalized yaml: %v", err)
			}

			doc := &root
			if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
				doc = doc.Content[0]
			}
			if doc.Kind == yaml.MappingNode {
				for i := 0; i < len(doc.Content); i += 2 {
					if doc.Content[i].Value == "interleaved" {
						t.Errorf("expected legacy 'interleaved' key to be removed")
					}
					if doc.Content[i].Value == "plugins" {
						pluginsNode := doc.Content[i+1]
						for j := 0; j < len(pluginsNode.Content); j += 2 {
							if pluginsNode.Content[j].Value == "features" {
								featuresNode := pluginsNode.Content[j+1]
								for _, item := range featuresNode.Content {
									for k := 0; k < len(item.Content); k += 2 {
										if item.Content[k].Value == "id" && item.Content[k+1].Value == interleavedthinking.ID {
											t.Errorf("expected NO feature entry synthesized for null node, but found %q", interleavedthinking.ID)
										}
									}
								}
							}
						}
					}
				}
			}
		})
	}
}

func TestNormalize_MalformedScalar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{
			name: "string scalar",
			input: `
interleaved: "invalid"
`,
		},
		{
			name: "unquoted string",
			input: `
interleaved: disabled
`,
		},
		{
			name: "number scalar",
			input: `
interleaved: 42
`,
		},
		{
			name: "boolean scalar",
			input: `
interleaved: true
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := legacyfeatureconfig.NormalizeYAML([]byte(tt.input))
			if err == nil {
				t.Fatal("expected error for malformed scalar legacy interleaved, got nil")
			}
			if !strings.Contains(err.Error(), "interleaved") {
				t.Fatalf("expected error message to name legacy key 'interleaved', got: %v", err)
			}
			if !strings.Contains(err.Error(), "must be a mapping") {
				t.Fatalf("expected error message to indicate it must be a mapping, got: %v", err)
			}
		})
	}
}

func TestNormalize_Keepwarm_Parity(t *testing.T) {
	t.Parallel()

	oldYAML := `
prompt_cache:
  keepwarm:
    enabled: true
    max_idle_duration: 2h
    renew_timeout: 3s
    max_refreshes_per_idle_epoch: 2
`

	newYAML := `
plugins:
  features:
    - id: keepwarm
      enabled: true
      config:
        enabled: true
        max_idle_duration: 2h
        renew_timeout: 3s
        max_refreshes_per_idle_epoch: 2
`

	normalizedBytes, err := legacyfeatureconfig.NormalizeYAML([]byte(oldYAML))
	if err != nil {
		t.Fatalf("NormalizeYAML oldYAML: %v", err)
	}

	oldConfigNode := extractFeatureConfigNode(t, normalizedBytes, keepwarm.ID)
	oldCfg, err := keepwarm.DecodeConfig(oldConfigNode)
	if err != nil {
		t.Fatalf("DecodeConfig old: %v", err)
	}

	newConfigNode := extractFeatureConfigNode(t, []byte(newYAML), keepwarm.ID)
	newCfg, err := keepwarm.DecodeConfig(newConfigNode)
	if err != nil {
		t.Fatalf("DecodeConfig new: %v", err)
	}

	if !reflect.DeepEqual(oldCfg, newCfg) {
		t.Fatalf("keepwarm parity mismatch:\n old: %+v\n new: %+v", oldCfg, newCfg)
	}
}

func TestNormalize_Keepwarm_Conflict(t *testing.T) {
	t.Parallel()

	conflictYAML := `
prompt_cache:
  keepwarm:
    enabled: true
plugins:
  features:
    - id: keepwarm
      enabled: true
`
	_, err := legacyfeatureconfig.NormalizeYAML([]byte(conflictYAML))
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "both legacy top-level") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestNormalize_Keepwarm_Null(t *testing.T) {
	t.Parallel()

	nullYAML := `
prompt_cache: null
`
	norm, err := legacyfeatureconfig.NormalizeYAML([]byte(nullYAML))
	if err != nil {
		t.Fatalf("NormalizeYAML: %v", err)
	}
	if strings.Contains(string(norm), "keepwarm") {
		t.Fatalf("expected no keepwarm synthesized for null prompt_cache, got: %s", string(norm))
	}
}
