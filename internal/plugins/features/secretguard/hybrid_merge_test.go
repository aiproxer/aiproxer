package secretguard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestMergePrivateHybridFindings_OverlappingOccurrencePreservesExactAndBetterLeaksProvenance(t *testing.T) {
	t.Parallel()

	span := betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 8, EndColumn: 24}
	exact := exactPrivateFinding{
		finding: sdk.Finding{
			SecretRefName:  "PROXY_API_KEY",
			Aliases:        []string{"API_KEY"},
			SourceCategory: sdk.SourceCategoryProxyEnv,
			Location:       "messages[0].parts[0]",
			DetectorID:     sdk.DetectorIDExact,
		},
		occurrences: []betterLeaksOccurrence{{
			value: []byte("overlap-secret"), span: span, ruleID: "PROXY_API_KEY",
		}},
	}
	discovery := betterLeaksFinding{
		RuleID: "generic-api-key", Confidence: sdk.ConfidenceHigh,
		Location: "messages[0].parts[0]", OccurrenceCount: 1,
		occurrences: []betterLeaksOccurrence{{
			value: []byte("overlap-secret"), span: span, ruleID: "generic-api-key",
		}},
	}

	got, err := mergeHybridFindings([]exactPrivateFinding{exact}, []betterLeaksFinding{discovery}, DetectorFacts{RuleIDs: []string{"generic-api-key"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("merged findings = %d, want one overlap result: %#v", len(got), got)
	}
	merged := got[0]
	if merged.SecretRefName != "PROXY_API_KEY" || merged.SourceCategory != sdk.SourceCategoryProxyEnv || !reflect.DeepEqual(merged.Aliases, []string{"API_KEY"}) {
		t.Fatalf("exact attribution was not preserved: %#v", merged)
	}
	if merged.DetectorID != sdk.DetectorIDExact || merged.RuleID != "generic-api-key" || merged.Confidence != sdk.ConfidenceHigh {
		t.Fatalf("BetterLeaks provenance was not attached to exact result: %#v", merged)
	}
	if merged.OccurrenceCount != 1 {
		t.Fatalf("overlap was double-counted: %#v", merged)
	}
}

func TestCollectExactPrivateFindings_NeutralPositionalMatcherDeduplicatesBetterLeaksOverlap(t *testing.T) {
	t.Parallel()

	const secret = "accepted-request-secret"
	input := []byte("before " + secret + " after " + secret)
	m := positionalCredentialMatcher{
		secret: []byte(secret),
		finding: sdk.Finding{
			SecretRefName:   "accepted-key",
			SourceCategory:  sdk.SourceCategoryRequestCred,
			OccurrenceCount: 2,
		},
	}
	fragment := LogicalFragment{Location: "messages[0].parts[0]", Raw: input}
	found, err := m.ScanBytes(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	private := collectExactPrivateFindings(m, fragment, found)
	if len(private) != 1 || len(private[0].occurrences) != 2 {
		t.Fatalf("private positional occurrences = %#v", private)
	}

	firstStart := bytes.Index(input, []byte(secret))
	firstEnd := firstStart + len(secret)
	firstSpan, err := spanForByteRange(input, firstStart, firstEnd)
	if err != nil {
		t.Fatal(err)
	}
	discovery := betterLeaksFinding{
		RuleID:          "generic-api-key",
		Confidence:      sdk.ConfidenceHigh,
		Location:        fragment.Location,
		OccurrenceCount: 1,
		occurrences: []betterLeaksOccurrence{{
			value: []byte(secret), span: firstSpan, start: firstStart, end: firstEnd,
			offsetsValid: true, fieldID: fragment.privateID, ruleID: "generic-api-key",
			role: betterLeaksOccurrencePrimary, representation: betterLeaksOccurrenceLiteral,
		}},
	}
	got, err := mergeHybridFindings(private, []betterLeaksFinding{discovery}, DetectorFacts{RuleIDs: []string{"generic-api-key"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].OccurrenceCount != 2 {
		t.Fatalf("overlap cardinality = %#v, want one finding with two occurrences", got)
	}
	if got[0].SecretRefName != "accepted-key" || got[0].SourceCategory != sdk.SourceCategoryRequestCred || got[0].RuleID != "generic-api-key" {
		t.Fatalf("accepted credential attribution/provenance = %#v", got[0])
	}
}

func TestCollectExactPrivateFindings_NeutralPositionalMatcherMapsJSONEscapes(t *testing.T) {
	t.Parallel()

	const secret = "accepted-request-secret"
	raw := []byte(`["accepted-request-secret","\u0061ccepted-request-secret"]`)
	m := positionalCredentialMatcher{
		secret: []byte(secret),
		finding: sdk.Finding{
			SecretRefName:  "accepted-key",
			SourceCategory: sdk.SourceCategoryRequestCred,
		},
	}
	findings, err := scanJSONPayload(t.Context(), m, raw)
	if err != nil {
		t.Fatal(err)
	}
	fragment := LogicalFragment{Location: "messages[0].parts[0]", Kind: FragmentJSON, Raw: raw}
	private := collectExactPrivateFindings(m, fragment, findings)
	if len(private) != 1 || len(private[0].occurrences) != 2 {
		t.Fatalf("JSON positional occurrences = %#v", private)
	}
	if private[0].occurrences[0].start == private[0].occurrences[1].start {
		t.Fatalf("escaped JSON occurrences collapsed to one offset: %#v", private[0].occurrences)
	}
}

type positionalCredentialMatcher struct {
	secret  []byte
	finding sdk.Finding
}

func (m positionalCredentialMatcher) ScanBytes(_ context.Context, input []byte) ([]sdk.Finding, error) {
	count := countTestOccurrences(input, m.secret)
	if count == 0 {
		return nil, nil
	}
	finding := m.finding
	finding.OccurrenceCount = count
	return []sdk.Finding{finding}, nil
}

func (m positionalCredentialMatcher) ScanString(ctx context.Context, input string) ([]sdk.Finding, error) {
	return m.ScanBytes(ctx, []byte(input))
}

func (m positionalCredentialMatcher) RedactBytes(_ context.Context, input []byte) ([]byte, []sdk.Finding, error) {
	return append([]byte(nil), input...), nil, nil
}

func (m positionalCredentialMatcher) RedactString(_ context.Context, input string) (string, []sdk.Finding, error) {
	return input, nil, nil
}

func (m positionalCredentialMatcher) ScanOccurrences(input []byte) []sdk.PositionalOccurrence {
	var out []sdk.PositionalOccurrence
	for from := 0; from < len(input); {
		i := bytes.Index(input[from:], m.secret)
		if i < 0 {
			break
		}
		start := from + i
		finding := m.finding
		finding.OccurrenceCount = 1
		out = append(out, sdk.PositionalOccurrence{Start: start, End: start + len(m.secret), Finding: finding})
		from = start + len(m.secret)
	}
	return out
}

func countTestOccurrences(input, needle []byte) int {
	count := 0
	for from := 0; len(needle) > 0 && from <= len(input)-len(needle); {
		i := bytes.Index(input[from:], needle)
		if i < 0 {
			break
		}
		count++
		from += i + len(needle)
	}
	return count
}

func TestMergePrivateHybridFindings_ExactRepeatedOccurrencesKeepCardinality(t *testing.T) {
	t.Parallel()

	exact := exactPrivateFinding{
		finding: sdk.Finding{
			SecretRefName:  "API_KEY",
			SourceCategory: sdk.SourceCategoryProxyEnv,
			Location:       "messages[0].parts[0]",
			DetectorID:     sdk.DetectorIDExact,
		},
		occurrences: []betterLeaksOccurrence{
			{value: []byte("same-secret"), span: betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: 11}, ruleID: "API_KEY", role: betterLeaksOccurrencePrimary},
			{value: []byte("same-secret"), span: betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 20, EndColumn: 30}, ruleID: "API_KEY", role: betterLeaksOccurrencePrimary},
		},
	}
	got, err := mergeHybridFindings([]exactPrivateFinding{exact}, nil, DetectorFacts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].OccurrenceCount != 2 {
		t.Fatalf("exact repeated cardinality = %#v, want two", got)
	}
}

func TestMergePrivateHybridFindings_ExactPriorityOrdersBeforeBetterLeaks(t *testing.T) {
	t.Parallel()

	span := betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: 10}
	exact := exactPrivateFinding{
		finding:     sdk.Finding{SecretRefName: "Z_EXACT", SourceCategory: sdk.SourceCategoryProxyEnv, Location: "messages[0]", DetectorID: sdk.DetectorIDExact},
		occurrences: []betterLeaksOccurrence{{value: []byte("exact-value"), span: span, ruleID: "Z_EXACT", role: betterLeaksOccurrencePrimary}},
	}
	discovery := betterLeaksFinding{
		RuleID: "a-rule", Confidence: sdk.ConfidenceMedium, Location: "messages[0]", OccurrenceCount: 1,
		occurrences: []betterLeaksOccurrence{{value: []byte("other-value"), span: betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 20, EndColumn: 30}, ruleID: "a-rule", role: betterLeaksOccurrencePrimary}},
	}
	got, err := mergeHybridFindings([]exactPrivateFinding{exact}, []betterLeaksFinding{discovery}, DetectorFacts{RuleIDs: []string{"a-rule"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].DetectorID != sdk.DetectorIDExact || got[1].DetectorID != sdk.DetectorIDBetterLeaks {
		t.Fatalf("detector priority order = %#v", got)
	}
}

func TestMergePrivateHybridFindings_RepeatedReportsAndComponentsCountOnce(t *testing.T) {
	t.Parallel()

	primary := betterLeaksOccurrence{value: []byte("repeated-secret"), span: betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: 15}, ruleID: "same-rule", role: betterLeaksOccurrencePrimary}
	component := betterLeaksOccurrence{value: []byte("repeated-secret"), span: primary.span, ruleID: "same-component", role: betterLeaksOccurrenceComponent}
	base := betterLeaksFinding{
		RuleID: "same-rule", Confidence: sdk.ConfidenceMedium,
		Location: "messages[0].parts[0]", OccurrenceCount: 1,
		occurrences: []betterLeaksOccurrence{primary, component},
	}
	duplicate := base
	duplicate.occurrences = []betterLeaksOccurrence{component, primary}

	got, err := mergeHybridFindings(nil, []betterLeaksFinding{duplicate, base}, DetectorFacts{RuleIDs: []string{"same-rule"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].OccurrenceCount != 1 {
		t.Fatalf("repeated report/component count = %#v, want one occurrence", got)
	}
}

func TestMergePrivateHybridFindings_DifferentSameRuleValuesRemainDistinctPrivately(t *testing.T) {
	t.Parallel()

	location := "messages[0].parts[0]"
	findings := []betterLeaksFinding{
		{RuleID: "same-rule", Confidence: sdk.ConfidenceMedium, Location: location, OccurrenceCount: 1, occurrences: []betterLeaksOccurrence{{value: []byte("secret-a"), span: betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: 8}, ruleID: "same-rule", role: betterLeaksOccurrencePrimary}}},
		{RuleID: "same-rule", Confidence: sdk.ConfidenceMedium, Location: location, OccurrenceCount: 1, occurrences: []betterLeaksOccurrence{{value: []byte("secret-b"), span: betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: 8}, ruleID: "same-rule", role: betterLeaksOccurrencePrimary}}},
	}

	private := mergePrivateHybridFindings(nil, findings)
	if len(private) != 1 || len(private[0].occurrences) != 2 {
		t.Fatalf("private same-rule identities = %#v, want two values in one location", private)
	}
	got, err := projectMergedHybridFindings(private, DetectorFacts{RuleIDs: []string{"same-rule"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].OccurrenceCount != 2 {
		t.Fatalf("public same-rule cardinality = %#v, want two", got)
	}
}

func TestMergePrivateHybridFindings_EqualValueAndLocationInDistinctFieldsRemainDistinct(t *testing.T) {
	t.Parallel()

	value := []byte("same-secret")
	findings := []betterLeaksFinding{
		{RuleID: "same-rule", Confidence: sdk.ConfidenceMedium, Location: "messages[0].parts[0]", OccurrenceCount: 1, occurrences: []betterLeaksOccurrence{{value: append([]byte(nil), value...), span: betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: 11}, ruleID: "same-rule", role: betterLeaksOccurrencePrimary}}},
		{RuleID: "same-rule", Confidence: sdk.ConfidenceMedium, Location: "messages[0].parts[1]", OccurrenceCount: 1, occurrences: []betterLeaksOccurrence{{value: append([]byte(nil), value...), span: betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: 11}, ruleID: "same-rule", role: betterLeaksOccurrencePrimary}}},
	}

	private := mergePrivateHybridFindings(nil, findings)
	if len(private) != 2 {
		t.Fatalf("distinct field identities collapsed: %#v", private)
	}
	got, err := projectMergedHybridFindings(private, DetectorFacts{RuleIDs: []string{"same-rule"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Location == got[1].Location {
		t.Fatalf("distinct fields were not retained: %#v", got)
	}
}

func TestMergePrivateHybridFindings_DeterministicAcrossInputPermutations(t *testing.T) {
	t.Parallel()

	base := []betterLeaksFinding{
		{RuleID: "z-rule", Confidence: sdk.ConfidenceLow, Location: "messages[1]", OccurrenceCount: 1, occurrences: []betterLeaksOccurrence{{value: []byte("z-secret"), span: betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: 8}, ruleID: "z-rule", role: betterLeaksOccurrencePrimary}}},
		{RuleID: "a-rule", Confidence: sdk.ConfidenceHigh, Location: "messages[0]", OccurrenceCount: 1, occurrences: []betterLeaksOccurrence{{value: []byte("a-secret"), span: betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: 8}, ruleID: "a-rule", role: betterLeaksOccurrencePrimary}}},
	}
	want, err := mergeHybridFindings(nil, base, DetectorFacts{RuleIDs: []string{"a-rule", "z-rule"}})
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	for seed := int64(1); seed <= 10; seed++ {
		perm := append([]betterLeaksFinding(nil), base...)
		rand.New(rand.NewSource(seed)).Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
		got, err := mergeHybridFindings(nil, perm, DetectorFacts{RuleIDs: []string{"a-rule", "z-rule"}})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != string(wantJSON) {
			t.Fatalf("seed %d changed ordering: got %s want %s", seed, encoded, wantJSON)
		}
	}
}

func TestMergePrivateHybridFindings_SerializedOutputNeverContainsPrivateValues(t *testing.T) {
	t.Parallel()

	const canary = "hybrid-private-canary"
	findings, err := mergeHybridFindings(nil, []betterLeaksFinding{{
		RuleID: "canary-rule", Confidence: sdk.ConfidenceMedium, Location: "messages[0]", OccurrenceCount: 1,
		occurrences: []betterLeaksOccurrence{{value: []byte(canary), span: betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: len(canary)}, ruleID: "canary-rule", role: betterLeaksOccurrencePrimary}},
	}}, DetectorFacts{RuleIDs: []string{"canary-rule"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(findings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), canary) || strings.Contains(string(raw), "occurrences") {
		t.Fatalf("private occurrence escaped merged SDK output: %s", raw)
	}
}

func TestCollectExactPrivateFindings_UsesAdmittedFragmentForSpans(t *testing.T) {
	t.Parallel()

	cat, err := engine.BuildCatalog([]engine.CatalogInput{{
		Name: "API_KEY", Value: testkit.SyntheticOpenAIAPIKey, SourceCategory: sdk.SourceCategoryProxyEnv,
	}}, 8)
	if err != nil {
		t.Fatal(err)
	}
	m := engine.AsMatcher(engine.NewMatcher(cat))
	fragment := LogicalFragment{Location: "messages[0].parts[0]", Raw: []byte("prefix " + testkit.SyntheticOpenAIAPIKey)}
	findings, err := m.ScanString(t.Context(), string(fragment.Raw))
	if err != nil {
		t.Fatal(err)
	}
	private := collectExactPrivateFindings(m, fragment, findings)
	if len(private) != 1 || len(private[0].occurrences) != 1 {
		t.Fatalf("private exact occurrences = %#v", private)
	}
	occurrence := private[0].occurrences[0]
	if occurrence.span.StartLine != 1 || occurrence.span.StartColumn != 8 || occurrence.span.EndColumn != 45 {
		t.Fatalf("private exact span = %+v", occurrence.span)
	}
}

func TestScanCall_RetainsExactPrivateOccurrencesAlongsideSDKFindings(t *testing.T) {
	t.Parallel()
	// The private-collection fixtures below enable hybrid merging without a
	// scanner, so exact span mapping is exercised independently of discovery.

	cat, err := engine.BuildCatalog([]engine.CatalogInput{{
		Name: "API_KEY", Value: testkit.SyntheticOpenAIAPIKey, SourceCategory: sdk.SourceCategoryProxyEnv,
	}}, 8)
	if err != nil {
		t.Fatal(err)
	}
	call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("prefix " + testkit.SyntheticOpenAIAPIKey)}}}}
	out, err := scanCall(t.Context(), call, engine.AsMatcher(engine.NewMatcher(cat)), modeScan, 1024, &GenerationServices{betterLeaksEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Findings) != 1 || len(out.exactPrivateFindings) != 1 || len(out.exactPrivateFindings[0].occurrences) != 1 {
		t.Fatalf("scan output lost private exact occurrence: findings=%#v private=%#v", out.Findings, out.exactPrivateFindings)
	}
}

