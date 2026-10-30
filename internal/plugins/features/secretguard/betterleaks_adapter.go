package secretguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	blconfig "github.com/betterleaks/betterleaks/v2/config"
	blregexp "github.com/betterleaks/betterleaks/v2/regexp"
	"github.com/betterleaks/betterleaks/v2/report"
	blscan "github.com/betterleaks/betterleaks/v2/scan"
	"github.com/betterleaks/betterleaks/v2/sources"
)

const (
	betterLeaksScanErrorCanceled      = "canceled"
	betterLeaksScanErrorDeadline      = "deadline exceeded"
	betterLeaksScanErrorCap           = "finding cap exceeded"
	betterLeaksScanErrorSource        = "source failure"
	betterLeaksScanErrorUnavailable   = "scanner failure"
	betterLeaksScanErrorProjection    = "projection failure"
	maxBetterLeaksProjectedField      = 256
	maxBetterLeaksProjectedComponents = 128
	// The shared admission default bounds one logical fragment to 2 MiB. Keep
	// the private occurrence identity complete within that bound; values above
	// it fail projection instead of being silently truncated.
	maxBetterLeaksOccurrenceBytes = DefaultScanMaxBytes
	maxBetterLeaksOccurrences     = 256
)

var (
	errBetterLeaksFindingCap = errors.New("betterleaks projected finding cap exceeded")
	errBetterLeaksProjection = errors.New("betterleaks finding projection failed")
)

// betterLeaksFinding is the bounded feature-private projection of one
// upstream report. It intentionally omits all match, fingerprint, capture,
// line, context, and source-content fields.
type betterLeaksFinding struct {
	RuleID          string
	Confidence      string
	Location        string
	OccurrenceCount int
	Components      []betterLeaksComponent

	// occurrences are feature-private rewrite inputs. They are deliberately
	// unexported so they cannot be serialized through a safe result or copied
	// into public findings by accident.
	occurrences []betterLeaksOccurrence
	// incompleteCoverage records omitted rewrite inputs without exposing upstream
	// reports or expanding bounded metadata. It must prevent redaction success.
	incompleteCoverage bool
}

type betterLeaksComponent struct {
	RuleID      string
	Optional    bool
	occurrences []betterLeaksOccurrence
}

type betterLeaksOccurrenceRole uint8

const (
	betterLeaksOccurrencePrimary betterLeaksOccurrenceRole = iota
	betterLeaksOccurrenceComponent
)

type betterLeaksOccurrenceRepresentation uint8

const (
	betterLeaksOccurrenceLiteral betterLeaksOccurrenceRepresentation = iota
	betterLeaksOccurrenceDecoded
)

type betterLeaksSpan struct {
	StartLine   int
	EndLine     int
	StartColumn int
	EndColumn   int
}

// betterLeaksLocationIndex is built once for an admitted logical fragment.
// Its line starts are a compact representation of the newline mapping; private
// occurrences retain the resulting absolute offsets so later rewrite and
// deduplication passes do not rescan the fragment.
type betterLeaksLocationIndex struct {
	lineStarts []int
	rawLength  int
}

func newBetterLeaksLocationIndex(raw []byte) *betterLeaksLocationIndex {
	if raw == nil {
		return nil
	}
	lineCount := 1
	for _, value := range raw {
		if value == '\n' {
			lineCount++
		}
	}
	lineStarts := make([]int, 1, lineCount)
	lineStarts[0] = 0
	for index, value := range raw {
		if value == '\n' {
			lineStarts = append(lineStarts, index+1)
		}
	}
	return &betterLeaksLocationIndex{lineStarts: lineStarts, rawLength: len(raw)}
}

func (m *betterLeaksLocationIndex) lineBounds(line int) (int, int, bool) {
	if m == nil || line < 1 || line > len(m.lineStarts) {
		return 0, 0, false
	}
	start := m.lineStarts[line-1]
	end := m.rawLength
	if line < len(m.lineStarts) {
		end = m.lineStarts[line] - 1
	}
	return start, end, true
}

func (m *betterLeaksLocationIndex) spanForLocation(location report.Location) (betterLeaksSpan, int, int, error) {
	return m.spanForSpan(betterLeaksSpan{
		StartLine:   location.StartLine,
		EndLine:     location.EndLine,
		StartColumn: location.StartColumn,
		EndColumn:   location.EndColumn,
	})
}

