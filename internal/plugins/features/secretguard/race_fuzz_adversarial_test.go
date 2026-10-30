package secretguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

const (
	adversarialFuzzMaxBytes   = 16 << 10
	adversarialFragmentRawMax = (adversarialFuzzMaxBytes - len("authoritative text:")) / 2
)

func TestBetterLeaksScanner_SharedGenerationConcurrentScansHaveStableSafeOrdering(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	fragments := []LogicalFragment{
		{
			Location:  "messages[0].parts[0]",
			Raw:       []byte("GITHUB_TOKEN=" + adapterGitHubToken + "\n" + adapterAWSAccessID + "\n" + adapterAWSSecretFixture),
			privateID: "text",
		},
		{
			Location:  "messages[0].parts[1]",
			Kind:      FragmentJSON,
			Raw:       []byte(`{"token":"` + adapterGitHubToken + `"}`),
			privateID: "json",
		},
	}
	warmup, err := detector.scanFragments(t.Context(), fragments)
	if err != nil {
		t.Fatal("scanner warmup failed")
	}
	warmupFacts, ok := betterLeaksCanaryFactsFor(warmup.Findings)
	if !ok || warmupFacts.findingCount < 2 || warmupFacts.githubOccurrences <= 0 ||
		warmupFacts.githubLocation != "messages[0].parts[0]" || warmupFacts.githubConfidence == "" || !warmupFacts.hasAWS {
		t.Fatal("scanner warmup did not produce the independent canary findings and provenance")
	}
	want := safeBetterLeaksFindingSignature(warmup.Findings)

	type result struct {
		signature string
		facts     betterLeaksCanaryFacts
		factsOK   bool
		err       error
	}
	const requestCount = 32
	results := make(chan result, requestCount)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range requestCount {
		wg.Go(func() {
			<-start
			got, scanErr := detector.scanFragments(t.Context(), fragments)
			facts, factsOK := betterLeaksCanaryFactsFor(got.Findings)
			results <- result{signature: safeBetterLeaksFindingSignature(got.Findings), facts: facts, factsOK: factsOK, err: scanErr}
		})
	}
	close(start)
	wg.Wait()
	close(results)

	for got := range results {
		if got.err != nil {
			t.Fatal("shared scanner returned an error during concurrent scans")
		}
		if !got.factsOK || got.facts != warmupFacts {
			t.Fatal("shared scanner lost expected canary count or provenance during concurrent scans")
		}
		if got.signature != want {
			t.Fatal("shared scanner produced nondeterministic safe finding order")
		}
	}
}

func TestBetterLeaksScanner_RequestScansDoNotGrowGoroutines(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	fragments := []LogicalFragment{{
		Location:  "messages[0].parts[0]",
		Raw:       []byte("GITHUB_TOKEN=" + adapterGitHubToken),
		privateID: "request",
	}}
	if _, err := detector.scanFragments(t.Context(), fragments); err != nil {
		t.Fatal("scanner warmup failed")
	}
	runtime.GC()
	runtime.Gosched()
	baseline := runtime.NumGoroutine()
	for range 96 {
		if _, err := detector.scanFragments(t.Context(), fragments); err != nil {
			t.Fatal("repeated scanner request failed")
		}
	}
	runtime.GC()
	runtime.Gosched()
	after := runtime.NumGoroutine()
	// The scanner owns a fixed worker set. A small allowance covers the test
	// runner and runtime finalizers, while catching one goroutine per request.
	if after > baseline+8 {
		t.Fatalf("request scans grew goroutines beyond bounded allowance: before=%d after=%d", baseline, after)
	}
}