func TestScanCall_HybridMergeDistinguishesToolResultTextAndJSONFields(t *testing.T) {
	t.Parallel()

	cat, err := engine.BuildCatalog([]engine.CatalogInput{{
		Name: "API_KEY", Value: "123456789", SourceCategory: sdk.SourceCategoryProxyEnv,
	}}, 8)
	if err != nil {
		t.Fatal(err)
	}
	call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
		Kind: aipapi.PartToolResult, ToolCallID: "call-1", Text: "123456789", Content: json.RawMessage(`123456789`),
	}}}}}
	out, err := scanCall(t.Context(), call, engine.AsMatcher(engine.NewMatcher(cat)), modeScan, 1024, &GenerationServices{betterLeaksEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := mergeHybridFindings(out.exactPrivateFindings, nil, DetectorFacts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].OccurrenceCount != 2 {
		t.Fatalf("same-location tool-result fields were merged: sdk=%+v merged=%+v", out.Findings, got)
	}
}

func TestScanCall_HybridMergePreservesEscapedJSONCardinality(t *testing.T) {
	t.Parallel()

	cat, err := engine.BuildCatalog([]engine.CatalogInput{{
		Name: "API_KEY", Value: "abcdefgh9", SourceCategory: sdk.SourceCategoryProxyEnv,
	}}, 8)
	if err != nil {
		t.Fatal(err)
	}
	call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
		Kind: aipapi.PartJSON, Content: json.RawMessage(`["abcdefgh9","a\u0062cdefgh9"]`),
	}}}}}
	out, err := scanCall(t.Context(), call, engine.AsMatcher(engine.NewMatcher(cat)), modeScan, 1024, &GenerationServices{betterLeaksEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := mergeHybridFindings(out.exactPrivateFindings, nil, DetectorFacts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].OccurrenceCount != 2 {
		t.Fatalf("escaped JSON positional coverage replaced aggregate count: sdk=%+v merged=%+v", out.Findings, got)
	}
}

func TestScanCall_HybridMergeDeduplicatesEscapedBetterLeaksOccurrence(t *testing.T) {
	t.Parallel()

	cat, err := engine.BuildCatalog([]engine.CatalogInput{{
		Name: "API_KEY", Value: adapterGitHubToken, SourceCategory: sdk.SourceCategoryProxyEnv,
	}}, 8)
	if err != nil {
		t.Fatal(err)
	}
	call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
		Kind:    aipapi.PartJSON,
		Content: json.RawMessage(`{"token":"\u0067hp_aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5"}`),
	}}}}}
	out, err := scanCall(t.Context(), call, engine.AsMatcher(engine.NewMatcher(cat)), modeScan, 1024, &GenerationServices{betterLeaksEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	scanner, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled: true, MinimumConfidence: sdk.ConfidenceMedium, MaxDecodeDepth: 3, Workers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := scanner.scanFragments(t.Context(), walkLogicalFragments(call, newScanBudget(1024)))
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Findings) == 0 {
		t.Fatal("scanner did not report escaped token")
	}
	got, err := mergeHybridFindings(out.exactPrivateFindings, discovery.Findings, scanner.policyFacts())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].OccurrenceCount != 1 || got[0].DetectorID != sdk.DetectorIDExact || got[0].RuleID != "github-pat" {
		t.Fatalf("escaped exact and BetterLeaks occurrence was not merged: exact=%+v discovery=%+v hybrid=%+v", out.Findings, discovery.Findings, got)
	}
}