func (m *betterLeaksLocationIndex) spanForSpan(span betterLeaksSpan) (betterLeaksSpan, int, int, error) {
	if m == nil || span.StartLine < 1 || span.EndLine < span.StartLine || span.StartColumn < 1 || span.EndColumn < 1 {
		return betterLeaksSpan{}, 0, 0, errBetterLeaksProjection
	}
	startLineStart, startLineEnd, ok := m.lineBounds(span.StartLine)
	if !ok {
		return betterLeaksSpan{}, 0, 0, errBetterLeaksProjection
	}
	endLineStart, endLineEnd, ok := m.lineBounds(span.EndLine)
	if !ok {
		return betterLeaksSpan{}, 0, 0, errBetterLeaksProjection
	}
	if span.StartColumn > startLineEnd-startLineStart || span.EndColumn > endLineEnd-endLineStart {
		return betterLeaksSpan{}, 0, 0, errBetterLeaksProjection
	}
	start := startLineStart + span.StartColumn - 1
	end := endLineStart + span.EndColumn
	if span.StartLine == span.EndLine && span.EndColumn < span.StartColumn {
		return betterLeaksSpan{}, 0, 0, errBetterLeaksProjection
	}
	if start < 0 || end <= start || end > m.rawLength {
		return betterLeaksSpan{}, 0, 0, errBetterLeaksProjection
	}
	return span, start, end, nil
}

func (m *betterLeaksLocationIndex) spanForByteRange(start, end int) (betterLeaksSpan, error) {
	if m == nil || start < 0 || end <= start || end > m.rawLength {
		return betterLeaksSpan{}, errPrivateOccurrenceSpan
	}
	startLine := sort.Search(len(m.lineStarts), func(index int) bool { return m.lineStarts[index] > start }) - 1
	endOffset := end - 1
	endLine := sort.Search(len(m.lineStarts), func(index int) bool { return m.lineStarts[index] > endOffset }) - 1
	if startLine < 0 || endLine < startLine {
		return betterLeaksSpan{}, errPrivateOccurrenceSpan
	}
	return betterLeaksSpan{
		StartLine:   startLine + 1,
		EndLine:     endLine + 1,
		StartColumn: start - m.lineStarts[startLine] + 1,
		EndColumn:   endOffset - m.lineStarts[endLine] + 1,
	}, nil
}

type betterLeaksOccurrence struct {
	value          []byte
	span           betterLeaksSpan
	start          int
	end            int
	offsetsValid   bool
	fieldID        string
	ruleID         string
	role           betterLeaksOccurrenceRole
	representation betterLeaksOccurrenceRepresentation
}

type betterLeaksScanResult struct {
	Findings []betterLeaksFinding
}

// betterLeaksLogicalFragmentSource is deliberately a one-fragment source.
// The upstream Reader splits large streams into independent chunks, which
// would discard multipart context at a chunk boundary. Logical fragments are
// admitted and bounded before this source is constructed, so yielding the
// complete fragment preserves the feature's canonical context contract.
type betterLeaksLogicalFragmentSource struct {
	// Text is the canonical representation for text fragments. Raw is retained
	// for JSON fragments and private adapter tests; only one is populated by the
	// fragment walker.
	text     string
	raw      []byte
	rawCache *[]byte
}

func (s betterLeaksLogicalFragmentSource) betterLeaksRaw() []byte {
	if s.raw != nil {
		return s.raw
	}
	if s.rawCache != nil {
		if *s.rawCache == nil {
			*s.rawCache = []byte(s.text)
		}
		return *s.rawCache
	}
	if s.text != "" {
		return []byte(s.text)
	}
	return nil
}

func (s betterLeaksLogicalFragmentSource) content() string {
	if s.text != "" || s.raw == nil {
		return s.text
	}
	return string(s.raw)
}

func (s betterLeaksLogicalFragmentSource) Fragments(ctx context.Context, yield sources.FragmentsFunc) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if yield == nil {
		return errors.New("betterleaks fragment source requires a yield function")
	}
	if err := yield(sources.Fragment{Raw: s.content()}, nil); err != nil {
		return err
	}
	return ctx.Err()
}

