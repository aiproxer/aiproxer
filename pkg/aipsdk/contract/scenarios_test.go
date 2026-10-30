package contract

import (
	"encoding/json"
	"testing"
)

func TestBaselineScenarioCorpusJSONRoundTrip(t *testing.T) {
	t.Parallel()
	corpus := BaselineScenarioCorpus()
	encoded, err := json.Marshal(corpus)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []ScenarioDescriptor
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(corpus) {
		t.Fatalf("decoded=%d want %d", len(decoded), len(corpus))
	}
	for i := range corpus {
		if decoded[i].ID != corpus[i].ID || decoded[i].Feature != corpus[i].Feature || decoded[i].Transport != corpus[i].Transport {
			t.Fatalf("scenario %d changed: %#v != %#v", i, decoded[i], corpus[i])
		}
	}
}

func TestBaselineScenarioCorpusSchema(t *testing.T) {
	t.Parallel()
	for _, scenario := range BaselineScenarioCorpus() {
		if scenario.ID == "" || scenario.Feature == "" || scenario.Transport == "" {
			t.Fatalf("incomplete scenario: %#v", scenario)
		}
	}
}

// TestBaselineScenarioCorpusTerminalRows pins the terminal-semantics
// promotion: the additive terminal block with stable IDs, reused feature
// labels, and declared transports. Pre-promotion consumers pin their IDs,
// so the terminal rows stay appended after every pre-promotion row.
func TestBaselineScenarioCorpusTerminalRows(t *testing.T) {
	t.Parallel()
	corpus := BaselineScenarioCorpus()
	want := []ScenarioDescriptor{
		{ID: "terminal-clean-text", Feature: FeatureText, Transport: TransportHTTP},
		{ID: "terminal-clean-tool-call", Feature: FeatureTools, Transport: TransportHTTP},
		{ID: "terminal-output-limit", Feature: FeatureText, Transport: TransportHTTP},
		{ID: "terminal-upstream-error", Feature: FeatureErrors, Transport: TransportStreaming},
		{ID: "terminal-usage-reported", Feature: FeatureUsage, Transport: TransportStreaming},
		{ID: "terminal-content-filter", Feature: FeatureErrors, Transport: TransportHTTP},
		{ID: "terminal-empty-turn", Feature: FeatureText, Transport: TransportHTTP},
		{ID: "terminal-error-taxonomy", Feature: FeatureErrors, Transport: TransportHTTP},
	}
	if len(corpus) < len(want) {
		t.Fatalf("corpus=%d scenarios, want at least %d", len(corpus), len(want))
	}
	tail := corpus[len(corpus)-len(want):]
	for i, w := range want {
		if tail[i].ID != w.ID || tail[i].Feature != w.Feature || tail[i].Transport != w.Transport {
			t.Fatalf("terminal row %d = (%q, %q, %q), want (%q, %q, %q)",
				i, tail[i].ID, tail[i].Feature, tail[i].Transport, w.ID, w.Feature, w.Transport)
		}
		if len(tail[i].Prerequisites) == 0 || len(tail[i].ExpectedEvidence) == 0 {
			t.Fatalf("terminal row %q lacks prerequisites/expected evidence", tail[i].ID)
		}
	}
	newIDs := map[ScenarioID]bool{}
	for _, w := range want {
		newIDs[w.ID] = true
	}
	for _, sc := range corpus[:len(corpus)-len(want)] {
		if newIDs[sc.ID] {
			t.Fatalf("promoted row %q is not appended after every pre-promotion row", sc.ID)
		}
	}
}
