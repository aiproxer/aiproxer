package secretguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	blconfig "github.com/betterleaks/betterleaks/v2/config"
	blregexp "github.com/betterleaks/betterleaks/v2/regexp"
	"github.com/betterleaks/betterleaks/v2/report"
	blscan "github.com/betterleaks/betterleaks/v2/scan"
	"github.com/betterleaks/betterleaks/v2/sources"
)

func TestBetterLeaksLocationProjection_ProjectionOnlyUsesBoundedMapping(t *testing.T) {
	raw := bytes.Repeat([]byte("x\n"), (2*1024*1024)/2)
	index := newBetterLeaksLocationIndex(raw)
	findings := newlineDenseBetterLeaksFindings()

	allocs := testing.AllocsPerRun(1, func() {
		for _, finding := range findings {
			projected, err := projectBetterLeaksFindingWithFieldIDAndLocationIndex(finding, "messages[0].parts[0]", "fragment[0]", raw, index)
			if err != nil {
				t.Fatalf("project newline-dense finding: %v", err)
			}
			wantStart := (finding.Location.StartLine - 1) * 2
			if len(projected.occurrences) != 1 || !projected.occurrences[0].offsetsValid || projected.occurrences[0].start != wantStart || projected.occurrences[0].end != wantStart+1 {
				t.Fatalf("projected occurrence did not retain absolute offsets: %+v", projected.occurrences)
			}
		}
	})
	if allocs > 2048 {
		t.Fatalf("newline-dense projection allocations = %.0f; want one compact index reused across 256 findings", allocs)
	}
}

func TestBetterLeaksLiteralProjection_MismatchUsesReportedRangeAndSharedIndex(t *testing.T) {
	raw, location, wantStart := newlineDenseTailLocationFixture()
	index := newBetterLeaksLocationIndex(raw)
	original := betterLeaksOccurrence{
		value:          []byte("tail-secret"),
		span:           betterLeaksSpan{StartLine: location.StartLine, EndLine: location.EndLine, StartColumn: location.StartColumn, EndColumn: location.EndColumn},
		representation: betterLeaksOccurrenceLiteral,
	}
	allocs := testing.AllocsPerRun(1, func() {
		occurrence := original
		normalizeBetterLeaksLiteralOccurrenceWithLocationIndex(raw, &occurrence, index)
		if !occurrence.offsetsValid || occurrence.start != wantStart || occurrence.end != wantStart+len(occurrence.value) {
			t.Fatalf("normalized value-group range = %+v; want %d..%d", occurrence, wantStart, wantStart+len(occurrence.value))
		}
		if occurrence.span.StartLine != location.StartLine || occurrence.span.StartColumn != len("prefix=")+1 || occurrence.span.EndColumn != occurrence.span.StartColumn+len(occurrence.value)-1 {
			t.Fatalf("normalized value-group span = %+v; want tail literal coordinates", occurrence.span)
		}
	})
	if allocs != 0 {
		t.Fatalf("mismatched literal normalization allocations = %.0f; want shared-index lookup without per-occurrence index rebuild", allocs)
	}
}

func TestBetterLeaksLocationProjection_ProductionPathAllocationBound(t *testing.T) {
	raw, findings := newlineDenseValueGroupFixture()
	if len(raw) != 2*1024*1024 || len(findings) != maxBetterLeaksOccurrences || findings[0].Location.StartLine < 1_000_000 {
		t.Fatalf("value-group fixture shape = bytes:%d findings:%d first_line:%d; want 2 MiB, 256 findings near tail", len(raw), len(findings), findings[0].Location.StartLine)
	}
	allocs := testing.AllocsPerRun(1, func() {
		index := newBetterLeaksLocationIndex(raw)
		for _, finding := range findings {
			projected, err := projectBetterLeaksFindingWithFieldIDAndLocationIndex(finding, "messages[0].parts[0]", "fragment[0]", raw, index)
			if err != nil || len(projected.occurrences) != 1 || !projected.occurrences[0].offsetsValid {
				t.Fatalf("production-path projection failed: err=%v occurrences=%d", err, len(projected.occurrences))
			}
		}
	})
	if allocs > 1024 {
		t.Fatalf("production-path allocation count = %.0f; want one fragment index plus bounded finding projection", allocs)
	}
}

func TestBetterLeaksLocationProjection_ValueGroupsRetainOffsetsThroughRewriteBridge(t *testing.T) {
	raw, reports := newlineDenseValueGroupFixture()
	index := newBetterLeaksLocationIndex(raw)
	findings := make([]betterLeaksFinding, 0, len(reports))
	for _, reportFinding := range reports {
		projected, err := projectBetterLeaksFindingWithFieldIDAndLocationIndex(reportFinding, "messages[0].parts[0]", "fragment[0]", raw, index)
		if err != nil {
			t.Fatal(err)
		}
		findings = append(findings, projected)
	}
	fragment := LogicalFragment{Location: "messages[0].parts[0]", Raw: raw, privateID: "fragment[0]"}
	candidates := betterLeaksLiteralCandidates(fragment, findings)
	if len(candidates) != len(reports) {
		t.Fatalf("value-group candidate count = %d; want %d", len(candidates), len(reports))
	}
	matcher, err := newBetterLeaksRewriteMatcher(nil, fragment, candidates)
	if err != nil {
		t.Fatal(err)
	}
	rewriteMatcher, ok := matcher.(*betterLeaksRewriteMatcher)
	if !ok {
		t.Fatalf("rewrite matcher type = %T; want *betterLeaksRewriteMatcher", matcher)
	}
	ranges := rewriteMatcher.rangesForRawInput(raw)
	if len(ranges) != len(reports) || ranges[0].start <= len(raw)/2 || ranges[len(ranges)-1].end > len(raw) {
		t.Fatalf("value-group rewrite ranges = %d first=%+v last=%+v; want all near-tail offsets", len(ranges), ranges[0], ranges[len(ranges)-1])
	}
}

