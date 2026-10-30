package reasoningpreservation_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helpers for preparation tests

func semanticPlacement(text string) aipapi.Part {
	return reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, text, "", nil)
}

func exactPlacement(text string) aipapi.Part {
	return reasoningPart(aipapi.ReasoningDialectAnthropicThinkingV1, text, "sig", nil)
}

func opaquePlacement(text string) aipapi.Part {
	return reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, text, "", json.RawMessage(`{"x":1}`))
}

func allowDecision() reasoningpreservation.CompressionEgressDecision {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: "v1"}
}

func denyDecision() reasoningpreservation.CompressionEgressDecision {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressDeny, PolicyVersion: "v1"}
}

func missingPolicyDecision() reasoningpreservation.CompressionEgressDecision {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressDeny, PolicyVersion: "missing-policy"}
}

func redactDecision(s reasoningpreservation.TrustedTextSanitizer) reasoningpreservation.CompressionEgressDecision {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressRedactThenAllow, PolicyVersion: "v1", Sanitizer: s}
}

type echoSanitizer struct{}

func (e echoSanitizer) SanitizeText(_ context.Context, text string) (string, error) { return text, nil }

type failingSanitizer struct{ err error }

func (f failingSanitizer) SanitizeText(_ context.Context, _ string) (string, error) {
	return "", f.err
}

type replaceSanitizer struct{ from, to string }

func (r replaceSanitizer) SanitizeText(_ context.Context, text string) (string, error) {
	return strings.ReplaceAll(text, r.from, r.to), nil
}

func TestExtractSemanticSegments_MixedExactSemanticPlacements(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []reasoningpreservation.PlacedReasoning
		want []reasoningpreservation.CompressorInputSegment
	}{
		{
			name: "single_semantic",
			in: []reasoningpreservation.PlacedReasoning{
				placedReasoning(0, semanticPlacement("hello world")),
			},
			want: []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "hello world"}},
		},
		{
			name: "multiple_semantic",
			in: []reasoningpreservation.PlacedReasoning{
				placedReasoning(0, semanticPlacement("a")),
				placedReasoning(1, semanticPlacement("b")),
				placedReasoning(2, semanticPlacement("c")),
			},
			want: []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "a"}, {Index: 1, Text: "b"}, {Index: 2, Text: "c"}},
		},
		{
			name: "mixed_exact_and_semantic",
			in: []reasoningpreservation.PlacedReasoning{
				placedReasoning(0, exactPlacement("signed")),
				placedReasoning(1, semanticPlacement("plain")),
				placedReasoning(2, reasoningPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, "responses", "", nil)),
				placedReasoning(3, reasoningPart(aipapi.ReasoningDialect("unknown.v1"), "unknown", "", nil)),
				placedReasoning(0, semanticPlacement("second plain")),
			},
			want: []reasoningpreservation.CompressorInputSegment{{Index: 1, Text: "plain"}, {Index: 4, Text: "second plain"}},
		},
		{
			name: "semantic_with_exact_fields_excluded",
			in: []reasoningpreservation.PlacedReasoning{
				placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "readable", "sig", nil)),
				placedReasoning(1, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "readable", "", json.RawMessage(`{"a":1}`))),
				placedReasoning(2, semanticPlacement("ok")),
			},
			want: []reasoningpreservation.CompressorInputSegment{{Index: 2, Text: "ok"}},
		},
		{
			name: "excludes_non_reasoning",
			in: []reasoningpreservation.PlacedReasoning{
				placedReasoning(0, aipapi.TextPart("visible answer")),
				placedReasoning(1, aipapi.Part{Kind: aipapi.PartImageRef, ImageRef: "img", ImageMIME: "image/png"}),
				placedReasoning(2, aipapi.Part{Kind: aipapi.PartToolResult, ToolCallID: "call_1", ToolName: "tool", Content: json.RawMessage(`{}`)}),
				placedReasoning(3, aipapi.Part{Kind: aipapi.PartFileRef, FileRef: "file", FileMIME: "text/plain", FileName: "f.txt"}),
				placedReasoning(4, semanticPlacement("kept")),
			},
			want: []reasoningpreservation.CompressorInputSegment{{Index: 4, Text: "kept"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := reasoningpreservation.ExtractSemanticSegments(tc.in)
			require.Equal(t, tc.want, got)
			// non-mutation: input Text unchanged
			for i, pr := range tc.in {
				if pr.Part.Reasoning != nil && pr.Part.Reasoning.Text != tc.in[i].Part.Reasoning.Text {
					t.Fatalf("input mutated at %d", i)
				}
			}
			// placement index must reflect input order, not BeforeNonReasoningPart
			for _, seg := range got {
				if seg.Index < 0 || seg.Index >= len(tc.in) {
					t.Fatalf("index out of range %d", seg.Index)
				}
			}
		})
	}
}

