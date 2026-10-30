package secretguard

import (
	"bytes"
	"cmp"
	"errors"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

var errPrivateOccurrenceSpan = errors.New("secretguard: exact occurrence span unavailable")

// exactPrivateFinding is the feature-private bridge from the exact matcher.
// Its occurrences contain request-local identity only and are discarded before
// a safe SDK finding is returned.
type exactPrivateFinding struct {
	finding        sdk.Finding
	occurrences    []betterLeaksOccurrence
	uncoveredCount int
}

// privateHybridFinding is the merger's transient representation. Occurrences
// are intentionally retained only until public projection has completed.
type privateHybridFinding struct {
	finding       sdk.Finding
	occurrences   []betterLeaksOccurrence
	fallbackCount int
}

func spanForByteRange(raw []byte, start, end int) (betterLeaksSpan, error) {
	return spanForByteRangeWithLocationIndex(raw, newBetterLeaksLocationIndex(raw), start, end)
}

func spanForByteRangeWithLocationIndex(raw []byte, locationIndex *betterLeaksLocationIndex, start, end int) (betterLeaksSpan, error) {
	if start < 0 || end <= start || end > len(raw) {
		return betterLeaksSpan{}, errPrivateOccurrenceSpan
	}
	if locationIndex == nil {
		return betterLeaksSpan{}, errPrivateOccurrenceSpan
	}
	return locationIndex.spanForByteRange(start, end)
}

// mergeHybridFindings performs the complete private merge and safe projection.
// BetterLeaks facts are supplied by the immutable generation so untrusted rule
// metadata cannot cross the SDK boundary.
func mergeHybridFindings(exact []exactPrivateFinding, discovery []betterLeaksFinding, facts DetectorFacts) ([]sdk.Finding, error) {
	return projectMergedHybridFindings(mergePrivateHybridFindings(exact, discovery), facts)
}

// mergePrivateHybridFindings deduplicates concrete values and spans while they
// are still private. Equal value/span pairs in different canonical locations
// remain distinct because location is part of the identity boundary.
func mergePrivateHybridFindings(exact []exactPrivateFinding, discovery []betterLeaksFinding) []privateHybridFinding {
	exactGroups := make([]privateHybridFinding, 0, len(exact))
	exactIndexes := make(map[privateFindingKey]int, len(exact))
	for _, input := range exact {
		finding := input.finding
		if finding.DetectorID == "" {
			finding.DetectorID = sdk.DetectorIDExact
		}
		occurrences := input.occurrences
		fallback := positiveOccurrenceCount(input.uncoveredCount)
		if len(occurrences) == 0 && fallback == 0 {
			fallback = positiveOccurrenceCount(finding.OccurrenceCount)
		}
		finding.OccurrenceCount = len(occurrences) + fallback
		if finding.OccurrenceCount == 0 {
			continue
		}
		key := privateFindingGroupKey(finding)
		index, exists := exactIndexes[key]
		if !exists {
			exactIndexes[key] = len(exactGroups)
			exactGroups = append(exactGroups, privateHybridFinding{finding: finding, occurrences: append([]betterLeaksOccurrence(nil), occurrences...), fallbackCount: fallback})
			continue
		}
		group := &exactGroups[index]
		group.occurrences = append(group.occurrences, occurrences...)
		group.fallbackCount += fallback
		group.finding.OccurrenceCount = len(group.occurrences) + group.fallbackCount
	}

	// Normalize each accumulated group once. The overlap index contains only
	// request-private value/field/span identities and lists actual attributions.
	type overlapKey struct {
		location   string
		occurrence privateOccurrenceKey
	}
	overlaps := make(map[overlapKey][]int)
	if len(discovery) > 0 {
		for i := range exactGroups {
			group := &exactGroups[i]
			group.occurrences = uniquePrivateOccurrences(group.occurrences)
			for _, occurrence := range group.occurrences {
				key := overlapKey{group.finding.Location, occurrenceKey(occurrence)}
				overlaps[key] = append(overlaps[key], i)
			}
		}
	}

	// Normalize BetterLeaks reports before matching them against exact spans.
	// Sorting makes duplicate-report handling independent of worker callback order.
	orderedDiscovery := append([]betterLeaksFinding(nil), discovery...)
	sort.SliceStable(orderedDiscovery, func(i, j int) bool {
		return compareBetterLeaksFindings(orderedDiscovery[i], orderedDiscovery[j]) < 0
	})

	discoveryGroups := make([]privateHybridFinding, 0, len(orderedDiscovery))
	discoveryIndexes := make(map[privateFindingKey]int, len(orderedDiscovery))
	for _, input := range orderedDiscovery {
		all := uniquePrivateOccurrences(input.occurrences)
		hasPrimary := false
		for _, occurrence := range all {
			hasPrimary = hasPrimary || occurrence.role == betterLeaksOccurrencePrimary
		}
		residual := make([]betterLeaksOccurrence, 0, len(all))
		for _, occurrence := range all {
			key := overlapKey{input.Location, occurrenceKey(occurrence)}
			matches, matched := overlaps[key]
			for _, index := range matches {
				attachBetterLeaksProvenance(&exactGroups[index].finding, input)
			}
			// Reports are ordered by rule/confidence, so the first report
			// supplies the winning provenance. Retain membership but consume
			// attribution lists once, including duplicate multipart reports.
			if len(matches) > 0 {
				overlaps[key] = nil
			}
			if !matched && (!hasPrimary || occurrence.role == betterLeaksOccurrencePrimary) {
				residual = append(residual, occurrence)
			}
		}

		primaryResidual := primaryPrivateOccurrences(residual)
		fallback := 0
		count := len(primaryResidual)
		if len(all) == 0 {
			fallback = positiveOccurrenceCount(input.OccurrenceCount)
			count = fallback
		}
		if count == 0 {
			// A report whose only concrete occurrence overlapped an exact match
			// has already been represented by the exact result.
			continue
		}
		finding := sdk.Finding{
			SourceCategory:  sdk.SourceCategoryUnknown,
			Location:        input.Location,
			OccurrenceCount: count,
			DetectorID:      sdk.DetectorIDBetterLeaks,
			RuleID:          input.RuleID,
			Confidence:      input.Confidence,
		}
		key := privateFindingGroupKey(finding)
		index, exists := discoveryIndexes[key]
		if !exists {
			discoveryIndexes[key] = len(discoveryGroups)
			discoveryGroups = append(discoveryGroups, privateHybridFinding{finding: finding, occurrences: residual, fallbackCount: fallback})
			continue
		}
		group := &discoveryGroups[index]
		group.occurrences = append(group.occurrences, residual...)
		group.fallbackCount += fallback
	}

	merged := append(exactGroups, discoveryGroups...)
	for i := range merged {
		merged[i].occurrences = sortedPrivateOccurrences(uniquePrivateOccurrences(merged[i].occurrences))
		if merged[i].fallbackCount > 0 && len(merged[i].occurrences) == 0 {
			merged[i].finding.OccurrenceCount = merged[i].fallbackCount
		} else if merged[i].finding.DetectorID == sdk.DetectorIDBetterLeaks {
			merged[i].finding.OccurrenceCount = len(primaryPrivateOccurrences(merged[i].occurrences))
			if merged[i].finding.OccurrenceCount == 0 {
				merged[i].finding.OccurrenceCount = len(merged[i].occurrences)
			}
		} else {
			merged[i].finding.OccurrenceCount = len(merged[i].occurrences) + merged[i].fallbackCount
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return comparePrivateFindings(merged[i], merged[j]) < 0
	})
	return merged
}

func projectMergedHybridFindings(private []privateHybridFinding, facts DetectorFacts) ([]sdk.Finding, error) {
	if len(private) == 0 {
		return nil, nil
	}
	out := make([]sdk.Finding, 0, len(private))
	for _, item := range private {
		finding := item.finding
		if finding.DetectorID == sdk.DetectorIDBetterLeaks {
			projected, err := projectSafeBetterLeaksFinding(betterLeaksFinding{
				RuleID:          finding.RuleID,
				Confidence:      finding.Confidence,
				Location:        finding.Location,
				OccurrenceCount: finding.OccurrenceCount,
			}, facts)
			if err != nil {
				return nil, err
			}
			out = append(out, projected)
			continue
		}
		if err := finding.Validate(); err != nil {
			return nil, errSafeFindingProjection
		}
		out = append(out, finding)
	}
	return out, nil
}

func attachBetterLeaksProvenance(exact *sdk.Finding, discovery betterLeaksFinding) {
	if exact == nil {
		return
	}
	if exact.RuleID == "" || discovery.RuleID < exact.RuleID || (discovery.RuleID == exact.RuleID && discovery.Confidence < exact.Confidence) {
		exact.RuleID = discovery.RuleID
		exact.Confidence = discovery.Confidence
	}
}

func primaryPrivateOccurrences(occurrences []betterLeaksOccurrence) []betterLeaksOccurrence {
	var primary []betterLeaksOccurrence
	for _, occurrence := range occurrences {
		if occurrence.role == betterLeaksOccurrencePrimary {
			primary = append(primary, occurrence)
		}
	}
	if len(primary) == 0 {
		return occurrences
	}
	return uniquePrivateOccurrences(primary)
}

func samePrivateOccurrence(occurrences []betterLeaksOccurrence, want betterLeaksOccurrence) bool {
	for _, occurrence := range occurrences {
		if privateOccurrenceEqual(occurrence, want) {
			return true
		}
	}
	return false
}

// privateOccurrenceEqual deliberately ignores detector role, rule and decode
// representation: the same bytes at the same span are one concrete occurrence.
func privateOccurrenceEqual(left, right betterLeaksOccurrence) bool {
	return left.fieldID == right.fieldID && left.span == right.span && bytes.Equal(left.value, right.value)
}

func uniquePrivateOccurrences(in []betterLeaksOccurrence) []betterLeaksOccurrence {
	if len(in) == 0 {
		return nil
	}
	out := make([]betterLeaksOccurrence, 0, len(in))
	seen := make(map[privateOccurrenceKey]struct{}, len(in))
	for _, occurrence := range in {
		key := occurrenceKey(occurrence)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		occurrence.value = bytes.Clone(occurrence.value)
		out = append(out, occurrence)
	}
	return out
}

// privateOccurrenceKey encodes complete private identity, not a public hash.
// String conversion owns its bytes; no unsafe alias or request-external state is used.
type privateOccurrenceKey struct {
	fieldID string
	span    betterLeaksSpan
	value   string
}

func occurrenceKey(occurrence betterLeaksOccurrence) privateOccurrenceKey {
	return privateOccurrenceKey{occurrence.fieldID, occurrence.span, string(occurrence.value)}
}

func sortedPrivateOccurrences(in []betterLeaksOccurrence) []betterLeaksOccurrence {
	out := append([]betterLeaksOccurrence(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i], out[j]
		if left.span != right.span {
			return compareBetterLeaksSpans(left.span, right.span) < 0
		}
		if c := bytes.Compare(left.value, right.value); c != 0 {
			return c < 0
		}
		if left.ruleID != right.ruleID {
			return left.ruleID < right.ruleID
		}
		if left.role != right.role {
			return left.role < right.role
		}
		return left.representation < right.representation
	})
	return out
}

