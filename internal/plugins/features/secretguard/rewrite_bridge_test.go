package secretguard

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestBetterLeaksLiteralCandidatesRequireOriginalFragmentBytes(t *testing.T) {
	raw := []byte("literal-secret\nother-secret")
	findings := []betterLeaksFinding{{
		Location: "messages[0].parts[0]",
		occurrences: []betterLeaksOccurrence{
			{
				value:          []byte("literal-secret"),
				span:           betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: 14},
				representation: betterLeaksOccurrenceLiteral,
				ruleID:         "literal-rule",
			},
			{
				value:          []byte("decoded-secret"),
				span:           betterLeaksSpan{StartLine: 2, EndLine: 2, StartColumn: 1, EndColumn: 14},
				representation: betterLeaksOccurrenceDecoded,
				ruleID:         "decoded-rule",
			},
			{
				value:          []byte("wrong-secret"),
				span:           betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: 14},
				representation: betterLeaksOccurrenceLiteral,
				ruleID:         "wrong-rule",
			},
		},
	}}

	got := betterLeaksLiteralCandidates(LogicalFragment{Location: findings[0].Location, Raw: raw}, findings)
	if len(got) != 1 || string(got[0].value) != "literal-secret" {
		t.Fatalf("literal candidate count/value match = %t (count=%d)", len(got) == 1 && string(got[0].value) == "literal-secret", len(got))
	}
}

func TestBetterLeaksLiteralCandidatesRespectSameLocationFieldIdentity(t *testing.T) {
	const token = "literal-secret"
	findings := []betterLeaksFinding{{
		Location: "messages[0].parts[0]",
		occurrences: []betterLeaksOccurrence{{
			value:          []byte(token),
			fieldID:        "fragment[0]",
			span:           betterLeaksSpan{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: len(token)},
			representation: betterLeaksOccurrenceLiteral,
		}},
	}}

	if got := betterLeaksLiteralCandidates(LogicalFragment{
		Location:  findings[0].Location,
		privateID: "fragment[1]",
		Raw:       []byte(token),
	}, findings); len(got) != 0 {
		t.Fatalf("same-location candidate crossed field identity (count=%d)", len(got))
	}
}

func TestBetterLeaksRewriteMatcherRedactsLiteralAndPreservesPrefix(t *testing.T) {
	const token = adapterGitHubToken
	matcher, err := newBetterLeaksRewriteMatcher(engine.AsMatcher(engine.NewMatcherWithOptions(nil, engine.MatcherOptions{
		PreserveKnownPrefixes: true,
		MaskByte:              '#',
	})), LogicalFragment{Kind: FragmentText, Raw: []byte("token=" + token), privateID: "fragment[0]"}, []betterLeaksOccurrence{{
		value:          []byte(token),
		representation: betterLeaksOccurrenceLiteral,
		ruleID:         "github-pat",
	}})
	if err != nil {
		t.Fatal(err)
	}
	redacted, findings, err := matcher.RedactString(context.Background(), "token="+token)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].DetectorID != sdk.DetectorIDBetterLeaks {
		t.Fatalf("candidate finding shape valid = %t (count=%d)", len(findings) == 1 && findings[0].DetectorID == sdk.DetectorIDBetterLeaks, len(findings))
	}
	maskedRemainder := strings.TrimPrefix(redacted, "token=ghp_")
	if strings.Contains(redacted, token) || !strings.Contains(redacted, "ghp_") || maskedRemainder == "" || strings.Trim(maskedRemainder, "#") != "" || len(redacted) != len("token="+token) {
		t.Fatalf("redacted candidate invariants: same_length=%t secret_removed=%t prefix_preserved=%t configured_mask=%t", len(redacted) == len("token="+token), !strings.Contains(redacted, token), strings.Contains(redacted, "ghp_"), maskedRemainder != "" && strings.Trim(maskedRemainder, "#") == "")
	}
}