type betterLeaksScanError struct {
	kind  string
	cause error
}

// betterLeaksConstructionError is the private boundary for upstream config and
// scanner construction failures. BetterLeaks may include source/config details
// in its errors, so only the bounded classification crosses this package.
// Is preserves private identity for generation failure handling without adding
// an unwrap chain that could expose the upstream error text to generic callers.
type betterLeaksConstructionError struct {
	cause error
}

func (e *betterLeaksConstructionError) Error() string {
	return "secretguard: betterleaks scanner construction failed"
}

func (e *betterLeaksConstructionError) Is(target error) bool {
	return e != nil && e.cause != nil && errors.Is(e.cause, target)
}

func newBetterLeaksConstructionError(err error) error {
	if err == nil {
		return nil
	}
	return &betterLeaksConstructionError{cause: err}
}

func (e *betterLeaksScanError) Error() string {
	if e == nil {
		return "secretguard: betterleaks scan failed"
	}
	return "secretguard: betterleaks scan " + e.kind
}

// Is preserves cancellation and private source identity without exposing the
// upstream error text (which may contain secret-bearing source details).
func (e *betterLeaksScanError) Is(target error) bool {
	return e != nil && e.cause != nil && errors.Is(e.cause, target)
}

// betterLeaksScanner is the feature-private generation handle for BetterLeaks.
// The upstream scanner is immutable after construction and safe for concurrent
// scans; keeping it behind this handle prevents BetterLeaks types from becoming
// part of the feature, runtime, or SDK contracts.
type betterLeaksScanner struct {
	scanner     *blscan.Scanner
	facts       DetectorFacts
	maxFindings int
}

// DetectorFacts is the bounded, request-independent BetterLeaks policy
// inventory for one immutable generation. It deliberately contains no
// upstream scanner/config types or finding content.
type DetectorFacts struct {
	Version           string
	ConfigHash        string
	RuleInventoryHash string
	ActiveRuleCount   int
	MinimumConfidence string
	MaxDecodeDepth    int
	Workers           int
	RuleIDs           []string
}

// newBetterLeaksScanner constructs one precompiled scanner for a runtime
// generation. Disabled policy returns no handle so callers cannot accidentally
// create BetterLeaks work for a disabled detector.
func newBetterLeaksScanner(policy BetterLeaksPolicy) (*betterLeaksScanner, error) {
	if !policy.Enabled {
		return nil, nil
	}
	if err := validateBetterLeaksScannerPolicy(policy); err != nil {
		return nil, err
	}

	cfg, err := blconfig.Default()
	if err != nil {
		return nil, newBetterLeaksConstructionError(err)
	}
	selected, facts, err := resolveBetterLeaksConfig(cfg, policy)
	if err != nil {
		return nil, err
	}

	scanner, err := blscan.New(
		selected,
		blscan.WithRegexEngine(blregexp.Stdlib{}),
		blscan.WithWorkers(policy.Workers),
		blscan.WithMinimumConfidence(policy.MinimumConfidence),
		blscan.WithMaxDecodeDepth(policy.MaxDecodeDepth),
		blscan.WithPrecompile(),
		blscan.WithAllowSignatures(),
	)
	if err != nil {
		return nil, newBetterLeaksConstructionError(err)
	}
	maxFindings := policy.MaxFindings
	if maxFindings <= 0 || maxFindings > DefaultBetterLeaksMaxFindings {
		maxFindings = DefaultBetterLeaksMaxFindings
	}
	return &betterLeaksScanner{scanner: scanner, facts: facts, maxFindings: maxFindings}, nil
}

// scanFragments scans the already-admitted logical fragments using the one
// immutable generation scanner. A source is constructed per fragment, while
// the BetterLeaks scanner itself is never reconstructed on the request path.
func (s *betterLeaksScanner) scanFragments(ctx context.Context, fragments []LogicalFragment) (betterLeaksScanResult, error) {
	var result betterLeaksScanResult
	if s == nil || s.scanner == nil {
		return result, newBetterLeaksUnavailableError()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return result, newBetterLeaksScanError(err)
	}
	for index := range fragments {
		fragment := &fragments[index]
		if fragment.Text == "" && len(fragment.Raw) == 0 {
			continue
		}
		source := betterLeaksLogicalFragmentSource{text: fragment.Text, raw: fragment.Raw, rawCache: &fragment.Raw}
		if err := s.scanSourceWithFieldID(ctx, source, fragment.Location, fragment.privateID, &result); err != nil {
			return result, err
		}
	}
	sortBetterLeaksFindings(result.Findings)
	return result, nil
}

