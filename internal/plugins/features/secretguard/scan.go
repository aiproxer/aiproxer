package secretguard

import (
	"bytes"
	"context"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

type scanMode int

const (
	modeScan scanMode = iota
	modeRedact
	modeLogScan
)

type scanOutcome struct {
	Findings             []sdk.Finding
	MutationCount        int
	BytesScanned         int
	ScanLimitHit         bool
	exactPrivateFindings []exactPrivateFinding
	discoveryFindings    []betterLeaksFinding
	discoveryFacts       DetectorFacts
	findingIndexes       map[safeFindingLocationKey]int
}

type safeFindingLocationKey struct {
	location, reference string
}

// mergeFindingsAt retains the exact-only first-attribution contract while
// indexing accumulated findings once for the lifetime of this scan.
func (out *scanOutcome) mergeFindingsAt(findings []sdk.Finding, location string) {
	if len(findings) == 0 {
		return
	}
	if out.findingIndexes == nil {
		out.findingIndexes = make(map[safeFindingLocationKey]int, len(out.Findings)+len(findings))
		for i, finding := range out.Findings {
			out.findingIndexes[safeFindingLocationKey{finding.Location, finding.SecretRefName}] = i
		}
	}
	for _, finding := range findings {
		key := safeFindingLocationKey{location, finding.SecretRefName}
		if i, exists := out.findingIndexes[key]; exists {
			out.Findings[i].OccurrenceCount += finding.OccurrenceCount
			continue
		}
		finding.Location = location
		out.findingIndexes[key] = len(out.Findings)
		out.Findings = append(out.Findings, finding)
	}
}

type exactOccurrenceMatcher = sdk.PositionalMatcher

// collectExactPrivateFindings retains exact spans only inside the feature
// boundary. The matcher receives the admitted fragment bytes and returns safe
// reference metadata; it has no catalog retrieval surface.
func collectExactPrivateFindings(m sdk.Matcher, fragment LogicalFragment, findings []sdk.Finding) []exactPrivateFinding {
	if m == nil || len(findings) == 0 {
		return nil
	}
	positional, ok := m.(exactOccurrenceMatcher)
	if !ok {
		return privateExactFindingsWithoutSpans(fragment, findings)
	}
	var private []betterLeaksOccurrence
	if fragment.Kind == FragmentJSON {
		// A successfully decoded JSON fragment is collected from the same
		// semantic tokens as scanJSONPayload. Raw scanning is reserved for the
		// decoder-failed fallback so escaped bytes and punctuation cannot add
		// independent occurrences.
		var mapped bool
		private, mapped = collectExactJSONOccurrencesMapped(positional, fragment.rawBytes(), fragment.privateID)
		if !mapped {
			private = collectExactRawOccurrences(positional, fragment.rawBytes(), fragment.privateID)
		}
	} else {
		private = collectExactRawOccurrences(positional, fragment.rawBytes(), fragment.privateID)
	}
	return exactPrivateFindingsAt(fragment, findings, private)
}

func collectExactPrivateFindingsForRedact(m sdk.Matcher, fragment LogicalFragment, findings []sdk.Finding) []exactPrivateFinding {
	if m == nil || len(findings) == 0 {
		return nil
	}
	positional, ok := m.(exactOccurrenceMatcher)
	if !ok {
		return privateExactFindingsWithoutSpans(fragment, findings)
	}
	var private []betterLeaksOccurrence
	if fragment.Kind == FragmentJSON {
		var mapped bool
		private, mapped = collectExactJSONRedactOccurrences(positional, fragment.rawBytes(), fragment.privateID)
		if !mapped {
			private = collectExactRawOccurrences(positional, fragment.rawBytes(), fragment.privateID)
		}
	} else {
		private = collectExactRawOccurrences(positional, fragment.rawBytes(), fragment.privateID)
	}
	return exactPrivateFindingsAt(fragment, findings, private)
}

func exactPrivateFindingsAt(fragment LogicalFragment, findings []sdk.Finding, private []betterLeaksOccurrence) []exactPrivateFinding {
	result := make([]exactPrivateFinding, 0, len(findings))
	for _, finding := range findings {
		if finding.DetectorID == sdk.DetectorIDBetterLeaks {
			continue
		}
		finding.Location = fragment.Location
		matched := make([]betterLeaksOccurrence, 0, len(private))
		for _, occurrence := range private {
			if occurrence.ruleID == finding.SecretRefName {
				matched = append(matched, occurrence)
			}
		}
		uncovered := max(finding.OccurrenceCount-len(matched), 0)
		result = append(result, exactPrivateFinding{finding: finding, occurrences: matched, uncoveredCount: uncovered})
	}
	return result
}

func collectExactRawOccurrences(m exactOccurrenceMatcher, raw []byte, fieldID string) []betterLeaksOccurrence {
	occurrences := m.ScanOccurrences(raw)
	private := make([]betterLeaksOccurrence, 0, len(occurrences))
	locationIndex := newBetterLeaksLocationIndex(raw)
	for _, occurrence := range occurrences {
		if occurrence.Start < 0 || occurrence.End <= occurrence.Start || occurrence.End > len(raw) {
			continue
		}
		span, err := spanForByteRangeWithLocationIndex(raw, locationIndex, occurrence.Start, occurrence.End)
		if err != nil {
			continue
		}
		private = append(private, betterLeaksOccurrence{
			value:          bytes.Clone(raw[occurrence.Start:occurrence.End]),
			span:           span,
			start:          occurrence.Start,
			end:            occurrence.End,
			offsetsValid:   true,
			fieldID:        fieldID,
			ruleID:         occurrence.Finding.SecretRefName,
			role:           betterLeaksOccurrencePrimary,
			representation: betterLeaksOccurrenceLiteral,
		})
	}
	return private
}

func privateExactFindingsWithoutSpans(fragment LogicalFragment, findings []sdk.Finding) []exactPrivateFinding {
	result := make([]exactPrivateFinding, 0, len(findings))
	for _, finding := range findings {
		finding.Location = fragment.Location
		result = append(result, exactPrivateFinding{finding: finding, uncoveredCount: positiveOccurrenceCount(finding.OccurrenceCount)})
	}
	return result
}

// scanCall scans the one admitted logical-fragment set. Locations are stable paths.
func scanCall(ctx context.Context, call *aipapi.Call, m sdk.Matcher, mode scanMode, maxBytes int, capabilities ...any) (scanOutcome, error) {
	var out scanOutcome
	defer func() {
		releaseBetterLeaksOccurrenceBytes(out.discoveryFindings)
	}()
	var generation *GenerationServices
	if len(capabilities) > 0 {
		generation, _ = capabilities[0].(*GenerationServices)
	}
	if call == nil || (m == nil && (generation == nil || !generation.betterLeaksEnabled)) {
		return out, nil
	}
	if err := validateJSONMirrors(call); err != nil {
		return out, err
	}
	if m == nil {
		m = engine.AsMatcher(engine.NewMatcher(nil))
	}
	if mode == modeRedact && generation != nil {
		positional, _ := m.(sdk.PositionalMatcher)
		m = generationRedactionMatcher{Matcher: m, positional: positional, options: generation.redaction}
	}

	budget := newScanBudget(maxBytes)
	fragments := walkLogicalFragments(call, budget)
	out.BytesScanned = budget.used
	out.ScanLimitHit = budget.limitHit
	var detectorErr error
	if generation != nil && generation.betterLeaksEnabled {
		discovery, err := generation.scanFragments(ctx, fragments)
		out.discoveryFindings = discovery.Findings
		out.discoveryFacts = generation.DetectorFacts()
		if err != nil {
			// Independent exact/request credentials remain observable on admitted
			// fragments after discovery failure. Never continue mutation or canceled work.
			if mode != modeLogScan || (ctx != nil && ctx.Err() != nil) {
				_ = finalizeHybridScanOutcome(&out)
				return out, err
			}
			detectorErr = err
		}
		if mode == modeRedact {
			if err := validateBetterLeaksRedactionEligibility(fragments, out.discoveryFindings); err != nil {
				if finalizeErr := finalizeHybridScanOutcome(&out); finalizeErr != nil {
					return out, finalizeErr
				}
				if out.ScanLimitHit {
					return out, nil
				}
				return out, err
			}
		}
	}
	for _, fragment := range fragments {
		if err := scanLogicalFragment(ctx, fragment, m, mode, &out, out.discoveryFindings, generation != nil && generation.betterLeaksEnabled); err != nil {
			if generation != nil && generation.betterLeaksEnabled {
				_ = finalizeHybridScanOutcome(&out)
			}
			return out, err
		}
	}
	if generation != nil && generation.betterLeaksEnabled {
		if err := finalizeHybridScanOutcome(&out); err != nil {
			return out, err
		}
	}
	return out, detectorErr
}

// validateBetterLeaksRedactionEligibility is the fail-closed gate before any
// working-clone mutation. Every reported occurrence must retain literal bytes
// and a span that proves those bytes are present in its admitted fragment.
// Required multipart components must also have at least one such occurrence.
func validateBetterLeaksRedactionEligibility(fragments []LogicalFragment, findings []betterLeaksFinding) error {
	for _, finding := range findings {
		if finding.incompleteCoverage || len(finding.occurrences) == 0 {
			return errBetterLeaksUnrewritable
		}
		for _, occurrence := range finding.occurrences {
			if !betterLeaksOccurrenceRewriteEligible(fragments, finding.Location, occurrence) {
				return errBetterLeaksUnrewritable
			}
		}
		for _, component := range finding.Components {
			if component.Optional {
				continue
			}
			if len(component.occurrences) == 0 {
				return errBetterLeaksUnrewritable
			}
			for _, occurrence := range component.occurrences {
				if !betterLeaksOccurrenceRewriteEligible(fragments, finding.Location, occurrence) {
					return errBetterLeaksUnrewritable
				}
			}
		}
	}
	return nil
}

func betterLeaksOccurrenceRewriteEligible(fragments []LogicalFragment, location string, occurrence betterLeaksOccurrence) bool {
	if occurrence.representation != betterLeaksOccurrenceLiteral || len(occurrence.value) == 0 {
		return false
	}
	if occurrence.offsetsValid {
		for _, fragment := range fragments {
			if fragment.Location != location || (occurrence.fieldID != "" && occurrence.fieldID != fragment.privateID) {
				continue
			}
			raw := fragment.rawBytes()
			if occurrence.start >= 0 && occurrence.end > occurrence.start && occurrence.end <= len(raw) && bytes.Equal(raw[occurrence.start:occurrence.end], occurrence.value) {
				return true
			}
		}
	}
	for _, fragment := range fragments {
		if fragment.Location != location {
			continue
		}
		if occurrence.fieldID != "" && occurrence.fieldID != fragment.privateID {
			continue
		}
		raw := fragment.rawBytes()
		if _, _, ok := literalBetterLeaksByteRange(raw, occurrence); ok {
			return true
		}
	}
	return false
}

func scanLogicalFragment(ctx context.Context, fragment LogicalFragment, m sdk.Matcher, mode scanMode, out *scanOutcome, discoveries []betterLeaksFinding, hybrid bool) error {
	activeMatcher := m
	if mode == modeRedact {
		candidates := betterLeaksLiteralCandidates(fragment, discoveries)
		if len(candidates) > 0 {
			var err error
			activeMatcher, err = newBetterLeaksRewriteMatcher(m, fragment, candidates)
			if err != nil {
				return err
			}
		}
	}
	switch mode {
	case modeScan, modeLogScan:
		var (
			findings []sdk.Finding
			err      error
		)
		if fragment.Kind == FragmentJSON {
			findings, err = scanJSONPayload(ctx, activeMatcher, fragment.rawBytes())
		} else {
			findings, err = activeMatcher.ScanString(ctx, fragment.textValue())
		}
		if err != nil {
			return err
		}
		out.mergeFindingsAt(findings, fragment.Location)
		if hybrid {
			out.exactPrivateFindings = append(out.exactPrivateFindings, collectExactPrivateFindings(m, fragment, findings)...)
		}
	case modeRedact:
		var (
			redacted []byte
			findings []sdk.Finding
			err      error
		)
		if fragment.Kind == FragmentJSON {
			redacted, findings, err = redactJSONPayload(ctx, activeMatcher, fragment.rawBytes())
		} else {
			var text string
			text, findings, err = activeMatcher.RedactString(ctx, fragment.textValue())
			if err == nil && text != fragment.textValue() {
				fragment.setText(text)
				out.MutationCount++
			}
		}
		if err != nil {
			if fragment.Kind == FragmentJSON {
				out.mergeFindingsAt(findings, fragment.Location)
			}
			if hybrid {
				out.exactPrivateFindings = append(out.exactPrivateFindings, collectExactPrivateFindingsForRedact(m, fragment, findings)...)
			}
			return err
		}
		out.mergeFindingsAt(findings, fragment.Location)
		if hybrid {
			out.exactPrivateFindings = append(out.exactPrivateFindings, collectExactPrivateFindingsForRedact(m, fragment, findings)...)
		}
		if fragment.Kind == FragmentJSON && !bytes.Equal(redacted, fragment.rawBytes()) {
			fragment.setRaw(redacted)
			out.MutationCount++
		}
	}
	if bridge, ok := activeMatcher.(*betterLeaksRewriteMatcher); ok && mode == modeRedact {
		return bridge.validateCoverage()
	}
	return nil
}

func finalizeHybridScanOutcome(out *scanOutcome) error {
	if out == nil {
		return nil
	}
	findings, err := mergeHybridFindings(out.exactPrivateFindings, out.discoveryFindings, out.discoveryFacts)
	if err != nil {
		return newBetterLeaksScanError(err)
	}
	out.Findings = findings
	return nil
}

// mergeFindings merges by Location+SecretRefName, summing OccurrenceCount and
// keeping the first SourceCategory/Aliases.
func mergeFindings(dst, src []sdk.Finding) []sdk.Finding {
	if len(src) == 0 {
		return dst
	}
	type key struct {
		loc string
		ref string
	}
	idx := make(map[key]int, len(dst))
	for i, f := range dst {
		idx[key{loc: f.Location, ref: f.SecretRefName}] = i
	}
	for _, f := range src {
		k := key{loc: f.Location, ref: f.SecretRefName}
		if j, ok := idx[k]; ok {
			dst[j].OccurrenceCount += f.OccurrenceCount
			continue
		}
		idx[k] = len(dst)
		dst = append(dst, f)
	}
	return dst
}
