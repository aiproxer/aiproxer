package configreload

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// Claim: startup-fixed bindings cannot rebind or capture through ordinary reload.
// Source: task 3.3 design. Mutant: classify only backend rows as reloadable.
func TestBackendProvisioning_RestartBeforeRebinding(t *testing.T) {
	for _, change := range []string{"add", "remove", "kind", "ref", "execution", "id", "row-kind", "enable"} {
		t.Run(change, func(t *testing.T) {
			makeConfig := func() *config.Config {
				return &config.Config{Plugins: config.PluginsConfig{
					Backends:         []config.PluginConfig{{ID: "agent", Kind: "agentkind"}},
					BackendDiscovery: config.BackendDiscoveryConfig{Instances: map[string]config.BackendInstanceProvisioning{"agent": {Kind: "agentkind", SecretRefs: map[string]config.BackendSecretRef{"key": {Env: "KEY"}}, ExecutionEnv: []string{"HOME"}}}},
				}}
			}
			a, c := makeConfig(), makeConfig()
			b := c.Plugins.BackendDiscovery.Instances["agent"]
			switch change {
			case "add":
				c.Plugins.BackendDiscovery.Instances["other"] = b
			case "remove":
				delete(c.Plugins.BackendDiscovery.Instances, "agent")
			case "kind":
				b.Kind = "other"
			case "ref":
				b.SecretRefs["key"] = config.BackendSecretRef{Env: "ROTATED"}
			case "execution":
				b.ExecutionEnv = []string{"HOME", "PATH"}
			case "id":
				c.Plugins.Backends[0].ID = "other"
			case "row-kind":
				c.Plugins.Backends[0].Kind = "other"
			case "enable":
				c.Plugins.Backends[0].Enabled = true
			}
			if change != "remove" {
				c.Plugins.BackendDiscovery.Instances["agent"] = b
			}
			_, err := Classify(a, c)
			if _, ok := errors.AsType[*RestartRequiredError](err); !ok {
				t.Fatalf("want restart, got %v", err)
			}
		})
	}
}