func TestScanCall_HybridMergeNumericEscapedExactOccurrenceCardinality(t *testing.T) {
	t.Parallel()

	cat, err := engine.BuildCatalog([]engine.CatalogInput{{
		Name: "NUMERIC_SECRET", Value: "123456789", SourceCategory: sdk.SourceCategoryProxyEnv,
	}}, 8)
	if err != nil {
		t.Fatal(err)
	}
	call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
		Kind:    aipapi.PartJSON,
		Content: json.RawMessage(`["123456789","\u003123456789"]`),
	}}}}}
	out, err := scanCall(t.Context(), call, engine.AsMatcher(engine.NewMatcher(cat)), modeScan, 1024, &GenerationServices{betterLeaksEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := mergeHybridFindings(out.exactPrivateFindings, nil, DetectorFacts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].OccurrenceCount != 2 {
		t.Fatalf("numeric escaped exact cardinality = %#v, want one finding with two occurrences", got)
	}
}

func TestScanCall_HybridMergeEscapedJSONKeyWithBetterLeaksProvenance(t *testing.T) {
	t.Parallel()

	cat, err := engine.BuildCatalog([]engine.CatalogInput{{
		Name: "GITHUB_TOKEN", Value: adapterGitHubToken, SourceCategory: sdk.SourceCategoryProxyEnv,
	}}, 8)
	if err != nil {
		t.Fatal(err)
	}
	call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
		Kind:    aipapi.PartJSON,
		Content: json.RawMessage(`{"\u0067hp_aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5":"value"}`),
	}}}}}
	out, err := scanCall(t.Context(), call, engine.AsMatcher(engine.NewMatcher(cat)), modeScan, 1024, &GenerationServices{betterLeaksEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	scanner, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled: true, MinimumConfidence: sdk.ConfidenceMedium, MaxDecodeDepth: 3, Workers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := scanner.scanFragments(t.Context(), walkLogicalFragments(call, newScanBudget(1024)))
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Findings) == 0 {
		t.Fatal("scanner did not report escaped JSON key")
	}
	got, err := mergeHybridFindings(out.exactPrivateFindings, discovery.Findings, scanner.policyFacts())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].OccurrenceCount != 1 || got[0].DetectorID != sdk.DetectorIDExact || got[0].RuleID != "github-pat" {
		t.Fatalf("escaped key exact and BetterLeaks occurrence was not merged: exact=%+v discovery=%+v hybrid=%+v", out.Findings, discovery.Findings, got)
	}
}