type privateFindingKey struct {
	location, detectorID, reference string
	source                          sdk.SourceCategory
	ruleID, confidence, aliases     string
}

func privateFindingGroupKey(finding sdk.Finding) privateFindingKey {
	// Length prefixes distinguish arbitrary alias bytes and list boundaries.
	var aliases strings.Builder
	for _, alias := range finding.Aliases {
		aliases.WriteString(strconv.Itoa(len(alias)))
		aliases.WriteByte(':')
		aliases.WriteString(alias)
	}
	return privateFindingKey{
		finding.Location, finding.DetectorID, finding.SecretRefName,
		finding.SourceCategory, finding.RuleID, finding.Confidence, aliases.String(),
	}
}

func comparePrivateFindings(left, right privateHybridFinding) int {
	if c := cmp.Compare(left.finding.Location, right.finding.Location); c != 0 {
		return c
	}
	if c := cmp.Compare(detectorPriority(left.finding.DetectorID), detectorPriority(right.finding.DetectorID)); c != 0 {
		return c
	}
	if c := cmp.Compare(left.finding.RuleID, right.finding.RuleID); c != 0 {
		return c
	}
	if c := cmp.Compare(left.finding.SecretRefName, right.finding.SecretRefName); c != 0 {
		return c
	}
	if c := cmp.Compare(string(left.finding.SourceCategory), string(right.finding.SourceCategory)); c != 0 {
		return c
	}
	if c := cmp.Compare(left.finding.Confidence, right.finding.Confidence); c != 0 {
		return c
	}
	if c := compareStringSlices(left.finding.Aliases, right.finding.Aliases); c != 0 {
		return c
	}
	if c := cmp.Compare(left.finding.OccurrenceCount, right.finding.OccurrenceCount); c != 0 {
		return c
	}
	return compareSortedPrivateOccurrenceLists(left.occurrences, right.occurrences)
}

