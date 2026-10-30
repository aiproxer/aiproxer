package modelcatalog

import (
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// MatchResult classifies catalog matching for a route model (design §Matcher).
type MatchResult struct {
	Kind       MatchKind
	InputModel string
	MatchedID  string
	Candidates []string
}

// Matcher resolves route model strings to catalog entries without mutating the candidate.
type Matcher interface {
	Match(candidate routing.AttemptCandidate, index *SnapshotIndex) MatchResult
}

// DefaultMatcher implements exact-then-normalized deterministic matching.
type DefaultMatcher struct{}

var _ Matcher = DefaultMatcher{}

// Match implements [Matcher].
func (DefaultMatcher) Match(candidate routing.AttemptCandidate, index *SnapshotIndex) MatchResult {
	input := strings.TrimSpace(candidate.Primary.Model)
	if index == nil || input == "" {
		return MatchResult{Kind: MatchNoMatch, InputModel: input}
	}
	if _, ok := index.byCatalogModelID[input]; ok {
		return MatchResult{
			Kind:       MatchExact,
			InputModel: input,
			MatchedID:  input,
			Candidates: []string{input},
		}
	}
	nr := NormalizeStripOneProviderPrefix(input)
	ids := index.catalogIDsForNormalized(nr)
	switch len(ids) {
	case 0:
		return MatchResult{Kind: MatchNoMatch, InputModel: input}
	case 1:
		return MatchResult{
			Kind:       MatchNonExact,
			InputModel: input,
			MatchedID:  ids[0],
			Candidates: []string{ids[0]},
		}
	default:
		out := append([]string(nil), ids...)
		return MatchResult{
			Kind:       MatchAmbiguous,
			InputModel: input,
			Candidates: out,
		}
	}
}

// NormalizeStripOneProviderPrefix removes one leading `provider/` segment (first '/').
// If there is no '/', the string is returned trimmed unchanged.
func NormalizeStripOneProviderPrefix(s string) string {
	s = strings.TrimSpace(s)
	_, after, ok := strings.Cut(s, "/")
	if !ok {
		return s
	}
	return after
}

func buildNormToIDs(byID map[string]ModelFacts) map[string][]string {
	normToIDs := make(map[string][]string, len(byID))
	for id := range byID {
		n := NormalizeStripOneProviderPrefix(id)
		normToIDs[n] = append(normToIDs[n], id)
	}
	for n, list := range normToIDs {
		slices.Sort(list)
		normToIDs[n] = list
	}
	return normToIDs
}

func buildSuffixToIDs(byID map[string]ModelFacts) map[string][]string {
	suffixToIDs := make(map[string][]string, len(byID))
	for id := range byID {
		suffix := NormalizeStripOneProviderPrefix(id)
		for _, key := range SuffixLookupKeys(suffix) {
			suffixToIDs[key] = append(suffixToIDs[key], id)
		}
	}
	for key, list := range suffixToIDs {
		slices.Sort(list)
		suffixToIDs[key] = list
	}
	return suffixToIDs
}

func (s *SnapshotIndex) catalogIDsForNormalized(normalized string) []string {
	if s == nil || s.normToIDs == nil {
		return nil
	}
	return s.normToIDs[normalized]
}