func TestScanCall_RedactJSONShortCircuitDoesNotCollectUnvisitedTokens(t *testing.T) {
	t.Parallel()

	cat, err := engine.BuildCatalog([]engine.CatalogInput{{
		Name: "NUMERIC_SECRET", Value: "123456789", SourceCategory: sdk.SourceCategoryProxyEnv,
	}}, 8)
	if err != nil {
		t.Fatal(err)
	}
	call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
		Kind:    aipapi.PartJSON,
		Content: json.RawMessage(`{"first":123456789,"later":"123456789"}`),
	}}}}}
	out, err := scanCall(t.Context(), call, engine.AsMatcher(engine.NewMatcher(cat)), modeRedact, 1024, &GenerationServices{betterLeaksEnabled: true, redaction: engine.MatcherOptions{MaskByte: '*'}})
	if err == nil {
		t.Fatal("redacting an exact non-string JSON scalar should fail closed")
	}
	if len(out.exactPrivateFindings) != 1 || len(out.exactPrivateFindings[0].occurrences) != 1 {
		t.Fatalf("redact short-circuit collected unvisited tokens: %#v", out.exactPrivateFindings)
	}
}

func TestScanCall_ExactJSONSemanticTokenMappingMatchesCanonicalTraversal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		secret string
		raw    string
		want   int
	}{
		{name: "number", secret: "123456789", raw: `123456789`, want: 1},
		{name: "boolean", secret: "true", raw: `[true,true]`, want: 2},
		{name: "null", secret: "null", raw: `[null,null]`, want: 2},
		{name: "duplicate_effective_key", secret: "dup-secret", raw: `{"dup-secret":"safe","dup-secret":"safe"}`, want: 1},
		{name: "trailing_content_ignored", secret: "123456789", raw: `["123456789"] 123456789`, want: 1},
		{name: "malformed_raw_fallback", secret: "123456789", raw: `{"value":"123456789" broken}`, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat, err := engine.BuildCatalog([]engine.CatalogInput{{
				Name: "TEST_SECRET", Value: tc.secret, SourceCategory: sdk.SourceCategoryProxyEnv,
			}}, 3)
			if err != nil {
				t.Fatal(err)
			}
			call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
				Kind:    aipapi.PartJSON,
				Content: json.RawMessage(tc.raw),
			}}}}}
			out, err := scanCall(t.Context(), call, engine.AsMatcher(engine.NewMatcher(cat)), modeScan, 1024, &GenerationServices{betterLeaksEnabled: true})
			if err != nil {
				t.Fatal(err)
			}
			got, err := mergeHybridFindings(out.exactPrivateFindings, nil, DetectorFacts{})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].OccurrenceCount != tc.want {
				t.Fatalf("semantic token cardinality = %#v, want one finding with %d occurrence(s)", got, tc.want)
			}
		})
	}
}