func TestBetterLeaksEligibility_AmbiguousStoredRangesFailClosedWithoutPerOccurrenceIndexBuild(t *testing.T) {
	raw, location, start, end := newlineDenseAmbiguousLocationFixture()
	index := newBetterLeaksLocationIndex(raw)
	projected, err := projectBetterLeaksFindingWithFieldIDAndLocationIndex(report.Finding{
		RuleID:   "ambiguous-value-group",
		Location: location,
		Match:    report.Match{Value: "ambiguous"},
	}, locationStringForTest, "fragment[0]", raw, index)
	if err != nil || len(projected.occurrences) != 1 || !projected.occurrences[0].offsetsValid || projected.occurrences[0].start != start || projected.occurrences[0].end != end {
		t.Fatalf("ambiguous projection = err:%v occurrences:%+v; want retained broad offsets after normalization refusal", err, projected.occurrences)
	}
	findings := make([]betterLeaksFinding, 0, maxBetterLeaksOccurrences)
	for range maxBetterLeaksOccurrences {
		findings = append(findings, projected)
	}
	fragments := []LogicalFragment{{Location: locationStringForTest, Raw: raw, privateID: "fragment[0]"}}
	allocs := testing.AllocsPerRun(1, func() {
		if err := validateBetterLeaksRedactionEligibility(fragments, findings); !errors.Is(err, errBetterLeaksUnrewritable) {
			t.Fatalf("ambiguous eligibility error = %v; want fail-closed unrewritable result", err)
		}
	})
	if allocs != 0 {
		t.Fatalf("ambiguous eligibility allocations = %.0f; want stored-offset validation without per-occurrence index allocation", allocs)
	}
}