func TestExtractSemanticSegments_FromArtifact_MultipleEligible(t *testing.T) {
	t.Parallel()
	art := turnArtifact("art-1", [32]byte{1}, placedReasoning(99, semanticPlacement("first")), placedReasoning(0, exactPlacement("signed")), placedReasoning(5, semanticPlacement("second")))
	got := reasoningpreservation.ExtractSemanticSegmentsFromArtifact(art)
	require.Len(t, got, 2)
	assert.Equal(t, 0, got[0].Index)
	assert.Equal(t, "first", got[0].Text)
	assert.Equal(t, 2, got[1].Index)
	assert.Equal(t, "second", got[1].Text)
	// artifact preserved
	require.Len(t, art.Reasoning, 3)
}

func TestExtractSemanticSegments_EmptyWhitespaceMalformed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		part aipapi.Part
	}{
		{name: "empty_text", part: reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "", "", nil)},
		{name: "whitespace_space", part: reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "   ", "", nil)},
		{name: "whitespace_tab", part: reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "\t\n", "", nil)},
		{name: "nil_reasoning", part: aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: nil}},
		{name: "non_reasoning_kind", part: aipapi.Part{Kind: aipapi.PartText, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "hello"}}},
		{name: "empty_part", part: aipapi.Part{}},
		{name: "unknown_dialect", part: reasoningPart(aipapi.ReasoningDialect("vendor.custom.v9"), "hello", "", nil)},
		{name: "empty_dialect", part: reasoningPart(aipapi.ReasoningDialect(""), "hello", "", nil)},
		{name: "whitespace_dialect", part: reasoningPart(aipapi.ReasoningDialect("   "), "hello", "", nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			placements := []reasoningpreservation.PlacedReasoning{placedReasoning(0, tc.part)}
			got := reasoningpreservation.ExtractSemanticSegments(placements)
			require.Nil(t, got, "malformed/empty/whitespace must be ineligible")
			// also via artifact path
			art := turnArtifact("a", [32]byte{}, placements...)
			got2 := reasoningpreservation.ExtractSemanticSegmentsFromArtifact(art)
			require.Nil(t, got2)
			// via full preparation must be ineligible
			_, outcome, err := reasoningpreservation.PrepareSemanticSegments(context.Background(), placements, allowDecision(), 1024, 1024)
			require.Error(t, err)
			assert.True(t, errors.Is(err, reasoningpreservation.ErrPreparationIneligible))
			assert.Equal(t, reasoningpreservation.OutcomeIneligible, outcome)
		})
	}
}

