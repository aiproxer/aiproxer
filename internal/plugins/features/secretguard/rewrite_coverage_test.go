package secretguard

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

type lengthChangingRewriteMatcher struct{ sdk.Matcher }

func (m lengthChangingRewriteMatcher) RedactBytes(_ context.Context, input []byte) ([]byte, []sdk.Finding, error) {
	return append(bytes.Clone(input), []byte(" unrelated mutation")...), nil, nil
}

func (m lengthChangingRewriteMatcher) RedactString(_ context.Context, input string) (string, []sdk.Finding, error) {
	return input + " unrelated mutation", nil, nil
}

type coverageTestResolver struct{ matcher sdk.Matcher }

func (r coverageTestResolver) Resolve(context.Context) (sdk.Matcher, error) { return r.matcher, nil }

func TestBetterLeaksRedactionCoverageBlocksUnrelatedSuccessfulMutation(t *testing.T) {
	guard, services := newBetterLeaksActionGuard(t, ActionRedact, DefaultScanMaxBytes, 256)
	services.MatcherResolver = coverageTestResolver{lengthChangingRewriteMatcher{engine.AsMatcher(engine.NewMatcher(nil))}}
	text := "GITHUB_TOKEN=" + adapterGitHubToken
	call := betterLeaksTextCall(text)
	decision, err := guard.Evaluate(t.Context(), &call, sdk.Meta{}, services)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != sdk.OutcomeBlock || decision.FailureKind != FailureKindUnrewritableDetectedSecret || decision.MutationCount != 0 {
		t.Fatalf("outcome=%s failure=%s mutations=%d", decision.Outcome, decision.FailureKind, decision.MutationCount)
	}
	if call.Messages[0].Parts[0].Text != text {
		t.Fatal("failed rewrite changed the original call")
	}
}

func TestBetterLeaksRedactionCoverageRejectsMissedJSONMappingAfterAnotherRewrite(t *testing.T) {
	text := "GITHUB_TOKEN=" + adapterGitHubToken
	raw := []byte(`["` + text + `","` + text + `"]`)
	var occurrences []betterLeaksOccurrence
	for from := 0; from < len(raw); {
		index := bytes.Index(raw[from:], []byte(adapterGitHubToken))
		if index < 0 {
			break
		}
		start := from + index
		span, err := spanForByteRange(raw, start, start+len(adapterGitHubToken))
		if err != nil {
			t.Fatal(err)
		}
		occurrences = append(occurrences, betterLeaksOccurrence{value: []byte(adapterGitHubToken), span: span, start: start, end: start + len(adapterGitHubToken), offsetsValid: true, ruleID: "github-pat"})
		from = start + len(adapterGitHubToken)
	}
	matcher, err := newBetterLeaksRewriteMatcher(nil, LogicalFragment{Kind: FragmentJSON, Raw: raw}, occurrences)
	if err != nil {
		t.Fatal(err)
	}
	bridge, ok := matcher.(*betterLeaksRewriteMatcher)
	if !ok {
		t.Fatal("expected a BetterLeaks rewrite bridge")
	}
	first, _, err := bridge.RedactString(t.Context(), text)
	if err != nil || strings.Contains(first, adapterGitHubToken) {
		t.Fatal("first rewrite did not apply")
	}
	bridge.jsonIndex = len(bridge.jsonTokens) // Force a missed later raw-to-decoded mapping.
	second, _, err := bridge.RedactString(t.Context(), text)
	if err != nil || second != text {
		t.Fatal("negative control did not miss its second candidate")
	}
	if !errors.Is(bridge.validateCoverage(), errBetterLeaksUnrewritable) {
		t.Fatal("partial rewrite was accepted")
	}
}
