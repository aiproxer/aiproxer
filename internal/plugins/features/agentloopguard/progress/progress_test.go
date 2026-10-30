package progress

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

func TestFingerprintIgnoresVolatileIdentityAndAttemptFields(t *testing.T) {
	t.Parallel()
	base := progressInput()
	want := Fingerprint(base, VerdictIncomplete)

	mutated := base
	mutated.Candidate.Reference = "candidate-2"
	mutated.Request.RequestID = "request-2"
	mutated.Request.TraceID = "trace-2"
	mutated.Request.ALegID = "a-leg-2"
	mutated.Request.BLegID = "b-leg-2"
	mutated.Policy.Revision = "policy-2"
	mutated.Continuation.TrajectoryRef = "trajectory-2"
	mutated.Continuation.Attempt = 7
	mutated.Evidence.Lineage = terminaldecision.EvidenceLineage{
		TrajectoryRef: "trajectory-3",
		ParentRef:     "parent-2",
		ProgressRef:   "progress-2",
		Attempt:       8,
	}
	mutated.Deadline = time.Now().Add(10 * time.Hour)
	mutated.Evidence.Actions[0].ItemID = "item-2"
	mutated.Evidence.Actions[0].CallID = "call-2"

	if got := Fingerprint(mutated, VerdictIncomplete); got != want {
		t.Fatalf("volatile mutation changed fingerprint: got %q want %q", got, want)
	}
}

func TestFingerprintChangesMaterialOutputActionObjectiveAndVerdict(t *testing.T) {
	t.Parallel()
	base := progressInput()
	want := Fingerprint(base, VerdictIncomplete)
	cases := map[string]func(*terminaldecision.Input){
		"candidate output": func(in *terminaldecision.Input) {
			in.Evidence.CandidateText = "materially different output"
		},
		"recent evidence": func(in *terminaldecision.Input) {
			in.Evidence.RecentText = "materially different instruction"
		},
		"objective": func(in *terminaldecision.Input) {
			in.Evidence.Objective = "different existing objective"
		},
		"action kind": func(in *terminaldecision.Input) {
			in.Evidence.Actions[0].Kind = aipapi.ItemKindToolCall
			in.Evidence.Actions[0].CallID = "call-1"
			in.Evidence.Actions[0].Name = "run_tests"
		},
		"action status": func(in *terminaldecision.Input) {
			in.Evidence.Actions[0].Status = aipapi.ItemStatusCompleted
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := base
			mutate(&in)
			if got := Fingerprint(in, VerdictIncomplete); got == want {
				t.Fatal("material mutation did not change fingerprint")
			}
		})
	}
	if got := Fingerprint(base, VerdictComplete); got == want {
		t.Fatal("verdict mutation did not change fingerprint")
	}
}

func TestEvaluateEquivalentProgressTripsNoProgressWithoutResettingBudget(t *testing.T) {
	t.Parallel()
	input := progressInput()
	input.Policy.MaxContinuationAttempts = 4
	cfg := Config{NoProgressLimit: 2}

	first, err := Evaluate(input, VerdictIncomplete, State{}, cfg)
	if err != nil {
		t.Fatalf("first Evaluate() error = %v", err)
	}
	if first.Action != ActionContinue || !first.NewProgress || first.ConsecutiveNoProgress != 0 {
		t.Fatalf("first evaluation = %#v, want initial continuation progress", first)
	}

	second, err := Evaluate(input, VerdictIncomplete, first.State, cfg)
	if err != nil {
		t.Fatalf("second Evaluate() error = %v", err)
	}
	if second.Action != ActionContinue || second.NewProgress || second.ConsecutiveNoProgress != 1 || second.BudgetExhausted {
		t.Fatalf("second evaluation = %#v, want one equivalent observation", second)
	}

	third, err := Evaluate(input, VerdictIncomplete, second.State, cfg)
	if err != nil {
		t.Fatalf("third Evaluate() error = %v", err)
	}
	if third.Action != ActionAllowStop || !third.NoProgressTripped || third.ConsecutiveNoProgress != 2 {
		t.Fatalf("third evaluation = %#v, want no-progress stop", third)
	}
	if third.Decision.Kind != terminaldecision.DecisionAllowStop || third.Decision.ReasonCode != ReasonNoProgress {
		t.Fatalf("third decision = %#v, want bounded no-progress allow-stop", third.Decision)
	}
}