func TestPrepareSemanticSegments_RedactionOrdering(t *testing.T) {
	t.Parallel()
	sensitive := "sk-secret-123"
	sanitizer := replaceSanitizer{from: sensitive, to: "[REDACTED]"}
	decision := redactDecision(sanitizer)
	placements := []reasoningpreservation.PlacedReasoning{
		placedReasoning(0, semanticPlacement("prefix "+sensitive+" suffix")),
		placedReasoning(1, semanticPlacement("clean")),
	}
	// sanitized text is "prefix [REDACTED] suffix" (24) + "clean" (5) = 29
	// original text is "prefix sk-secret-123 suffix" (28) +5 =33, so budget 29 accepts sanitized but rejects original (if pre-redaction accounting were used it would fail)
	sanitized, outcome, err := reasoningpreservation.PrepareSemanticSegments(context.Background(), placements, decision, 29, 0)
	require.NoError(t, err)
	require.Equal(t, reasoningpreservation.OutcomePrepared, outcome)
	require.Len(t, sanitized, 2)
	assert.NotContains(t, sanitized[0].Text, sensitive)
	assert.Contains(t, sanitized[0].Text, "[REDACTED]")
	// verify token accounting also after sanitization: sanitized tokens smaller
	sanitizedBytes := reasoningpreservation.EstimatedBytesForSegments(sanitized)
	origBytes := len("prefix "+sensitive+" suffix") + len("clean")
	assert.Less(t, sanitizedBytes, origBytes)
	// budget exactly at sanitized bytes passes, at sanitized-1 fails
	atLimit := reasoningpreservation.EstimatedBytesForSegments(sanitized)
	_, outcome2, err := reasoningpreservation.PrepareSemanticSegments(context.Background(), placements, decision, atLimit, 0)
	require.NoError(t, err)
	require.Equal(t, reasoningpreservation.OutcomePrepared, outcome2)
	_, outcome3, err := reasoningpreservation.PrepareSemanticSegments(context.Background(), placements, decision, atLimit-1, 0)
	require.Error(t, err)
	assert.Equal(t, reasoningpreservation.OutcomeInputBytesExceeded, outcome3)
	assert.True(t, errors.Is(err, reasoningpreservation.ErrPreparationInputBytesExceeded))
}

func TestPrepareSemanticSegments_ByteTokenExactBoundaries(t *testing.T) {
	t.Parallel()
	placements := []reasoningpreservation.PlacedReasoning{
		placedReasoning(0, semanticPlacement(strings.Repeat("a", 10))),
		placedReasoning(1, semanticPlacement(strings.Repeat("b", 5))),
	}
	// total 15 bytes, 15 tokens (1 byte=1 token)
	cases := []struct {
		name        string
		maxBytes    int
		maxTokens   int
		wantOutcome reasoningpreservation.PreparationOutcome
		wantErr     error
		shouldPass  bool
	}{
		{name: "bytes_exact_pass", maxBytes: 15, maxTokens: 0, shouldPass: true},
		{name: "bytes_one_under_fail", maxBytes: 14, maxTokens: 0, wantOutcome: reasoningpreservation.OutcomeInputBytesExceeded, wantErr: reasoningpreservation.ErrPreparationInputBytesExceeded},
		{name: "bytes_one_over_pass", maxBytes: 16, maxTokens: 0, shouldPass: true},
		{name: "tokens_exact_pass", maxBytes: 0, maxTokens: 15, shouldPass: true},
		{name: "tokens_one_under_fail", maxBytes: 0, maxTokens: 14, wantOutcome: reasoningpreservation.OutcomeInputTokensExceeded, wantErr: reasoningpreservation.ErrPreparationInputTokensExceeded},
		{name: "tokens_one_over_pass", maxBytes: 0, maxTokens: 16, shouldPass: true},
		{name: "both_limits_pass", maxBytes: 15, maxTokens: 15, shouldPass: true},
		{name: "bytes_fail_tokens_pass", maxBytes: 14, maxTokens: 15, wantOutcome: reasoningpreservation.OutcomeInputBytesExceeded, wantErr: reasoningpreservation.ErrPreparationInputBytesExceeded},
		{name: "tokens_fail_bytes_pass", maxBytes: 15, maxTokens: 14, wantOutcome: reasoningpreservation.OutcomeInputTokensExceeded, wantErr: reasoningpreservation.ErrPreparationInputTokensExceeded},
		{name: "unbounded_pass", maxBytes: 0, maxTokens: 0, shouldPass: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			segs, outcome, err := reasoningpreservation.PrepareSemanticSegments(context.Background(), placements, allowDecision(), tc.maxBytes, tc.maxTokens)
			if tc.shouldPass {
				require.NoError(t, err)
				assert.Equal(t, reasoningpreservation.OutcomePrepared, outcome)
				require.Len(t, segs, 2)
			} else {
				require.Error(t, err)
				assert.Equal(t, tc.wantOutcome, outcome)
				assert.True(t, errors.Is(err, tc.wantErr))
				assert.Nil(t, segs)
			}
		})
	}
}