// scanSource is the private source boundary used by scanFragments and by
// adapter tests. The source callback projects every report immediately and
// stops before the request-wide projected finding cap can be exceeded.
func (s *betterLeaksScanner) scanSource(ctx context.Context, source sources.Source, location string, result *betterLeaksScanResult) error {
	return s.scanSourceWithFieldID(ctx, source, location, "", result)
}

func betterLeaksFindingNeedsRaw(finding report.Finding) bool {
	if betterLeaksOccurrenceNeedsRaw(finding.Match.Value, finding.DecodeDepth, finding.Encodings) {
		return true
	}
	for _, set := range finding.ComponentSets {
		for _, component := range set.Components {
			if betterLeaksOccurrenceNeedsRaw(component.Match.Value, component.DecodeDepth, component.Encodings) {
				return true
			}
		}
	}
	return false
}

func betterLeaksOccurrenceNeedsRaw(value string, decodeDepth int, encodings []string) bool {
	return value != "" && decodeDepth == 0 && len(encodings) == 0
}

func (s *betterLeaksScanner) scanSourceWithFieldID(ctx context.Context, source sources.Source, location, fieldID string, result *betterLeaksScanResult) error {
	if s == nil || s.scanner == nil || source == nil || result == nil {
		return newBetterLeaksUnavailableError()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return newBetterLeaksScanError(err)
	}
	var admittedRaw []byte
	var locationIndex *betterLeaksLocationIndex
	getAdmittedRaw := func() []byte {
		if admittedRaw != nil {
			return admittedRaw
		}
		if rawSource, ok := source.(interface{ betterLeaksRaw() []byte }); ok {
			admittedRaw = rawSource.betterLeaksRaw()
		}
		return admittedRaw
	}
	getLocationIndex := func() *betterLeaksLocationIndex {
		if locationIndex != nil {
			return locationIndex
		}
		raw := getAdmittedRaw()
		if raw == nil {
			return nil
		}
		locationIndex = newBetterLeaksLocationIndex(raw)
		return locationIndex
	}
	_, err := s.scanner.Scan(ctx, source, func(finding report.Finding) error {
		if len(result.Findings) >= s.findingCap() {
			return errBetterLeaksFindingCap
		}
		var admitted []byte
		if betterLeaksFindingNeedsRaw(finding) {
			admitted = getAdmittedRaw()
		}
		var index *betterLeaksLocationIndex
		if admitted != nil {
			index = getLocationIndex()
		}
		projected, err := projectBetterLeaksFindingWithFieldIDAndLocationIndex(finding, location, fieldID, admitted, index)
		if err != nil {
			return err
		}
		result.Findings = append(result.Findings, projected)
		return nil
	})
	if err != nil {
		return newBetterLeaksScanError(err)
	}
	if err := ctx.Err(); err != nil {
		return newBetterLeaksScanError(err)
	}
	return nil
}

func (s *betterLeaksScanner) findingCap() int {
	if s == nil || s.maxFindings <= 0 || s.maxFindings > DefaultBetterLeaksMaxFindings {
		return DefaultBetterLeaksMaxFindings
	}
	return s.maxFindings
}

func projectBetterLeaksFinding(finding report.Finding, location string, admittedRaw []byte) (betterLeaksFinding, error) {
	return projectBetterLeaksFindingWithFieldID(finding, location, "", admittedRaw)
}

func projectBetterLeaksFindingWithFieldID(finding report.Finding, location, fieldID string, admittedRaw []byte) (betterLeaksFinding, error) {
	var locationIndex *betterLeaksLocationIndex
	if admittedRaw != nil {
		locationIndex = newBetterLeaksLocationIndex(admittedRaw)
	}
	return projectBetterLeaksFindingWithFieldIDAndLocationIndex(finding, location, fieldID, admittedRaw, locationIndex)
}

