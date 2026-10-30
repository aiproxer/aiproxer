package reasoningpreservation_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedactionOccursBeforeBudgetAndSubmission(t *testing.T) {
	t.Parallel()
	sensitive := "my secret is sk-secret-123"
	sanitizer := &recordingSanitizer2{replacement: "[REDACTED]"}
	decision := reasoningpreservation.CompressionEgressDecision{
		Action:        reasoningpreservation.EgressRedactThenAllow,
		PolicyVersion: "v1",
		Sanitizer:     sanitizer,
	}
	segments := []reasoningpreservation.CompressorInputSegment{
		{Index: 0, Text: sensitive},
		{Index: 2, Text: "hi"},
	}
	// pipeline helper applies redaction then budgets
	sanitized, outcome, err := reasoningpreservation.PrepareCompressorInput(context.Background(), segments, decision, 1024)
	require.NoError(t, err)
	require.NotEqual(t, "denied", outcome)
	require.Len(t, sanitized, 2)
	// sensitive must appear only sanitized
	joined := sanitized[0].Text + sanitized[1].Text
	assert.NotContains(t, joined, "sk-secret-123")
	assert.Contains(t, joined, "[REDACTED]")
	assert.Equal(t, 2, sanitizer.Calls, "sanitizer must be invoked before budgeting")
	// budget must account sanitized bytes, not original
	// original would be len(sensitive)= ~25, sanitized is len("[REDACTED]")=10, so budget 15 would reject original but accept sanitized
	sanitized2, _, err := reasoningpreservation.PrepareCompressorInput(context.Background(), segments, decision, 15)
	require.NoError(t, err)
	assert.Equal(t, "[REDACTED]", sanitized2[0].Text)
	// fake compressor must never receive unredacted
	fake := &fakeCompressor2{}
	require.NoError(t, fake.Submit(sanitized))
	assert.NotContains(t, fake.ReceivedText(), "sk-secret-123")
}

func TestRedaction_DenyNothingProceeds(t *testing.T) {
	t.Parallel()
	sanitizer := &recordingSanitizer2{replacement: "[REDACTED]"}
	decision := reasoningpreservation.CompressionEgressDecision{
		Action:        reasoningpreservation.EgressDeny,
		PolicyVersion: "v1",
		Sanitizer:     sanitizer,
	}
	segments := []reasoningpreservation.CompressorInputSegment{
		{Index: 0, Text: "sk-secret-123"},
	}
	sanitized, outcome, err := reasoningpreservation.PrepareCompressorInput(context.Background(), segments, decision, 1024)
	require.Error(t, err)
	assert.Contains(t, outcome, "denied")
	assert.Nil(t, sanitized)
	assert.Equal(t, 0, sanitizer.Calls, "deny must not invoke sanitizer")
}

func TestRedaction_SanitizedRetainsPlacementNoPrincipal(t *testing.T) {
	t.Parallel()
	sanitizer := &recordingSanitizer2{replacement: "X"}
	decision := reasoningpreservation.CompressionEgressDecision{
		Action:        reasoningpreservation.EgressRedactThenAllow,
		PolicyVersion: "v1",
		Sanitizer:     sanitizer,
	}
	segments := []reasoningpreservation.CompressorInputSegment{
		{Index: 0, Text: "a"},
		{Index: 5, Text: "sk-secret-123"},
	}
	sanitized, _, err := reasoningpreservation.PrepareCompressorInput(context.Background(), segments, decision, 1024)
	require.NoError(t, err)
	require.Len(t, sanitized, 2)
	assert.Equal(t, 0, sanitized[0].Index)
	assert.Equal(t, 5, sanitized[1].Index)
	for _, seg := range sanitized {
		assert.NotContains(t, seg.Text, "principal")
		assert.NotContains(t, seg.Text, "sess-")
		assert.NotContains(t, seg.Text, "trace-")
	}
}

type recordingSanitizer2 struct {
	replacement string
	Calls       int
}

func (r *recordingSanitizer2) SanitizeText(_ context.Context, text string) (string, error) {
	r.Calls++
	if strings.Contains(text, "sk-secret") {
		return r.replacement, nil
	}
	return text, nil
}

type fakeCompressor2 struct {
	received []reasoningpreservation.CompressorInputSegment
}

func (f *fakeCompressor2) Submit(segs []reasoningpreservation.CompressorInputSegment) error {
	f.received = append([]reasoningpreservation.CompressorInputSegment(nil), segs...)
	return nil
}

func (f *fakeCompressor2) ReceivedText() string {
	var b strings.Builder
	for _, s := range f.received {
		b.WriteString(s.Text)
	}
	return b.String()
}