func TestScanCall_CanceledHybridRedactionLeavesCallerInputUntouched(t *testing.T) {
	generation := newAdversarialGenerationServices(t)
	secret := testkit.SyntheticOpenAIAPIKey
	matcher := newExactStub(secret, "OPENAI_API_KEY", sdk.SourceCategoryProxyEnv)
	call := aipapi.Call{Messages: []aipapi.Message{{
		Role: aipapi.RoleUser,
		Parts: []aipapi.Part{
			aipapi.TextPart("prefix " + secret),
			{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"token":"` + secret + `"}`)},
		},
	}}}
	before := aipapi.CloneCall(call)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := scanCall(ctx, &call, matcher, modeRedact, adversarialFuzzMaxBytes, generation)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled hybrid scan did not return context cancellation")
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("canceled hybrid redaction committed a partial mutation")
	}
	assertAdversarialSecretsAbsent(t, err.Error())
}

func FuzzLogicalFragmentMapping_BoundedAndDeterministic(f *testing.F) {
	f.Add([]byte(`{"token":"malformed`))
	f.Add([]byte{0x00, 0xff, '{', ']'})
	f.Add([]byte("plain logical content"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = append([]byte(nil), testkit.CapBytes(raw, adversarialFragmentRawMax)...)
		if len(raw) == 0 {
			raw = []byte("fuzz-empty-canary")
		}
		itemText := "authoritative text:" + string(raw)
		itemJSON := string(raw)
		call := &aipapi.Call{
			Messages: []aipapi.Message{{
				Role: aipapi.RoleUser,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "legacy text:" + string(raw)},
					{Kind: aipapi.PartJSON, Content: json.RawMessage("legacy:" + string(raw))},
				},
			}},
			// Non-nil Items makes the authoritative representation win over the
			// legacy Messages slice, including for malformed JSON content.
			Items: []aipapi.Item{{
				Kind: aipapi.ItemKindMessage,
				Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: itemText},
					{Kind: aipapi.ContentPartJSON, Text: itemJSON},
				},
			}},
		}
		firstBudget := newScanBudget(adversarialFuzzMaxBytes)
		first := walkLogicalFragments(call, firstBudget)
		secondBudget := newScanBudget(adversarialFuzzMaxBytes)
		second := walkLogicalFragments(call, secondBudget)

		if firstBudget.used > adversarialFuzzMaxBytes || secondBudget.used > adversarialFuzzMaxBytes {
			t.Fatal("logical fragment walker exceeded its shared byte budget")
		}
		if logicalFragmentSummary(first) != logicalFragmentSummary(second) {
			t.Fatal("logical fragment mapping changed across identical walks")
		}
		if len(first) != 2 || firstBudget.used != len(itemText)+len(itemJSON) || secondBudget.used != firstBudget.used || firstBudget.limitHit || secondBudget.limitHit {
			t.Fatal("authoritative fragment mapping did not admit the required bounded fragments")
		}
		wantFragments := []struct {
			location string
			kind     FragmentKind
			raw      string
		}{
			{location: "items[0].content[0]", kind: FragmentText, raw: itemText},
			{location: "items[0].content[1]", kind: FragmentJSON, raw: itemJSON},
		}
		for i, want := range wantFragments {
			got := first[i]
			if got.Location != want.location || got.Kind != want.kind || got.textValue() != want.raw {
				t.Fatalf("authoritative fragment %d did not preserve required location, kind, and content", i)
			}
		}
		if strings.Contains(first[0].Location, "messages") || strings.Contains(first[1].Location, "messages") {
			t.Fatal("legacy message content bypassed authoritative item mapping")
		}
		seenIDs := make(map[string]struct{}, len(first))
		for _, fragment := range first {
			if fragment.Text == "" && len(fragment.Raw) == 0 || fragment.privateID == "" {
				t.Fatal("walker emitted an empty or identity-free fragment")
			}
			if _, duplicate := seenIDs[fragment.privateID]; duplicate {
				t.Fatal("walker reused a private field identity")
			}
			seenIDs[fragment.privateID] = struct{}{}
		}
	})
}

func FuzzHybridRedaction_BoundedMalformedJSONCancellationAndPrivacy(f *testing.F) {
	generation := newAdversarialGenerationServicesForFuzz(f)
	secret := adapterGitHubToken
	f.Add([]byte(`{"token":"`+secret+`"}`), false, false)
	f.Add([]byte(`{"token":"`+secret), false, true)
	f.Add([]byte("plain text"), true, true)
	f.Fuzz(func(t *testing.T, raw []byte, canceled, malformed bool) {
		raw = append([]byte(nil), testkit.CapBytes(raw, adversarialFuzzMaxBytes)...)
		safeRaw := strings.ReplaceAll(string(raw), secret, "fuzz-secret")
		if malformed && safeRaw == "" {
			safeRaw = "{"
		}
		call := newHybridCanaryCall(secret)
		if malformed {
			call.Messages[0].Parts = append(call.Messages[0].Parts, aipapi.Part{Kind: aipapi.PartJSON, Content: json.RawMessage(safeRaw)})
		}
		before := aipapi.CloneCall(call)
		ctx, cancel := context.WithCancel(t.Context())
		if canceled {
			cancel()
		}
		defer cancel()
		matcher := newExactStub(secret, "GITHUB_TOKEN", sdk.SourceCategoryProxyEnv)
		guard := NewGuard(decodeActionConfig(ActionRedact))
		services := sdk.Services{
			MatcherResolver: staticResolver{m: matcher},
			Capability:      generation,
		}
		if !canceled {
			validCall := newHybridCanaryCall(secret)
			validDecision, validErr := guard.Evaluate(t.Context(), &validCall, sdk.Meta{}, services)
			if validErr != nil {
				t.Fatal("valid supported hybrid canary returned an error")
			}
			assertValidSupportedHybridRedaction(t, &validCall, validDecision, secret)
		}
		decision, err := guard.Evaluate(ctx, &call, sdk.Meta{}, services)
		if canceled {
			if err == nil || !errors.Is(err, context.Canceled) {
				t.Fatal("canceled redaction did not return context cancellation")
			}
			if !reflect.DeepEqual(call, before) {
				t.Fatal("canceled redaction committed a partial caller mutation")
			}
			assertAdversarialSecretsAbsent(t, err.Error())
			return
		}
		if err != nil {
			assertAdversarialSecretsAbsent(t, err.Error())
			if !reflect.DeepEqual(call, before) {
				t.Fatal("redaction error changed caller input")
			}
			return
		}
		assertAdversarialDecisionSafe(t, decision)
		if malformed && (decision.Outcome == sdk.OutcomeBlock || decision.FailureKind != "") {
			if decision.MutationCount != 0 {
				t.Fatal("failed redaction reported mutations")
			}
			if !reflect.DeepEqual(call, before) {
				t.Fatal("failed redaction committed a partial caller mutation")
			}
			return
		}
		if malformed {
			if decision.MutationCount > 0 && callContainsAdversarialSecret(call) {
				t.Fatal("malformed redaction left synthetic secret material in the call")
			}
			return
		}
		assertValidSupportedHybridRedaction(t, &call, decision, secret)
	})
}

func newHybridCanaryCall(secret string) aipapi.Call {
	return aipapi.Call{Messages: []aipapi.Message{{
		Role: aipapi.RoleUser,
		Parts: []aipapi.Part{
			{Kind: aipapi.PartText, Text: "GITHUB_TOKEN=" + secret + " suffix"},
			{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"token":` + jsonQuote(secret) + `}`)},
		},
	}}}
}

func assertValidSupportedHybridRedaction(t *testing.T, call *aipapi.Call, decision sdk.Decision, secret string) {
	t.Helper()
	assertAdversarialDecisionSafe(t, decision)
	assertHybridCanaryFindings(t, decision)
	if decision.Outcome != sdk.OutcomeRedacted || decision.MutationCount != 2 || decision.FailureKind != "" || len(decision.Findings) != 4 {
		t.Fatalf("valid supported hybrid did not produce the expected redacted decision shape: outcome=%q mutations=%d failure_set=%t findings=%d", decision.Outcome, decision.MutationCount, decision.FailureKind != "", len(decision.Findings))
	}
	if bytes.Contains([]byte(call.Messages[0].Parts[0].Text), []byte(secret)) {
		t.Fatal("successful text redaction left the canary")
	}
	if !json.Valid(call.Messages[0].Parts[1].Content) || bytes.Contains(call.Messages[0].Parts[1].Content, []byte(secret)) {
		t.Fatal("successful JSON redaction was invalid or left the canary")
	}
	var redacted map[string]any
	if err := json.Unmarshal(call.Messages[0].Parts[1].Content, &redacted); err != nil {
		t.Fatal("successful JSON redaction was not independently decodable")
	}
	if got, _ := redacted["token"].(string); got == "" || got == secret {
		t.Fatal("successful JSON redaction did not sanitize the canary token")
	}
}

func FuzzBetterLeaksFindingCapAndErrorPrivacy(f *testing.F) {
	detector := newTestBetterLeaksScannerForFuzz(f)
	f.Add([]byte(strings.Repeat("GITHUB_TOKEN="+adapterGitHubToken+"\n", DefaultBetterLeaksMaxFindings+1)), true)
	f.Add([]byte("no findings"), false)
	f.Fuzz(func(t *testing.T, raw []byte, withCanary bool) {
		raw = append([]byte(nil), testkit.CapBytes(raw, adversarialFuzzMaxBytes)...)
		if withCanary {
			raw = append([]byte("GITHUB_TOKEN="+adapterGitHubToken+"\n"), raw...)
			raw = append([]byte(nil), testkit.CapBytes(raw, adversarialFuzzMaxBytes)...)
		}
		result, err := detector.scanFragments(t.Context(), []LogicalFragment{{
			Location:  "messages[0].parts[0]",
			Raw:       raw,
			privateID: "fuzz",
		}})
		if len(result.Findings) > DefaultBetterLeaksMaxFindings {
			t.Fatal("BetterLeaks projected more findings than the request cap")
		}
		if err != nil {
			assertAdversarialSecretsAbsent(t, err.Error())
			if !errors.Is(err, errBetterLeaksFindingCap) {
				t.Fatal("bounded adversarial scan returned an unexpected error class")
			}
			if len(result.Findings) != DefaultBetterLeaksMaxFindings {
				t.Fatal("finding cap error did not retain the bounded finding prefix")
			}
			return
		}
		if withCanary {
			if len(result.Findings) == 0 {
				t.Fatal("canary scan returned no findings without a cap error")
			}
			if _, ok := findBetterLeaksProjectedFinding(result.Findings, "github-pat"); !ok {
				t.Fatal("canary scan lost github-pat provenance without a cap error")
			}
		} else if len(result.Findings) == 0 {
			// A bounded no-finding scan is allowed to pass even when the input
			// is larger than the finding cap; zero results are not an error.
			return
		}
	})
}

func TestBetterLeaksScanner_FindingCapAccumulationAcrossLogicalFragments(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	fragments := make([]LogicalFragment, DefaultBetterLeaksMaxFindings+1)
	for i := range fragments {
		fragments[i] = LogicalFragment{
			Location:  fmt.Sprintf("items[%d].content[0]", i),
			Raw:       []byte("GITHUB_TOKEN=" + adapterGitHubToken),
			privateID: fmt.Sprintf("fragment-%d", i),
		}
	}
	result, err := detector.scanFragments(t.Context(), fragments)
	if err == nil || !errors.Is(err, errBetterLeaksFindingCap) {
		t.Fatal("multi-fragment finding overflow did not return the bounded cap classifier")
	}
	assertAdversarialSecretsAbsent(t, err.Error())
	if len(result.Findings) != DefaultBetterLeaksMaxFindings {
		t.Fatal("multi-fragment finding overflow returned an unbounded or incomplete prefix")
	}
	for _, finding := range result.Findings {
		if finding.RuleID != "github-pat" || finding.OccurrenceCount != 1 || finding.Confidence == "" || finding.Location == "" {
			t.Fatal("multi-fragment cap result lost finding provenance")
		}
	}
}

func TestBetterLeaksScanner_FindingCapZeroResultsDoesNotError(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	fragments := make([]LogicalFragment, DefaultBetterLeaksMaxFindings+1)
	for i := range fragments {
		fragments[i] = LogicalFragment{
			Location:  fmt.Sprintf("items[%d].content[0]", i),
			Raw:       []byte("safe logical content"),
			privateID: fmt.Sprintf("safe-%d", i),
		}
	}
	result, err := detector.scanFragments(t.Context(), fragments)
	if err != nil {
		t.Fatal("zero-result scan crossed the finding cap")
	}
	if len(result.Findings) != 0 {
		t.Fatal("zero-result scan unexpectedly projected findings")
	}
}

func FuzzBetterLeaksScanErrorSanitization(f *testing.F) {
	f.Add("upstream detail: " + testkit.SyntheticOpenAIAPIKey)
	f.Add("malformed source with secret-like bytes")
	detector := newTestBetterLeaksScannerForFuzz(f)
	f.Fuzz(func(t *testing.T, detail string) {
		detail = detail[:min(len(detail), adversarialFuzzMaxBytes)]
		untrustedDetail := "untrusted-source-detail:" + detail
		err := detector.scanSource(t.Context(), adapterErrorSource{err: errors.New(untrustedDetail)}, "messages[0]", &betterLeaksScanResult{})
		if err == nil {
			t.Fatal("adversarial source failure was not returned")
		}
		assertAdversarialSecretsAbsent(t, err.Error())
		if strings.Contains(err.Error(), untrustedDetail) {
			t.Fatal("BetterLeaks source error echoed untrusted detail")
		}
	})
}

func newAdversarialGenerationServices(t *testing.T) *GenerationServices {
	t.Helper()
	services, err := BuildGenerationServices(DetectorPolicy{BetterLeaks: BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		MaxFindings:       DefaultBetterLeaksMaxFindings,
	}}, engine.NewDisabledSource())
	if err != nil {
		t.Fatal("failed to build adversarial generation services")
	}
	return services
}

func newAdversarialGenerationServicesForFuzz(f *testing.F) *GenerationServices {
	f.Helper()
	services, err := BuildGenerationServices(DetectorPolicy{BetterLeaks: BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		MaxFindings:       DefaultBetterLeaksMaxFindings,
	}}, engine.NewDisabledSource())
	if err != nil {
		f.Fatal("failed to build fuzz generation services")
	}
	return services
}

func newTestBetterLeaksScannerForFuzz(f *testing.F) *betterLeaksScanner {
	f.Helper()
	detector, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		MaxFindings:       DefaultBetterLeaksMaxFindings,
	})
	if err != nil {
		f.Fatal("failed to build fuzz scanner")
	}
	return detector
}

func safeBetterLeaksFindingSignature(findings []betterLeaksFinding) string {
	var b strings.Builder
	for _, finding := range findings {
		fmt.Fprintf(&b, "%s|%s|%s|%d|%d|", finding.Location, finding.RuleID, finding.Confidence, finding.OccurrenceCount, len(finding.occurrences))
		for _, component := range finding.Components {
			fmt.Fprintf(&b, "%s:%t:%d|", component.RuleID, component.Optional, len(component.occurrences))
		}
		for _, occurrence := range finding.occurrences {
			fmt.Fprintf(&b, "%s:%d:%d:%d:%d:%d:%d|", occurrence.fieldID, occurrence.span.StartLine, occurrence.span.EndLine, occurrence.span.StartColumn, occurrence.span.EndColumn, occurrence.role, occurrence.representation)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

type betterLeaksCanaryFacts struct {
	findingCount      int
	githubOccurrences int
	githubLocation    string
	githubConfidence  string
	hasAWS            bool
}

func betterLeaksCanaryFactsFor(findings []betterLeaksFinding) (betterLeaksCanaryFacts, bool) {
	facts := betterLeaksCanaryFacts{findingCount: len(findings)}
	for _, finding := range findings {
		switch finding.RuleID {
		case "github-pat":
			facts.githubOccurrences += finding.OccurrenceCount
			if facts.githubLocation == "" {
				facts.githubLocation = finding.Location
				facts.githubConfidence = finding.Confidence
			}
		case "aws-access-token":
			facts.hasAWS = true
		}
	}
	return facts, facts.findingCount > 0 && facts.githubOccurrences > 0 && facts.githubLocation != "" && facts.githubConfidence != ""
}

func assertHybridCanaryFindings(t *testing.T, decision sdk.Decision) {
	t.Helper()
	wantLocations := map[string]struct {
		exact       bool
		betterLeaks bool
	}{
		"messages[0].parts[0]": {},
		"messages[0].parts[1]": {},
	}
	for _, finding := range decision.Findings {
		facts, expected := wantLocations[finding.Location]
		if !expected {
			continue
		}
		switch finding.DetectorID {
		case sdk.DetectorIDExact:
			if finding.RuleID == "" && finding.OccurrenceCount == 1 {
				facts.exact = true
			}
		case sdk.DetectorIDBetterLeaks:
			if finding.RuleID == "github-pat" && finding.Confidence != "" && finding.OccurrenceCount == 1 {
				facts.betterLeaks = true
			}
		default:
			t.Fatal("hybrid canary finding used an unexpected detector provenance")
		}
		wantLocations[finding.Location] = facts
	}
	for location, facts := range wantLocations {
		if !facts.exact || !facts.betterLeaks {
			t.Fatalf("hybrid canary exact and BetterLeaks overlap was lost at expected location %s: exact=%t betterleaks=%t", location, facts.exact, facts.betterLeaks)
		}
	}
}

func logicalFragmentSummary(fragments []LogicalFragment) string {
	var b strings.Builder
	for _, fragment := range fragments {
		fmt.Fprintf(&b, "%s|%d|%d|%s\n", fragment.Location, fragment.Kind, len(fragment.rawBytes()), fragment.privateID)
	}
	return b.String()
}

func assertAdversarialDecisionSafe(t *testing.T, decision sdk.Decision) {
	t.Helper()
	assertFindingsSafe(t, decision.Findings)
	for _, finding := range decision.Findings {
		assertAdversarialSecretsAbsent(t, finding.SecretRefName)
		assertAdversarialSecretsAbsent(t, finding.Location)
		for _, alias := range finding.Aliases {
			assertAdversarialSecretsAbsent(t, alias)
		}
	}
	assertAdversarialSecretsAbsent(t, decision.FailureKind)
	assertAdversarialSecretsAbsent(t, decision.FailureReason)
}

func assertAdversarialSecretsAbsent(t *testing.T, value string) {
	t.Helper()
	for _, needle := range append(testkit.AllSyntheticSecretGuardValues(), adapterGitHubToken, adapterAWSAccessID, adapterAWSSecretFixture) {
		if needle != "" && strings.Contains(value, needle) {
			t.Fatal("adversarial secret material appeared in a diagnostic")
		}
	}
}

func callContainsAdversarialSecret(call aipapi.Call) bool {
	encoded, err := json.Marshal(call)
	if err != nil {
		return false
	}
	for _, value := range append(testkit.AllSyntheticSecretGuardValues(), adapterGitHubToken, adapterAWSAccessID, adapterAWSSecretFixture) {
		if value != "" && bytes.Contains(encoded, []byte(value)) {
			return true
		}
	}
	return false
}

func rawWasValidJSON(raw []byte) bool {
	return len(raw) > 0 && json.Valid(raw)
}
