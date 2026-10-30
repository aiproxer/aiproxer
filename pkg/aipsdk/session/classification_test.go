package session_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestClassificationZeroValueIsUnknown(t *testing.T) {
	t.Parallel()

	var got session.Classification
	if got.Kind != session.KindUnknown {
		t.Fatalf("zero-value kind = %q, want unknown", got.Kind)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("zero-value unknown classification is invalid: %v", err)
	}
	if got.IsCodingAgent() {
		t.Fatal("zero-value classification must not be coding_agent")
	}
}

func TestClassificationValidCodingAgentSources(t *testing.T) {
	t.Parallel()

	for _, source := range []session.ClassificationSource{
		session.SourceLocalIdentity,
		session.SourceLocalTooling,
		session.SourceRemote,
	} {
		t.Run(string(source), func(t *testing.T) {
			t.Parallel()
			got := session.Classification{
				Kind:       session.KindCodingAgent,
				Source:     source,
				Confidence: session.ConfidenceHigh,
				Evidence:   session.EvidenceCode("client.codex_v1-verified"),
				Revision:   1,
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("valid classification rejected: %v", err)
			}
			if !got.IsCodingAgent() {
				t.Fatal("valid positive classification should be coding_agent")
			}
		})
	}
}

func TestClassificationValidateRejectsMalformedValuesWithoutEchoingInput(t *testing.T) {
	t.Parallel()

	valid := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   session.EvidenceCode("client.codex"),
		Revision:   1,
	}
	tests := []struct {
		name  string
		value session.Classification
	}{
		{name: "unsupported kind", value: withClassification(valid, func(c *session.Classification) { c.Kind = session.Kind("secret-client-name") })},
		{name: "missing source", value: withClassification(valid, func(c *session.Classification) { c.Source = "" })},
		{name: "unsupported source", value: withClassification(valid, func(c *session.Classification) { c.Source = session.ClassificationSource("vendor-result-secret") })},
		{name: "missing confidence", value: withClassification(valid, func(c *session.Classification) { c.Confidence = session.ConfidenceUnknown })},
		{name: "unsupported confidence", value: withClassification(valid, func(c *session.Classification) { c.Confidence = session.ConfidenceBand("raw-remote-score-0.99") })},
		{name: "missing evidence", value: withClassification(valid, func(c *session.Classification) { c.Evidence = "" })},
		{name: "evidence exceeds byte limit", value: withClassification(valid, func(c *session.Classification) {
			c.Evidence = session.EvidenceCode(strings.Repeat("a", session.MaxEvidenceCodeBytes+1))
		})},
		{name: "evidence contains path separator", value: withClassification(valid, func(c *session.Classification) { c.Evidence = session.EvidenceCode("/private/path") })},
		{name: "evidence contains whitespace", value: withClassification(valid, func(c *session.Classification) { c.Evidence = session.EvidenceCode("client identity") })},
		{name: "evidence contains non-ascii", value: withClassification(valid, func(c *session.Classification) { c.Evidence = session.EvidenceCode("client.é") })},
		{name: "revision is zero", value: withClassification(valid, func(c *session.Classification) { c.Revision = 0 })},
		{name: "unknown carries data", value: session.Classification{Kind: session.KindUnknown, Source: session.SourceRemote}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.value.Validate()
			if err == nil {
				t.Fatal("malformed classification unexpectedly validated")
			}
			if got := tc.value.IsCodingAgent(); got {
				t.Fatal("malformed positive classification was treated as coding_agent")
			}
			for _, raw := range []string{"secret-client-name", "vendor-result-secret", "raw-remote-score-0.99", "/private/path", "client identity"} {
				if strings.Contains(err.Error(), raw) {
					t.Fatalf("validation error echoed input %q: %v", raw, err)
				}
			}
		})
	}
}

func TestClassificationEvidenceCodeAcceptsOnlyBoundedSafeASCII(t *testing.T) {
	t.Parallel()

	valid := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalTooling,
		Confidence: session.ConfidenceHigh,
		Evidence:   session.EvidenceCode(strings.Repeat("a", session.MaxEvidenceCodeBytes)),
		Revision:   1,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("evidence code at max byte length rejected: %v", err)
	}
}

func withClassification(value session.Classification, change func(*session.Classification)) session.Classification {
	change(&value)
	return value
}
