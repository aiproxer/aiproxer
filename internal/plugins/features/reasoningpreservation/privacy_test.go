package reasoningpreservation_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
)

func TestSafeOutcome_wireValuesExact_contractLock(t *testing.T) {
	t.Parallel()
	want := map[reasoningpreservation.SafeOutcome]string{
		reasoningpreservation.OutcomeObserved:        "observed",
		reasoningpreservation.OutcomePreserved:       "preserved",
		reasoningpreservation.OutcomeMissing:         "missing",
		reasoningpreservation.OutcomeRestored:        "restored",
		reasoningpreservation.OutcomeAmbiguous:       "ambiguous",
		reasoningpreservation.OutcomeConflicting:     "conflicting",
		reasoningpreservation.OutcomeUnmatched:       "unmatched",
		reasoningpreservation.OutcomeUnrepresentable: "unrepresentable",
		reasoningpreservation.OutcomeStateError:      "state_error",
		reasoningpreservation.OutcomeEvicted:         "evicted",
		reasoningpreservation.OutcomeOversize:        "oversize",
	}
	for outcome, wire := range want {
		if string(outcome) != wire {
			t.Fatalf("outcome %v wire=%q want %q", outcome, outcome, wire)
		}
	}
}

func TestFormatSafeDiagnostic_noSensitiveLeakage(t *testing.T) {
	t.Parallel()
	sensitive := struct {
		reasoningText string
		signature     string
		opaqueHex     string
		promptExcerpt string
		anchorHex     string
		sessionOpaque string
		freeformModel string
	}{
		reasoningText: "super-secret-chain-of-thought",
		signature:     "anthropic-thinking-signature-abc123",
		opaqueHex:     "deadbeefcafebabe",
		promptExcerpt: "user prompt excerpt about secrets",
		anchorHex:     "0123456789abcdef0123456789abcdef",
		sessionOpaque: "authoritative-session-partition-uuid",
		freeformModel: "moonshot-v1-8k-custom-label",
	}
	counts := map[string]int{"restored": 1, "bytes": 128}
	diag, err := reasoningpreservation.FormatSafeDiagnostic(
		reasoningpreservation.OutcomeRestored,
		"rule-openrouter-kimi",
		counts,
	)
	redNotImplemented(t, err, "FormatSafeDiagnostic must be implemented")
	if err != nil {
		t.Fatalf("FormatSafeDiagnostic: %v", err)
	}
	if diag == "" {
		t.Fatal("RED: FormatSafeDiagnostic must emit safe structured diagnostics")
	}
	forbidden := []string{
		sensitive.reasoningText,
		sensitive.signature,
		sensitive.opaqueHex,
		sensitive.promptExcerpt,
		sensitive.anchorHex,
		sensitive.sessionOpaque,
		sensitive.freeformModel,
	}
	for _, needle := range forbidden {
		if strings.Contains(diag, needle) {
			t.Fatalf("diagnostic leaked %q in %q", needle, diag)
		}
	}
	if !strings.Contains(diag, "restored") {
		t.Fatalf("diagnostic must include safe outcome label, got %q", diag)
	}
}

func TestProjectSafeError_noSensitiveLeakage(t *testing.T) {
	t.Parallel()
	errText, err := reasoningpreservation.ProjectSafeError(leakageError{
		reasoning: "hidden reasoning payload",
		signature: "sig-deadbeef",
		anchor:    "0123456789abcdef",
		session:   "session-partition-secret",
		model:     "kimi-k2-preview",
		prompt:    "prompt excerpt",
	})
	redNotImplemented(t, err, "ProjectSafeError must be implemented")
	if err != nil {
		t.Fatalf("ProjectSafeError: %v", err)
	}
	if errText == "" {
		t.Fatal("RED: ProjectSafeError must return stable safe text")
	}
	for _, needle := range []string{
		"hidden reasoning payload",
		"sig-deadbeef",
		"0123456789abcdef",
		"session-partition-secret",
		"kimi-k2-preview",
		"prompt excerpt",
	} {
		if strings.Contains(errText, needle) {
			t.Fatalf("ProjectSafeError leaked %q in %q", needle, errText)
		}
	}
}

type leakageError struct {
	reasoning string
	signature string
	anchor    string
	session   string
	model     string
	prompt    string
}

func (e leakageError) Error() string {
	return "reasoning=" + e.reasoning +
		" signature=" + e.signature +
		" anchor=" + e.anchor +
		" session=" + e.session +
		" model=" + e.model +
		" prompt=" + e.prompt
}

func TestPrivacy_forbiddenNeedlesTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		outcome reasoningpreservation.SafeOutcome
		ruleID  string
	}{
		{name: "observed", outcome: reasoningpreservation.OutcomeObserved, ruleID: "builtin-kimi"},
		{name: "preserved", outcome: reasoningpreservation.OutcomePreserved, ruleID: "rule-1"},
		{name: "missing", outcome: reasoningpreservation.OutcomeMissing, ruleID: "rule-1"},
		{name: "restored", outcome: reasoningpreservation.OutcomeRestored, ruleID: "rule-1"},
		{name: "ambiguous", outcome: reasoningpreservation.OutcomeAmbiguous, ruleID: "rule-1"},
		{name: "conflicting", outcome: reasoningpreservation.OutcomeConflicting, ruleID: "rule-1"},
		{name: "unmatched", outcome: reasoningpreservation.OutcomeUnmatched, ruleID: "rule-1"},
		{name: "unrepresentable", outcome: reasoningpreservation.OutcomeUnrepresentable, ruleID: "rule-1"},
		{name: "state_error", outcome: reasoningpreservation.OutcomeStateError, ruleID: "rule-1"},
		{name: "evicted", outcome: reasoningpreservation.OutcomeEvicted, ruleID: "rule-1"},
		{name: "oversize", outcome: reasoningpreservation.OutcomeOversize, ruleID: "rule-1"},
	}
	needles := []string{
		"chain-of-thought-secret",
		"thinking-signature-value",
		"cafebabeopaque",
		"0123456789abcdef0123456789abcdef",
		"authoritative-session-id",
		"moonshot-v1-8k",
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			counts := map[string]int{"count": 1}
			for _, needle := range needles {
				counts[needle] = 1
			}
			out, err := reasoningpreservation.FormatSafeDiagnostic(tc.outcome, tc.ruleID+"/"+needles[0], counts)
			redNotImplemented(t, err, "FormatSafeDiagnostic must be implemented")
			if err != nil {
				t.Fatalf("FormatSafeDiagnostic: %v", err)
			}
			if out == "" {
				t.Fatal("RED: expected safe diagnostic output")
			}
			for _, needle := range needles {
				if strings.Contains(out, needle) {
					t.Fatalf("FormatSafeDiagnostic leaked %q in %q", needle, out)
				}
			}
			if !strings.Contains(out, string(tc.outcome)) {
				t.Fatalf("diagnostic must include outcome %q, got %q", tc.outcome, out)
			}

			projected, err := reasoningpreservation.ProjectSafeError(leakageError{
				reasoning: needles[0],
				signature: needles[1],
				anchor:    needles[3],
				session:   needles[4],
				model:     needles[5],
				prompt:    needles[2],
			})
			redNotImplemented(t, err, "ProjectSafeError must be implemented")
			if err != nil {
				t.Fatalf("ProjectSafeError: %v", err)
			}
			if projected == "" {
				t.Fatal("RED: ProjectSafeError must return stable safe text")
			}
			for _, needle := range needles {
				if strings.Contains(projected, needle) {
					t.Fatalf("ProjectSafeError leaked %q in %q", needle, projected)
				}
			}
		})
	}
}
