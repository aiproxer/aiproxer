package agentloopguard

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/progress"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

func TestProviderAcceptanceMatrix(t *testing.T) {
	t.Parallel()

	const secret = "acceptance-secret-do-not-leak"
	tests := []struct {
		name       string
		cause      terminaldecision.CandidateCause
		committed  bool
		response   string
		mutate     func(*terminaldecision.Input)
		wantKind   terminaldecision.DecisionKind
		wantReason string
		wantCalls  int
	}{
		{
			name:       "safe concrete unfinished action continues",
			response:   `{"kind":"INCOMPLETE","objective":"resume the existing test"}`,
			wantKind:   terminaldecision.DecisionContinue,
			wantReason: progress.ReasonUnfinished,
			wantCalls:  1,
		},
		{
			name:       "complete answer stops",
			response:   `{"kind":"COMPLETE"}`,
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: progress.ReasonComplete,
			wantCalls:  1,
		},
		{
			name:     "user directed question stops",
			response: `{"kind":"COMPLETE"}`,
			mutate: func(in *terminaldecision.Input) {
				in.Evidence.CandidateText = "Would you like to provide the missing account?"
				in.Evidence.Actions[1] = terminaldecision.ActionFact{}
				in.Evidence.ActionCount = 1
			},
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: progress.ReasonComplete,
			wantCalls:  1,
		},
		{
			name:     "optional improvement stops",
			response: `{"kind":"COMPLETE"}`,
			mutate: func(in *terminaldecision.Input) {
				in.Evidence.CandidateText = "The requested change is complete; an optional cleanup is available."
				in.Evidence.Actions[1] = terminaldecision.ActionFact{}
				in.Evidence.ActionCount = 1
			},
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: progress.ReasonComplete,
			wantCalls:  1,
		},
		{
			name:     "user owned next steps stop",
			response: `{"kind":"COMPLETE"}`,
			mutate: func(in *terminaldecision.Input) {
				in.Evidence.CandidateText = "The change is ready; the user must deploy it."
				in.Evidence.Actions[1] = terminaldecision.ActionFact{}
				in.Evidence.ActionCount = 1
			},
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: progress.ReasonComplete,
			wantCalls:  1,
		},
		{
			name:     "quoted future action stops",
			response: `{"kind":"COMPLETE"}`,
			mutate: func(in *terminaldecision.Input) {
				in.Evidence.CandidateText = "Later, the team may consider migrating this service."
				in.Evidence.Actions[1] = terminaldecision.ActionFact{}
				in.Evidence.ActionCount = 1
			},
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: progress.ReasonComplete,
			wantCalls:  1,
		},
		{
			name:       "refusal stops authoritatively",
			cause:      terminaldecision.CandidateCauseRefusal,
			committed:  true,
			response:   `{"kind":"INCOMPLETE","objective":"must not be used"}`,
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: "authoritative_candidate",
		},
		{
			name:       "content filter stops authoritatively",
			cause:      terminaldecision.CandidateCauseContentFilter,
			committed:  true,
			response:   `{"kind":"INCOMPLETE","objective":"must not be used"}`,
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: "authoritative_candidate",
		},
		{
			name:       "cancellation stops authoritatively",
			cause:      terminaldecision.CandidateCauseCancellation,
			committed:  true,
			response:   `{"kind":"INCOMPLETE","objective":"must not be used"}`,
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: "authoritative_candidate",
		},
		{
			name:      "unsafe partial action stops",
			cause:     terminaldecision.CandidateCauseProviderError,
			committed: true,
			response:  `{"kind":"INCOMPLETE","objective":"must not be used"}`,
			mutate: func(in *terminaldecision.Input) {
				in.Evidence.Actions[0] = terminaldecision.ActionFact{
					CallID: "call-unsafe", Kind: "tool_call", Status: "in_progress", Name: "run_sensitive_tool",
				}
				in.Evidence.Actions[1] = terminaldecision.ActionFact{}
				in.Evidence.ActionCount = 1
			},
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: "unsafe_action",
			wantCalls:  0,
		},
		{
			name:       "missing verifier stops",
			response:   "",
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: progress.ReasonUncertain,
			wantCalls:  0,
		},
		{
			name:       "malformed verifier stops",
			response:   "not-json",
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: progress.ReasonUncertain,
			wantCalls:  1,
		},
		{
			name:       "uncertain verifier stops",
			response:   `{"kind":"UNCERTAIN"}`,
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: progress.ReasonUncertain,
			wantCalls:  1,
		},
		{
			name:       "committed transport follows verifier",
			cause:      terminaldecision.CandidateCauseTransport,
			committed:  true,
			response:   `{"kind":"INCOMPLETE","objective":"resume the existing test"}`,
			wantKind:   terminaldecision.DecisionContinue,
			wantReason: progress.ReasonUnfinished,
			wantCalls:  1,
		},
		{
			name:       "committed limit follows verifier",
			cause:      terminaldecision.CandidateCauseLimit,
			committed:  true,
			response:   `{"kind":"INCOMPLETE","objective":"resume the existing test"}`,
			wantKind:   terminaldecision.DecisionContinue,
			wantReason: progress.ReasonUnfinished,
			wantCalls:  1,
		},
		{
			name:       "pre-output transport stops before verifier",
			cause:      terminaldecision.CandidateCauseTransport,
			response:   `{"kind":"INCOMPLETE","objective":"must not be used"}`,
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: "pre_output_transport",
			wantCalls:  0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cause := test.cause
			if cause == "" {
				cause = terminaldecision.CandidateCauseNormal
			}
			in := algInput(cause)
			committed := test.committed
			if test.name != "pre-output transport stops before verifier" && !committed {
				committed = true
			}
			in.Candidate.OutputCommitted = committed
			in.Evidence.CandidateText = "candidate contains " + secret
			in.Evidence.Actions[0].Name = "tool contains " + secret
			if test.mutate != nil {
				test.mutate(&in)
			}

			var collector *providerSemanticCollector
			if test.response != "" {
				collector = &providerSemanticCollector{responses: []string{test.response}}
				in.Auxiliary = collector
			}

			decision, err := NewProvider(Config{Enabled: true}).Decide(context.Background(), in)
			if err != nil {
				t.Fatalf("Decide() error = %v", err)
			}
			if decision.Kind != test.wantKind || decision.ReasonCode != test.wantReason {
				t.Fatalf("decision = %#v, want kind=%q reason=%q", decision, test.wantKind, test.wantReason)
			}
			if collector != nil && collector.calls != test.wantCalls {
				t.Fatalf("verifier calls = %d, want %d", collector.calls, test.wantCalls)
			}
			if err := decision.Validate(); err != nil {
				t.Fatalf("decision validation error = %v", err)
			}
			assertProviderDecisionPrivacy(t, decision, secret)
		})
	}
}

