package secretguard

import (
	"errors"
	"slices"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// errSafeFindingProjection is intentionally content-free. It is the only
// failure classification exposed by the feature-private safe projector, so
// malformed upstream metadata cannot become an error-string exfiltration path.
var errSafeFindingProjection = errors.New("secretguard: safe finding projection failed")

// projectSafeBetterLeaksFinding converts a feature-private BetterLeaks
// projection into the public SDK vocabulary. Only bounded provenance crosses
// this boundary; private occurrence values, spans, and decoder state are not
// copied or reachable from the returned Finding.
func projectSafeBetterLeaksFinding(finding betterLeaksFinding, facts DetectorFacts) (sdk.Finding, error) {
	if finding.RuleID == "" || !containsPinnedRule(facts.RuleIDs, finding.RuleID) {
		return sdk.Finding{}, errSafeFindingProjection
	}
	projected := sdk.Finding{
		SourceCategory:  sdk.SourceCategoryUnknown,
		Location:        finding.Location,
		OccurrenceCount: finding.OccurrenceCount,
		DetectorID:      sdk.DetectorIDBetterLeaks,
		RuleID:          finding.RuleID,
		Confidence:      finding.Confidence,
	}
	if err := projected.Validate(); err != nil {
		return sdk.Finding{}, errSafeFindingProjection
	}
	return projected, nil
}

// projectSafeFinding applies the generation's pinned rule inventory to one
// private BetterLeaks report. The scanner handle is immutable and its facts
// are request-independent, so the returned SDK finding remains safe to copy.
func (s *betterLeaksScanner) projectSafeFinding(finding betterLeaksFinding) (sdk.Finding, error) {
	if s == nil {
		return sdk.Finding{}, errSafeFindingProjection
	}
	return projectSafeBetterLeaksFinding(finding, s.facts)
}

func containsPinnedRule(ruleIDs []string, want string) bool {
	return slices.Contains(ruleIDs, want)
}