func projectBetterLeaksFindingWithFieldIDAndLocationIndex(finding report.Finding, location, fieldID string, admittedRaw []byte, locationIndex *betterLeaksLocationIndex) (betterLeaksFinding, error) {
	if len(finding.RuleID) > maxBetterLeaksProjectedField || len(location) > maxBetterLeaksProjectedField {
		return betterLeaksFinding{}, errBetterLeaksProjection
	}
	if finding.Confidence != "" && boundedBetterLeaksConfidence(finding.Confidence) == "" {
		return betterLeaksFinding{}, errBetterLeaksProjection
	}
	projected := betterLeaksFinding{
		RuleID:             boundedBetterLeaksField(finding.RuleID),
		Confidence:         boundedBetterLeaksConfidence(finding.Confidence),
		Location:           boundedBetterLeaksField(location),
		OccurrenceCount:    1,
		incompleteCoverage: finding.ComponentSetsTruncated,
	}
	if occurrence, ok, err := newBetterLeaksOccurrenceWithLocationIndex(
		betterLeaksOccurrencePrimary,
		projected.RuleID,
		finding.Match.Value,
		finding.Location,
		finding.DecodeDepth,
		finding.Encodings,
		admittedRaw,
		locationIndex,
	); ok {
		normalizeBetterLeaksLiteralOccurrenceWithLocationIndex(admittedRaw, &occurrence, locationIndex)
		projected.occurrences = append(projected.occurrences, occurrence)
	} else if err != nil {
		return betterLeaksFinding{}, err
	}
	seen := make(map[string]int)
components:
	for _, set := range finding.ComponentSets {
		for _, component := range set.Components {
			if len(component.RuleID) > maxBetterLeaksProjectedField {
				return betterLeaksFinding{}, errBetterLeaksProjection
			}
			ruleID := boundedBetterLeaksField(component.RuleID)
			if ruleID == "" {
				continue
			}
			_, known := seen[ruleID]
			if (!known && len(projected.Components) >= maxBetterLeaksProjectedComponents) ||
				(component.Match.Value != "" && len(projected.occurrences) >= maxBetterLeaksOccurrences) {
				projected.incompleteCoverage = true
				break components
			}
			if index, ok := seen[ruleID]; ok {
				if !component.Optional {
					projected.Components[index].Optional = false
				}
				if occurrence, ok, err := newBetterLeaksOccurrenceWithLocationIndex(
					betterLeaksOccurrenceComponent,
					ruleID,
					component.Match.Value,
					component.Location,
					component.DecodeDepth,
					component.Encodings,
					admittedRaw,
					locationIndex,
				); ok {
					normalizeBetterLeaksLiteralOccurrenceWithLocationIndex(admittedRaw, &occurrence, locationIndex)
					projected.Components[index].occurrences = append(projected.Components[index].occurrences, occurrence)
					projected.occurrences = append(projected.occurrences, occurrence)
				} else if err != nil {
					return betterLeaksFinding{}, err
				}
				continue
			}
			seen[ruleID] = len(projected.Components)
			projected.Components = append(projected.Components, betterLeaksComponent{RuleID: ruleID, Optional: component.Optional})
			if occurrence, ok, err := newBetterLeaksOccurrenceWithLocationIndex(
				betterLeaksOccurrenceComponent,
				ruleID,
				component.Match.Value,
				component.Location,
				component.DecodeDepth,
				component.Encodings,
				admittedRaw,
				locationIndex,
			); ok {
				normalizeBetterLeaksLiteralOccurrenceWithLocationIndex(admittedRaw, &occurrence, locationIndex)
				projected.Components[len(projected.Components)-1].occurrences = append(projected.Components[len(projected.Components)-1].occurrences, occurrence)
				projected.occurrences = append(projected.occurrences, occurrence)
			} else if err != nil {
				return betterLeaksFinding{}, err
			}
		}
	}
	sort.Slice(projected.Components, func(i, j int) bool {
		if projected.Components[i].RuleID != projected.Components[j].RuleID {
			return projected.Components[i].RuleID < projected.Components[j].RuleID
		}
		return !projected.Components[i].Optional && projected.Components[j].Optional
	})
	for i := range projected.occurrences {
		projected.occurrences[i].fieldID = fieldID
	}
	for componentIndex := range projected.Components {
		for occurrenceIndex := range projected.Components[componentIndex].occurrences {
			projected.Components[componentIndex].occurrences[occurrenceIndex].fieldID = fieldID
		}
	}
	return projected, nil
}