func TestProviderAcceptanceMatrixStopsOnNoProgressAndBudget(t *testing.T) {
	t.Parallel()

	provider := NewProvider(Config{Enabled: true, MaxSemanticContinuations: 5, NoProgressLimit: 2})
	in := algInput(terminaldecision.CandidateCauseNormal)
	in.Candidate.OutputCommitted = true
	in.Policy.MaxContinuationAttempts = 5
	collector := &providerSemanticCollector{responses: []string{
		`{"kind":"INCOMPLETE","objective":"resume the existing test"}`,
		`{"kind":"INCOMPLETE","objective":"resume the existing test"}`,
		`{"kind":"INCOMPLETE","objective":"resume the existing test"}`,
	}}
	in.Auxiliary = collector

	first, err := provider.Decide(context.Background(), in)
	if err != nil || first.Kind != terminaldecision.DecisionContinue || first.Continue == nil {
		t.Fatalf("first decision = %#v, err=%v; want continue", first, err)
	}
	in.Continuation.Attempt = 2
	in.Evidence.Lineage.Attempt = 2
	in.Evidence.Lineage.ProgressRef = first.Continue.ControlRef
	second, err := provider.Decide(context.Background(), in)
	if err != nil || second.Kind != terminaldecision.DecisionContinue || second.Continue == nil {
		t.Fatalf("second decision = %#v, err=%v; want continue", second, err)
	}
	in.Continuation.Attempt = 3
	in.Evidence.Lineage.Attempt = 3
	in.Evidence.Lineage.ProgressRef = second.Continue.ControlRef
	third, err := provider.Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("third Decide() error = %v", err)
	}
	if third.Kind != terminaldecision.DecisionAllowStop || third.ReasonCode != progress.ReasonNoProgress {
		t.Fatalf("third decision = %#v, want no-progress allow-stop", third)
	}

	assertProviderDecisionPrivacy(t, third, "acceptance-secret-do-not-leak")

	t.Run("budget", func(t *testing.T) {
		provider := NewProvider(Config{Enabled: true, MaxSemanticContinuations: 3, NoProgressLimit: 64})
		in := algInput(terminaldecision.CandidateCauseNormal)
		in.Candidate.OutputCommitted = true
		in.Policy.MaxContinuationAttempts = 3
		in.Auxiliary = &providerSemanticCollector{responses: []string{
			`{"kind":"INCOMPLETE","objective":"resume the existing test"}`,
			`{"kind":"INCOMPLETE","objective":"resume the existing test"}`,
			`{"kind":"INCOMPLETE","objective":"resume the existing test"}`,
		}}

		first, err := provider.Decide(context.Background(), in)
		if err != nil || first.Kind != terminaldecision.DecisionContinue || first.Continue == nil {
			t.Fatalf("first decision = %#v, err=%v; want continue", first, err)
		}
		in.Continuation.Attempt = 2
		in.Evidence.Lineage.Attempt = 2
		in.Evidence.Lineage.ProgressRef = first.Continue.ControlRef
		second, err := provider.Decide(context.Background(), in)
		if err != nil || second.Kind != terminaldecision.DecisionContinue || second.Continue == nil {
			t.Fatalf("second decision = %#v, err=%v; want continue", second, err)
		}
		in.Continuation.Attempt = 3
		in.Evidence.Lineage.Attempt = 3
		in.Evidence.Lineage.ProgressRef = second.Continue.ControlRef
		third, err := provider.Decide(context.Background(), in)
		if err != nil {
			t.Fatalf("third Decide() error = %v", err)
		}
		if third.Kind != terminaldecision.DecisionAllowStop || third.ReasonCode != progress.ReasonBudgetExhausted {
			t.Fatalf("third decision = %#v, want budget allow-stop", third)
		}
		assertProviderDecisionPrivacy(t, third, "acceptance-secret-do-not-leak")
	})
}

func assertProviderDecisionPrivacy(t *testing.T, decision terminaldecision.Decision, forbidden ...string) {
	t.Helper()
	if len(decision.ReasonCode) > terminaldecision.MaxReasonCodeBytes {
		t.Fatalf("reason code length = %d, want <= %d", len(decision.ReasonCode), terminaldecision.MaxReasonCodeBytes)
	}
	metadata := decision.ReasonCode
	if decision.Continue != nil {
		if len(decision.Continue.ControlRef) > terminaldecision.MaxIdentifierBytes {
			t.Fatalf("control ref length = %d, want <= %d", len(decision.Continue.ControlRef), terminaldecision.MaxIdentifierBytes)
		}
		metadata += "\x00" + decision.Continue.ControlRef + "\x00" + decision.Continue.Provenance
	}
	for _, value := range forbidden {
		if strings.Contains(metadata, value) {
			t.Fatalf("decision metadata contains forbidden content %q: %q", value, metadata)
		}
	}
}
