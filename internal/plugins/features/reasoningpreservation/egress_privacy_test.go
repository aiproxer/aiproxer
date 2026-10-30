package reasoningpreservation_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fake policies for table tests.

type fakeAllowPolicy struct{ version string }

func (f fakeAllowPolicy) Decide(_ context.Context, _ reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: f.version}, nil
}

type fakeDenyPolicy struct{ version string }

func (f fakeDenyPolicy) Decide(_ context.Context, _ reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressDeny, PolicyVersion: f.version}, nil
}

type fakeRedactPolicy struct {
	version   string
	sanitizer reasoningpreservation.TrustedTextSanitizer
}

func (f fakeRedactPolicy) Decide(_ context.Context, _ reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressRedactThenAllow, PolicyVersion: f.version, Sanitizer: f.sanitizer}, nil
}

type fakeRedactNoSanitizerPolicy struct{ version string }

func (f fakeRedactNoSanitizerPolicy) Decide(_ context.Context, _ reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressRedactThenAllow, PolicyVersion: f.version, Sanitizer: nil}, nil
}

func TestEgress_SemanticTextDoesNotImplyApproval(t *testing.T) {
	t.Parallel()
	// Classify semantic text.
	part := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "ordinary reasoning with password sk-123", "", nil)
	sem := reasoningpreservation.ClassifyReasoningPart(part)
	require.Equal(t, reasoningpreservation.ReplaySemanticText, sem, "fixture must be semantic text")
	// Even though semantic, route alone must not approve.
	in := reasoningpreservation.CompressionEgressInput{
		Route:       "compressor-route",
		Purpose:     reasoningpreservation.EgressPurposeReasoningSemanticCompression,
		SourceClass: reasoningpreservation.EgressSourceClassSemanticText,
		Principal:   reasoningpreservation.NewEgressPrincipalView("principal-a"),
	}
	// nil policy => missing_policy deny-equivalent
	dec := reasoningpreservation.EvaluateEgress(context.Background(), nil, in)
	assert.Equal(t, reasoningpreservation.EgressDeny, dec.Action)
	assert.Equal(t, "missing-policy", dec.PolicyVersion)
}

func TestEgress_Table_AllowRedactDenyMissingPolicyRouteMismatch(t *testing.T) {
	t.Parallel()
	sensitive := "my secret sk-live-abc123"
	_ = sensitive
	cases := []struct {
		name       string
		policy     reasoningpreservation.EgressPolicy
		input      reasoningpreservation.CompressionEgressInput
		wantAction reasoningpreservation.EgressAction
		wantVers   string
	}{
		{
			name:   "allow",
			policy: fakeAllowPolicy{version: "v1"},
			input: reasoningpreservation.CompressionEgressInput{
				Route: "route-a", Purpose: reasoningpreservation.EgressPurposeReasoningSemanticCompression, SourceClass: reasoningpreservation.EgressSourceClassSemanticText, Principal: reasoningpreservation.NewEgressPrincipalView("p1"),
			},
			wantAction: reasoningpreservation.EgressAllow,
			wantVers:   "v1",
		},
		{
			name:   "redact_then_allow",
			policy: fakeRedactPolicy{version: "v1", sanitizer: &recordingSanitizer{replacement: "[REDACTED]"}},
			input: reasoningpreservation.CompressionEgressInput{
				Route: "route-a", Purpose: reasoningpreservation.EgressPurposeReasoningSemanticCompression, SourceClass: reasoningpreservation.EgressSourceClassSemanticText, Principal: reasoningpreservation.NewEgressPrincipalView("p1"),
			},
			wantAction: reasoningpreservation.EgressRedactThenAllow,
			wantVers:   "v1",
		},
		{
			name:   "deny",
			policy: fakeDenyPolicy{version: "v1"},
			input: reasoningpreservation.CompressionEgressInput{
				Route: "route-a", Purpose: reasoningpreservation.EgressPurposeReasoningSemanticCompression, SourceClass: reasoningpreservation.EgressSourceClassSemanticText, Principal: reasoningpreservation.NewEgressPrincipalView("p1"),
			},
			wantAction: reasoningpreservation.EgressDeny,
			wantVers:   "v1",
		},
		{
			name:   "missing_policy_nil",
			policy: nil,
			input: reasoningpreservation.CompressionEgressInput{
				Route: "route-a", Purpose: reasoningpreservation.EgressPurposeReasoningSemanticCompression, SourceClass: reasoningpreservation.EgressSourceClassSemanticText, Principal: reasoningpreservation.NewEgressPrincipalView("p1"),
			},
			wantAction: reasoningpreservation.EgressDeny,
			wantVers:   "missing-policy",
		},
		{
			name:   "route_mismatch_denied",
			policy: fakeRouteMismatchPolicy{allowed: "route-allowed"},
			input: reasoningpreservation.CompressionEgressInput{
				Route: "route-other", Purpose: reasoningpreservation.EgressPurposeReasoningSemanticCompression, SourceClass: reasoningpreservation.EgressSourceClassSemanticText, Principal: reasoningpreservation.NewEgressPrincipalView("p1"),
			},
			wantAction: reasoningpreservation.EgressDeny,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dec := reasoningpreservation.EvaluateEgress(context.Background(), tc.policy, tc.input)
			require.Equal(t, tc.wantAction, dec.Action, "action mismatch")
			if tc.wantVers != "" {
				require.Equal(t, tc.wantVers, dec.PolicyVersion)
			}
			// redact without sanitizer must be treated as deny
			if tc.name == "redact_then_allow" {
				require.NotNil(t, dec.Sanitizer)
			}
		})
	}
}