func newBetterLeaksOccurrence(role betterLeaksOccurrenceRole, ruleID, value string, location report.Location, decodeDepth int, encodings []string, admittedRaw []byte) (betterLeaksOccurrence, bool, error) {
	var locationIndex *betterLeaksLocationIndex
	if admittedRaw != nil {
		locationIndex = newBetterLeaksLocationIndex(admittedRaw)
	}
	return newBetterLeaksOccurrenceWithLocationIndex(role, ruleID, value, location, decodeDepth, encodings, admittedRaw, locationIndex)
}

func newBetterLeaksOccurrenceWithLocationIndex(role betterLeaksOccurrenceRole, ruleID, value string, location report.Location, decodeDepth int, encodings []string, admittedRaw []byte, locationIndex *betterLeaksLocationIndex) (betterLeaksOccurrence, bool, error) {
	if value == "" {
		return betterLeaksOccurrence{}, false, nil
	}
	if len(value) > maxBetterLeaksOccurrenceBytes {
		return betterLeaksOccurrence{}, false, errBetterLeaksProjection
	}
	span, start, end, err := spanForBetterLeaksLocationWithLocationIndex(location, admittedRaw, locationIndex)
	if err != nil {
		return betterLeaksOccurrence{}, false, err
	}
	representation := betterLeaksOccurrenceLiteral
	if decodeDepth > 0 || len(encodings) > 0 {
		representation = betterLeaksOccurrenceDecoded
	}
	return betterLeaksOccurrence{
		value:          []byte(value),
		span:           span,
		start:          start,
		end:            end,
		offsetsValid:   admittedRaw != nil,
		ruleID:         ruleID,
		role:           role,
		representation: representation,
	}, true, nil
}

func spanForBetterLeaksLocation(location report.Location, admittedRaw []byte) (betterLeaksSpan, error) {
	var locationIndex *betterLeaksLocationIndex
	if admittedRaw != nil {
		locationIndex = newBetterLeaksLocationIndex(admittedRaw)
	}
	span, _, _, err := spanForBetterLeaksLocationWithLocationIndex(location, admittedRaw, locationIndex)
	return span, err
}

func spanForBetterLeaksLocationWithLocationIndex(location report.Location, admittedRaw []byte, locationIndex *betterLeaksLocationIndex) (betterLeaksSpan, int, int, error) {
	span := betterLeaksSpan{
		StartLine:   location.StartLine,
		EndLine:     location.EndLine,
		StartColumn: location.StartColumn,
		EndColumn:   location.EndColumn,
	}
	if location.StartLine < 0 || location.EndLine < 0 || location.StartColumn < 0 || location.EndColumn < 0 {
		return betterLeaksSpan{}, 0, 0, errBetterLeaksProjection
	}
	if admittedRaw == nil {
		return span, 0, 0, nil
	}
	if allBetterLeaksCoordinatesZero(location) {
		return betterLeaksSpan{}, 0, 0, errBetterLeaksProjection
	}
	if locationIndex == nil {
		locationIndex = newBetterLeaksLocationIndex(admittedRaw)
	}
	return locationIndex.spanForLocation(location)
}

func allBetterLeaksCoordinatesZero(location report.Location) bool {
	return location.StartLine == 0 && location.EndLine == 0 && location.StartColumn == 0 && location.EndColumn == 0
}

func boundedBetterLeaksField(value string) string {
	if len(value) <= maxBetterLeaksProjectedField {
		return value
	}
	return value[:maxBetterLeaksProjectedField]
}

func boundedBetterLeaksConfidence(value string) string {
	switch value {
	case blscan.ConfidenceLow, blscan.ConfidenceMedium, blscan.ConfidenceHigh:
		return value
	default:
		return ""
	}
}

