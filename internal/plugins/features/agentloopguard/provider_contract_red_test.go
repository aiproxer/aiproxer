package agentloopguard

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// RED contract: the concrete feature exposes only the generic terminal
// decision provider. The constructor is intentionally the smallest public
// feature seam; configuration and registration remain platform composition.
func TestProviderReturnsOnlyBoundedAllowStopOrContinue(t *testing.T) {
	t.Parallel()

	provider := NewProvider()
	if _, err := terminaldecision.ProviderIdentity(provider); err != nil {
		t.Fatalf("provider identity is not bounded: %v", err)
	}

	cases := []terminaldecision.CandidateCause{
		terminaldecision.CandidateCauseNormal,
		terminaldecision.CandidateCauseTransport,
		terminaldecision.CandidateCauseLimit,
		terminaldecision.CandidateCauseProviderError,
		terminaldecision.CandidateCauseCancellation,
	}
	for _, cause := range cases {
		t.Run(string(cause), func(t *testing.T) {
			t.Parallel()
			decision, err := provider.Decide(context.Background(), algInput(cause))
			if err != nil {
				// Provider errors are a valid boundary outcome. The platform owns
				// their normalization to allow-stop; they never authorize work.
				return
			}
			if decision.Kind != terminaldecision.DecisionAllowStop && decision.Kind != terminaldecision.DecisionContinue {
				t.Fatalf("Decide(%q) kind = %q, want allow_stop or continue", cause, decision.Kind)
			}
			if err := decision.Validate(); err != nil {
				t.Fatalf("Decide(%q) returned invalid decision: %v", cause, err)
			}
		})
	}
}

// The provider boundary has no terminal, backend, snapshot, call, or steering
// authority. Keep this check at the concrete feature boundary; no source
// scanner or platform lifecycle test is needed for the provider contract.
func TestProviderSurfaceExposesNoPlatformAuthority(t *testing.T) {
	t.Parallel()

	provider := NewProvider()
	if provider == nil {
		t.Fatal("NewProvider returned nil")
	}
	typ := reflect.TypeOf(provider)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.NumMethod() != 2 {
		t.Fatalf("provider exposes %d public methods, want only ID and Decide", typ.NumMethod())
	}
	for _, method := range []string{
		"ClaimTerminal",
		"OpenBackend",
		"MutateSnapshot",
		"AppendCall",
		"AppendMessage",
		"AppendItem",
	} {
		if _, ok := typ.MethodByName(method); ok {
			t.Fatalf("provider exposes forbidden platform authority %s", method)
		}
	}
}

func TestProviderActionableEvidenceReturnsBoundedContinuation(t *testing.T) {
	t.Parallel()
	provider := NewProvider()
	in := algInput(terminaldecision.CandidateCauseNormal)
	in.Candidate.OutputCommitted = true
	in.Auxiliary = &providerSemanticCollector{responses: []string{`{"kind":"INCOMPLETE","objective":"resume tests"}`}}

	decision, err := provider.Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if decision.Kind != terminaldecision.DecisionContinue {
		t.Fatalf("actionable evidence kind = %q, want continue", decision.Kind)
	}
	if err := decision.Validate(); err != nil {
		t.Fatalf("actionable decision invalid: %v", err)
	}
	if decision.Continue.TrajectoryRef != in.Evidence.Lineage.TrajectoryRef {
		t.Fatalf("trajectory ref = %q, want %q", decision.Continue.TrajectoryRef, in.Evidence.Lineage.TrajectoryRef)
	}
	if decision.Continue.Instruction == in.Evidence.Objective || decision.Continue.Instruction == in.Evidence.CandidateText {
		t.Fatal("continuation instruction copied unbounded evidence text")
	}
}

