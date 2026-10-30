package terminaldecision

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

func TestInputValidateAcceptsBoundedCanonicalValue(t *testing.T) {
	t.Parallel()
	in := validInput()
	if err := in.Validate(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}

	copy := in
	copy.Request.RequestID = "changed"
	copy.Policy.Revision = "changed"
	if in.Request.RequestID == copy.Request.RequestID || in.Policy.Revision == copy.Policy.Revision {
		t.Fatal("input copy shares mutable request or policy state")
	}
}

func TestInputValidateRejectsMissingRequiredCanonicalFields(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Input){
		"candidate cause":  func(in *Input) { in.Candidate.Cause = CandidateCause("") },
		"request identity": func(in *Input) { in.Request.RequestID = "" },
		"policy revision":  func(in *Input) { in.Policy.Revision = "" },
		"deadline":         func(in *Input) { in.Deadline = time.Time{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := validInput()
			mutate(&in)
			if err := in.Validate(); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Validate() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestCandidateCauseContract(t *testing.T) {
	t.Parallel()
	causes := []struct {
		cause         CandidateCause
		authoritative bool
	}{
		{CandidateCauseNormal, false},
		{CandidateCauseTransport, false},
		{CandidateCauseLimit, false},
		{CandidateCauseProviderError, false},
		{CandidateCauseRefusal, true},
		{CandidateCauseContentFilter, true},
		{CandidateCauseCancellation, true},
		{CandidateCauseAuthorityDenied, true},
	}
	for _, tc := range causes {
		if !tc.cause.IsKnown() {
			t.Errorf("cause %q is not known", tc.cause)
		}
		if got := tc.cause.Authoritative(); got != tc.authoritative {
			t.Errorf("cause %q authoritative = %v, want %v", tc.cause, got, tc.authoritative)
		}
	}
}

func TestInputValidateRejectsUnknownCandidateCauseWithoutRawText(t *testing.T) {
	t.Parallel()
	rawCause := strings.Repeat("attacker-cause-", 1024)
	in := validInput()
	in.Candidate.Cause = CandidateCause(rawCause)
	err := in.Validate()
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown cause error = %v, want ErrInvalidInput", err)
	}
	if strings.Contains(err.Error(), rawCause) {
		t.Fatal("unknown cause error contains attacker-controlled cause text")
	}
	if len(err.Error()) > 256 {
		t.Fatalf("unknown cause error length = %d, want at most 256 bytes", len(err.Error()))
	}
}

func TestInputValidateEnforcesIdentifierBounds(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Input, string){
		"candidate reference":  func(in *Input, value string) { in.Candidate.Reference = value },
		"request id":           func(in *Input, value string) { in.Request.RequestID = value },
		"trace id":             func(in *Input, value string) { in.Request.TraceID = value },
		"a leg id":             func(in *Input, value string) { in.Request.ALegID = value },
		"policy revision":      func(in *Input, value string) { in.Policy.Revision = value },
		"trajectory reference": func(in *Input, value string) { in.Continuation.TrajectoryRef = value },
	}
	for name, set := range cases {
		t.Run(name+" at maximum", func(t *testing.T) {
			t.Parallel()
			in := validInput()
			set(&in, strings.Repeat("x", MaxIdentifierBytes))
			if err := in.Validate(); err != nil {
				t.Fatalf("maximum-sized field rejected: %v", err)
			}
		})
		t.Run(name+" over maximum", func(t *testing.T) {
			t.Parallel()
			in := validInput()
			set(&in, strings.Repeat("x", MaxIdentifierBytes+1))
			if err := in.Validate(); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("over-bound field error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestInputValidateAcceptsBoundedEvidenceProjection(t *testing.T) {
	t.Parallel()
	in := validInput()
	in.Evidence = Evidence{
		Objective:     "finish the requested change",
		RecentText:    "run the focused tests",
		CandidateText: "the implementation is ready",
		Actions: [MaxEvidenceActions]ActionFact{
			{
				ItemID: "item-1",
				CallID: "call-1",
				Kind:   aipapi.ItemKindToolResult,
				Status: aipapi.ItemStatusCompleted,
				Name:   "go_test",
			},
			{
				ItemID: "item-2",
				CallID: "call-2",
				Kind:   aipapi.ItemKindToolCall,
				Status: aipapi.ItemStatusInProgress,
				Name:   "read_file",
			},
		},
		ActionCount:        2,
		ExplicitCompletion: true,
		Lineage: EvidenceLineage{
			TrajectoryRef: "trajectory-1",
			ParentRef:     "trajectory-0",
			ProgressRef:   "progress-1",
			Attempt:       1,
		},
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("bounded evidence rejected: %v", err)
	}

	copy := in
	copy.Evidence.Actions[0].ItemID = "changed"
	copy.Evidence.Lineage.ProgressRef = "changed"
	if in.Evidence.Actions[0].ItemID == copy.Evidence.Actions[0].ItemID {
		t.Fatal("input copy shares evidence action state")
	}
	if in.Evidence.Lineage.ProgressRef == copy.Evidence.Lineage.ProgressRef {
		t.Fatal("input copy shares evidence lineage state")
	}
}

// TestInputValidateAcceptsIndependentCompletionFlags is the additive contract
// fixture for the completion expectation/observation pair (design Completion
// Evidence and Pending Result; requirements 3.2-3.4, 6.1-6.2, 7.1, 7.6, 9.3).
//
// The two flags are separate facts, not a relation: an expected signal may be
// absent, and a native observation needs no proxy expectation. The validator
// therefore accepts all four combinations, the zero value stays the correct
// projection for a response that never had a proxy-owned control protocol, and
// both flags copy by value with the rest of Input.
func TestInputValidateAcceptsIndependentCompletionFlags(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		observed bool
		expected bool
	}{
		{name: "neither observed nor expected"},
		{name: "observed without expectation", observed: true},
		{name: "expected without observation", expected: true},
		{name: "observed while expected", observed: true, expected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			in := validInput()
			in.Evidence.ExplicitCompletion = tc.observed
			in.Evidence.ExplicitCompletionExpected = tc.expected
			if err := in.Validate(); err != nil {
				t.Fatalf("observed=%t expected=%t rejected: %v", tc.observed, tc.expected, err)
			}

			copied := in
			if copied.Evidence.ExplicitCompletion != tc.observed || copied.Evidence.ExplicitCompletionExpected != tc.expected {
				t.Fatalf("value copy changed the completion flags: observed=%t expected=%t",
					copied.Evidence.ExplicitCompletion, copied.Evidence.ExplicitCompletionExpected)
			}

			// Flipping both flags on the copy must not reach the original, which
			// is what proves the flags are value data rather than shared state.
			copied.Evidence.ExplicitCompletion = !tc.observed
			copied.Evidence.ExplicitCompletionExpected = !tc.expected
			if in.Evidence.ExplicitCompletion != tc.observed || in.Evidence.ExplicitCompletionExpected != tc.expected {
				t.Fatal("input copy shares completion flag state")
			}
		})
	}
}

// TestEvidenceZeroValueKeepsLegacyProjection pins backward compatibility: an
// evidence value that predates the expectation flag, and a provider that never
// sets it, remain a valid bounded canonical projection.
func TestEvidenceZeroValueKeepsLegacyProjection(t *testing.T) {
	t.Parallel()

	in := validInput()
	in.Evidence = Evidence{
		Objective:     "finish the requested change",
		RecentText:    "run the focused tests",
		CandidateText: "the implementation is ready",
	}
	if in.Evidence.ExplicitCompletion || in.Evidence.ExplicitCompletionExpected {
		t.Fatal("the zero evidence value must project neither an observation nor an expectation")
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("zero-value evidence rejected: %v", err)
	}
}

func TestInputValidateRejectsUnboundedOrMalformedEvidence(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Input){
		"objective": func(in *Input) {
			in.Evidence.Objective = strings.Repeat("o", MaxEvidenceTextBytes+1)
		},
		"recent text": func(in *Input) {
			in.Evidence.RecentText = strings.Repeat("r", MaxEvidenceTextBytes+1)
		},
		"candidate text": func(in *Input) {
			in.Evidence.CandidateText = strings.Repeat("c", MaxEvidenceTextBytes+1)
		},
		"action count": func(in *Input) {
			in.Evidence.ActionCount = MaxEvidenceActions + 1
		},
		"action item id": func(in *Input) {
			in.Evidence.ActionCount = 1
			in.Evidence.Actions[0] = ActionFact{ItemID: strings.Repeat("i", MaxIdentifierBytes+1), Kind: aipapi.ItemKindMessage, Status: aipapi.ItemStatusCompleted}
		},
		"unknown action kind": func(in *Input) {
			in.Evidence.ActionCount = 1
			in.Evidence.Actions[0] = ActionFact{ItemID: "item-1", Kind: aipapi.ItemKind("unknown"), Status: aipapi.ItemStatusCompleted}
		},
		"unknown action status": func(in *Input) {
			in.Evidence.ActionCount = 1
			in.Evidence.Actions[0] = ActionFact{ItemID: "item-1", Kind: aipapi.ItemKindMessage, Status: aipapi.ItemStatus("unknown")}
		},
		"tool action call id": func(in *Input) {
			in.Evidence.ActionCount = 1
			in.Evidence.Actions[0] = ActionFact{ItemID: "item-1", Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusInProgress}
		},
		"tool action name": func(in *Input) {
			in.Evidence.ActionCount = 1
			in.Evidence.Actions[0] = ActionFact{ItemID: "item-1", CallID: "call-1", Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusInProgress}
		},
		"lineage reference": func(in *Input) {
			in.Evidence.Lineage.ProgressRef = strings.Repeat("p", MaxIdentifierBytes+1)
		},
		"non-zero trailing action": func(in *Input) {
			in.Evidence.Actions[MaxEvidenceActions-1] = ActionFact{ItemID: "unexpected", Kind: aipapi.ItemKindMessage, Status: aipapi.ItemStatusCompleted}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := validInput()
			mutate(&in)
			if err := in.Validate(); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Validate() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestProviderIdentityIsStableAndBounded(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", "   ", strings.Repeat("p", MaxProviderIDBytes+1)} {
		if err := ValidateProviderID(id); !errors.Is(err, ErrInvalidProvider) {
			t.Fatalf("ValidateProviderID(%q) error = %v, want ErrInvalidProvider", id, err)
		}
	}
	if err := ValidateProviderID(strings.Repeat("p", MaxProviderIDBytes)); err != nil {
		t.Fatalf("maximum-sized provider identity rejected: %v", err)
	}
	if err := ValidateProviderID("provider.example"); err != nil {
		t.Fatalf("bounded provider identity rejected: %v", err)
	}
}

func TestDecisionValidateAcceptsEachDecisionKind(t *testing.T) {
	t.Parallel()
	cases := []Decision{
		{Kind: DecisionAllowStop, ReasonCode: "complete"},
		{Kind: DecisionContinue, ReasonCode: "unfinished", Continue: &ContinuationIntent{
			TrajectoryRef: "trajectory-1",
			ControlRef:    "control-1",
			Instruction:   "continue the existing objective",
			Provenance:    "internal-control",
			ReasonCode:    "unfinished",
		}},
		{Kind: DecisionSurfaceFailure, ReasonCode: "bounded_failure"},
	}
	for _, decision := range cases {
		if err := decision.Validate(); err != nil {
			t.Fatalf("decision kind %q rejected: %v", decision.Kind, err)
		}
	}
}

func TestDecisionValidateRejectsUnknownAndEmptyKinds(t *testing.T) {
	t.Parallel()
	for _, kind := range []DecisionKind{"", "unknown", "continue-ish"} {
		decision := Decision{Kind: kind, ReasonCode: "reason"}
		if err := decision.Validate(); !errors.Is(err, ErrInvalidDecision) {
			t.Fatalf("decision kind %q error = %v, want ErrInvalidDecision", kind, err)
		}
	}
}

func TestDecisionValidateBoundsUnknownKindError(t *testing.T) {
	t.Parallel()
	attackerText := strings.Repeat("unknown-kind-payload-", 1024)
	decision := Decision{Kind: DecisionKind(attackerText), ReasonCode: "reason"}
	err := decision.Validate()
	if !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("unknown decision error = %v, want ErrInvalidDecision", err)
	}
	if strings.Contains(err.Error(), attackerText) {
		t.Fatal("unknown decision error contains attacker-controlled kind text")
	}
	if len(err.Error()) > 256 {
		t.Fatalf("unknown decision error length = %d, want at most 256 bytes", len(err.Error()))
	}
}

func TestDecisionValidateRejectsInvalidContinuationCombinations(t *testing.T) {
	t.Parallel()
	validIntent := &ContinuationIntent{
		TrajectoryRef: "trajectory-1",
		Provenance:    "internal-control",
		ReasonCode:    "unfinished",
	}
	cases := []Decision{
		{Kind: DecisionAllowStop, ReasonCode: "complete", Continue: validIntent},
		{Kind: DecisionSurfaceFailure, ReasonCode: "failure", Continue: validIntent},
		{Kind: DecisionContinue, ReasonCode: "unfinished"},
		{Kind: DecisionContinue, ReasonCode: "unfinished", Continue: &ContinuationIntent{}},
	}
	for _, decision := range cases {
		if err := decision.Validate(); !errors.Is(err, ErrInvalidDecision) {
			t.Fatalf("invalid decision %+v error = %v, want ErrInvalidDecision", decision, err)
		}
	}
}

func TestDecisionValidateEnforcesReasonAndIntentBounds(t *testing.T) {
	t.Parallel()
	decision := Decision{Kind: DecisionAllowStop, ReasonCode: strings.Repeat("r", MaxReasonCodeBytes)}
	if err := decision.Validate(); err != nil {
		t.Fatalf("maximum reason code rejected: %v", err)
	}
	decision.ReasonCode += "r"
	if err := decision.Validate(); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("over-bound reason code error = %v, want ErrInvalidDecision", err)
	}

	decision = Decision{Kind: DecisionContinue, ReasonCode: "unfinished", Continue: &ContinuationIntent{
		TrajectoryRef: "trajectory-1",
		Provenance:    "internal-control",
		ReasonCode:    "unfinished",
		Instruction:   strings.Repeat("i", MaxInstructionBytes),
	}}
	if err := decision.Validate(); err != nil {
		t.Fatalf("maximum instruction rejected: %v", err)
	}
	decision.Continue.Instruction += "i"
	if err := decision.Validate(); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("over-bound instruction error = %v, want ErrInvalidDecision", err)
	}

	for name, set := range map[string]func(*ContinuationIntent, string){
		"trajectory reference": func(intent *ContinuationIntent, value string) { intent.TrajectoryRef = value },
		"control reference":    func(intent *ContinuationIntent, value string) { intent.ControlRef = value },
	} {
		t.Run(name+" at maximum", func(t *testing.T) {
			t.Parallel()
			intent := ContinuationIntent{
				TrajectoryRef: "trajectory-1",
				ControlRef:    "control-1",
				Provenance:    "internal-control",
				ReasonCode:    "unfinished",
			}
			set(&intent, strings.Repeat("x", MaxIdentifierBytes))
			if err := intent.Validate(); err != nil {
				t.Fatalf("maximum-sized field rejected: %v", err)
			}
		})
		t.Run(name+" over maximum", func(t *testing.T) {
			t.Parallel()
			intent := ContinuationIntent{
				TrajectoryRef: "trajectory-1",
				ControlRef:    "control-1",
				Provenance:    "internal-control",
				ReasonCode:    "unfinished",
			}
			set(&intent, strings.Repeat("x", MaxIdentifierBytes+1))
			if err := intent.Validate(); !errors.Is(err, ErrInvalidDecision) {
				t.Fatalf("over-bound field error = %v, want ErrInvalidDecision", err)
			}
		})
	}
}

func TestProviderSurfaceIsNarrowAndValueBounded(t *testing.T) {
	t.Parallel()
	providerType := reflect.TypeFor[Provider]()
	if providerType.NumMethod() != 2 {
		t.Fatalf("Provider exposes %d methods, want exactly ID and Decide", providerType.NumMethod())
	}
	for _, methodName := range []string{"ID", "Decide"} {
		if _, ok := providerType.MethodByName(methodName); !ok {
			t.Fatalf("Provider missing %s method", methodName)
		}
	}
	if _, ok := providerType.MethodByName("ClaimTerminal"); ok {
		t.Fatal("Provider must not expose terminal-claim capability")
	}
	if _, ok := providerType.MethodByName("OpenBackend"); ok {
		t.Fatal("Provider must not expose backend-opening capability")
	}

	assertNoMutablePublicFields(t, reflect.TypeFor[Input](), "Input")
}

func TestProviderDecideUsesContextAndInputByValue(t *testing.T) {
	t.Parallel()
	var provider Provider = stubProvider{id: "provider.example"}
	ctx := t.Context()
	decision, err := provider.Decide(ctx, validInput())
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if err := decision.Validate(); err != nil {
		t.Fatalf("stub decision invalid: %v", err)
	}
}

func validInput() Input {
	return Input{
		Candidate: CanonicalTerminalCandidate{
			Cause:     CandidateCauseNormal,
			Reference: "candidate-1",
		},
		Request: RequestIdentity{
			RequestID: "request-1",
			TraceID:   "trace-1",
			ALegID:    "a-leg-1",
		},
		Policy: PolicySnapshot{
			Revision:                "policy-1",
			MaxContinuationAttempts: 2,
		},
		Continuation: ContinuationEvidence{
			TrajectoryRef: "trajectory-1",
			Attempt:       1,
		},
		Deadline: time.Now().Add(time.Minute),
	}
}

type stubProvider struct{ id string }

func (p stubProvider) ID() string { return p.id }

func (p stubProvider) Decide(context.Context, Input) (Decision, error) {
	return Decision{Kind: DecisionAllowStop, ReasonCode: "complete"}, nil
}

func assertNoMutablePublicFields(t *testing.T, typ reflect.Type, path string) {
	t.Helper()
	if typ.Kind() != reflect.Struct {
		return
	}
	for field := range typ.Fields() {
		if field.PkgPath != "" { // unexported implementation details are not SDK state.
			continue
		}
		fieldPath := path + "." + field.Name
		if field.Name == "Auxiliary" && field.Type == reflect.TypeFor[auxiliary.Client]() {
			continue
		}
		switch field.Type.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface, reflect.Func, reflect.Chan:
			t.Fatalf("%s exposes mutable/reference field type %s", fieldPath, field.Type)
		case reflect.Struct:
			if field.Type != reflect.TypeFor[time.Time]() {
				assertNoMutablePublicFields(t, field.Type, fieldPath)
			}
		}
	}
}