func TestScanCall_ExactJSONSemanticTokenMappingCanonicalEdgeFixtures(t *testing.T) {
	t.Parallel()

	const githubToken = `ghp_aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5`
	cases := []struct {
		name   string
		secret string
		raw    string
		want   int
	}{
		{name: "large_exponent_with_escaped_github_token", secret: githubToken, raw: `[1e999,"\u0067hp_aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5"]`, want: 1},
		{name: "large_exponent_does_not_scan_escape_digits", secret: "1234", raw: `[1e999,"1234","\u1234"]`, want: 1},
		{name: "primitive_trailing_without_space", secret: "123456789", raw: `123456789true123456789`, want: 1},
		{name: "boolean_trailing_without_space", secret: "true", raw: `truetrue`, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat, err := engine.BuildCatalog([]engine.CatalogInput{{
				Name: "API_KEY", Value: tc.secret, SourceCategory: sdk.SourceCategoryProxyEnv,
			}}, 3)
			if err != nil {
				t.Fatal(err)
			}
			call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
				Kind: aipapi.PartJSON, Content: json.RawMessage(tc.raw),
			}}}}}
			out, err := scanCall(t.Context(), call, engine.AsMatcher(engine.NewMatcher(cat)), modeScan, 1024, &GenerationServices{betterLeaksEnabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Findings) != 1 || out.Findings[0].OccurrenceCount != tc.want {
				t.Fatalf("canonical exact cardinality = %#v, want one finding with %d occurrence(s)", out.Findings, tc.want)
			}
			got, err := mergeHybridFindings(out.exactPrivateFindings, nil, DetectorFacts{})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].OccurrenceCount != tc.want {
				t.Fatalf("canonical exact count = %#v, want one finding with %d occurrence(s)", got, tc.want)
			}
		})
	}
}