func TestProviderConservativelyAllowsStopForUnsafeOrInsufficientEvidence(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*terminaldecision.Input){
		"authoritative cancellation": func(in *terminaldecision.Input) {
			in.Candidate.Cause = terminaldecision.CandidateCauseCancellation
			in.Candidate.OutputCommitted = true
		},
		"explicit completion": func(in *terminaldecision.Input) {
			in.Candidate.OutputCommitted = true
			in.Evidence.ExplicitCompletion = true
		},
		"no partial action": func(in *terminaldecision.Input) {
			in.Candidate.OutputCommitted = true
			in.Evidence.Actions[1] = terminaldecision.ActionFact{}
			in.Evidence.ActionCount = 1
		},
		"pre-commit candidate": func(in *terminaldecision.Input) {
			in.Candidate.OutputCommitted = false
		},
		"missing objective": func(in *terminaldecision.Input) {
			in.Candidate.OutputCommitted = true
			in.Evidence.Objective = ""
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			provider := NewProvider()
			in := algInput(terminaldecision.CandidateCauseNormal)
			mutate(&in)
			decision, err := provider.Decide(context.Background(), in)
			if err != nil {
				t.Fatalf("Decide() error = %v", err)
			}
			if decision.Kind != terminaldecision.DecisionAllowStop || decision.Continue != nil {
				t.Fatalf("unsafe/insufficient evidence decision = %#v, want allow-stop without intent", decision)
			}
			if err := decision.Validate(); err != nil {
				t.Fatalf("allow-stop decision invalid: %v", err)
			}
		})
	}
}

func TestProviderCanceledContextAllowsStop(t *testing.T) {
	t.Parallel()
	provider := NewProvider()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	decision, err := provider.Decide(ctx, algInput(terminaldecision.CandidateCauseNormal))
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if decision.Kind != terminaldecision.DecisionAllowStop || decision.Continue != nil {
		t.Fatalf("canceled context decision = %#v, want allow-stop without intent", decision)
	}
}

// Contract ratchet: ALG consumes the platform-owned bounded evidence projection
// through terminaldecision.Input. This compile-time reference prevents a
// feature-owned replacement input; bounds and value-copy guarantees belong to
// the terminaldecision package.
func TestCanonicalInputCarriesBoundedALGEvidenceProjection(t *testing.T) {
	t.Parallel()

	// This direct field reference is intentional: it must remain a
	// terminaldecision-owned value, not an agentloopguard-owned facade.
	var in terminaldecision.Input
	_ = in.Evidence
}

func algInput(cause terminaldecision.CandidateCause) terminaldecision.Input {
	return terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{
			Cause:     cause,
			Reference: "candidate-1",
		},
		Request: terminaldecision.RequestIdentity{
			RequestID: "request-1",
			TraceID:   "trace-1",
			ALegID:    "a-leg-1",
			BLegID:    "b-leg-1",
		},
		Policy: terminaldecision.PolicySnapshot{
			Revision:                "policy-1",
			MaxContinuationAttempts: 2,
		},
		Continuation: terminaldecision.ContinuationEvidence{
			TrajectoryRef: "trajectory-1",
			Attempt:       1,
		},
		Evidence: terminaldecision.Evidence{
			Objective:     "finish the requested change",
			RecentText:    "run the focused tests",
			CandidateText: "the implementation is ready",
			Actions: [terminaldecision.MaxEvidenceActions]terminaldecision.ActionFact{
				{
					ItemID: "item-1",
					CallID: "call-1",
					Kind:   aipapi.ItemKindToolResult,
					Status: aipapi.ItemStatusCompleted,
					Name:   "go_test",
				},
				{
					ItemID: "item-2",
					Kind:   aipapi.ItemKindMessage,
					Status: aipapi.ItemStatusInProgress,
				},
			},
			ActionCount: 2,
			Lineage: terminaldecision.EvidenceLineage{
				TrajectoryRef: "trajectory-1",
				ParentRef:     "trajectory-0",
				ProgressRef:   "progress-1",
				Attempt:       1,
			},
		},
		Deadline: time.Date(2035, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}