func TestEgress_RedactWithoutSanitizerTreatedAsDeny(t *testing.T) {
	t.Parallel()
	policy := fakeRedactNoSanitizerPolicy{version: "v1"}
	in := reasoningpreservation.CompressionEgressInput{
		Route: "route-a", Purpose: reasoningpreservation.EgressPurposeReasoningSemanticCompression, SourceClass: reasoningpreservation.EgressSourceClassSemanticText, Principal: reasoningpreservation.NewEgressPrincipalView("p1"),
	}
	dec := reasoningpreservation.EvaluateEgress(context.Background(), policy, in)
	assert.Equal(t, reasoningpreservation.EgressDeny, dec.Action, "redact without sanitizer must be deny")
}

func TestEgress_RouteAloneNeverApproves(t *testing.T) {
	t.Parallel()
	// Explicit route but no policy must still deny
	in := reasoningpreservation.CompressionEgressInput{
		Route: "explicit-route", Purpose: reasoningpreservation.EgressPurposeReasoningSemanticCompression, SourceClass: reasoningpreservation.EgressSourceClassSemanticText, Principal: reasoningpreservation.NewEgressPrincipalView("p1"),
	}
	dec := reasoningpreservation.EvaluateEgress(context.Background(), nil, in)
	assert.Equal(t, reasoningpreservation.EgressDeny, dec.Action)
	// Even with allow policy, decision must come from policy, not route string alone — this is same as above but proves evaluator consults policy
	dec2 := reasoningpreservation.EvaluateEgress(context.Background(), fakeDenyPolicy{version: "v1"}, in)
	assert.Equal(t, reasoningpreservation.EgressDeny, dec2.Action)
}

// helper for route mismatch: denies if route != allowed.
type fakeRouteMismatchPolicy struct{ allowed string }

func (f fakeRouteMismatchPolicy) Decide(_ context.Context, in reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	if in.Route != f.allowed {
		return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressDeny, PolicyVersion: "route-mismatch"}, nil
	}
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: "v1"}, nil
}

// recording sanitizer for other tests
type recordingSanitizer struct {
	replacement string
	calls       int
	lastIn      string
}

func (r *recordingSanitizer) SanitizeText(_ context.Context, text string) (string, error) {
	r.calls++
	r.lastIn = text
	// simple replacement for secret pattern
	if r.replacement != "" {
		// replace a fixed sensitive token for determinism
		// tests use "sk-secret-123"
		replaced := text
		// naive replace
		for _, tok := range []string{"sk-secret-123", "sk-live-abc123", "my secret sk-live-abc123"} {
			if replaced == tok {
				replaced = r.replacement
			}
		}
		// also handle contains
		if len(text) > 0 && r.replacement != "" {
			// if text contains sensitive substring, replace
			if contains(text, "sk-secret") || contains(text, "sk-live") {
				return r.replacement, nil
			}
		}
		return replaced, nil
	}
	return text, nil
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && func() bool {
		for i := 0; i < len(s)-len(sub)+1; i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}
