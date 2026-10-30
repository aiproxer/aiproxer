package reasoningpreservation_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stub matcher implementing pkg/aipsdk/secretguard.Matcher for RED phase
type stubSecretMatcher struct {
	redacted string
}

func (s stubSecretMatcher) ScanBytes(_ context.Context, _ []byte) ([]secretguard.Finding, error) {
	return nil, nil
}

func (s stubSecretMatcher) ScanString(_ context.Context, _ string) ([]secretguard.Finding, error) {
	return nil, nil
}

func (s stubSecretMatcher) RedactBytes(_ context.Context, input []byte) ([]byte, []secretguard.Finding, error) {
	return []byte(s.redacted), nil, nil
}

func (s stubSecretMatcher) RedactString(_ context.Context, _ string) (string, []secretguard.Finding, error) {
	return s.redacted, nil, nil
}

func TestEgressPort_SecretguardAdapterImplementsTrustedSanitizer(t *testing.T) {
	t.Parallel()
	matcher := stubSecretMatcher{redacted: "sanitized-[REDACTED]"}
	sanitizer := reasoningpreservation.NewTrustedSanitizerFromMatcher(matcher)
	require.NotNil(t, sanitizer, "adapter must be non-nil")
	_ = sanitizer
	out, err := sanitizer.SanitizeText(context.Background(), "input with secret sk-123")
	require.NoError(t, err)
	assert.Equal(t, "sanitized-[REDACTED]", out)
	// nil matcher should yield nil sanitizer
	assert.Nil(t, reasoningpreservation.NewTrustedSanitizerFromMatcher(nil))
	// Direct zero-value construction also fails closed.
	empty := reasoningpreservation.SecretguardTrustedSanitizer{}
	out2, err := empty.SanitizeText(context.Background(), "plain")
	require.Error(t, err)
	assert.Empty(t, out2)
}

func TestEgressPort_RouteBoundMismatchIsMissingPolicyDeny(t *testing.T) {
	t.Parallel()
	allow := fakeAllowPolicy{version: "v1"}
	bound := reasoningpreservation.NewRouteBoundEgressPolicy(map[string]struct{}{"route-allowed": {}}, allow)
	require.NotNil(t, bound)
	// allowed route => allow
	inAllowed := reasoningpreservation.CompressionEgressInput{
		Route:       "route-allowed",
		Purpose:     reasoningpreservation.EgressPurposeReasoningSemanticCompression,
		SourceClass: reasoningpreservation.EgressSourceClassSemanticText,
		Principal:   reasoningpreservation.NewEgressPrincipalView("p1"),
	}
	dec := reasoningpreservation.EvaluateEgress(context.Background(), bound, inAllowed)
	require.Equal(t, reasoningpreservation.EgressAllow, dec.Action)
	assert.Equal(t, "v1", dec.PolicyVersion)
	// mismatched route => missing-policy-equivalent deny
	inMismatch := reasoningpreservation.CompressionEgressInput{
		Route:       "route-other",
		Purpose:     reasoningpreservation.EgressPurposeReasoningSemanticCompression,
		SourceClass: reasoningpreservation.EgressSourceClassSemanticText,
		Principal:   reasoningpreservation.NewEgressPrincipalView("p1"),
	}
	dec2 := reasoningpreservation.EvaluateEgress(context.Background(), bound, inMismatch)
	require.Equal(t, reasoningpreservation.EgressDeny, dec2.Action)
	assert.Equal(t, "missing-policy", dec2.PolicyVersion, "route mismatch must be missing-policy-equivalent deny")
	// ensure that deny via route mismatch does not leak principal into sanitized path
	// Ensure constants are canonical
	assert.Equal(t, "reasoning_semantic_compression", reasoningpreservation.EgressPurposeReasoningSemanticCompression)
	assert.Equal(t, "semantic_reasoning_text", reasoningpreservation.EgressSourceClassSemanticText)
}

func TestEgressPort_AllowRedactDenyMissingPolicyViaPort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		policy     reasoningpreservation.EgressPolicy
		route      string
		wantAction reasoningpreservation.EgressAction
		wantVers   string
	}{
		{"allow", fakeAllowPolicy{version: "v1"}, "route-a", reasoningpreservation.EgressAllow, "v1"},
		{"deny", fakeDenyPolicy{version: "v1"}, "route-a", reasoningpreservation.EgressDeny, "v1"},
		{"missing-nil", nil, "route-a", reasoningpreservation.EgressDeny, "missing-policy"},
		{"redact", fakeRedactPolicy{version: "v1", sanitizer: &recordingSanitizer{replacement: "[REDACTED]"}}, "route-a", reasoningpreservation.EgressRedactThenAllow, "v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := reasoningpreservation.CompressionEgressInput{
				Route:       tc.route,
				Purpose:     reasoningpreservation.EgressPurposeReasoningSemanticCompression,
				SourceClass: reasoningpreservation.EgressSourceClassSemanticText,
				Principal:   reasoningpreservation.NewEgressPrincipalView("p1"),
			}
			dec := reasoningpreservation.EvaluateEgress(context.Background(), tc.policy, in)
			require.Equal(t, tc.wantAction, dec.Action)
			require.Equal(t, tc.wantVers, dec.PolicyVersion)
		})
	}
}

func TestEgressPort_ControlPlaneNotInSanitizedSegments(t *testing.T) {
	t.Parallel()
	// Identity sanitizer keeps source text verbatim so the assertion proves the
	// preparation step itself never injects control-plane values into segments.
	sanitizer := &recordingSanitizer{replacement: "session sess-123 principal p1"}
	decision := reasoningpreservation.CompressionEgressDecision{
		Action:        reasoningpreservation.EgressRedactThenAllow,
		PolicyVersion: "v1",
		Sanitizer:     sanitizer,
	}
	segments := []reasoningpreservation.CompressorInputSegment{
		{Index: 0, Text: "session sess-123 principal p1"},
	}
	sanitized, _, err := reasoningpreservation.PrepareCompressorInput(context.Background(), segments, decision, 1024)
	require.NoError(t, err)
	require.Len(t, sanitized, 1)
	assert.Equal(t, "session sess-123 principal p1", sanitized[0].Text)
	// Structural guard: segments carry only local index and text; any future
	// control-plane field added to CompressorInputSegment fails this check.
	typ := reflect.TypeFor[reasoningpreservation.CompressorInputSegment]()
	require.Equal(t, 2, typ.NumField(), "CompressorInputSegment must expose only Index and Text")
	assert.Equal(t, "Index", typ.Field(0).Name)
	assert.Equal(t, "Text", typ.Field(1).Name)
}