func TestJSONOccurrenceTokens_UsesCanonicalDepthLimitAsFallback(t *testing.T) {
	t.Parallel()

	raw := []byte(strings.Repeat("[", 10001) + "true" + strings.Repeat("]", 10001))
	if _, err := decodeJSONPreserveNumbers(raw); err == nil {
		t.Fatal("canonical decoder accepted a JSON value beyond its depth limit")
	}
	if _, err := decodedJSONOccurrenceMappings(raw); err == nil {
		t.Fatal("occurrence mapper accepted a JSON value beyond canonical decoder depth")
	}
}

func TestJSONOccurrenceTokens_MatchesCanonicalSemanticTokens(t *testing.T) {
	t.Parallel()

	cases := []string{`1e999`, `-1e999`, `1e-999`, `123456789true123456789`, `truetrue`, `falsefalse`, `nullnull`, `01`, `-01`, `1.2.3`, `1e2x`, `1e`, `1e+`, `-`, `[01]`, `[truetrue]`, `[1e999,"1234","\u1234"]`, `{"z":1e999,"a":null}`, `{"k":"first","\u006b":"last"}`, `{"k":{"old":1},"k":{"new":2}}`, `"\ud800"`, `"\udc00"`, `"\ud800\udc00"`, `"\ud800\ud800"`, `"\u0000\n\t\b\r\f\"\\\/"`, `[]suffix`, `{}suffix`, `"first""second"`, `{"k":[true,false,null,-0,1.20e+02]}`, `{"k":1,}`, `[1,]`, `{"k" 1}`, `["\q"]`, "\"" + string([]byte{0xff, 0xc0, 0x80}) + "\""}
	for _, raw := range append([]string(nil), cases...) {
		cases = append(cases, " \t\r\n"+raw)
		cases = append(cases, "["+raw+"]")
		cases = append(cases, `{"k":`+raw+`}`)
	}
	cases = append(cases, strings.Repeat("[", 10001)+"true"+strings.Repeat("]", 10001))

	for i, raw := range cases {
		t.Run(fmt.Sprintf("case_%03d", i), func(t *testing.T) {
			canonical, canonicalErr := decodeJSONPreserveNumbers([]byte(raw))
			tokens, mappedErr := decodedJSONOccurrenceMappings([]byte(raw))
			if (canonicalErr == nil) != (mappedErr == nil) {
				t.Fatalf("canonical accepted=%v mapper accepted=%v", canonicalErr == nil, mappedErr == nil)
			}
			if canonicalErr != nil {
				return
			}

			want := make([]string, 0)
			var walk func(any)
			walk = func(value any) {
				switch value := value.(type) {
				case string:
					want = append(want, value)
				case json.Number:
					want = append(want, value.String())
				case bool:
					if value {
						want = append(want, "true")
					} else {
						want = append(want, "false")
					}
				case nil:
					want = append(want, "null")
				case []any:
					for _, child := range value {
						walk(child)
					}
				case map[string]any:
					keys := make([]string, 0, len(value))
					for key := range value {
						keys = append(keys, key)
					}
					sort.Strings(keys)
					for _, key := range keys {
						want = append(want, key)
						walk(value[key])
					}
				}
			}
			walk(canonical)

			got := make([]string, 0, len(tokens))
			for _, token := range tokens {
				got = append(got, string(token.decoded))
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("semantic tokens differ: got %#v want %#v", got, want)
			}
		})
	}
}
