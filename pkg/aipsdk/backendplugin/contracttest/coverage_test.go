package contracttest

import (
	"slices"
	"testing"
)

func TestConnectorFamilyCoverageManifestIsBounded(t *testing.T) {
	t.Parallel()
	if len(CurrentConnectorFamilyCoverage) == 0 {
		t.Fatal("connector coverage manifest is empty")
	}
	required := []string{"codex", "opencode", "ollama", "vllm", "huggingface", "acp-derivative"}
	families := make(map[string]bool, len(CurrentConnectorFamilyCoverage))
	for _, entry := range CurrentConnectorFamilyCoverage {
		if entry.ModulePath == "" || entry.Family == "" || entry.Subject == "" {
			t.Fatalf("incomplete coverage entry: %+v", entry)
		}
		families[entry.Family] = true
	}
	for _, family := range required {
		if !families[family] && !slices.ContainsFunc(CurrentConnectorFamilyCoverage, func(entry ConnectorFamilyCoverage) bool {
			return entry.Subject == family
		}) {
			t.Fatalf("required connector family %q has no coverage entry", family)
		}
	}
}
