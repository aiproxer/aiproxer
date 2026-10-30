package secretguard

import (
	"strings"
	"testing"
)

const syntheticSecret = "sk-secretguard-validation"

func TestDecisionValidateAcceptsLegalShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		decision Decision
	}{
		{
			name:     "pass",
			decision: Decision{Outcome: OutcomePass},
		},
		{
			name: "log_scan_limit",
			decision: Decision{
				Outcome:       OutcomeLog,
				ScanLimitHit:  true,
				FailureKind:   "scan_limit",
				FailureReason: "scan_max_bytes exceeded",
			},
		},
		{
			name: "log_with_findings",
			decision: Decision{
				Outcome: OutcomeLog,
				Findings: []Finding{{
					SecretRefName:   "OPENAI_API_KEY",
					SourceCategory:  SourceCategoryProxyEnv,
					Location:        "messages[0].parts[0]",
					OccurrenceCount: 1,
				}},
			},
		},
		{
			name: "log_detector_failure_without_findings",
			decision: Decision{
				Outcome:       OutcomeLog,
				FailureKind:   FailureKindDetectorFailure,
				FailureReason: "detector scan failed",
			},
		},
		{
			name: "redacted",
			decision: Decision{
				Outcome:       OutcomeRedacted,
				Findings:      []Finding{{SecretRefName: "OPENAI_API_KEY", Location: "messages[0].parts[0]", OccurrenceCount: 1}},
				MutationCount: 1,
			},
		},
		{
			name: "block",
			decision: Decision{
				Outcome:  OutcomeBlock,
				Findings: []Finding{{SecretRefName: "OPENAI_API_KEY", Aliases: []string{"sk-test"}, SourceCategory: SourceCategoryProxyEnv, Location: "messages[0].parts[0]", OccurrenceCount: 1}},
			},
		},
		{
			name: "block_unsupported_json_token",
			decision: Decision{
				Outcome:       OutcomeBlock,
				Findings:      []Finding{{SecretRefName: "OPENAI_API_KEY", Location: "messages[0].parts[0]", OccurrenceCount: 1}},
				FailureKind:   "unsupported_json_token",
				FailureReason: "unsupported JSON token encountered",
			},
		},
		{
			name: "block_unrewritable_detected_secret",
			decision: Decision{
				Outcome:       OutcomeBlock,
				Findings:      []Finding{{DetectorID: DetectorIDBetterLeaks, RuleID: "rule", Location: "messages[0].parts[0]", OccurrenceCount: 1}},
				FailureKind:   FailureKindUnrewritableDetectedSecret,
				FailureReason: "detected secret cannot be safely rewritten",
			},
		},
		{
			name: "block_scan_limit",
			decision: Decision{
				Outcome:       OutcomeBlock,
				ScanLimitHit:  true,
				FailureKind:   "scan_limit",
				FailureReason: "scan_max_bytes exceeded",
			},
		},
		{
			name: "empty_source_category_treated_as_unknown",
			decision: Decision{
				Outcome: OutcomeBlock,
				Findings: []Finding{{
					SecretRefName:   "OPENAI_API_KEY",
					SourceCategory:  "",
					Location:        "messages[0].parts[0]",
					OccurrenceCount: 1,
				}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.decision.Validate(); err != nil {
				t.Fatalf("valid decision rejected: %#v -> %v", tc.decision, err)
			}
		})
	}
}