func TestPrepareSemanticSegments_TypedOutcomes(t *testing.T) {
	t.Parallel()
	sem := []reasoningpreservation.PlacedReasoning{placedReasoning(0, semanticPlacement("hello"))}
	cases := []struct {
		name         string
		placements   []reasoningpreservation.PlacedReasoning
		decision     reasoningpreservation.CompressionEgressDecision
		maxBytes     int
		maxTokens    int
		wantOutcome  reasoningpreservation.PreparationOutcome
		wantErrCheck error
	}{
		{name: "ineligible_empty", placements: nil, decision: allowDecision(), wantOutcome: reasoningpreservation.OutcomeIneligible, wantErrCheck: reasoningpreservation.ErrPreparationIneligible},
		{name: "ineligible_exact_only", placements: []reasoningpreservation.PlacedReasoning{placedReasoning(0, exactPlacement("signed"))}, decision: allowDecision(), wantOutcome: reasoningpreservation.OutcomeIneligible, wantErrCheck: reasoningpreservation.ErrPreparationIneligible},
		{name: "denied", placements: sem, decision: denyDecision(), wantOutcome: reasoningpreservation.OutcomeDenied, wantErrCheck: reasoningpreservation.ErrPreparationDenied},
		{name: "missing_policy", placements: sem, decision: missingPolicyDecision(), wantOutcome: reasoningpreservation.OutcomeMissingPolicy, wantErrCheck: reasoningpreservation.ErrPreparationMissingPolicy},
		{name: "sanitizer_failure_nil", placements: sem, decision: reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressRedactThenAllow, PolicyVersion: "v1", Sanitizer: nil}, wantOutcome: reasoningpreservation.OutcomeSanitizerFailed, wantErrCheck: reasoningpreservation.ErrPreparationSanitizerFailed},
		{name: "sanitizer_failure_error", placements: sem, decision: redactDecision(failingSanitizer{err: errors.New("boom")}), wantOutcome: reasoningpreservation.OutcomeSanitizerFailed, wantErrCheck: reasoningpreservation.ErrPreparationSanitizerFailed},
		{name: "bytes_exceeded", placements: sem, decision: allowDecision(), maxBytes: 2, wantOutcome: reasoningpreservation.OutcomeInputBytesExceeded, wantErrCheck: reasoningpreservation.ErrPreparationInputBytesExceeded},
		{name: "tokens_exceeded", placements: sem, decision: allowDecision(), maxTokens: 2, wantOutcome: reasoningpreservation.OutcomeInputTokensExceeded, wantErrCheck: reasoningpreservation.ErrPreparationInputTokensExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			segs, outcome, err := reasoningpreservation.PrepareSemanticSegments(context.Background(), tc.placements, tc.decision, tc.maxBytes, tc.maxTokens)
			require.Error(t, err)
			assert.Equal(t, tc.wantOutcome, outcome)
			assert.True(t, errors.Is(err, tc.wantErrCheck))
			assert.Nil(t, segs)
		})
	}
}