func TestEvaluateNewProgressResetsOnlyConsecutiveCounter(t *testing.T) {
	t.Parallel()
	input := progressInput()
	input.Policy.MaxContinuationAttempts = 4
	cfg := Config{NoProgressLimit: 2}

	first, err := Evaluate(input, VerdictIncomplete, State{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Evaluate(input, VerdictIncomplete, first.State, cfg)
	if err != nil {
		t.Fatal(err)
	}
	progressed := input
	progressed.Evidence.CandidateText = "new canonical output"
	third, err := Evaluate(progressed, VerdictIncomplete, second.State, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !third.NewProgress || third.ConsecutiveNoProgress != 0 || third.TotalAttempts != 3 || third.BudgetExhausted {
		t.Fatalf("new progress evaluation = %#v, want reset counter and retained total", third)
	}

	fourth, err := Evaluate(progressed, VerdictIncomplete, third.State, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Action != ActionAllowStop || !fourth.BudgetExhausted || fourth.TotalAttempts != 4 {
		t.Fatalf("budget evaluation = %#v, want terminal budget exhaustion", fourth)
	}
}

func TestEvaluateStopsForCompleteUserDependentAndUncertainVerdicts(t *testing.T) {
	t.Parallel()
	for _, verdict := range []Verdict{VerdictComplete, VerdictNeedsUser, VerdictUncertain} {
		t.Run(string(verdict), func(t *testing.T) {
			t.Parallel()
			result, err := Evaluate(progressInput(), verdict, State{}, Config{})
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if result.Action != ActionAllowStop || result.Decision.Kind != terminaldecision.DecisionAllowStop {
				t.Fatalf("result = %#v, want allow-stop", result)
			}
			if result.Decision.Continue != nil {
				t.Fatal("stop decision must not carry continuation intent")
			}
		})
	}
}

func TestBuildIntentIsBoundedAndExplicitlyInternal(t *testing.T) {
	t.Parallel()
	in := progressInput()
	in.Evidence.Objective = strings.Repeat("objective ", 300)
	intent, ok := BuildIntent(in, VerdictIncomplete, strings.Repeat("reason ", 300))
	if !ok {
		t.Fatal("BuildIntent() did not build actionable intent")
	}
	if err := intent.Validate(); err != nil {
		t.Fatalf("intent validation error = %v", err)
	}
	if len(intent.Instruction) > terminaldecision.MaxInstructionBytes {
		t.Fatalf("instruction bytes = %d, want <= %d", len(intent.Instruction), terminaldecision.MaxInstructionBytes)
	}
	lower := strings.ToLower(intent.Instruction)
	for _, phrase := range []string{
		"internal recovery",
		"not a new user request",
		"approval",
		"permission",
		"scope",
		"resume exactly that work",
		"last safe point",
		"end normally",
		"do not assume",
	} {
		if !strings.Contains(lower, phrase) {
			t.Fatalf("instruction missing %q: %s", phrase, intent.Instruction)
		}
	}
	if strings.Contains(intent.Instruction, strings.Repeat("objective ", 300)) || strings.Contains(intent.Instruction, strings.Repeat("reason ", 300)) {
		t.Fatal("instruction retained unbounded reason or objective")
	}
}

func TestBuildIntentStopsWhenNoSafePointOrUserDependent(t *testing.T) {
	t.Parallel()
	noTrajectory := progressInput()
	noTrajectory.Evidence.Lineage.TrajectoryRef = ""
	noTrajectory.Continuation.TrajectoryRef = ""
	if _, ok := BuildIntent(noTrajectory, VerdictIncomplete, "unfinished"); ok {
		t.Fatal("intent built without a canonical safe trajectory")
	}
	if _, ok := BuildIntent(progressInput(), VerdictNeedsUser, "needs user"); ok {
		t.Fatal("intent built for user-dependent work")
	}
	if _, ok := BuildIntent(progressInput(), VerdictComplete, "complete"); ok {
		t.Fatal("intent built for completed work")
	}
}

func TestEvaluateInvalidInputFailsClosed(t *testing.T) {
	t.Parallel()
	in := progressInput()
	in.Request.RequestID = ""
	result, err := Evaluate(in, VerdictIncomplete, State{}, Config{})
	if err == nil || !errors.Is(err, terminaldecision.ErrInvalidInput) {
		t.Fatalf("Evaluate() error = %v, want invalid input", err)
	}
	if result.Action != ActionAllowStop || result.Decision.Kind != terminaldecision.DecisionAllowStop {
		t.Fatalf("invalid input result = %#v, want allow-stop", result)
	}
}

func progressInput() terminaldecision.Input {
	return terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{
			Cause:           terminaldecision.CandidateCauseNormal,
			Reference:       "candidate-1",
			OutputCommitted: true,
		},
		Request: terminaldecision.RequestIdentity{
			RequestID: "request-1",
			TraceID:   "trace-1",
			ALegID:    "a-leg-1",
			BLegID:    "b-leg-1",
		},
		Policy: terminaldecision.PolicySnapshot{
			Revision:                "policy-1",
			MaxContinuationAttempts: 4,
		},
		Continuation: terminaldecision.ContinuationEvidence{
			TrajectoryRef: "trajectory-1",
			Attempt:       1,
		},
		Evidence: terminaldecision.Evidence{
			Objective:     "finish the requested change",
			RecentText:    "run the focused tests",
			CandidateText: "the implementation is ready",
			Actions: [terminaldecision.MaxEvidenceActions]terminaldecision.ActionFact{{
				ItemID: "item-1",
				Kind:   aipapi.ItemKindMessage,
				Status: aipapi.ItemStatusInProgress,
			}},
			ActionCount: 1,
			Lineage: terminaldecision.EvidenceLineage{
				TrajectoryRef: "trajectory-1",
				ParentRef:     "parent-1",
				ProgressRef:   "progress-1",
				Attempt:       1,
			},
		},
		Deadline: time.Date(2035, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}