func BenchmarkBetterLeaksLocationProjection_NewlineDenseFragmentProjectionOnly(b *testing.B) {
	raw := bytes.Repeat([]byte("x\n"), (2*1024*1024)/2)
	index := newBetterLeaksLocationIndex(raw)
	findings := newlineDenseBetterLeaksFindings()
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, finding := range findings {
			if _, err := projectBetterLeaksFindingWithFieldIDAndLocationIndex(finding, "messages[0].parts[0]", "fragment[0]", raw, index); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkBetterLeaksLocationProjection_NewlineDenseFragmentProductionPath(b *testing.B) {
	raw, findings := newlineDenseValueGroupFixture()
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := newBetterLeaksLocationIndex(raw)
		for _, finding := range findings {
			if _, err := projectBetterLeaksFindingWithFieldIDAndLocationIndex(finding, "messages[0].parts[0]", "fragment[0]", raw, index); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func newlineDenseNearCapScannerFixture(tb testing.TB) (*betterLeaksScanner, []LogicalFragment) {
	tb.Helper()
	scanner, err := newBetterLeaksScanner(BetterLeaksPolicy{Enabled: true, MinimumConfidence: "medium", Workers: 1, MaxFindings: 256, IsolateRules: []string{"github-pat"}})
	if err != nil {
		tb.Fatal(err)
	}
	tail := []byte(strings.Repeat("GITHUB_TOKEN="+adapterGitHubToken+"\n", 256))
	prefixBytes := DefaultScanMaxBytes - len(tail)
	raw := make([]byte, DefaultScanMaxBytes)
	for i := range prefixBytes {
		if i%2 == 0 {
			raw[i] = 'x'
		} else {
			raw[i] = '\n'
		}
	}
	copy(raw[prefixBytes:], tail)
	return scanner, []LogicalFragment{{Kind: FragmentText, Raw: raw, Location: locationStringForTest, privateID: "fragment[0]"}}
}

func TestBetterLeaksScan_NewlineDenseNearCap(t *testing.T) {
	scanner, fragments := newlineDenseNearCapScannerFixture(t)
	result, err := scanner.scanFragments(t.Context(), fragments)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseBetterLeaksOccurrenceBytes(result.Findings)
	if len(result.Findings) != 256 {
		t.Fatalf("finding count=%d; want 256", len(result.Findings))
	}
	for _, finding := range result.Findings {
		if len(finding.occurrences) != 1 || !finding.occurrences[0].offsetsValid || finding.occurrences[0].start < DefaultScanMaxBytes/2 {
			t.Fatal("finding lacks validated near-tail offsets")
		}
	}
}

func BenchmarkBetterLeaksScan_NewlineDenseNearCap(b *testing.B) {
	scanner, fragments := newlineDenseNearCapScannerFixture(b)
	b.ReportAllocs()
	b.SetBytes(DefaultScanMaxBytes)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := scanner.scanFragments(b.Context(), fragments)
		if err != nil || len(result.Findings) != 256 {
			b.Fatalf("scan err=%v finding count=%d", err, len(result.Findings))
		}
		releaseBetterLeaksOccurrenceBytes(result.Findings)
	}
}

func BenchmarkBetterLeaksScan_NewlineDenseDefaultPolicy(b *testing.B) {
	_, fragments := newlineDenseNearCapScannerFixture(b)
	scanner, err := newBetterLeaksScanner(BetterLeaksPolicy{Enabled: true, MinimumConfidence: DefaultBetterLeaksConfidence, MaxDecodeDepth: DefaultBetterLeaksDecodeDepth, Workers: 1, MaxFindings: 256})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(DefaultScanMaxBytes)
	b.ResetTimer()
	capFailures := 0
	for i := 0; i < b.N; i++ {
		result, err := scanner.scanFragments(b.Context(), fragments)
		if (err != nil && !errors.Is(err, errBetterLeaksFindingCap)) || len(result.Findings) != 256 {
			b.Fatalf("scan err=%v finding count=%d", err, len(result.Findings))
		}
		if err != nil {
			capFailures++
		}
		releaseBetterLeaksOccurrenceBytes(result.Findings)
	}
	b.ReportMetric(float64(capFailures)/float64(b.N), "cap-failures/op")
}

func newlineDenseTailLocationFixture() ([]byte, report.Location, int) {
	const targetBytes = 2 * 1024 * 1024
	tail := []byte("prefix=tail-secret;suffix\n")
	prefix := bytes.Repeat([]byte("x\n"), (targetBytes-len(tail))/2)
	raw := append(prefix, tail...)
	line := len(prefix)/2 + 1
	return raw, report.Location{
		StartLine:   line,
		EndLine:     line,
		StartColumn: 1,
		EndColumn:   len(tail) - 1,
	}, len(prefix) + len("prefix=")
}

func newlineDenseValueGroupFixture() ([]byte, []report.Finding) {
	const targetBytes = 2 * 1024 * 1024
	var tail strings.Builder
	findings := make([]report.Finding, 0, maxBetterLeaksOccurrences)
	for index := range maxBetterLeaksOccurrences {
		value := fmt.Sprintf("tail-secret-%03d", index)
		line := "prefix=" + value + ";suffix\n"
		startLine := index + 1
		findings = append(findings, report.Finding{
			RuleID: "newline-dense-value-group",
			Location: report.Location{
				StartLine:   startLine,
				EndLine:     startLine,
				StartColumn: 1,
				EndColumn:   len(line) - 1,
			},
			Match: report.Match{Value: value},
		})
		tail.WriteString(line)
	}
	tailBytes := []byte(tail.String())
	prefix := bytes.Repeat([]byte("x\n"), (targetBytes-len(tailBytes))/2)
	raw := append(prefix, tailBytes...)
	lineOffset := len(prefix) / 2
	for index := range findings {
		findings[index].Location.StartLine += lineOffset
		findings[index].Location.EndLine += lineOffset
	}
	return raw, findings
}

const locationStringForTest = "messages[0].parts[0]"

func newlineDenseAmbiguousLocationFixture() ([]byte, report.Location, int, int) {
	const targetBytes = 2 * 1024 * 1024
	tail := []byte("prefix=ambiguous;ambiguous;suffix\n")
	prefix := bytes.Repeat([]byte("x\n"), (targetBytes-len(tail))/2)
	raw := append(prefix, tail...)
	line := len(prefix)/2 + 1
	location := report.Location{StartLine: line, EndLine: line, StartColumn: 1, EndColumn: len(tail) - 1}
	return raw, location, len(prefix), len(prefix) + len(tail) - 1
}

func newlineDenseBetterLeaksFindings() []report.Finding {
	findings := make([]report.Finding, 0, maxBetterLeaksOccurrences)
	for line := 1; line <= maxBetterLeaksOccurrences; line++ {
		findings = append(findings, report.Finding{
			RuleID: "newline-dense-rule",
			Location: report.Location{
				StartLine:   line,
				EndLine:     line,
				StartColumn: 1,
				EndColumn:   1,
			},
			Match: report.Match{Value: "x"},
		})
	}
	return findings
}

const adapterGitHubToken = "ghp_aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5"

const (
	adapterAWSAccessID      = "AKIALALEMEL33243OLIA"
	adapterAWSSecretFixture = "aws_secret_access_key = \"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\""
)

func TestNewBetterLeaksScanner_AllowMarkersDoNotSuppressFindings(t *testing.T) {
	detector, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: blscan.ConfidenceMedium,
		MaxDecodeDepth:    1,
		Workers:           1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detector == nil {
		t.Fatal("enabled policy must construct a scanner")
	}

	for _, marker := range []string{"betterleaks:allow", "gitleaks:allow"} {
		t.Run(marker, func(t *testing.T) {
			findings := scanBetterLeaksTestFragment(t, detector, "GITHUB_TOKEN="+adapterGitHubToken+" // "+marker)
			if len(findings) == 0 {
				t.Fatalf("marker %q suppressed a detectable credential", marker)
			}
		})
	}
}

func TestBetterLeaksScanner_FingerprintMatchesPinnedAlgorithm(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	findings := scanBetterLeaksTestFragment(t, detector, "GITHUB_TOKEN="+adapterGitHubToken)
	if len(findings) == 0 {
		t.Fatal("fingerprint fixture produced no finding")
	}
	// Pinned BetterLeaks fingerprint of adapterGitHubToken. Hard-coded so an
	// upstream algorithm change fails loudly instead of moving both sides of
	// the comparison together.
	const want = "c436cee6879ff8dca3fdc44ebc06b98eedcd6a26e541e8c73f69ccc1c811778b"
	if got := findings[0].Match.Fingerprint; got != want {
		t.Fatalf("fingerprint = %q; want pinned %q", got, want)
	}
}

func scanBetterLeaksTestFragment(t *testing.T, detector *betterLeaksScanner, content string) []report.Finding {
	t.Helper()
	var findings []report.Finding
	_, err := detector.scanner.Scan(context.Background(), betterLeaksLogicalFragmentSource{raw: []byte(content)}, func(finding report.Finding) error {
		findings = append(findings, finding)
		return nil
	})
	if err != nil {
		t.Fatalf("scan fragment: %v", err)
	}
	return findings
}

func defaultBetterLeaksConfig() (*blconfig.Config, error) {
	return blconfig.Default()
}

func betterLeaksRule(cfg *blconfig.Config, ruleID string) blconfig.Rule {
	rule, ok := cfg.Rule(ruleID)
	if !ok {
		panic("missing BetterLeaks rule " + ruleID)
	}
	return rule
}

func policyFinding(findings []report.Finding, ruleID string) (report.Finding, bool) {
	for _, finding := range findings {
		if finding.RuleID == ruleID {
			return finding, true
		}
	}
	return report.Finding{}, false
}

func componentFindingIDs(finding report.Finding) []string {
	seen := make(map[string]struct{})
	var ids []string
	for _, set := range finding.ComponentSets {
		for _, component := range set.Components {
			if _, ok := seen[component.RuleID]; ok {
				continue
			}
			seen[component.RuleID] = struct{}{}
			ids = append(ids, component.RuleID)
		}
	}
	sort.Strings(ids)
	return ids
}

func TestNewBetterLeaksScanner_RejectsInvalidConstructionPolicy(t *testing.T) {
	tests := []struct {
		name   string
		policy BetterLeaksPolicy
	}{
		{
			name: "zero workers",
			policy: BetterLeaksPolicy{
				Enabled:           true,
				MinimumConfidence: blscan.ConfidenceMedium,
				MaxDecodeDepth:    1,
				Workers:           0,
			},
		},
		{
			name: "negative workers",
			policy: BetterLeaksPolicy{
				Enabled:           true,
				MinimumConfidence: blscan.ConfidenceMedium,
				MaxDecodeDepth:    1,
				Workers:           -1,
			},
		},
		{
			name: "invalid confidence",
			policy: BetterLeaksPolicy{
				Enabled:           true,
				MinimumConfidence: "unknown",
				MaxDecodeDepth:    1,
				Workers:           1,
			},
		},
		{
			name: "negative decode depth",
			policy: BetterLeaksPolicy{
				Enabled:           true,
				MinimumConfidence: blscan.ConfidenceMedium,
				MaxDecodeDepth:    -1,
				Workers:           1,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if detector, err := newBetterLeaksScanner(test.policy); err == nil || detector != nil {
				t.Fatalf("newBetterLeaksScanner(%+v) = detector=%v, err=%v; want construction error", test.policy, detector, err)
			}
		})
	}
}

func TestNewBetterLeaksScanner_DisabledPolicyDoesNotConstructHandle(t *testing.T) {
	detector, err := newBetterLeaksScanner(BetterLeaksPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if detector != nil {
		t.Fatal("disabled policy must not construct a scanner handle")
	}
}

func TestBetterLeaksScanner_ScanCanceledContext(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := detector.scanFragments(ctx, []LogicalFragment{{
		Location: "messages[0].parts[0]",
		Raw:      []byte("GITHUB_TOKEN=" + adapterGitHubToken),
	}})
	if err == nil {
		t.Fatal("canceled scan returned nil error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scan error = %v; want context.Canceled", err)
	}
	if strings.Contains(err.Error(), adapterGitHubToken) {
		t.Fatalf("canceled scan error leaked secret: %v", err)
	}
}

func TestBetterLeaksScanner_ScanSourceErrorIsSanitizedAndIdentifiable(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	sourceErr := errors.New("source failed while reading " + adapterGitHubToken)
	result := betterLeaksScanResult{}

	err := detector.scanSource(context.Background(), adapterErrorSource{err: sourceErr}, "messages[0]", &result)
	if err == nil {
		t.Fatal("source failure returned nil error")
	}
	if !errors.Is(err, sourceErr) {
		t.Fatalf("source error = %v; errors.Is does not preserve source identity", err)
	}
	if strings.Contains(err.Error(), adapterGitHubToken) || strings.Contains(err.Error(), "source failed") {
		t.Fatalf("source error leaked upstream detail: %v", err)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("source failure produced %d findings", len(result.Findings))
	}
}

func TestBetterLeaksConstructionErrorIsSanitizedAndIdentifiable(t *testing.T) {
	t.Parallel()

	secret := "scanner-construction-secret-canary"
	sourceErr := errors.New("upstream configuration contains " + secret)
	err := newBetterLeaksConstructionError(sourceErr)
	if err == nil {
		t.Fatal("construction error sanitizer returned nil")
	}
	if !errors.Is(err, sourceErr) {
		t.Fatal("sanitized construction error must preserve private error identity")
	}
	if errors.Unwrap(err) != nil {
		t.Fatal("sanitized construction error must not expose an unwrap chain")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "upstream configuration") {
		t.Fatal("construction error exposed upstream detail")
	}
	if wrapped := fmt.Errorf("generation rejected: %w", err); strings.Contains(wrapped.Error(), secret) {
		t.Fatal("generic wrapping exposed upstream detail")
	}
}

func TestBetterLeaksScanner_FindingCapSpansSourceFragments(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	result := betterLeaksScanResult{}
	err := detector.scanSource(context.Background(), adapterRepeatedSource{
		count: 257,
		raw:   "GITHUB_TOKEN=" + adapterGitHubToken,
	}, "messages[0].parts[0]", &result)
	if err == nil {
		t.Fatal("finding cap was not enforced")
	}
	if !errors.Is(err, errBetterLeaksFindingCap) {
		t.Fatalf("finding cap error = %v; want cap classification", err)
	}
	if len(result.Findings) != DefaultBetterLeaksMaxFindings {
		t.Fatalf("projected findings = %d; want %d", len(result.Findings), DefaultBetterLeaksMaxFindings)
	}
	if strings.Contains(err.Error(), adapterGitHubToken) {
		t.Fatalf("finding cap error leaked secret: %v", err)
	}
}

func TestBetterLeaksScanner_ProjectsRequiredComponentsAndSafeMetadata(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	result, err := detector.scanFragments(context.Background(), []LogicalFragment{{
		Location: "messages[0].parts[0]",
		Raw:      []byte(awsMultipartFragment),
	}})
	if err != nil {
		t.Fatal(err)
	}
	finding, ok := findBetterLeaksProjectedFinding(result.Findings, "aws-access-token")
	if !ok {
		t.Fatalf("projected findings did not include aws-access-token (count=%d)", len(result.Findings))
	}
	if !containsBetterLeaksComponent(finding.Components, "aws-secret-access-key") {
		t.Fatalf("required multipart component was not projected (count=%d)", len(finding.Components))
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), adapterGitHubToken) || strings.Contains(string(encoded), "AKIALALEMEL33243OLIA") {
		t.Fatalf("projected result contains raw secret material: %s", encoded)
	}
}

func TestBetterLeaksScanner_PreservesWholeLogicalFragmentContext(t *testing.T) {
	detector, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: blscan.ConfidenceMedium,
		MaxDecodeDepth:    1,
		Workers:           1,
		IsolateRules:      []string{"aws-access-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, padding := range []int{124940, 124950, 124960} {
		t.Run(fmt.Sprintf("padding_%d", padding), func(t *testing.T) {
			raw := strings.Repeat("x", padding) + "\n" + adapterAWSAccessID + "\n\n" + adapterAWSSecretFixture
			result, err := detector.scanFragments(context.Background(), []LogicalFragment{{
				Location: "messages[0].parts[0]",
				Raw:      []byte(raw),
			}})
			if err != nil {
				t.Fatal(err)
			}
			finding, ok := findBetterLeaksProjectedFinding(result.Findings, "aws-access-token")
			if !ok {
				t.Fatalf("whole logical fragment lost multipart context at padding %d (count=%d)", padding, len(result.Findings))
			}
			if !containsBetterLeaksComponent(finding.Components, "aws-secret-access-key") {
				t.Fatalf("whole logical fragment lost required component at padding %d (count=%d)", padding, len(finding.Components))
			}
		})
	}
}

func TestBetterLeaksScanner_PreservesLongLineCoordinates(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	raw := strings.Repeat("x", 1_500_000) + " GITHUB_TOKEN=" + adapterGitHubToken
	result, err := detector.scanFragments(context.Background(), []LogicalFragment{{
		Location: "messages[0].parts[0]",
		Raw:      []byte(raw),
	}})
	if err != nil {
		t.Fatal(err)
	}
	finding, ok := findBetterLeaksProjectedFinding(result.Findings, "github-pat")
	if !ok {
		t.Fatalf("long-line scan did not project github-pat (count=%d)", len(result.Findings))
	}
	wantStart := strings.Index(raw, adapterGitHubToken) + 1
	wantEnd := wantStart + len(adapterGitHubToken) - 1
	for _, occurrence := range finding.occurrences {
		if string(occurrence.value) != adapterGitHubToken {
			continue
		}
		if occurrence.span.StartColumn != wantStart || occurrence.span.EndColumn != wantEnd {
			t.Fatalf("long-line occurrence span = %+v; want columns %d..%d", occurrence.span, wantStart, wantEnd)
		}
		return
	}
	t.Fatal("long-line projection lost the github token occurrence")
}

func TestBetterLeaksLogicalFragmentSource_YieldsOneWholeFragment(t *testing.T) {
	raw := []byte(strings.Repeat("x", 125000))
	var fragments []sources.Fragment
	err := (betterLeaksLogicalFragmentSource{raw: raw}).Fragments(context.Background(), func(fragment sources.Fragment, fragmentErr error) error {
		if fragmentErr != nil {
			return fragmentErr
		}
		fragments = append(fragments, fragment)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 1 {
		t.Fatalf("logical fragment source yielded %d fragments; want 1", len(fragments))
	}
	if fragments[0].Raw != string(raw) {
		t.Fatal("logical fragment source changed admitted raw content")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (betterLeaksLogicalFragmentSource{raw: raw}).Fragments(ctx, func(sources.Fragment, error) error {
		t.Fatal("canceled logical fragment source invoked its callback")
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled logical fragment source error = %v; want context.Canceled", err)
	}
}

func TestBetterLeaksLogicalFragmentSource_TextDefersByteMaterialization(t *testing.T) {
	text := strings.Repeat("x", 128*1024)
	var raw []byte
	source := betterLeaksLogicalFragmentSource{text: text, rawCache: &raw}
	var yielded sources.Fragment
	if err := source.Fragments(context.Background(), func(fragment sources.Fragment, fragmentErr error) error {
		if fragmentErr != nil {
			return fragmentErr
		}
		yielded = fragment
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if yielded.Raw != text {
		t.Fatal("text source changed its immutable content")
	}
	if raw != nil {
		t.Fatalf("source materialized bytes while yielding text: %d", len(raw))
	}
	got := source.betterLeaksRaw()
	if string(got) != text || &got[0] != &raw[0] {
		t.Fatal("source did not retain one lazily materialized byte representation")
	}
}

func TestBetterLeaksFindingNeedsRawOnlyForLiteralOccurrences(t *testing.T) {
	tests := []struct {
		name    string
		finding report.Finding
		want    bool
	}{
		{name: "literal primary", finding: report.Finding{Match: report.Match{Value: "literal"}}, want: true},
		{name: "decoded primary", finding: report.Finding{DecodeDepth: 1, Match: report.Match{Value: "decoded"}}, want: false},
		{name: "literal component", finding: report.Finding{ComponentSets: []report.ComponentSet{{Components: []report.ComponentFinding{{Match: report.Match{Value: "literal"}}}}}}, want: true},
		{name: "decoded component", finding: report.Finding{ComponentSets: []report.ComponentSet{{Components: []report.ComponentFinding{{DecodeDepth: 1, Match: report.Match{Value: "decoded"}}}}}}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := betterLeaksFindingNeedsRaw(tc.finding); got != tc.want {
				t.Fatalf("betterLeaksFindingNeedsRaw()=%t, want %t", got, tc.want)
			}
		})
	}
}

func TestProjectBetterLeaksFinding_PreservesPrivateOccurrenceIdentity(t *testing.T) {
	location := "messages[0].parts[0]"
	first, err := projectBetterLeaksFinding(report.Finding{
		RuleID:     "same-rule",
		Confidence: blscan.ConfidenceMedium,
		Location:   report.Location{StartLine: 4, EndLine: 4, StartColumn: 2, EndColumn: 15},
		Match:      report.Match{Value: "literal-secret-a"},
		ComponentSets: []report.ComponentSet{{Components: []report.ComponentFinding{{
			RuleID:      "same-component",
			DecodeDepth: 1,
			Location:    report.Location{StartLine: 4, EndLine: 4, StartColumn: 20, EndColumn: 34},
			Match:       report.Match{Value: "decoded-secret-a"},
		}}}},
	}, location, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := projectBetterLeaksFinding(report.Finding{
		RuleID:     "same-rule",
		Confidence: blscan.ConfidenceMedium,
		Location:   report.Location{StartLine: 4, EndLine: 4, StartColumn: 2, EndColumn: 15},
		Match:      report.Match{Value: "literal-secret-b"},
	}, location, nil)
	if err != nil {
		t.Fatal(err)
	}

	if first.RuleID != second.RuleID || first.Location != second.Location {
		t.Fatalf("same-rule findings lost canonical identity: first=%s/%s second=%s/%s", first.RuleID, first.Location, second.RuleID, second.Location)
	}
	if len(first.occurrences) != 2 || len(second.occurrences) != 1 {
		t.Fatalf("private occurrence projection = first=%d second=%d; want component and repeated occurrences", len(first.occurrences), len(second.occurrences))
	}
	if string(first.occurrences[0].value) != "literal-secret-a" || first.occurrences[0].representation != betterLeaksOccurrenceLiteral {
		t.Fatalf("literal primary occurrence was not retained: representation=%d", first.occurrences[0].representation)
	}
	if first.occurrences[0].span != (betterLeaksSpan{StartLine: 4, EndLine: 4, StartColumn: 2, EndColumn: 15}) {
		t.Fatalf("primary occurrence span = %+v; want source span", first.occurrences[0].span)
	}
	if first.occurrences[1].role != betterLeaksOccurrenceComponent || first.occurrences[1].representation != betterLeaksOccurrenceDecoded {
		t.Fatalf("decoded component identity was not retained: role=%d representation=%d", first.occurrences[1].role, first.occurrences[1].representation)
	}
	if string(first.occurrences[0].value) == string(second.occurrences[0].value) {
		t.Fatal("distinct same-rule values were collapsed")
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "literal-secret-a") || strings.Contains(string(encoded), "decoded-secret-a") {
		t.Fatalf("private occurrence values were marshaled: %s", encoded)
	}
}

func TestProjectBetterLeaksFinding_PreservesDistinctLongValues(t *testing.T) {
	prefix := strings.Repeat("long-secret-", 400)
	firstValue := prefix + "suffix-a"
	secondValue := prefix + "suffix-b"
	first, err := projectBetterLeaksFinding(report.Finding{
		RuleID: "same-rule",
		Match:  report.Match{Value: firstValue},
	}, "messages[0].parts[0]", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := projectBetterLeaksFinding(report.Finding{
		RuleID: "same-rule",
		Match:  report.Match{Value: secondValue},
	}, "messages[0].parts[0]", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.occurrences) != 1 || len(second.occurrences) != 1 {
		t.Fatalf("long-value occurrence projection = first=%d second=%d; want one each", len(first.occurrences), len(second.occurrences))
	}
	if string(first.occurrences[0].value) != firstValue || string(second.occurrences[0].value) != secondValue {
		t.Fatal("long-value occurrence identity was truncated")
	}
	if string(first.occurrences[0].value) == string(second.occurrences[0].value) {
		t.Fatal("distinct long-value suffixes were collapsed")
	}
}

func TestProjectBetterLeaksFinding_RejectsUnrepresentableIdentity(t *testing.T) {
	tooLarge := strings.Repeat("s", maxBetterLeaksOccurrenceBytes+1)
	if _, err := projectBetterLeaksFinding(report.Finding{
		RuleID: "oversized-rule",
		Match:  report.Match{Value: tooLarge},
	}, "messages[0].parts[0]", nil); !errors.Is(err, errBetterLeaksProjection) {
		t.Fatalf("oversized occurrence error = %v; want bounded projection failure", err)
	}

	if _, err := projectBetterLeaksFinding(report.Finding{
		RuleID: "out-of-bounds-rule",
		Location: report.Location{
			StartLine: 1, EndLine: 1, StartColumn: 33, EndColumn: 33,
		},
		Match: report.Match{Value: "secret"},
	}, "messages[0].parts[0]", []byte(strings.Repeat("x", 32))); !errors.Is(err, errBetterLeaksProjection) {
		t.Fatalf("out-of-bounds location error = %v; want bounded projection failure", err)
	}
}

func TestBetterLeaksScanner_PreservesDeterministicFragmentLocations(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	result, err := detector.scanFragments(context.Background(), []LogicalFragment{
		{Location: "messages[1].parts[0]", Raw: []byte("GITHUB_TOKEN=" + adapterGitHubToken)},
		{Location: "messages[0].parts[0]", Raw: []byte("GITHUB_TOKEN=" + adapterGitHubToken)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 2 {
		t.Fatalf("projected findings = %d; want 2", len(result.Findings))
	}
	if result.Findings[0].Location != "messages[0].parts[0]" || result.Findings[1].Location != "messages[1].parts[0]" {
		t.Fatalf("projected locations are not deterministic: %q, %q", result.Findings[0].Location, result.Findings[1].Location)
	}
}

func TestBetterLeaksScanner_IsSafeForConcurrentReuse(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	const requests = 16
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for i := range requests {
		wg.Go(func() {
			result, err := detector.scanFragments(context.Background(), []LogicalFragment{{
				Location: fmt.Sprintf("messages[%d].parts[0]", i),
				Raw:      []byte("GITHUB_TOKEN=" + adapterGitHubToken),
			}})
			if err == nil && len(result.Findings) == 0 {
				err = errors.New("concurrent scan produced no finding")
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func newTestBetterLeaksScanner(t *testing.T) *betterLeaksScanner {
	t.Helper()
	detector, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: blscan.ConfidenceMedium,
		MaxDecodeDepth:    1,
		Workers:           4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detector == nil {
		t.Fatal("test scanner is nil")
	}
	return detector
}

func findBetterLeaksProjectedFinding(findings []betterLeaksFinding, ruleID string) (betterLeaksFinding, bool) {
	for _, finding := range findings {
		if finding.RuleID == ruleID {
			return finding, true
		}
	}
	return betterLeaksFinding{}, false
}

func containsBetterLeaksComponent(components []betterLeaksComponent, ruleID string) bool {
	for _, component := range components {
		if component.RuleID == ruleID {
			return true
		}
	}
	return false
}

type adapterErrorSource struct {
	err error
}

func (s adapterErrorSource) Fragments(ctx context.Context, yield sources.FragmentsFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return yield(sources.Fragment{}, s.err)
}

type adapterRepeatedSource struct {
	count int
	raw   string
}

func (s adapterRepeatedSource) Fragments(ctx context.Context, yield sources.FragmentsFunc) error {
	for i := 0; i < s.count; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := yield(sources.Fragment{Raw: s.raw}, nil); err != nil {
			return err
		}
	}
	return nil
}

func TestMultipartProjectionRepair_LocalCoverageBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		components  int
		unique      bool
		truncated   bool
		wantBlocked bool
	}{
		{name: "upstream truncated", truncated: true, wantBlocked: true},
		{name: "components at cap", components: maxBetterLeaksProjectedComponents, unique: true},
		{name: "components exceeding cap", components: maxBetterLeaksProjectedComponents + 1, unique: true, wantBlocked: true},
		{name: "occurrences at cap", components: maxBetterLeaksOccurrences - 1},
		{name: "occurrences exceeding cap", components: maxBetterLeaksOccurrences, wantBlocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finding := report.Finding{RuleID: "github-pat", Match: report.Match{Value: "x"}, Location: report.Location{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: 1}, ComponentSetsTruncated: tc.truncated}
			for i := 0; i < tc.components; i++ {
				id := "component"
				if tc.unique {
					id = fmt.Sprintf("component-%d", i)
				}
				finding.ComponentSets = append(finding.ComponentSets, report.ComponentSet{Components: []report.ComponentFinding{{RuleID: id, Match: finding.Match, Location: finding.Location}}})
			}
			projected, err := projectBetterLeaksFindingWithFieldID(finding, "messages[0].parts[0]", "fragment[0]", []byte("x"))
			if err != nil {
				t.Fatal(err)
			}
			defer releaseBetterLeaksOccurrenceBytes([]betterLeaksFinding{projected})
			if len(projected.Components) > maxBetterLeaksProjectedComponents || len(projected.occurrences) > maxBetterLeaksOccurrences {
				t.Fatal("projection exceeded local bounds")
			}
			if got, want := len(projected.occurrences), min(tc.components+1, min(maxBetterLeaksOccurrences, maxBetterLeaksProjectedComponents+1)); tc.unique && got != want {
				t.Fatalf("unique component occurrences=%d; want %d", got, want)
			}
			if !tc.unique && len(projected.occurrences) != min(tc.components+1, maxBetterLeaksOccurrences) {
				t.Fatal("repeated component projection dropped occurrences before its cap")
			}
			err = validateBetterLeaksRedactionEligibility([]LogicalFragment{{Location: projected.Location, privateID: "fragment[0]", Raw: []byte("x")}}, []betterLeaksFinding{projected})
			if errors.Is(err, errBetterLeaksUnrewritable) != tc.wantBlocked {
				t.Fatalf("incomplete coverage blocked=%t; want %t", err != nil, tc.wantBlocked)
			}
		})
	}
}

func TestMultipartProjectionRepair_ComponentCapStillAdmitsKnownRuleOccurrences(t *testing.T) {
	finding := report.Finding{RuleID: "github-pat", Match: report.Match{Value: "x"}, Location: report.Location{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: 1}}
	for i := range maxBetterLeaksProjectedComponents {
		finding.ComponentSets = append(finding.ComponentSets, report.ComponentSet{Components: []report.ComponentFinding{{RuleID: fmt.Sprintf("component-%d", i), Match: finding.Match, Location: finding.Location}}})
	}
	finding.ComponentSets = append(finding.ComponentSets, finding.ComponentSets[0])
	projected, err := projectBetterLeaksFinding(finding, "messages[0].parts[0]", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	defer releaseBetterLeaksOccurrenceBytes([]betterLeaksFinding{projected})
	if len(projected.occurrences) != maxBetterLeaksProjectedComponents+2 {
		t.Fatal("component metadata cap dropped a known rule occurrence")
	}
	if err := validateBetterLeaksRedactionEligibility([]LogicalFragment{{Location: projected.Location, Raw: []byte("x")}}, []betterLeaksFinding{projected}); err != nil {
		t.Fatal("complete known-rule projection blocked at metadata cap")
	}
}

func TestMultipartProjectionRepair_RealScannerTruncationBlocksClonePublication(t *testing.T) {
	guard, services := newBetterLeaksActionGuard(t, ActionRedact, 0, 0)
	generation, ok := services.Capability.(*GenerationServices)
	if !ok {
		t.Fatal("fixture capability is not generation services")
	}
	scanner, err := blscan.New(&blconfig.Config{Rules: []blconfig.Rule{
		{ID: "github-pat", Regex: `ghp_[A-Za-z0-9]{36}`, Components: []blconfig.Component{{RuleID: "github-fine-grained-pat"}}},
		{ID: "github-fine-grained-pat", Regex: `companion-[0-9]{3}`, SkipReport: true},
	}}, blscan.WithWorkers(1), blscan.WithRegexEngine(blregexp.Stdlib{}))
	if err != nil {
		t.Fatal(err)
	}
	generation.betterLeaks.scanner = scanner
	var text strings.Builder
	text.WriteString(adapterGitHubToken + "\n")
	for i := range 101 {
		fmt.Fprintf(&text, "companion-%03d\n", i)
	}
	var upstream report.Finding
	_, err = scanner.Scan(context.Background(), betterLeaksLogicalFragmentSource{text: text.String()}, func(f report.Finding) error { upstream = f; return nil })
	if err != nil || !upstream.ComponentSetsTruncated || len(upstream.ComponentSets) != 100 {
		t.Fatal("fixture did not trigger pinned scanner component truncation")
	}
	call := betterLeaksTextCall(text.String())
	call.Messages[0].Parts = append(call.Messages[0].Parts, aipapi.TextPart(adapterGitHubToken))
	// The unrelated candidate would successfully redact on its own.
	services.MatcherResolver = staticResolver{m: newExactStub(adapterGitHubToken, "REQUEST_CREDENTIAL", sdk.SourceCategoryRequestCred)}
	before := aipapi.CloneCall(call)
	decision, err := guard.Evaluate(context.Background(), &call, sdk.Meta{}, services)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != sdk.OutcomeBlock || decision.FailureKind != FailureKindUnrewritableDetectedSecret || decision.MutationCount != 0 {
		fatalDecisionSummary(t, decision)
	}
	if !reflect.DeepEqual(before, call) {
		t.Fatal("incomplete multipart redaction published a clone")
	}
	if err := decision.Validate(); err != nil {
		t.Fatal(err)
	}
	assertBetterLeaksDecisionSafe(t, decision)
}

func TestMultipartProjectionRepair_LogFailureRetainsIndependentFindingsAndLimit(t *testing.T) {
	for _, category := range []sdk.SourceCategory{sdk.SourceCategoryProxyEnv, sdk.SourceCategoryRequestCred} {
		t.Run(string(category), func(t *testing.T) {
			for _, limited := range []bool{false, true} {
				text := "GITHUB_TOKEN=" + adapterGitHubToken + " independent-secret"
				limit := 0
				if limited {
					limit = len(text) * 2
				}
				guard, services := newBetterLeaksActionGuard(t, ActionLog, limit, 1)
				services.MatcherResolver = staticResolver{m: newExactStub("independent-secret", "EXACT_CANARY", category)}
				call := betterLeaksTextCall(text)
				call.Messages[0].Parts = append(call.Messages[0].Parts, aipapi.TextPart(text))
				if limited {
					call.Messages[0].Parts = append(call.Messages[0].Parts, aipapi.TextPart(strings.Repeat("x", 20)))
				}
				before := aipapi.CloneCall(call)
				decision, err := guard.Evaluate(context.Background(), &call, sdk.Meta{}, services)
				if err != nil {
					t.Fatal(err)
				}
				wantKind, wantReason := sdk.FailureKindDetectorFailure, "betterleaks scan failed"
				if limited {
					wantKind, wantReason = FailureKindScanLimit, "scan_max_bytes exceeded; betterleaks scan failed"
				}
				if decision.Outcome != sdk.OutcomeLog || decision.FailureKind != wantKind || decision.ScanLimitHit != limited || decision.FailureReason != wantReason {
					fatalDecisionSummary(t, decision)
				}
				count := 0
				for _, f := range decision.Findings {
					if f.SecretRefName == "EXACT_CANARY" && f.SourceCategory == category {
						count++
					}
				}
				if count != 2 {
					t.Fatalf("independent exact findings=%d; want 2 admitted locations", count)
				}
				if !reflect.DeepEqual(before, call) {
					t.Fatal("log failure mutated call")
				}
				if err := decision.Validate(); err != nil {
					t.Fatal(err)
				}
				assertBetterLeaksDecisionSafe(t, decision)
			}
		})
	}
}