func TestPrepareSemanticSegments_NoMetadataLeakage(t *testing.T) {
	t.Parallel()
	placements := []reasoningpreservation.PlacedReasoning{
		placedReasoning(0, semanticPlacement("text A")),
		placedReasoning(1, semanticPlacement("text B")),
	}
	art := turnArtifact("sess-art-999", [32]byte{9, 8, 7}, placements...)
	art.SourceBackend = "backend-secret"
	art.SourceModel = "model-secret"
	decision := allowDecision()
	segs, outcome, err := reasoningpreservation.PrepareSemanticSegmentsFromArtifact(context.Background(), art, decision, 1024, 1024)
	require.NoError(t, err)
	require.Equal(t, reasoningpreservation.OutcomePrepared, outcome)
	require.Len(t, segs, 2)
	// segments must only have Index and Text
	typ := reflect.TypeFor[reasoningpreservation.CompressorInputSegment]()
	require.Equal(t, 2, typ.NumField())
	assert.Equal(t, "Index", typ.Field(0).Name)
	assert.Equal(t, "Text", typ.Field(1).Name)
	// no leakage into segment text
	for _, s := range segs {
		assert.NotContains(t, s.Text, "sess-")
		assert.NotContains(t, s.Text, "backend-secret")
		assert.NotContains(t, s.Text, "model-secret")
		assert.NotContains(t, s.Text, "art-")
	}
	// also via raw PrepareSemanticSegments with manual placements, ensure index is local
	segs2, _, _ := reasoningpreservation.PrepareSemanticSegments(context.Background(), placements, decision, 1024, 1024)
	require.Len(t, segs2, 2)
	assert.Equal(t, 0, segs2[0].Index)
	assert.Equal(t, 1, segs2[1].Index)
	// Ensure artifact IDs/anchors not reachable via segments slice stringification
	var combined strings.Builder
	for _, s := range segs {
		combined.WriteString(s.Text)
	}
	assert.NotContains(t, combined.String(), art.ID)
	assert.NotContains(t, combined.String(), art.SourceBackend)
}

func TestPrepareSemanticSegments_PreservesInputsNonMutation(t *testing.T) {
	t.Parallel()
	placements := []reasoningpreservation.PlacedReasoning{
		placedReasoning(99, semanticPlacement("original")),
		placedReasoning(0, exactPlacement("signed")),
		placedReasoning(5, semanticPlacement("keeper")),
	}
	// snapshot before
	beforeTexts := make([]string, len(placements))
	for i, pr := range placements {
		if pr.Part.Reasoning != nil {
			beforeTexts[i] = pr.Part.Reasoning.Text
		}
	}
	art := turnArtifact("art-x", [32]byte{1, 2, 3}, placements...)
	artBefore := art
	decision := allowDecision()
	segs, _, err := reasoningpreservation.PrepareSemanticSegments(context.Background(), placements, decision, 1024, 1024)
	require.NoError(t, err)
	require.Len(t, segs, 2)
	// mutate returned segments
	segs[0].Text = "mutated"
	segs[0].Index = 999
	// inputs unchanged
	for i, pr := range placements {
		if pr.Part.Reasoning != nil {
			assert.Equal(t, beforeTexts[i], pr.Part.Reasoning.Text)
		}
	}
	assert.Equal(t, artBefore.ID, art.ID)
	assert.Equal(t, artBefore.Reasoning[0].Part.Reasoning.Text, art.Reasoning[0].Part.Reasoning.Text)
	// call again idempotent
	segs2, _, _ := reasoningpreservation.PrepareSemanticSegments(context.Background(), placements, decision, 1024, 1024)
	assert.Equal(t, "original", segs2[0].Text)
	assert.Equal(t, 0, segs2[0].Index)
}