func sortBetterLeaksFindings(findings []betterLeaksFinding) {
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Location != findings[j].Location {
			return findings[i].Location < findings[j].Location
		}
		if findings[i].RuleID != findings[j].RuleID {
			return findings[i].RuleID < findings[j].RuleID
		}
		if findings[i].Confidence != findings[j].Confidence {
			return findings[i].Confidence < findings[j].Confidence
		}
		if len(findings[i].Components) != len(findings[j].Components) {
			return len(findings[i].Components) < len(findings[j].Components)
		}
		for componentIndex := range findings[i].Components {
			left := findings[i].Components[componentIndex]
			right := findings[j].Components[componentIndex]
			if left.RuleID != right.RuleID {
				return left.RuleID < right.RuleID
			}
			if left.Optional != right.Optional {
				return !left.Optional && right.Optional
			}
		}
		return findings[i].OccurrenceCount < findings[j].OccurrenceCount
	})
}

func newBetterLeaksScanError(err error) error {
	if err == nil {
		return nil
	}
	kind := betterLeaksScanErrorSource
	switch {
	case errors.Is(err, errBetterLeaksFindingCap):
		kind = betterLeaksScanErrorCap
	case errors.Is(err, errBetterLeaksProjection):
		kind = betterLeaksScanErrorProjection
	case errors.Is(err, context.Canceled):
		kind = betterLeaksScanErrorCanceled
	case errors.Is(err, context.DeadlineExceeded):
		kind = betterLeaksScanErrorDeadline
	}
	return &betterLeaksScanError{kind: kind, cause: err}
}

func newBetterLeaksUnavailableError() error {
	return &betterLeaksScanError{kind: betterLeaksScanErrorUnavailable}
}

// policyFacts returns a defensive copy so diagnostics cannot mutate the
// immutable generation inventory.
func (s *betterLeaksScanner) policyFacts() DetectorFacts {
	if s == nil {
		return DetectorFacts{}
	}
	facts := s.facts
	facts.RuleIDs = append([]string(nil), facts.RuleIDs...)
	return facts
}