func compareStringSlices(left, right []string) int {
	for i := 0; i < len(left) && i < len(right); i++ {
		if c := cmp.Compare(left[i], right[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(left), len(right))
}

func comparePrivateOccurrenceLists(left, right []betterLeaksOccurrence) int {
	left = sortedPrivateOccurrences(left)
	right = sortedPrivateOccurrences(right)
	return compareSortedPrivateOccurrenceLists(left, right)
}

func compareSortedPrivateOccurrenceLists(left, right []betterLeaksOccurrence) int {
	for i := 0; i < len(left) && i < len(right); i++ {
		if c := compareBetterLeaksSpans(left[i].span, right[i].span); c != 0 {
			return c
		}
		if c := bytes.Compare(left[i].value, right[i].value); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(left), len(right))
}

func compareBetterLeaksSpans(left, right betterLeaksSpan) int {
	for _, pair := range [][2]int{{left.StartLine, right.StartLine}, {left.EndLine, right.EndLine}, {left.StartColumn, right.StartColumn}, {left.EndColumn, right.EndColumn}} {
		if c := cmp.Compare(pair[0], pair[1]); c != 0 {
			return c
		}
	}
	return 0
}

func detectorPriority(detector string) int {
	if detector == sdk.DetectorIDExact || detector == "" {
		return 0
	}
	return 1
}

func positiveOccurrenceCount(count int) int {
	if count < 0 {
		return 0
	}
	return count
}

func compareBetterLeaksFindings(left, right betterLeaksFinding) int {
	if c := cmp.Compare(left.Location, right.Location); c != 0 {
		return c
	}
	if c := cmp.Compare(left.RuleID, right.RuleID); c != 0 {
		return c
	}
	if c := cmp.Compare(left.Confidence, right.Confidence); c != 0 {
		return c
	}
	return comparePrivateOccurrenceLists(left.occurrences, right.occurrences)
}