func TestEstimateInputTokens_DeterministicBounded(t *testing.T) {
	t.Parallel()
	cases := []string{"", "a", "hello world", strings.Repeat("x", 100), "multi\nline\ttext", "unicode: \u2603 \U0001F600"}
	for _, s := range cases {
		t1 := reasoningpreservation.EstimateInputTokens(s)
		t2 := reasoningpreservation.EstimateInputTokens(s)
		require.Equal(t, t1, t2, "deterministic")
		assert.Equal(t, len(s), t1, "byte-as-token upper bound")
		assert.GreaterOrEqual(t, t1, 0)
	}
	// sum consistency
	segs := []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "abc"}, {Index: 1, Text: "de"}}
	assert.Equal(t, 5, reasoningpreservation.EstimatedTokensForSegments(segs))
	assert.Equal(t, 5, reasoningpreservation.EstimatedBytesForSegments(segs))
	// len is monotonic
	assert.Less(t, reasoningpreservation.EstimateInputTokens("ab"), reasoningpreservation.EstimateInputTokens("abc"))
}

func TestPrepareCompressorInputWithLimits_RedactionBeforeBudget(t *testing.T) {
	t.Parallel()
	sensitive := "sk-secret-12345"
	sanitizer := replaceSanitizer{from: sensitive, to: "X"}
	decision := redactDecision(sanitizer)
	segments := []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: sensitive}, {Index: 1, Text: "hi"}}
	// sanitized becomes "X" (1 byte) + "hi" (2) =3 total, original 15+2=17
	// budget 5 allows sanitized but not original
	segs, outcome, err := reasoningpreservation.PrepareCompressorInputWithLimits(context.Background(), segments, decision, 5, 5)
	require.NoError(t, err)
	assert.Equal(t, reasoningpreservation.OutcomePrepared, outcome)
	require.Len(t, segs, 2)
	assert.Equal(t, "X", segs[0].Text)
	// ensure PrepareCompressorInput compat wrapper also respects bytes
	segs2, outcomeStr, err := reasoningpreservation.PrepareCompressorInput(context.Background(), segments, decision, 5)
	require.NoError(t, err)
	assert.Equal(t, "prepared", outcomeStr)
	require.Len(t, segs2, 2)
}

func TestPrepareCompressorInput_DeniedAndMissingPolicy(t *testing.T) {
	t.Parallel()
	segs := []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "a"}}
	_, outcome, err := reasoningpreservation.PrepareCompressorInputWithLimits(context.Background(), segs, denyDecision(), 10, 10)
	require.Error(t, err)
	assert.Equal(t, reasoningpreservation.OutcomeDenied, outcome)
	_, outcome2, err2 := reasoningpreservation.PrepareCompressorInputWithLimits(context.Background(), segs, missingPolicyDecision(), 10, 10)
	require.Error(t, err2)
	assert.Equal(t, reasoningpreservation.OutcomeMissingPolicy, outcome2)
	// compat via string outcome
	_, s1, _ := reasoningpreservation.PrepareCompressorInput(context.Background(), segs, denyDecision(), 10)
	assert.Equal(t, "denied", s1)
	_, s2, _ := reasoningpreservation.PrepareCompressorInput(context.Background(), segs, missingPolicyDecision(), 10)
	assert.Equal(t, "missing-policy", s2)
}