func TestDecisionValidateRejectsMalformedShapes(t *testing.T) {
	t.Parallel()
	secret := syntheticSecret
	long := strings.Repeat("a", decisionMaxTokenBytes+1)
	longReason := strings.Repeat("b", decisionMaxReasonBytes+1)
	longLocation := strings.Repeat("c", findingMaxLocationBytes+1)
	longAlias := strings.Repeat("d", findingMaxAliasBytes+1)
	tooManyAliases := make([]string, findingMaxAliases+1)
	for i := range tooManyAliases {
		tooManyAliases[i] = "alias"
	}

	cases := []struct {
		name     string
		decision Decision
		want     string
	}{
		{
			name:     "unknown_outcome",
			decision: Decision{Outcome: Outcome("nope-" + secret)},
			want:     "outcome",
		},
		{
			name: "pass_with_findings",
			decision: Decision{
				Outcome:  OutcomePass,
				Findings: []Finding{{SecretRefName: "OPENAI_API_KEY", Location: "messages[0].parts[0]", OccurrenceCount: 1}},
			},
			want: "pass",
		},
		{
			name: "pass_with_mutation",
			decision: Decision{
				Outcome:       OutcomePass,
				MutationCount: 1,
			},
			want: "mutation_count",
		},
		{
			name: "pass_scan_limit_with_metadata",
			decision: Decision{
				Outcome:       OutcomePass,
				ScanLimitHit:  true,
				FailureKind:   "scan_limit",
				FailureReason: "scan_max_bytes exceeded",
			},
			want: "pass_shape",
		},
		{
			name: "pass_scan_limit_with_findings",
			decision: Decision{
				Outcome:       OutcomePass,
				ScanLimitHit:  true,
				FailureKind:   "scan_limit",
				FailureReason: "scan_max_bytes exceeded",
				Findings:      []Finding{{SecretRefName: "OPENAI_API_KEY", Location: "messages[0].parts[0]", OccurrenceCount: 1}},
			},
			want: "pass_shape",
		},
		{
			name: "pass_scan_limit_missing_failure_kind",
			decision: Decision{
				Outcome:       OutcomePass,
				ScanLimitHit:  true,
				FailureReason: "scan_max_bytes exceeded",
			},
			want: "pass_shape",
		},
		{
			name: "log_without_findings_or_scan_limit",
			decision: Decision{
				Outcome: OutcomeLog,
			},
			want: "log",
		},
		{
			name: "log_with_mutation",
			decision: Decision{
				Outcome:       OutcomeLog,
				MutationCount: 1,
			},
			want: "mutation_count",
		},
		{
			name: "log_with_failure_without_scan_limit",
			decision: Decision{
				Outcome:       OutcomeLog,
				FailureKind:   "scan_limit",
				FailureReason: "scan_max_bytes exceeded",
			},
			want: "log",
		},
		{
			name: "log_detector_failure_without_reason",
			decision: Decision{
				Outcome:     OutcomeLog,
				FailureKind: FailureKindDetectorFailure,
			},
			want: "failure_reason",
		},
		{
			name: "log_unknown_failure_kind",
			decision: Decision{
				Outcome:       OutcomeLog,
				FailureKind:   "scanner_failure",
				FailureReason: "detector scan failed",
			},
			want: "log_shape",
		},
		{
			name: "redacted_without_mutation",
			decision: Decision{
				Outcome:  OutcomeRedacted,
				Findings: []Finding{{SecretRefName: "OPENAI_API_KEY", Location: "messages[0].parts[0]", OccurrenceCount: 1}},
			},
			want: "mutation_count",
		},
		{
			name: "redacted_without_findings",
			decision: Decision{
				Outcome:       OutcomeRedacted,
				MutationCount: 1,
			},
			want: "redacted",
		},
		{
			name: "redacted_with_failure",
			decision: Decision{
				Outcome:       OutcomeRedacted,
				MutationCount: 1,
				Findings:      []Finding{{SecretRefName: "OPENAI_API_KEY", Location: "messages[0].parts[0]", OccurrenceCount: 1}},
				FailureKind:   "scan_limit",
				FailureReason: "scan_max_bytes exceeded",
			},
			want: "redacted",
		},
		{
			name: "block_unsupported_json_token_without_findings",
			decision: Decision{
				Outcome:       OutcomeBlock,
				FailureKind:   "unsupported_json_token",
				FailureReason: "unsupported JSON token encountered",
			},
			want: "block_shape",
		},
		{
			name: "block_arbitrary_failure_kind",
			decision: Decision{
				Outcome:       OutcomeBlock,
				Findings:      []Finding{{SecretRefName: "OPENAI_API_KEY", Location: "messages[0].parts[0]", OccurrenceCount: 1}},
				FailureKind:   "provider_error",
				FailureReason: "provider_error",
			},
			want: "failure_kind",
		},
		{
			name: "block_with_mutation",
			decision: Decision{
				Outcome:       OutcomeBlock,
				MutationCount: 1,
				Findings:      []Finding{{SecretRefName: "OPENAI_API_KEY", Location: "messages[0].parts[0]", OccurrenceCount: 1}},
			},
			want: "mutation_count",
		},
		{
			name: "finding_empty_secret_ref",
			decision: Decision{
				Outcome: OutcomeBlock,
				Findings: []Finding{{
					SecretRefName:   "",
					Location:        "messages[0].parts[0]",
					OccurrenceCount: 1,
				}},
			},
			want: "secret_ref",
		},
		{
			name: "finding_secret_ref_too_long",
			decision: Decision{
				Outcome: OutcomeBlock,
				Findings: []Finding{{
					SecretRefName:   long,
					Location:        "messages[0].parts[0]",
					OccurrenceCount: 1,
				}},
			},
			want: "secret_ref",
		},
		{
			name: "finding_location_with_control_chars",
			decision: Decision{
				Outcome: OutcomeBlock,
				Findings: []Finding{{
					SecretRefName:   "OPENAI_API_KEY",
					Location:        "messages[0]\nparts[0]",
					OccurrenceCount: 1,
				}},
			},
			want: "location",
		},
		{
			name: "finding_location_too_long",
			decision: Decision{
				Outcome: OutcomeBlock,
				Findings: []Finding{{
					SecretRefName:   "OPENAI_API_KEY",
					Location:        longLocation,
					OccurrenceCount: 1,
				}},
			},
			want: "location",
		},
		{
			name: "finding_zero_occurrence",
			decision: Decision{
				Outcome: OutcomeBlock,
				Findings: []Finding{{
					SecretRefName:   "OPENAI_API_KEY",
					Location:        "messages[0].parts[0]",
					OccurrenceCount: 0,
				}},
			},
			want: "occurrence",
		},
		{
			name: "finding_alias_empty",
			decision: Decision{
				Outcome: OutcomeBlock,
				Findings: []Finding{{
					SecretRefName:   "OPENAI_API_KEY",
					Aliases:         []string{""},
					Location:        "messages[0].parts[0]",
					OccurrenceCount: 1,
				}},
			},
			want: "alias",
		},
		{
			name: "finding_alias_too_long",
			decision: Decision{
				Outcome: OutcomeBlock,
				Findings: []Finding{{
					SecretRefName:   "OPENAI_API_KEY",
					Aliases:         []string{longAlias},
					Location:        "messages[0].parts[0]",
					OccurrenceCount: 1,
				}},
			},
			want: "alias",
		},
		{
			name: "finding_alias_count_too_large",
			decision: Decision{
				Outcome: OutcomeBlock,
				Findings: []Finding{{
					SecretRefName:   "OPENAI_API_KEY",
					Aliases:         tooManyAliases,
					Location:        "messages[0].parts[0]",
					OccurrenceCount: 1,
				}},
			},
			want: "alias",
		},
		{
			name: "finding_alias_with_control_chars",
			decision: Decision{
				Outcome: OutcomeBlock,
				Findings: []Finding{{
					SecretRefName:   "OPENAI_API_KEY",
					Aliases:         []string{"alias\tname"},
					Location:        "messages[0].parts[0]",
					OccurrenceCount: 1,
				}},
			},
			want: "alias",
		},
		{
			name: "finding_source_category_invalid",
			decision: Decision{
				Outcome: OutcomeBlock,
				Findings: []Finding{{
					SecretRefName:   "OPENAI_API_KEY",
					SourceCategory:  SourceCategory("bogus"),
					Location:        "messages[0].parts[0]",
					OccurrenceCount: 1,
				}},
			},
			want: "source_category",
		},
		{
			name: "failure_kind_with_control_chars",
			decision: Decision{
				Outcome:       OutcomeLog,
				ScanLimitHit:  true,
				FailureKind:   "scan\nlimit",
				FailureReason: "scan_max_bytes exceeded",
			},
			want: "failure_kind",
		},
		{
			name: "failure_reason_empty_when_scan_limit_hit",
			decision: Decision{
				Outcome:      OutcomeBlock,
				ScanLimitHit: true,
				FailureKind:  "scan_limit",
			},
			want: "failure_reason",
		},
		{
			name: "failure_reason_too_long",
			decision: Decision{
				Outcome:       OutcomeBlock,
				ScanLimitHit:  true,
				FailureKind:   "scan_limit",
				FailureReason: longReason,
			},
			want: "failure_reason",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.decision.Validate()
			if err == nil {
				t.Fatalf("malformed decision accepted: %#v", tc.decision)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q must contain %q", err.Error(), tc.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("validation error leaked synthetic secret: %q", err.Error())
			}
		})
	}
}