func resolveBetterLeaksConfig(cfg *blconfig.Config, policy BetterLeaksPolicy) (*blconfig.Config, DetectorFacts, error) {
	if cfg == nil {
		return nil, DetectorFacts{}, fmt.Errorf("secretguard: betterleaks configuration is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, DetectorFacts{}, newBetterLeaksConstructionError(err)
	}

	disable, err := normalizeRuleSelectors("disable_rules", policy.DisableRules)
	if err != nil {
		return nil, DetectorFacts{}, err
	}
	isolate, err := normalizeRuleSelectors("isolate_rules", policy.IsolateRules)
	if err != nil {
		return nil, DetectorFacts{}, err
	}
	if len(disable) > 0 && len(isolate) > 0 {
		return nil, DetectorFacts{}, fmt.Errorf("secretguard: betterleaks.disable_rules and betterleaks.isolate_rules are mutually exclusive")
	}
	if err := validateRuleSelectors(cfg, "disable_rules", disable); err != nil {
		return nil, DetectorFacts{}, err
	}
	if err := validateRuleSelectors(cfg, "isolate_rules", isolate); err != nil {
		return nil, DetectorFacts{}, err
	}

	selected := cfg
	switch {
	case len(disable) > 0:
		removed := make(map[string]struct{}, len(disable))
		for _, id := range disable {
			removed[id] = struct{}{}
		}
		if hasRemovedRequiredComponent(cfg, removed) {
			return nil, DetectorFacts{}, fmt.Errorf("secretguard: betterleaks.disable_rules cannot remove a required component")
		}
		selected = filterBetterLeaksRules(cfg, func(rule blconfig.Rule) bool {
			_, ok := removed[rule.ID]
			return !ok
		})
	case len(isolate) > 0:
		active := make(map[string]struct{}, len(isolate))
		var addRequired func(string) error
		addRequired = func(id string) error {
			if _, ok := active[id]; ok {
				return nil
			}
			rule, ok := cfg.Rule(id)
			if !ok {
				// Selectors were validated above. This path is defensive and
				// keeps the error bounded if upstream config resolution changes.
				return fmt.Errorf("secretguard: betterleaks isolate_rules has an invalid component closure")
			}
			active[id] = struct{}{}
			for _, component := range rule.Components {
				if component.Optional {
					continue
				}
				if err := addRequired(component.RuleID); err != nil {
					return err
				}
			}
			return nil
		}
		for _, id := range isolate {
			if err := addRequired(id); err != nil {
				return nil, DetectorFacts{}, err
			}
		}
		selected = filterBetterLeaksRules(cfg, func(rule blconfig.Rule) bool {
			_, ok := active[rule.ID]
			return ok
		})
	}
	selected = pruneUnselectedOptionalComponents(selected)
	if err := selected.Validate(); err != nil {
		return nil, DetectorFacts{}, fmt.Errorf("secretguard: selected betterleaks configuration is invalid")
	}

	ruleIDs := make([]string, 0, len(selected.Rules))
	for _, rule := range selected.Rules {
		ruleIDs = append(ruleIDs, rule.ID)
	}
	facts := DetectorFacts{
		Version:           betterLeaksPinnedVersion,
		ConfigHash:        selected.Hash(),
		RuleInventoryHash: hashRuleInventory(ruleIDs),
		ActiveRuleCount:   len(ruleIDs),
		MinimumConfidence: policy.MinimumConfidence,
		MaxDecodeDepth:    policy.MaxDecodeDepth,
		Workers:           policy.Workers,
		RuleIDs:           ruleIDs,
	}
	return selected, facts, nil
}

func validateRuleSelectors(cfg *blconfig.Config, field string, selectors []string) error {
	for _, selector := range selectors {
		// Do not include the supplied selector in the error. Selector values
		// are operator input and may contain sensitive data.
		if _, ok := cfg.Rule(selector); ok {
			continue
		}
		return fmt.Errorf("secretguard: betterleaks.%s contains an unknown rule selector", field)
	}
	return nil
}

func filterBetterLeaksRules(cfg *blconfig.Config, keep func(blconfig.Rule) bool) *blconfig.Config {
	selected := *cfg
	selected.Rules = make([]blconfig.Rule, 0, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		if keep(rule) {
			selected.Rules = append(selected.Rules, rule)
		}
	}
	return &selected
}

func hasRemovedRequiredComponent(cfg *blconfig.Config, removed map[string]struct{}) bool {
	for _, rule := range cfg.Rules {
		if _, ruleRemoved := removed[rule.ID]; ruleRemoved {
			continue
		}
		for _, component := range rule.Components {
			if component.Optional {
				continue
			}
			if _, componentRemoved := removed[component.RuleID]; componentRemoved {
				return true
			}
		}
	}
	return false
}

// pruneUnselectedOptionalComponents keeps selected roots valid when an
// optional component rule was not selected. Required component references are
// preserved; the isolation closure already retains their rules.
func pruneUnselectedOptionalComponents(cfg *blconfig.Config) *blconfig.Config {
	available := make(map[string]struct{}, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		available[rule.ID] = struct{}{}
	}
	selected := *cfg
	selected.Rules = make([]blconfig.Rule, len(cfg.Rules))
	for i, rule := range cfg.Rules {
		selected.Rules[i] = rule
		if len(rule.Components) == 0 {
			continue
		}
		components := make([]blconfig.Component, 0, len(rule.Components))
		for _, component := range rule.Components {
			if component.Optional {
				if _, ok := available[component.RuleID]; !ok {
					continue
				}
			}
			components = append(components, component)
		}
		selected.Rules[i].Components = components
	}
	return &selected
}

func hashRuleInventory(ruleIDs []string) string {
	canonical := append([]string(nil), ruleIDs...)
	sort.Strings(canonical)
	raw, _ := json.Marshal(canonical)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validateBetterLeaksScannerPolicy(policy BetterLeaksPolicy) error {
	if policy.Workers < 1 || policy.Workers > MaxBetterLeaksWorkers {
		return fmt.Errorf("secretguard: betterleaks workers must be between 1 and %d", MaxBetterLeaksWorkers)
	}
	if policy.MaxDecodeDepth < 0 || policy.MaxDecodeDepth > MaxBetterLeaksDecodeDepth {
		return fmt.Errorf("secretguard: betterleaks max decode depth must be between 0 and %d", MaxBetterLeaksDecodeDepth)
	}
	switch policy.MinimumConfidence {
	case blscan.ConfidenceLow, blscan.ConfidenceMedium, blscan.ConfidenceHigh:
	default:
		return fmt.Errorf("secretguard: betterleaks minimum confidence must be low, medium, or high")
	}
	return nil
}