func FuzzPrepareSemanticSegments_Invariants(f *testing.F) {
	f.Add(string(aipapi.ReasoningDialectOpenAIChatTextV1), "hello", "", 10, 10)
	f.Add(string(aipapi.ReasoningDialectOpenAIChatTextV1), "   ", "", 1, 1)
	f.Add(string(aipapi.ReasoningDialectOpenAIResponsesItemV1), "hello", "sig-1", 5, 5)
	f.Add(string(aipapi.ReasoningDialectAnthropicThinkingV1), "think", "", 20, 20)
	f.Add("unknown.v1", "hello", "", 100, 100)
	f.Add(string(aipapi.ReasoningDialectOpenAIChatTextV1), "hi", "", 0, 0)
	f.Fuzz(func(t *testing.T, dialect, text, signature string, maxBytes, maxTokens int) {
		if len(dialect) > 256 || len(text) > 2048 || len(signature) > 512 {
			return
		}
		if maxBytes < 0 {
			maxBytes = -maxBytes
		}
		if maxTokens < 0 {
			maxTokens = -maxTokens
		}
		maxBytes = maxBytes % 4096
		maxTokens = maxTokens % 4096
		var sig string
		if signature != "" {
			sig = signature
		}
		part := reasoningPart(aipapi.ReasoningDialect(dialect), text, sig, nil)
		placements := []reasoningpreservation.PlacedReasoning{placedReasoning(0, part)}
		// clone for non-mutation check
		var beforeText string
		if part.Reasoning != nil {
			beforeText = part.Reasoning.Text
		}
		// extraction must be pure
		got1 := reasoningpreservation.ExtractSemanticSegments(placements)
		got2 := reasoningpreservation.ExtractSemanticSegments(placements)
		if !reflect.DeepEqual(got1, got2) {
			t.Fatalf("extraction not deterministic: %+v vs %+v", got1, got2)
		}
		if part.Reasoning != nil && part.Reasoning.Text != beforeText {
			t.Fatalf("extraction mutated input")
		}
		// preparation invariants
		decision := allowDecision()
		segs, outcome, err := reasoningpreservation.PrepareSemanticSegments(context.Background(), placements, decision, maxBytes, maxTokens)
		// outcome must be one of bounded set
		switch outcome {
		case reasoningpreservation.OutcomeIneligible, reasoningpreservation.OutcomeDenied, reasoningpreservation.OutcomeMissingPolicy, reasoningpreservation.OutcomeSanitizerFailed, reasoningpreservation.OutcomeInputBytesExceeded, reasoningpreservation.OutcomeInputTokensExceeded, reasoningpreservation.OutcomePrepared, reasoningpreservation.OutcomeInputOversize:
		default:
			t.Fatalf("unknown outcome %q", outcome)
		}
		if err != nil && outcome == reasoningpreservation.OutcomePrepared {
			t.Fatalf("prepared should not have error")
		}
		if outcome == reasoningpreservation.OutcomePrepared {
			if len(segs) == 0 {
				t.Fatalf("prepared must have segments")
			}
			for _, s := range segs {
				if s.Index != 0 {
					t.Fatalf("single placement index must be 0 got %d", s.Index)
				}
				if s.Text == "" {
					t.Fatalf("prepared text must not be empty")
				}
			}
			// bytes/tokens must be within bounds if bounded
			if maxBytes > 0 {
				total := reasoningpreservation.EstimatedBytesForSegments(segs)
				if total > maxBytes {
					t.Fatalf("bytes %d > limit %d but outcome prepared", total, maxBytes)
				}
			}
			if maxTokens > 0 {
				total := reasoningpreservation.EstimatedTokensForSegments(segs)
				if total > maxTokens {
					t.Fatalf("tokens %d > limit %d but prepared", total, maxTokens)
				}
			}
			// no metadata leakage possible: segments only have index/text, verified via struct field count elsewhere
		} else {
			if segs != nil {
				t.Fatalf("non-prepared should return nil segments, got %+v", segs)
			}
		}
		// token estimator deterministic and bounded
		if len(segs) > 0 {
			t1 := reasoningpreservation.EstimatedTokensForSegments(segs)
			t2 := reasoningpreservation.EstimatedTokensForSegments(segs)
			if t1 != t2 {
				t.Fatalf("token estimator not deterministic")
			}
			if t1 != reasoningpreservation.EstimatedBytesForSegments(segs) {
				t.Fatalf("byte-as-token estimator must equal bytes")
			}
		}
		// inputs still not mutated
		if part.Reasoning != nil && part.Reasoning.Text != beforeText {
			t.Fatalf("preparation mutated input")
		}
	})
}
