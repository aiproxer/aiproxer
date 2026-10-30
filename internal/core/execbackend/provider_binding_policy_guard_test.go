package execbackend_test

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
)

// policyYAML renders one backend_discovery policy configuration.
func policyYAML(t *testing.T, value string) yaml.Node {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("economic_evidence_policy: "+value+"\n"), &node); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return node
}

// TestConnectorEconomicEvidencePolicySetsMatch pins the canonical typed policy
// of internal/core/execbackend to the closed set the configuration decoder
// accepts. The configuration package cannot import the execbackend package
// without an import cycle, so the two sets are pinned here.
func TestConnectorEconomicEvidencePolicySetsMatch(t *testing.T) {
	t.Parallel()
	want := map[string]execbackend.ConnectorEconomicEvidencePolicy{
		"degrade": execbackend.ConnectorEconomicEvidenceDegrade,
		"strict":  execbackend.ConnectorEconomicEvidenceStrict,
	}
	for value, policy := range want {
		parsed, err := execbackend.ParseConnectorEconomicEvidencePolicy(value)
		if err != nil {
			t.Fatalf("parse %q: %v", value, err)
		}
		if parsed != policy {
			t.Fatalf("parse %q = %q, want %q", value, parsed, policy)
		}
	}
	if _, err := execbackend.ParseConnectorEconomicEvidencePolicy("refuse"); err == nil {
		t.Fatal("the canonical policy parser accepted an unknown value")
	}
	// The configuration decoder accepts exactly the same members.
	for _, value := range []string{"degrade", "strict"} {
		if _, err := config.DecodeBackendDiscovery(policyYAML(t, value)); err != nil {
			t.Fatalf("configuration rejected %q: %v", value, err)
		}
	}
	if _, err := config.DecodeBackendDiscovery(policyYAML(t, "refuse")); err == nil {
		t.Fatal("the configuration decoder accepted an unknown policy value")
	}
}