func TestBetterLeaksRewriteMatcherRedactsOnlyVerifiedTextOccurrence(t *testing.T) {
	const token = "same-secret"
	raw := []byte("detected=" + token + " undetected=" + token)
	start := bytes.Index(raw, []byte(token))
	span, err := spanForByteRange(raw, start, start+len(token))
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := newBetterLeaksRewriteMatcher(engine.AsMatcher(engine.NewMatcher(nil)), LogicalFragment{
		Kind:      FragmentText,
		Location:  "messages[0].parts[0]",
		privateID: "fragment[0]",
		Raw:       raw,
	}, []betterLeaksOccurrence{{
		value:          []byte(token),
		span:           span,
		fieldID:        "fragment[0]",
		representation: betterLeaksOccurrenceLiteral,
		ruleID:         "contextual-rule",
	}})
	if err != nil {
		t.Fatal(err)
	}
	redacted, _, err := matcher.RedactString(context.Background(), string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(redacted) != len(raw) {
		t.Fatalf("rewrite changed byte length: same_length=%t", len(redacted) == len(raw))
	}
	firstEnd := start + len(token)
	secondStart := strings.LastIndex(redacted, token)
	if strings.Contains(redacted[:firstEnd], token) || secondStart <= firstEnd || !strings.Contains(redacted[secondStart:], token) {
		t.Fatalf("verified occurrence eligibility: first_removed=%t second_preserved=%t", !strings.Contains(redacted[:firstEnd], token), secondStart > firstEnd && strings.Contains(redacted[secondStart:], token))
	}
}

func TestBetterLeaksRewriteMatcherRedactsOnlyVerifiedJSONToken(t *testing.T) {
	const token = "same-secret"
	raw := []byte(`{"detected":"same-secret","undetected":"same-secret"}`)
	start := bytes.Index(raw, []byte(token))
	span, err := spanForByteRange(raw, start, start+len(token))
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := newBetterLeaksRewriteMatcher(engine.AsMatcher(engine.NewMatcher(nil)), LogicalFragment{
		Kind:      FragmentJSON,
		Location:  "messages[0].parts[0]",
		privateID: "fragment[0]",
		Raw:       raw,
	}, []betterLeaksOccurrence{{
		value:          []byte(token),
		span:           span,
		fieldID:        "fragment[0]",
		representation: betterLeaksOccurrenceLiteral,
		ruleID:         "contextual-json-rule",
	}})
	if err != nil {
		t.Fatal(err)
	}
	redacted, _, err := redactJSONPayload(context.Background(), matcher, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(redacted) {
		t.Fatal("contextual JSON rewrite produced invalid JSON")
	}
	var decoded map[string]string
	if err := json.Unmarshal(redacted, &decoded); err != nil {
		t.Fatal("contextual JSON rewrite did not decode")
	}
	if decoded["detected"] == token || decoded["undetected"] != token || len(decoded["detected"]) != len(token) {
		t.Fatalf("verified JSON token invariants: detected_changed=%t undetected_preserved=%t detected_length_preserved=%t", decoded["detected"] != token, decoded["undetected"] == token, len(decoded["detected"]) == len(token))
	}
}

func TestBetterLeaksRewriteMatcherPreservesExactMaskAndRedactsRemainingTextOverlap(t *testing.T) {
	const (
		exactToken = "strict-secret"
		wholeToken = "strict-secret-tail"
	)
	raw := []byte("detected=" + wholeToken + " unrelated=unrelated-value")
	start := bytes.Index(raw, []byte(wholeToken))
	span, err := spanForByteRange(raw, start, start+len(wholeToken))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := engine.BuildCatalog([]engine.CatalogInput{{Name: "STRICT_SUBSTRING", Value: exactToken}}, 8)
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := newBetterLeaksRewriteMatcher(engine.AsMatcher(engine.NewMatcher(cat)), LogicalFragment{
		Kind:      FragmentText,
		Location:  "messages[0].parts[0]",
		privateID: "fragment[0]",
		Raw:       raw,
	}, []betterLeaksOccurrence{{
		value:          []byte(wholeToken),
		span:           span,
		fieldID:        "fragment[0]",
		representation: betterLeaksOccurrenceLiteral,
		ruleID:         "strict-substring-rule",
	}})
	if err != nil {
		t.Fatal(err)
	}
	redacted, _, err := matcher.RedactString(context.Background(), string(raw))
	if err != nil {
		t.Fatal(err)
	}
	separator := strings.Index(redacted, " unrelated=")
	if separator < 0 || strings.Contains(redacted[:separator], wholeToken) || strings.Contains(redacted[:separator], "tail") || !strings.Contains(redacted[separator:], "unrelated-value") {
		t.Fatalf("text overlap invariants: first_scrubbed=%t unrelated_preserved=%t same_length=%t", separator >= 0 && !strings.Contains(redacted[:separator], wholeToken) && !strings.Contains(redacted[:separator], "tail"), separator >= 0 && strings.Contains(redacted[separator:], "unrelated-value"), len(redacted) == len(raw))
	}
	if len(redacted) != len(raw) {
		t.Fatal("text overlap changed byte length")
	}
}

func TestBetterLeaksRewriteMatcherPreservesExactMaskAndRedactsRemainingJSONOverlap(t *testing.T) {
	const (
		exactToken = "strict-secret"
		wholeToken = "strict-secret-tail"
	)
	raw := []byte(`{"detected":"` + wholeToken + `","unrelated":"unrelated-value"}`)
	start := bytes.Index(raw, []byte(wholeToken))
	span, err := spanForByteRange(raw, start, start+len(wholeToken))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := engine.BuildCatalog([]engine.CatalogInput{{Name: "STRICT_SUBSTRING", Value: exactToken}}, 8)
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := newBetterLeaksRewriteMatcher(engine.AsMatcher(engine.NewMatcher(cat)), LogicalFragment{
		Kind:      FragmentJSON,
		Location:  "messages[0].parts[0]",
		privateID: "fragment[0]",
		Raw:       raw,
	}, []betterLeaksOccurrence{{
		value:          []byte(wholeToken),
		span:           span,
		fieldID:        "fragment[0]",
		representation: betterLeaksOccurrenceLiteral,
		ruleID:         "strict-substring-json-rule",
	}})
	if err != nil {
		t.Fatal(err)
	}
	redacted, _, err := redactJSONPayload(context.Background(), matcher, raw)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(redacted, &decoded); err != nil {
		t.Fatal("JSON overlap rewrite did not decode")
	}
	if decoded["detected"] == wholeToken || strings.Contains(decoded["detected"], "tail") || decoded["unrelated"] != "unrelated-value" || len(decoded["detected"]) != len(wholeToken) {
		t.Fatalf("JSON overlap invariants: first_scrubbed=%t unrelated_preserved=%t length_preserved=%t", decoded["detected"] != wholeToken && !strings.Contains(decoded["detected"], "tail"), decoded["unrelated"] == "unrelated-value", len(decoded["detected"]) == len(wholeToken))
	}
}

func TestBetterLeaksRewriteMatcherRedactsPartialDiscoveryOverlapWithoutTouchingUnrelatedText(t *testing.T) {
	const (
		firstToken  = "prefix-secret"
		secondToken = "secret-tail"
	)
	raw := []byte("detected=" + "prefix-secret-tail" + " unrelated=" + secondToken)
	firstStart := bytes.Index(raw, []byte(firstToken))
	secondStart := bytes.Index(raw[firstStart:], []byte(secondToken)) + firstStart
	firstSpan, err := spanForByteRange(raw, firstStart, firstStart+len(firstToken))
	if err != nil {
		t.Fatal(err)
	}
	secondSpan, err := spanForByteRange(raw, secondStart, secondStart+len(secondToken))
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := newBetterLeaksRewriteMatcher(engine.AsMatcher(engine.NewMatcher(nil)), LogicalFragment{
		Kind:      FragmentText,
		Location:  "messages[0].parts[0]",
		privateID: "fragment[0]",
		Raw:       raw,
	}, []betterLeaksOccurrence{
		{value: []byte(firstToken), span: firstSpan, fieldID: "fragment[0]", representation: betterLeaksOccurrenceLiteral, ruleID: "partial-first"},
		{value: []byte(secondToken), span: secondSpan, fieldID: "fragment[0]", representation: betterLeaksOccurrenceLiteral, ruleID: "partial-second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	redacted, _, err := matcher.RedactString(context.Background(), string(raw))
	if err != nil {
		t.Fatal(err)
	}
	separator := strings.Index(redacted, " unrelated=")
	if separator < 0 || strings.Contains(redacted[:separator], "prefix-secret") || strings.Contains(redacted[:separator], "secret-tail") || strings.Contains(redacted[:separator], "tail") || !strings.Contains(redacted[separator:], secondToken) || len(redacted) != len(raw) {
		t.Fatalf("partial overlap invariants: detected_scrubbed=%t unrelated_preserved=%t same_length=%t", separator >= 0 && !strings.Contains(redacted[:separator], "prefix-secret") && !strings.Contains(redacted[:separator], "secret-tail") && !strings.Contains(redacted[:separator], "tail"), separator >= 0 && strings.Contains(redacted[separator:], secondToken), len(redacted) == len(raw))
	}
}

func TestGuard_BetterLeaksLogDetectorFailureDecisionValidatesWithoutFindings(t *testing.T) {
	services, err := BuildGenerationServices(DetectorPolicy{
		BetterLeaks: BetterLeaksPolicy{
			Enabled:           true,
			MinimumConfidence: DefaultBetterLeaksConfidence,
			MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
			Workers:           1,
			MaxFindings:       DefaultBetterLeaksMaxFindings,
		},
	}, engine.NewDisabledSource())
	if err != nil {
		t.Fatal(err)
	}
	call := betterLeaksTextCall("GITHUB_TOKEN=" + adapterGitHubToken)
	// Exercise the existing private unavailable-scanner failure boundary. A
	// canceled context is control flow and must never become a log decision.
	services.betterLeaks.scanner = nil
	decision, err := NewGuard(Config{Action: ActionLog}).Evaluate(t.Context(), &call, sdk.Meta{}, sdk.Services{
		MatcherResolver: engine.NewStaticMatcherResolver(nil, engine.MatcherOptions{}),
		Capability:      services,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != sdk.OutcomeLog || decision.FailureKind != sdk.FailureKindDetectorFailure {
		t.Fatalf("detector failure decision shape: outcome=%q failure_kind=%q", decision.Outcome, decision.FailureKind)
	}
	if len(decision.Findings) != 0 {
		t.Fatal("unavailable scanner produced findings")
	}
	assertBetterLeaksDecisionSafe(t, decision)
	if err := decision.Validate(); err != nil {
		t.Fatal("detector failure decision should validate")
	}
}

func TestGuard_BetterLeaksDiscoveryDoesNotRequireExactMatcherResolver(t *testing.T) {
	services, err := BuildGenerationServices(DetectorPolicy{
		BetterLeaks: BetterLeaksPolicy{
			Enabled:           true,
			MinimumConfidence: DefaultBetterLeaksConfidence,
			MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
			Workers:           1,
			MaxFindings:       DefaultBetterLeaksMaxFindings,
		},
	}, engine.NewDisabledSource())
	if err != nil {
		t.Fatal(err)
	}
	call := betterLeaksTextCall("GITHUB_TOKEN=" + adapterGitHubToken)
	decision, err := NewGuard(Config{Action: ActionBlock}).Evaluate(context.Background(), &call, sdk.Meta{}, sdk.Services{
		Capability: services,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != sdk.OutcomeBlock || len(decision.Findings) == 0 {
		t.Fatalf("discovery-only decision shape: blocked=%t findings=%d", decision.Outcome == sdk.OutcomeBlock, len(decision.Findings))
	}
}

func TestGuard_BetterLeaksExactOverlapUsesOneCanonicalFinding(t *testing.T) {
	const token = adapterGitHubToken
	cat, err := engine.BuildCatalog([]engine.CatalogInput{{
		Name:              "GITHUB_TOKEN",
		Value:             token,
		KnownPublicPrefix: "ghp_",
		SourceCategory:    sdk.SourceCategoryProxyEnv,
	}}, 8)
	if err != nil {
		t.Fatal(err)
	}
	services, err := BuildGenerationServices(DetectorPolicy{
		BetterLeaks: BetterLeaksPolicy{
			Enabled:           true,
			MinimumConfidence: DefaultBetterLeaksConfidence,
			MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
			Workers:           1,
			MaxFindings:       DefaultBetterLeaksMaxFindings,
		},
	}, engine.NewDisabledSource())
	if err != nil {
		t.Fatal(err)
	}
	call := betterLeaksTextCall("GITHUB_TOKEN=" + token)
	decision, err := NewGuard(Config{Action: ActionRedact}).Evaluate(context.Background(), &call, sdk.Meta{}, sdk.Services{
		MatcherResolver: engine.NewStaticMatcherResolver(cat, engine.MatcherOptions{PreserveKnownPrefixes: true}),
		Capability:      services,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != sdk.OutcomeRedacted || decision.MutationCount != 1 || len(decision.Findings) != 1 {
		t.Fatalf("overlap decision invariants: redacted=%t mutations=%d findings=%d", decision.Outcome == sdk.OutcomeRedacted, decision.MutationCount, len(decision.Findings))
	}
	if decision.Findings[0].DetectorID != sdk.DetectorIDExact || decision.Findings[0].OccurrenceCount != 1 {
		t.Fatalf("overlap finding invariants: exact=%t occurrences=%d", decision.Findings[0].DetectorID == sdk.DetectorIDExact, decision.Findings[0].OccurrenceCount)
	}
	if bytes.Contains([]byte(call.Messages[0].Parts[0].Text), []byte(token)) {
		t.Fatal("overlapping token survived redaction")
	}
}

func TestGuard_BetterLeaksMultipartLiteralComponentsReachRewriteBridge(t *testing.T) {
	services, err := BuildGenerationServices(DetectorPolicy{
		BetterLeaks: BetterLeaksPolicy{
			Enabled:           true,
			MinimumConfidence: DefaultBetterLeaksConfidence,
			MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
			Workers:           1,
			MaxFindings:       DefaultBetterLeaksMaxFindings,
		},
	}, engine.NewDisabledSource())
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(awsMultipartFragment)
	call := betterLeaksTextCall(string(raw))
	decision, err := NewGuard(Config{Action: ActionRedact}).Evaluate(context.Background(), &call, sdk.Meta{}, sdk.Services{
		MatcherResolver: engine.NewStaticMatcherResolver(nil, engine.MatcherOptions{}),
		Capability:      services,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := call.Messages[0].Parts[0].Text
	if decision.Outcome != sdk.OutcomeRedacted || decision.MutationCount == 0 {
		t.Fatalf("multipart decision invariants: redacted=%t mutations=%d", decision.Outcome == sdk.OutcomeRedacted, decision.MutationCount)
	}
	if strings.Contains(got, adapterAWSAccessID) || strings.Contains(got, adapterAWSSecretFixture) || len(got) != len(raw) {
		t.Fatalf("multipart rewrite invariants: access_removed=%t secret_removed=%t same_length=%t", !strings.Contains(got, adapterAWSAccessID), !strings.Contains(got, adapterAWSSecretFixture), len(got) == len(raw))
	}
}
