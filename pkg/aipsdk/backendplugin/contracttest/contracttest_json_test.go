package contracttest

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/contract"
)

func TestCertificationResultJSONRoundTripAndValidation(t *testing.T) {
	t.Parallel()
	results := make([]ScenarioResult, 0, len(contract.BaselineScenarioCorpus()))
	for _, scenario := range contract.BaselineScenarioCorpus() {
		results = append(results, ScenarioResult{ID: string(scenario.ID), Executed: true, Rejected: true})
	}
	result := CertificationResult{
		PluginID: "test", Version: "1", Negotiated: backendplugin.Negotiation{Compatible: true},
		ScenarioResults: results,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded CertificationResult
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.ScenarioResults) != len(result.ScenarioResults) {
		t.Fatalf("roundtrip scenarios=%d want %d", len(decoded.ScenarioResults), len(result.ScenarioResults))
	}
	for i := range result.ScenarioResults {
		if decoded.ScenarioResults[i].ID != result.ScenarioResults[i].ID {
			t.Fatalf("scenario %d changed", i)
		}
	}
	// The executable runner tests validate complete artifacts; this test guards
	// that malformed or unknown/missing scenario IDs cannot be unmarshaled.
	for _, payload := range []string{
		`{"plugin_id":"test","version":"1","negotiated":{"compatible":true},"scenario_results":[{"id":"unknown","executed":true}]}`,
		`{"plugin_id":"test","version":"1","negotiated":{"compatible":true},"scenario_results":[{"executed":true}]}`,
	} {
		if err := json.Unmarshal([]byte(payload), &decoded); err == nil {
			t.Fatal("invalid scenario artifact accepted")
		}
	}
	if _, err := json.Marshal(CertificationResult{}); err == nil {
		t.Fatal("invalid certification marshaled")
	}
}
