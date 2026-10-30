package config_test

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestRegistrationsFromConfigPassesOpaquePluginConfigThrough(t *testing.T) {
	t.Parallel()

	const yamlDoc = `
server:
  address: ":9999"
plugins:
  frontends:
    - id: test-frontend
      enabled: true
      config:
        vendor_only:
          nested: [1, 2]
          flag: true
`

	var cfg config.Config
	if err := yaml.Unmarshal([]byte(yamlDoc), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	regs := config.RegistrationsFromConfig(&cfg)
	if len(regs) != 1 {
		t.Fatalf("expected 1 registration, got %d", len(regs))
	}

	enc, err := yaml.Marshal(&regs[0].Config.Node)
	if err != nil {
		t.Fatalf("marshal opaque node: %v", err)
	}

	out := string(enc)
	if !strings.Contains(out, "vendor_only") || !strings.Contains(out, "nested") {
		t.Fatalf("opaque config subtree not preserved in yaml output:\n%s", out)
	}
}

func TestRegistrationsFromConfigNilConfigReturnsNil(t *testing.T) {
	t.Parallel()

	if regs := config.RegistrationsFromConfig(nil); regs != nil {
		t.Fatalf("expected nil slice, got %#v", regs)
	}
}
