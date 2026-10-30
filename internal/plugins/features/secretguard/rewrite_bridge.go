package secretguard

import (
	"bytes"
	"context"
	"errors"
	"sort"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

var errBetterLeaksUnrewritable = errors.New("secretguard: betterleaks finding has no safely rewritable literal representation")

func betterLeaksLiteralCandidates(fragment LogicalFragment, findings []betterLeaksFinding) []betterLeaksOccurrence {
	if (fragment.Text == "" && len(fragment.Raw) == 0) || len(findings) == 0 {
		return nil
	}
	raw := fragment.rawBytes()
	var out []betterLeaksOccurrence
	var locationIndex *betterLeaksLocationIndex
	for _, finding := range findings {
		if finding.Location != fragment.Location {
			continue
		}
		for _, occurrence := range finding.occurrences {
			if occurrence.fieldID != "" && occurrence.fieldID != fragment.privateID {
				continue
			}
			if occurrence.representation != betterLeaksOccurrenceLiteral || len(occurrence.value) == 0 {
				continue
			}
			if !occurrence.offsetsValid {
				if locationIndex == nil {
					locationIndex = newBetterLeaksLocationIndex(raw)
				}
			}
			start, end, ok := literalBetterLeaksByteRangeWithLocationIndex(raw, occurrence, locationIndex)
			if !ok {
				continue
			}
			if !occurrence.offsetsValid || occurrence.start != start || occurrence.end != end {
				occurrence.start, occurrence.end = start, end
				occurrence.offsetsValid = true
				if locationIndex == nil {
					locationIndex = newBetterLeaksLocationIndex(raw)
				}
				occurrence.span, _ = spanForByteRangeWithLocationIndex(raw, locationIndex, start, end)
			}
			out = append(out, occurrence)
		}
	}
	return uniquePrivateOccurrences(out)
}

func normalizeBetterLeaksLiteralOccurrence(raw []byte, occurrence *betterLeaksOccurrence) {
	normalizeBetterLeaksLiteralOccurrenceWithLocationIndex(raw, occurrence, nil)
}

func normalizeBetterLeaksLiteralOccurrenceWithLocationIndex(raw []byte, occurrence *betterLeaksOccurrence, locationIndex *betterLeaksLocationIndex) {
	if occurrence == nil || occurrence.representation != betterLeaksOccurrenceLiteral || len(raw) == 0 || len(occurrence.value) == 0 {
		return
	}
	if occurrence.offsetsValid && occurrence.end > occurrence.start && occurrence.end <= len(raw) && bytes.Equal(raw[occurrence.start:occurrence.end], occurrence.value) {
		return
	}
	if locationIndex == nil {
		locationIndex = newBetterLeaksLocationIndex(raw)
	}
	if start, end, ok := literalBetterLeaksByteRangeWithLocationIndex(raw, *occurrence, locationIndex); ok {
		occurrence.start, occurrence.end = start, end
		occurrence.offsetsValid = true
		if span, err := spanForByteRangeWithLocationIndex(raw, locationIndex, start, end); err == nil {
			occurrence.span = span
		}
	}
}

func literalBetterLeaksByteRange(raw []byte, occurrence betterLeaksOccurrence) (int, int, bool) {
	return literalBetterLeaksByteRangeWithLocationIndex(raw, occurrence, nil)
}

func literalBetterLeaksByteRangeWithLocationIndex(raw []byte, occurrence betterLeaksOccurrence, locationIndex *betterLeaksLocationIndex) (int, int, bool) {
	if start, end, ok := betterLeaksOccurrenceByteRange(raw, occurrence, locationIndex); ok && bytes.Equal(raw[start:end], occurrence.value) {
		return start, end, true
	}
	reportedStart, reportedEnd, reportedOK := betterLeaksOccurrenceByteRange(raw, occurrence, locationIndex)
	if !reportedOK {
		// A few private compatibility callers provide only a literal value. Keep
		// their historical unique-value behavior; upstream findings always carry
		// a validated location and take the bounded branch below.
		if occurrence.span != (betterLeaksSpan{}) {
			return 0, 0, false
		}
		reportedStart, reportedEnd = 0, len(raw)
	}
	if len(occurrence.value) == 0 || reportedEnd-reportedStart < len(occurrence.value) {
		return 0, 0, false
	}
	foundStart := -1
	for from := reportedStart; from <= reportedEnd-len(occurrence.value); {
		relative := bytes.Index(raw[from:reportedEnd], occurrence.value)
		if relative < 0 {
			break
		}
		start := from + relative
		if foundStart >= 0 {
			return 0, 0, false
		}
		foundStart = start
		from = start + 1
	}
	if foundStart < 0 {
		return 0, 0, false
	}
	return foundStart, foundStart + len(occurrence.value), true
}

func betterLeaksOccurrenceByteRange(raw []byte, occurrence betterLeaksOccurrence, locationIndex *betterLeaksLocationIndex) (int, int, bool) {
	if occurrence.offsetsValid && occurrence.start >= 0 && occurrence.end > occurrence.start && occurrence.end <= len(raw) {
		return occurrence.start, occurrence.end, true
	}
	if locationIndex != nil {
		if _, start, end, err := locationIndex.spanForSpan(occurrence.span); err == nil {
			return start, end, true
		}
	}
	return betterLeaksSpanByteRange(raw, occurrence.span)
}

func betterLeaksSpanByteRange(raw []byte, span betterLeaksSpan) (int, int, bool) {
	if len(raw) == 0 || span.StartLine < 1 || span.EndLine < span.StartLine || span.StartColumn < 1 || span.EndColumn < span.StartColumn {
		return 0, 0, false
	}
	line := 1
	lineStart := 0
	start := -1
	end := -1
	for i := 0; i <= len(raw); i++ {
		atEnd := i == len(raw)
		if line == span.StartLine && start < 0 && span.StartColumn <= i-lineStart+1 {
			start = lineStart + span.StartColumn - 1
		}
		if atEnd || raw[i] == '\n' {
			lineLength := i - lineStart
			if line == span.StartLine && start < 0 && span.StartColumn <= lineLength+1 {
				start = lineStart + span.StartColumn - 1
			}
			if line == span.EndLine {
				if span.EndColumn > lineLength || span.EndColumn < 1 {
					return 0, 0, false
				}
				end = lineStart + span.EndColumn
				break
			}
			line++
			lineStart = i + 1
		}
	}
	if start < 0 || end <= start || end > len(raw) {
		return 0, 0, false
	}
	return start, end, true
}

func releaseBetterLeaksOccurrenceBytes(findings []betterLeaksFinding) {
	for i := range findings {
		for j := range findings[i].occurrences {
			clear(findings[i].occurrences[j].value)
			findings[i].occurrences[j].value = nil
		}
		for j := range findings[i].Components {
			for k := range findings[i].Components[j].occurrences {
				clear(findings[i].Components[j].occurrences[k].value)
				findings[i].Components[j].occurrences[k].value = nil
			}
		}
	}
}

// betterLeaksRewriteMatcher overlays verified literal BetterLeaks spans on the
// existing exact matcher. It never turns a value into a fragment-wide pattern:
// every transient rewrite retains the original byte range and field identity.
type betterLeaksRewriteMatcher struct {
	exact              sdk.Matcher
	raw                []byte
	kind               FragmentKind
	options            engine.MatcherOptions
	occurrences        []betterLeaksOccurrence
	covered            map[privateOccurrenceKey]struct{}
	jsonTokens         []betterLeaksJSONToken
	jsonIndex          int
	jsonCandidateIndex int
}

type betterLeaksJSONToken struct {
	mapping     jsonStringMapping
	stringValue bool
	index       int
}

type betterLeaksRewriteRange struct {
	occurrence betterLeaksOccurrence
	start      int
	end        int
}

func newBetterLeaksRewriteMatcher(exact sdk.Matcher, fragment LogicalFragment, occurrences []betterLeaksOccurrence) (sdk.Matcher, error) {
	if len(occurrences) == 0 {
		return exact, nil
	}
	raw := fragment.rawBytes()
	verified := make([]betterLeaksOccurrence, 0, len(occurrences))
	for _, occurrence := range occurrences {
		if occurrence.fieldID != "" && occurrence.fieldID != fragment.privateID {
			continue
		}
		if occurrence.representation != betterLeaksOccurrenceLiteral || len(occurrence.value) == 0 {
			continue
		}
		var locationIndex *betterLeaksLocationIndex
		var start, end int
		var ok bool
		if occurrence.offsetsValid {
			start, end, ok = literalBetterLeaksByteRangeWithLocationIndex(raw, occurrence, nil)
		} else {
			locationIndex = newBetterLeaksLocationIndex(raw)
			start, end, ok = literalBetterLeaksByteRangeWithLocationIndex(raw, occurrence, locationIndex)
		}
		if !ok {
			continue
		}
		if !occurrence.offsetsValid || occurrence.start != start || occurrence.end != end {
			occurrence.start, occurrence.end = start, end
			occurrence.offsetsValid = true
			if locationIndex == nil {
				locationIndex = newBetterLeaksLocationIndex(raw)
			}
			occurrence.span, _ = spanForByteRangeWithLocationIndex(raw, locationIndex, start, end)
		}
		verified = append(verified, occurrence)
	}
	if len(verified) == 0 {
		return exact, nil
	}
	verified = uniquePrivateOccurrences(verified)
	m := &betterLeaksRewriteMatcher{
		exact:       exact,
		raw:         raw,
		kind:        fragment.Kind,
		options:     betterLeaksRewriteOptions(exact),
		occurrences: verified,
	}
	if fragment.Kind == FragmentJSON {
		index := 0
		if err := walkJSONOccurrenceTokens(raw, func(mapping jsonStringMapping, stringValue, _ bool) bool {
			rawStart, rawEnd, valid := mapping.rawRange(0, len(mapping.decoded))
			if valid {
				for _, occurrence := range verified {
					if occurrence.start >= rawStart && occurrence.end <= rawEnd {
						m.jsonTokens = append(m.jsonTokens, betterLeaksJSONToken{mapping: mapping, stringValue: stringValue, index: index})
						break
					}
				}
			}
			index++
			return true
		}); err == nil {
			for _, occurrence := range verified {
				if !betterLeaksJSONOccurrenceHasToken(m.raw, occurrence, m.jsonTokens) {
					return nil, errBetterLeaksUnrewritable
				}
			}
		}
	}
	return m, nil
}

func betterLeaksJSONOccurrenceHasToken(raw []byte, occurrence betterLeaksOccurrence, tokens []betterLeaksJSONToken) bool {
	start, end, ok := betterLeaksOccurrenceByteRange(raw, occurrence, nil)
	if !ok {
		return false
	}
	for _, token := range tokens {
		rawStart, rawEnd, ok := token.mapping.rawRange(0, len(token.mapping.decoded))
		if ok && start >= rawStart && end <= rawEnd {
			return true
		}
	}
	return false
}

func betterLeaksRewriteOptions(exact sdk.Matcher) engine.MatcherOptions {
	opts := engine.MatcherOptions{PreserveKnownPrefixes: true}
	if provider, ok := exact.(interface{ RedactionOptions() engine.MatcherOptions }); ok {
		opts = provider.RedactionOptions()
	}
	return opts
}

func (m *betterLeaksRewriteMatcher) ScanBytes(ctx context.Context, input []byte) ([]sdk.Finding, error) {
	if m == nil || m.exact == nil {
		return nil, nil
	}
	return m.exact.ScanBytes(ctx, input)
}

func (m *betterLeaksRewriteMatcher) ScanString(ctx context.Context, input string) ([]sdk.Finding, error) {
	if m == nil {
		return nil, nil
	}
	findings, err := m.exactScanString(ctx, input)
	if err != nil {
		return nil, err
	}
	ranges := m.rangesForInput([]byte(input))
	return append(findings, betterLeaksCandidateFindings(ranges)...), nil
}

func (m *betterLeaksRewriteMatcher) RedactBytes(ctx context.Context, input []byte) ([]byte, []sdk.Finding, error) {
	if m == nil {
		return bytes.Clone(input), nil, nil
	}
	redacted := bytes.Clone(input)
	var findings []sdk.Finding
	if m.exact != nil {
		var err error
		redacted, findings, err = m.exact.RedactBytes(ctx, input)
		if err != nil {
			return nil, findings, err
		}
	}
	ranges := m.rangesForRawInput(input)
	redacted, candidateFindings := applyBetterLeaksRanges(input, redacted, ranges, m.options)
	m.recordCoverage(input, redacted, ranges)
	return redacted, append(findings, candidateFindings...), nil
}

func (m *betterLeaksRewriteMatcher) RedactString(ctx context.Context, input string) (string, []sdk.Finding, error) {
	if m == nil {
		return input, nil, nil
	}
	var (
		redacted string
		findings []sdk.Finding
		err      error
	)
	if m.exact != nil {
		redacted, findings, err = m.exact.RedactString(ctx, input)
		if err != nil {
			return "", findings, err
		}
	} else {
		redacted = input
	}
	ranges := m.rangesForInput([]byte(input))
	out, candidateFindings := applyBetterLeaksRanges([]byte(input), []byte(redacted), ranges, m.options)
	m.recordCoverage([]byte(input), out, ranges)
	return string(out), append(findings, candidateFindings...), nil
}

func (m *betterLeaksRewriteMatcher) recordCoverage(input, output []byte, ranges []betterLeaksRewriteRange) {
	if len(input) != len(output) {
		return
	}
	mask := m.options.MaskByte
	if mask == 0 {
		mask = '*'
	}
	for _, candidate := range ranges {
		if candidate.start < 0 || candidate.end > len(output) || candidate.end <= candidate.start || bytes.Equal(input[candidate.start:candidate.end], output[candidate.start:candidate.end]) {
			continue
		}
		start := candidate.start
		if m.options.PreserveKnownPrefixes {
			prefix := engine.DetectKnownPublicPrefix(string(candidate.occurrence.value))
			if len(prefix) < candidate.end-start {
				start += len(prefix)
			}
		}
		complete := true
		for index := start; index < candidate.end; index++ {
			if output[index] == input[index] && output[index] != mask {
				complete = false
				break
			}
		}
		if complete {
			if m.covered == nil {
				m.covered = make(map[privateOccurrenceKey]struct{}, len(m.occurrences))
			}
			m.covered[occurrenceKey(candidate.occurrence)] = struct{}{}
		}
	}
}

func (m *betterLeaksRewriteMatcher) validateCoverage() error {
	for _, occurrence := range m.occurrences {
		if _, covered := m.covered[occurrenceKey(occurrence)]; !covered {
			return errBetterLeaksUnrewritable
		}
	}
	return nil
}

func (m *betterLeaksRewriteMatcher) exactScanString(ctx context.Context, input string) ([]sdk.Finding, error) {
	if m.exact == nil {
		return nil, nil
	}
	return m.exact.ScanString(ctx, input)
}

func (m *betterLeaksRewriteMatcher) rangesForRawInput(input []byte) []betterLeaksRewriteRange {
	if len(m.occurrences) == 0 {
		return nil
	}
	var out []betterLeaksRewriteRange
	for _, occurrence := range m.occurrences {
		start, end, ok := betterLeaksOccurrenceByteRange(m.raw, occurrence, nil)
		if !ok || start < 0 || end > len(input) || !bytes.Equal(input[start:end], occurrence.value) {
			continue
		}
		out = append(out, betterLeaksRewriteRange{occurrence: occurrence, start: start, end: end})
	}
	return sortedBetterLeaksRewriteRanges(out)
}

func (m *betterLeaksRewriteMatcher) rangesForInput(input []byte) []betterLeaksRewriteRange {
	if m.kind != FragmentJSON || len(m.jsonTokens) == 0 {
		return m.rangesForRawInput(input)
	}
	index := m.jsonIndex
	m.jsonIndex++
	if m.jsonCandidateIndex >= len(m.jsonTokens) {
		return nil
	}
	token := &m.jsonTokens[m.jsonCandidateIndex]
	if token.index != index {
		return nil
	}
	m.jsonCandidateIndex++
	rawStart, rawEnd, ok := token.mapping.rawRange(0, len(token.mapping.decoded))
	if !ok {
		return nil
	}
	var out []betterLeaksRewriteRange
	for _, occurrence := range m.occurrences {
		occurrenceStart, occurrenceEnd, spanOK := betterLeaksOccurrenceByteRange(m.raw, occurrence, nil)
		if !spanOK || occurrenceStart < rawStart || occurrenceEnd > rawEnd {
			continue
		}
		start, end, mapOK := mappingRawRangeToDecoded(&token.mapping, occurrenceStart, occurrenceEnd)
		if !mapOK || end > len(input) || !bytes.Equal(input[start:end], occurrence.value) {
			continue
		}
		out = append(out, betterLeaksRewriteRange{occurrence: occurrence, start: start, end: end})
	}
	return sortedBetterLeaksRewriteRanges(out)
}

func mappingRawRangeToDecoded(mapping *jsonStringMapping, rawStart, rawEnd int) (int, int, bool) {
	if rawEnd <= rawStart || !mapping.materializeBoundaries() {
		return 0, 0, false
	}
	if mapping.boundaries == nil && !mapping.mapped {
		start, end := rawStart-mapping.rawStart, rawEnd-mapping.rawStart
		return start, end, start >= 0 && end > start && end <= len(mapping.decoded)
	}
	if mapping.boundaries != nil && len(mapping.boundaries) != len(mapping.decoded)+1 {
		return 0, 0, false
	}
	// Boundaries are monotonic, with repeats for multi-byte decoded runes.
	// Lower-bound searches preserve the earliest matching boundary semantics
	// for either compact exceptional intervals or the dense fallback map.
	length := len(mapping.decoded) + 1
	start := sort.Search(length, func(i int) bool { return mapping.boundaryAt(i) >= rawStart })
	if start >= length || mapping.boundaryAt(start) != rawStart {
		return 0, 0, false
	}
	end := start + 1 + sort.Search(length-start-1, func(i int) bool { return mapping.boundaryAt(start+1+i) >= rawEnd })
	if end >= length || mapping.boundaryAt(end) != rawEnd {
		return 0, 0, false
	}
	return start, end, true
}

func sortedBetterLeaksRewriteRanges(ranges []betterLeaksRewriteRange) []betterLeaksRewriteRange {
	sort.SliceStable(ranges, func(i, j int) bool {
		if ranges[i].start != ranges[j].start {
			return ranges[i].start < ranges[j].start
		}
		if ranges[i].end != ranges[j].end {
			return ranges[i].end > ranges[j].end
		}
		return ranges[i].occurrence.ruleID < ranges[j].occurrence.ruleID
	})
	return ranges
}

func applyBetterLeaksRanges(input, redacted []byte, ranges []betterLeaksRewriteRange, opts engine.MatcherOptions) ([]byte, []sdk.Finding) {
	if len(ranges) == 0 || len(input) != len(redacted) {
		return redacted, nil
	}
	// Exact redaction runs first. Keep those bytes intact, then union every
	// verified BetterLeaks range so a partial exact overlap cannot leave the
	// remainder of a discovered credential exposed.
	out := bytes.Clone(redacted)
	mask := opts.MaskByte
	if mask == 0 {
		mask = '*'
	}
	protected := make([]bool, len(input))
	valid := make([]betterLeaksRewriteRange, 0, len(ranges))
	for _, candidate := range ranges {
		if candidate.start < 0 || candidate.end > len(input) || candidate.end <= candidate.start || !bytes.Equal(input[candidate.start:candidate.end], candidate.occurrence.value) {
			continue
		}
		prefixLen := 0
		if opts.PreserveKnownPrefixes {
			prefix := engine.DetectKnownPublicPrefix(string(candidate.occurrence.value))
			if len(prefix) > 0 && len(prefix) < candidate.end-candidate.start && bytes.HasPrefix(candidate.occurrence.value, []byte(prefix)) {
				prefixLen = len(prefix)
			}
		}
		for index := candidate.start; index < candidate.start+prefixLen; index++ {
			protected[index] = true
		}
		valid = append(valid, candidate)
	}
	var applied []betterLeaksRewriteRange
	for _, candidate := range valid {
		changed := false
		for index := candidate.start; index < candidate.end; index++ {
			if protected[index] || out[index] != input[index] {
				continue
			}
			out[index] = mask
			changed = true
		}
		if changed {
			applied = append(applied, candidate)
		}
	}
	return out, betterLeaksCandidateFindings(applied)
}

func betterLeaksCandidateFindings(ranges []betterLeaksRewriteRange) []sdk.Finding {
	if len(ranges) == 0 {
		return nil
	}
	counts := make(map[string]int, len(ranges))
	for _, candidate := range ranges {
		if candidate.occurrence.ruleID != "" {
			counts[candidate.occurrence.ruleID]++
		}
	}
	ruleIDs := make([]string, 0, len(counts))
	for ruleID := range counts {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	out := make([]sdk.Finding, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		out = append(out, sdk.Finding{
			SourceCategory:  sdk.SourceCategoryUnknown,
			OccurrenceCount: counts[ruleID],
			DetectorID:      sdk.DetectorIDBetterLeaks,
			RuleID:          ruleID,
		})
	}
	return out
}

// ScanOccurrences delegates only to the exact matcher. Transient BetterLeaks
// occurrences are merged from discovery provenance and must not become exact
// catalog occurrences.
func (m *betterLeaksRewriteMatcher) ScanOccurrences(input []byte) []sdk.PositionalOccurrence {
	if m == nil || m.exact == nil {
		return nil
	}
	if positional, ok := m.exact.(sdk.PositionalMatcher); ok {
		return positional.ScanOccurrences(input)
	}
	return nil
}
