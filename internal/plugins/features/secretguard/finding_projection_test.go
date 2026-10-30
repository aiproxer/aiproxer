package secretguard

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

const projectedFindingCanary = "projected-finding-canary-secret"

func TestProjectSafeBetterLeaksFinding_UsesBoundedProvenanceOnly(t *testing.T) {
	t.Parallel()
	finding := betterLeaksFinding{
		RuleID:          "github-pat",
		Confidence:      sdk.ConfidenceHigh,
		Location:        "messages[0].parts[0]",
		OccurrenceCount: 1,
		occurrences: []betterLeaksOccurrence{{
			value: []byte(projectedFindingCanary),
		}},
	}
	got, err := projectSafeBetterLeaksFinding(finding, DetectorFacts{RuleIDs: []string{"github-pat"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.DetectorID != sdk.DetectorIDBetterLeaks || got.RuleID != "github-pat" || got.Confidence != sdk.ConfidenceHigh {
		t.Fatalf("provenance: %#v", got)
	}
	if got.SourceCategory != sdk.SourceCategoryUnknown || got.Location != finding.Location || got.OccurrenceCount != 1 {
		t.Fatalf("safe finding fields: %#v", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("projected finding must validate: %v", err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), projectedFindingCanary) || strings.Contains(string(raw), "occurrences") {
		t.Fatalf("private occurrence identity escaped safe finding: %s", raw)
	}
}

func TestProjectSafeBetterLeaksFinding_RejectsUntrustedMetadata(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		finding betterLeaksFinding
		facts   DetectorFacts
	}{
		{
			name: "unknown rule",
			finding: betterLeaksFinding{
				RuleID:          "attacker-rule",
				OccurrenceCount: 1,
			},
			facts: DetectorFacts{RuleIDs: []string{"github-pat"}},
		},
		{
			name: "invalid confidence",
			finding: betterLeaksFinding{
				RuleID:          "github-pat",
				Confidence:      "critical",
				OccurrenceCount: 1,
			},
			facts: DetectorFacts{RuleIDs: []string{"github-pat"}},
		},
		{
			name: "malicious location",
			finding: betterLeaksFinding{
				RuleID:          "github-pat",
				Location:        "messages[0]\n" + projectedFindingCanary,
				OccurrenceCount: 1,
			},
			facts: DetectorFacts{RuleIDs: []string{"github-pat"}},
		},
		{
			name: "zero occurrence",
			finding: betterLeaksFinding{
				RuleID:          "github-pat",
				OccurrenceCount: 0,
			},
			facts: DetectorFacts{RuleIDs: []string{"github-pat"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := projectSafeBetterLeaksFinding(tc.finding, tc.facts)
			if err == nil || !errors.Is(err, errSafeFindingProjection) {
				t.Fatalf("projection error = %v, want bounded projection error", err)
			}
			if strings.Contains(err.Error(), projectedFindingCanary) || strings.Contains(err.Error(), "attacker-rule") {
				t.Fatalf("projection error leaked untrusted metadata: %v", err)
			}
		})
	}
}

func TestProjectSafeBetterLeaksFinding_RequiresPinnedRuleInventory(t *testing.T) {
	t.Parallel()
	finding := betterLeaksFinding{RuleID: "github-pat", OccurrenceCount: 1}
	if _, err := projectSafeBetterLeaksFinding(finding, DetectorFacts{}); !errors.Is(err, errSafeFindingProjection) {
		t.Fatalf("projection with empty pinned inventory error = %v", err)
	}
}

func TestBetterLeaksScanner_ProjectSafeFindingUsesPinnedInventory(t *testing.T) {
	t.Parallel()
	detector := newTestBetterLeaksScanner(t)
	result, err := detector.scanFragments(t.Context(), []LogicalFragment{{
		Location: "messages[0].parts[0]",
		Raw:      []byte("GITHUB_TOKEN=" + adapterGitHubToken),
	}})
	if err != nil {
		t.Fatal(err)
	}
	finding, ok := findBetterLeaksProjectedFinding(result.Findings, "github-pat")
	if !ok {
		t.Fatal("scanner did not produce the pinned github-pat rule")
	}
	projected, err := detector.projectSafeFinding(finding)
	if err != nil {
		t.Fatal(err)
	}
	if projected.DetectorID != sdk.DetectorIDBetterLeaks || projected.RuleID != "github-pat" {
		t.Fatalf("projected provenance: %#v", projected)
	}
}