func TestFindingValidateAcceptsBoundedProvenanceAndLegacyShape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		finding Finding
	}{
		{
			name: "legacy exact finding",
			finding: Finding{
				SecretRefName:   "OPENAI_API_KEY",
				SourceCategory:  SourceCategoryProxyEnv,
				Location:        "messages[0].parts[0]",
				OccurrenceCount: 1,
			},
		},
		{
			name: "bounded betterleaks provenance",
			finding: Finding{
				SecretRefName:   "",
				SourceCategory:  SourceCategoryUnknown,
				Location:        "messages[0].parts[0]",
				OccurrenceCount: 1,
				DetectorID:      DetectorIDBetterLeaks,
				RuleID:          "github-pat",
				Confidence:      ConfidenceHigh,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.finding.Validate(); err != nil {
				t.Fatalf("finding rejected: %v", err)
			}
		})
	}
}

func TestFindingValidateRejectsUntrustedProvenanceMetadata(t *testing.T) {
	t.Parallel()
	malicious := "rule\nsecret"
	cases := []struct {
		name    string
		finding Finding
		want    string
	}{
		{
			name: "unknown detector",
			finding: Finding{
				SecretRefName:   "known-ref",
				OccurrenceCount: 1,
				DetectorID:      "scanner-" + malicious,
			},
			want: "detector_id",
		},
		{
			name: "detector control character",
			finding: Finding{
				SecretRefName:   "known-ref",
				OccurrenceCount: 1,
				DetectorID:      "exact\x00",
			},
			want: "detector_id",
		},
		{
			name: "rule syntax",
			finding: Finding{
				SecretRefName:   "known-ref",
				OccurrenceCount: 1,
				DetectorID:      DetectorIDBetterLeaks,
				RuleID:          malicious,
			},
			want: "rule_id",
		},
		{
			name: "rule too long",
			finding: Finding{
				SecretRefName:   "known-ref",
				OccurrenceCount: 1,
				DetectorID:      DetectorIDBetterLeaks,
				RuleID:          strings.Repeat("r", findingMaxRuleIDBytes+1),
			},
			want: "rule_id",
		},
		{
			name: "confidence outside closed set",
			finding: Finding{
				SecretRefName:   "known-ref",
				OccurrenceCount: 1,
				DetectorID:      DetectorIDBetterLeaks,
				RuleID:          "github-pat",
				Confidence:      "critical",
			},
			want: "confidence",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.finding.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want field %q", err, tc.want)
			}
			if strings.Contains(err.Error(), malicious) {
				t.Fatalf("validation error leaked untrusted metadata: %q", err)
			}
		})
	}
}
