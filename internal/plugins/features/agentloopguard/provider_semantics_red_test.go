package agentloopguard

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/progress"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

type providerSemanticCollector struct {
	responses []string
	calls     int
}

func (c *providerSemanticCollector) Collect(context.Context, auxiliary.Request) (aipapi.Collected, error) {
	var out aipapi.Collected
	if c.calls < len(c.responses) {
		out.Text.WriteString(c.responses[c.calls])
	}
	c.calls++
	return out, nil
}

func (*providerSemanticCollector) Stream(context.Context, auxiliary.Request) (aipapi.EventStream, error) {
	return nil, nil
}

func TestProviderExplicitCompletionTrustSkipsVerifier(t *testing.T) {
	t.Parallel()

	collector := &providerSemanticCollector{responses: []string{`{"kind":"INCOMPLETE","objective":"resume tests"}`}}
	in := semanticProviderInput()
	in.Evidence.ExplicitCompletion = true
	in.Auxiliary = collector
	decision, err := NewProvider(Config{Enabled: true, ExplicitCompletionPolicy: ExplicitCompletionPolicyTrust}).Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Kind != terminaldecision.DecisionAllowStop || decision.ReasonCode != reasonExplicitComplete || collector.calls != 0 {
		t.Fatalf("decision=%+v calls=%d, want trusted explicit allow-stop without verifier", decision, collector.calls)
	}
}

func TestProviderNonExplicitCandidateUsesVerifier(t *testing.T) {
	t.Parallel()

	collector := &providerSemanticCollector{responses: []string{`{"kind":"INCOMPLETE","objective":"resume tests"}`}}
	in := semanticProviderInput()
	in.Auxiliary = collector
	decision, err := NewProvider(Config{Enabled: true, ExplicitCompletionPolicy: ExplicitCompletionPolicyTrust}).Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if collector.calls != 1 || decision.Kind != terminaldecision.DecisionContinue || decision.Continue == nil {
		t.Fatalf("decision=%+v calls=%d, want verifier-backed continuation", decision, collector.calls)
	}
}

func TestProviderExplicitCompletionVerifyUsesVerifierAndAllowsSafeIncomplete(t *testing.T) {
	t.Parallel()

	collector := &providerSemanticCollector{responses: []string{`{"kind":"INCOMPLETE","reason":"more work","objective":"resume tests"}`}}
	in := semanticProviderInput()
	in.Evidence.ExplicitCompletion = true
	in.Auxiliary = collector
	decision, err := NewProvider(Config{Enabled: true, ExplicitCompletionPolicy: ExplicitCompletionPolicyVerify}).Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if collector.calls != 1 || decision.Kind != terminaldecision.DecisionContinue || decision.Continue == nil {
		t.Fatalf("decision=%+v calls=%d, want verified continuation", decision, collector.calls)
	}
	if _, err := progress.DecodeState(decision.Continue.ControlRef); err != nil {
		t.Fatalf("continuation control ref is not progress state: %v", err)
	}
}

func TestProviderVerifierFailureAndUncertainFailClosed(t *testing.T) {
	t.Parallel()

	for name, aux := range map[string]auxiliary.Client{
		"missing":   nil,
		"malformed": &providerSemanticCollector{responses: []string{`not-json`}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := semanticProviderInput()
			in.Evidence.ExplicitCompletion = true
			in.Auxiliary = aux
			decision, err := NewProvider(Config{Enabled: true, ExplicitCompletionPolicy: ExplicitCompletionPolicyVerify}).Decide(context.Background(), in)
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if decision.Kind != terminaldecision.DecisionAllowStop || decision.Continue != nil {
				t.Fatalf("decision=%+v, want conservative allow-stop", decision)
			}
		})
	}
}

func TestProviderUsesSmallerPlatformSemanticCap(t *testing.T) {
	t.Parallel()

	in := semanticProviderInput()
	in.Policy.MaxContinuationAttempts = 2
	in.Continuation.Attempt = 2
	in.Evidence.Lineage.ProgressRef, _ = progress.EncodeState(progress.State{})
	in.Auxiliary = &providerSemanticCollector{responses: []string{
		`{"kind":"INCOMPLETE","objective":"resume tests"}`,
		`{"kind":"INCOMPLETE","objective":"resume tests"}`,
		`{"kind":"INCOMPLETE","objective":"resume tests"}`,
	}}
	decision, err := NewProvider(Config{Enabled: true, MaxSemanticContinuations: 5}).Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Kind != terminaldecision.DecisionAllowStop || decision.ReasonCode != progress.ReasonBudgetExhausted {
		t.Fatalf("decision=%+v, want platform-cap budget stop", decision)
	}
}

func TestProviderProgressStateSurvivesB1B2AndTripsNoProgress(t *testing.T) {
	t.Parallel()

	provider := NewProvider(Config{Enabled: true, MaxSemanticContinuations: 4, NoProgressLimit: 2})
	in := semanticProviderInput()
	in.Policy.MaxContinuationAttempts = 4
	in.Auxiliary = &providerSemanticCollector{responses: []string{
		`{"kind":"INCOMPLETE","objective":"resume tests"}`,
		`{"kind":"INCOMPLETE","objective":"resume tests"}`,
		`{"kind":"INCOMPLETE","objective":"resume tests"}`,
	}}

	first, err := provider.Decide(context.Background(), in)
	if err != nil || first.Kind != terminaldecision.DecisionContinue || first.Continue == nil {
		t.Fatalf("first decision=%+v err=%v, want continuation", first, err)
	}
	secondInput := in
	secondInput.Continuation.Attempt = 2
	secondInput.Evidence.Lineage.Attempt = 2
	secondInput.Evidence.Lineage.ProgressRef = first.Continue.ControlRef
	second, err := provider.Decide(context.Background(), secondInput)
	if err != nil || second.Kind != terminaldecision.DecisionContinue || second.Continue == nil {
		t.Fatalf("second decision=%+v err=%v, want repeated continuation", second, err)
	}
	thirdInput := secondInput
	thirdInput.Continuation.Attempt = 3
	thirdInput.Evidence.Lineage.Attempt = 3
	thirdInput.Evidence.Lineage.ProgressRef = second.Continue.ControlRef
	third, err := provider.Decide(context.Background(), thirdInput)
	if err != nil {
		t.Fatalf("third Decide: %v", err)
	}
	if third.Kind != terminaldecision.DecisionAllowStop || third.ReasonCode != progress.ReasonNoProgress {
		t.Fatalf("third decision=%+v, want no-progress stop", third)
	}
}

func TestProviderMalformedOrMissingContinuationStateStops(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"malformed", ""} {
		in := semanticProviderInput()
		in.Continuation.Attempt = 2
		in.Evidence.Lineage.Attempt = 2
		in.Evidence.Lineage.ProgressRef = state
		decision, err := NewProvider(Config{Enabled: true}).Decide(context.Background(), in)
		if err != nil {
			t.Fatalf("state %q Decide: %v", state, err)
		}
		if decision.Kind != terminaldecision.DecisionAllowStop || decision.Continue != nil {
			t.Fatalf("state %q decision=%+v, want conservative allow-stop", state, decision)
		}
	}
}

func semanticProviderInput() terminaldecision.Input {
	in := algInput(terminaldecision.CandidateCauseNormal)
	in.Candidate.OutputCommitted = true
	in.Evidence.Lineage.ProgressRef = ""
	in.Evidence.Lineage.Attempt = 1
	in.Policy.MaxContinuationAttempts = 4
	return in
}
