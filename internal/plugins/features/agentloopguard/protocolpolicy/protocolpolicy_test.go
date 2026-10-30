package protocolpolicy

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/protocolstate"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// approvedClauses are the exact approved semantic clauses. Every one of them
// must survive verbatim in the built instruction.
var approvedClauses = []string{
	"<automated-completion-protocol-repair>",
	"The previous model turn ended without the required `attempt_completion` signal.\n" +
		"This is proxy-internal recovery control, not a new user request, approval,\n" +
		"permission, or scope expansion.",
	"If all work requested by the user is complete, call `attempt_completion` now\n" +
		"with a concise final `result`.",
	"If concrete requested work remains and can proceed without new user input,\n" +
		"continue exactly that work from the retained safe point.",
	"If further progress requires user input, permission, credentials, clarification,\n" +
		"or a choice, request that input normally and end. Do not assume it.",
	"Do not invent, repeat, broaden, optimize, or discover work merely because this\n" +
		"recovery message was sent.",
	"</automated-completion-protocol-repair>",
}

func defaultLimits() protocolstate.Limits {
	return protocolstate.Limits{MaxReprompts: 1, NoProgressLimit: 2}
}

func stockPlatform() protocolstate.Platform {
	return protocolstate.Platform{Cap: 3, Attempt: 1}
}

func normalInput() terminaldecision.Input {
	return terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{
			Cause:           terminaldecision.CandidateCauseNormal,
			Reference:       "cand-1",
			OutputCommitted: true,
		},
		Request: terminaldecision.RequestIdentity{
			RequestID: "req-1",
			TraceID:   "trace-1",
			ALegID:    "a-1",
			BLegID:    "b-1",
		},
		Policy: terminaldecision.PolicySnapshot{
			Revision:                "rev-1",
			MaxContinuationAttempts: 3,
		},
		Continuation: terminaldecision.ContinuationEvidence{
			TrajectoryRef: "traj-1",
			Attempt:       1,
		},
		Evidence: terminaldecision.Evidence{
			Objective:                  "refactor the parser",
			RecentText:                 "recent canonical text",
			CandidateText:              "final candidate text",
			ExplicitCompletion:         false,
			ExplicitCompletionExpected: true,
			Lineage: terminaldecision.EvidenceLineage{
				TrajectoryRef: "traj-1",
				ProgressRef:   "b-1",
				Attempt:       1,
			},
		},
		Deadline: time.Now().Add(time.Minute),
	}
}

func toolAction(status aipapi.ItemStatus) terminaldecision.ActionFact {
	return terminaldecision.ActionFact{
		ItemID: "item-1",
		CallID: "call-1",
		Kind:   aipapi.ItemKindToolCall,
		Status: status,
		Name:   "read_file",
	}
}

func withActions(in terminaldecision.Input, actions ...terminaldecision.ActionFact) terminaldecision.Input {
	out := in
	out.Evidence.Actions = [terminaldecision.MaxEvidenceActions]terminaldecision.ActionFact{}
	copy(out.Evidence.Actions[:], actions)
	out.Evidence.ActionCount = uint8(len(actions))
	return out
}

func mustEvaluate(t *testing.T, in terminaldecision.Input, prior protocolstate.State, limits protocolstate.Limits, platform protocolstate.Platform) Result {
	t.Helper()
	result, err := evaluate(in, prior, limits, platform, protocolstate.Encode)
	if err != nil {
		t.Fatalf("Evaluate() unexpected error: %v", err)
	}
	return result
}

func mustContinue(t *testing.T, in terminaldecision.Input, prior protocolstate.State, limits protocolstate.Limits, platform protocolstate.Platform) Result {
	t.Helper()
	result := mustEvaluate(t, in, prior, limits, platform)
	if result.Decision.Kind != terminaldecision.DecisionContinue {
		t.Fatalf("expected continue decision, got kind=%q reason=%q", result.Decision.Kind, result.Decision.ReasonCode)
	}
	if result.Decision.Continue == nil {
		t.Fatal("continue decision carries nil intent")
	}
	if err := result.Decision.Validate(); err != nil {
		t.Fatalf("decision must be SDK-valid: %v", err)
	}
	if !result.Committed {
		t.Fatal("continue decision must commit a proposed state")
	}
	return result
}

func mustStop(t *testing.T, in terminaldecision.Input, prior protocolstate.State, limits protocolstate.Limits, platform protocolstate.Platform, wantReason string) Result {
	t.Helper()
	result := mustEvaluate(t, in, prior, limits, platform)
	if result.Decision.Kind != terminaldecision.DecisionAllowStop {
		t.Fatalf("expected allow_stop, got kind=%q reason=%q", result.Decision.Kind, result.Decision.ReasonCode)
	}
	if result.Decision.Continue != nil {
		t.Fatal("allow_stop must not carry continuation intent")
	}
	if wantReason != "" && result.Decision.ReasonCode != wantReason {
		t.Fatalf("expected reason %q, got %q", wantReason, result.Decision.ReasonCode)
	}
	if err := result.Decision.Validate(); err != nil {
		t.Fatalf("stop decision must be SDK-valid: %v", err)
	}
	if result.Committed {
		t.Fatal("allow_stop must not commit a state")
	}
	if result.NextState != prior {
		t.Fatalf("allow_stop must preserve prior state: got %+v want %+v", result.NextState, prior)
	}
	return result
}

// nextInput moves the lineage forward to the emitted control token, as the
// platform would for the following candidate.
func nextInput(in terminaldecision.Input, result Result, attempt uint8) terminaldecision.Input {
	out := in
	out.Evidence.Lineage.ProgressRef = result.Decision.Continue.ControlRef
	out.Evidence.Lineage.Attempt = attempt
	out.Continuation.Attempt = attempt
	out.Request.BLegID = "b-" + string(rune('0'+attempt))
	return out
}

func TestContinueCarriesApprovedClausesProvenanceTrajectoryAndToken(t *testing.T) {
	t.Parallel()

	in := normalInput()
	result := mustContinue(t, in, protocolstate.State{}, defaultLimits(), stockPlatform())
	intent := result.Decision.Continue

	for _, clause := range approvedClauses {
		if !strings.Contains(intent.Instruction, clause) {
			t.Fatalf("instruction is missing an approved clause:\n%s", clause)
		}
	}
	if !strings.Contains(intent.Instruction, in.Evidence.Objective) {
		t.Fatal("instruction must carry the existing bounded objective")
	}
	if intent.Provenance != "internal-control" {
		t.Fatalf("expected internal-control provenance, got %q", intent.Provenance)
	}
	if intent.TrajectoryRef != "traj-1" {
		t.Fatalf("expected retained trajectory reference, got %q", intent.TrajectoryRef)
	}
	if intent.ReasonCode != ReasonMissingSignal {
		t.Fatalf("expected bounded fixed reason, got %q", intent.ReasonCode)
	}
	if len(intent.ReasonCode) > terminaldecision.MaxReasonCodeBytes {
		t.Fatalf("reason code exceeds SDK bound: %d", len(intent.ReasonCode))
	}
	if len(intent.ControlRef) > terminaldecision.MaxIdentifierBytes {
		t.Fatalf("control reference exceeds SDK bound: %d", len(intent.ControlRef))
	}
	if !strings.HasPrefix(intent.ControlRef, protocolstate.TokenPrefix) {
		t.Fatalf("control reference must use the preferred namespace, got %q", intent.ControlRef)
	}
	decoded, err := protocolstate.Decode(intent.ControlRef)
	if err != nil {
		t.Fatalf("control reference must decode as preferred state: %v", err)
	}
	if decoded.Reprompts != 1 {
		t.Fatalf("expected exactly one consumed reprompt, got %d", decoded.Reprompts)
	}
	if result.NextState != decoded {
		t.Fatalf("proposed state must equal the encoded control state: %+v vs %+v", result.NextState, decoded)
	}
}

func TestFirstMissingSignalContinuesThenSecondUnmarkedStopsAtDefaultCap(t *testing.T) {
	t.Parallel()

	limits := defaultLimits()
	in := normalInput()
	first := mustContinue(t, in, protocolstate.State{}, limits, stockPlatform())
	if first.NextState.Reprompts != 1 {
		t.Fatalf("first candidate must consume exactly one reprompt, got %d", first.NextState.Reprompts)
	}

	second := nextInput(in, first, 2)
	second.Evidence.CandidateText = "still no completion signal"
	stopped := mustStop(t, second, first.NextState, limits, protocolstate.Platform{Cap: 3, Attempt: 2}, ReasonBudgetExhausted)
	if stopped.NextState.Reprompts != 1 {
		t.Fatalf("exhausted stop must preserve consumed total, got %d", stopped.NextState.Reprompts)
	}
}

func TestCapThreeWithChangedProgressNeverReplenishesTotal(t *testing.T) {
	t.Parallel()

	limits := protocolstate.Limits{MaxReprompts: 3, NoProgressLimit: 8}
	prior := protocolstate.State{}
	in := normalInput()
	for round := range 3 {
		in.Evidence.CandidateText = "progress round " + string(rune('a'+round))
		result := mustContinue(t, in, prior, limits, stockPlatform())
		if result.NextState.Reprompts != round+1 {
			t.Fatalf("round %d: expected total %d, got %d", round, round+1, result.NextState.Reprompts)
		}
		prior = result.NextState
		in = nextInput(in, result, uint8(round+2))
	}
	in.Evidence.CandidateText = "progress round d"
	mustStop(t, in, prior, limits, protocolstate.Platform{Cap: 3, Attempt: 4}, ReasonBudgetExhausted)
}

func TestRepeatedFingerprintTripsNoProgressBreaker(t *testing.T) {
	t.Parallel()

	limits := protocolstate.Limits{MaxReprompts: 3, NoProgressLimit: 2}
	first := mustContinue(t, normalInput(), protocolstate.State{}, limits, stockPlatform())
	if first.NextState.ConsecutiveNoProgress != 0 {
		t.Fatalf("first observation must establish a zero baseline, got %d", first.NextState.ConsecutiveNoProgress)
	}

	second := nextInput(normalInput(), first, 2)
	second.Evidence.CandidateText = normalInput().Evidence.CandidateText
	repeated := mustContinue(t, second, first.NextState, limits, protocolstate.Platform{Cap: 3, Attempt: 2})
	if repeated.NextState.ConsecutiveNoProgress != 1 {
		t.Fatalf("equal fingerprint must increment no-progress, got %d", repeated.NextState.ConsecutiveNoProgress)
	}
	if repeated.NextState.Reprompts != 2 {
		t.Fatalf("total must keep counting, got %d", repeated.NextState.Reprompts)
	}

	third := nextInput(normalInput(), repeated, 3)
	third.Evidence.CandidateText = normalInput().Evidence.CandidateText
	mustStop(t, third, repeated.NextState, limits, protocolstate.Platform{Cap: 3, Attempt: 3}, ReasonNoProgress)
}

func TestPositivePlatformCapStopsAtBoundAndZeroSnapshotAddsNoDefault(t *testing.T) {
	t.Parallel()

	limits := defaultLimits()
	in := normalInput()
	mustContinue(t, in, protocolstate.State{}, limits, protocolstate.Platform{Cap: 3, Attempt: 1})
	mustContinue(t, in, protocolstate.State{}, limits, protocolstate.Platform{Cap: 3, Attempt: 2})
	mustStop(t, in, protocolstate.State{}, limits, protocolstate.Platform{Cap: 3, Attempt: 3}, ReasonBudgetExhausted)
	// A zero snapshot cap is resolved by core and adds no feature-side default.
	zeroSnapshot := mustContinue(t, in, protocolstate.State{}, limits, protocolstate.Platform{Cap: 0, Attempt: 200})
	if zeroSnapshot.Decision.Continue.ControlRef == "" {
		t.Fatal("zero platform snapshot must not stop a fresh candidate")
	}
}

func TestConfigCapThreeUnderStockCoreYieldsTwoRepairs(t *testing.T) {
	t.Parallel()

	limits := protocolstate.Limits{MaxReprompts: 3, NoProgressLimit: 8}
	in := normalInput()

	first := mustContinue(t, in, protocolstate.State{}, limits, stockPlatform())
	in = nextInput(in, first, 2)
	in.Evidence.CandidateText = "stock round b"

	second := mustContinue(t, in, first.NextState, limits, protocolstate.Platform{Cap: 3, Attempt: 2})
	if second.NextState.Reprompts != 2 {
		t.Fatalf("expected two repairs under stock core, got %d", second.NextState.Reprompts)
	}
	in = nextInput(in, second, 3)
	in.Evidence.CandidateText = "stock round c"

	// The stock core platform cap of three, intersected with the candidate
	// attempt, stops the third repair even though the configured cap allows it.
	mustStop(t, in, second.NextState, limits, protocolstate.Platform{Cap: 3, Attempt: 3}, ReasonBudgetExhausted)
}

func mustLoadState(t *testing.T, in terminaldecision.Input) protocolstate.State {
	t.Helper()
	state, err := LoadState(in)
	if err != nil {
		t.Fatalf("LoadState() unexpected error: %v", err)
	}
	return state
}

func TestLoadStateRecognizesInitialBLegReferenceIncludingLaterSeq(t *testing.T) {
	t.Parallel()

	in := normalInput()
	in.Continuation.Attempt = 4
	in.Evidence.Lineage.Attempt = 4
	state := mustLoadState(t, in)
	if state != (protocolstate.State{}) {
		t.Fatalf("exact current b-leg reference denotes initial zero state, got %+v", state)
	}

	retry := normalInput()
	retry.Request.BLegID = ""
	retry.Continuation.TrajectoryRef = "traj-9"
	retry.Evidence.Lineage.TrajectoryRef = "traj-9"
	retry.Evidence.Lineage.ProgressRef = "traj-9"
	retry.Continuation.Attempt = 3
	if got := mustLoadState(t, retry); got != (protocolstate.State{}) {
		t.Fatalf("trajectory fallback denotes initial zero state, got %+v", got)
	}
}

func TestLoadStateDecodesPreferredTokenAndRejectsReservedNamespaces(t *testing.T) {
	t.Parallel()

	token, err := protocolstate.Encode(protocolstate.State{Reprompts: 1, LastFingerprint: fingerprintOf(t, normalInput())})
	if err != nil {
		t.Fatalf("Encode() unexpected error: %v", err)
	}
	decoded := normalInput()
	decoded.Evidence.Lineage.ProgressRef = token
	decoded.Evidence.Lineage.Attempt = 2
	decoded.Continuation.Attempt = 2
	state := mustLoadState(t, decoded)
	if state.Reprompts != 1 {
		t.Fatalf("preferred token must decode on a later candidate, got %+v", state)
	}

	for name, ref := range map[string]string{
		"malformed preferred": protocolstate.TokenPrefix + "!!!not-base64!!!",
		"truncated preferred": protocolstate.TokenPrefix + "AQ",
		"legacy reserved":     "alg-state-v1." + token[len(protocolstate.TokenPrefix):],
		"foreign unmatched":   "some-other-controller-token",
		"empty at later seq":  "",
	} {
		bad := normalInput()
		bad.Evidence.Lineage.ProgressRef = ref
		bad.Evidence.Lineage.Attempt = 2
		bad.Continuation.Attempt = 2
		if _, err := LoadState(bad); !errors.Is(err, protocolstate.ErrInvalidState) && !errors.Is(err, protocolstate.ErrInvalidToken) {
			t.Fatalf("%s reference must stop as invalid state, got %v", name, err)
		}
	}

	initial := normalInput()
	initial.Evidence.Lineage.ProgressRef = "alg-state-v1.corrupt"
	if _, err := LoadState(initial); err == nil {
		t.Fatal("legacy reserved namespace must stop even on an initial candidate")
	}
	malformedInitial := normalInput()
	malformedInitial.Evidence.Lineage.ProgressRef = protocolstate.TokenPrefix + "@@"
	if _, err := LoadState(malformedInitial); err == nil {
		t.Fatal("malformed preferred namespace must stop even on an initial candidate")
	}
	empty := normalInput()
	empty.Evidence.Lineage.ProgressRef = ""
	if got := mustLoadState(t, empty); got != (protocolstate.State{}) {
		t.Fatalf("empty reference bootstraps zero state at initial attempt, got %+v", got)
	}
}

func fingerprintOf(t *testing.T, in terminaldecision.Input) string {
	t.Helper()
	fingerprint := protocolstate.Fingerprint(in)
	if fingerprint == "" {
		t.Fatal("fingerprint must not be empty")
	}
	return fingerprint
}

func TestAuthoritativeAndExplicitCompletionAndPreOutputFailureStop(t *testing.T) {
	t.Parallel()

	for _, cause := range []terminaldecision.CandidateCause{
		terminaldecision.CandidateCauseRefusal,
		terminaldecision.CandidateCauseContentFilter,
		terminaldecision.CandidateCauseCancellation,
		terminaldecision.CandidateCauseAuthorityDenied,
	} {
		in := normalInput()
		in.Candidate.Cause = cause
		mustStop(t, in, protocolstate.State{}, defaultLimits(), stockPlatform(), ReasonAuthoritative)
	}

	completed := normalInput()
	completed.Evidence.ExplicitCompletion = true
	mustStop(t, completed, protocolstate.State{}, defaultLimits(), stockPlatform(), ReasonExplicitCompletion)

	// Only actual canonical failure causes fall back to the existing
	// stream-recovery owner. A clean Normal candidate is not a failure cause,
	// so it must not be labeled one here.
	for _, cause := range []terminaldecision.CandidateCause{
		terminaldecision.CandidateCauseTransport,
		terminaldecision.CandidateCauseProviderError,
		terminaldecision.CandidateCauseLimit,
	} {
		preOutput := normalInput()
		preOutput.Candidate.Cause = cause
		preOutput.Candidate.OutputCommitted = false
		mustStop(t, preOutput, protocolstate.State{}, defaultLimits(), stockPlatform(), ReasonPreOutputFailure)
	}
}

func TestInactiveExpectationStopsWithBoundedReason(t *testing.T) {
	t.Parallel()

	in := normalInput()
	in.Evidence.ExplicitCompletionExpected = false
	stopped := mustStop(t, in, protocolstate.State{}, defaultLimits(), stockPlatform(), ReasonProtocolInactive)
	if stopped.Decision.ReasonCode != "completion_protocol_inactive" {
		t.Fatalf("expected the approved inactive reason code, got %q", stopped.Decision.ReasonCode)
	}
}

func TestSafeCompletedOrdinaryActionsContinueAndUnsafeStateStops(t *testing.T) {
	t.Parallel()

	safe := withActions(normalInput(),
		terminaldecision.ActionFact{ItemID: "item-1", Kind: aipapi.ItemKindMessage, Status: aipapi.ItemStatusCompleted},
		toolAction(aipapi.ItemStatusCompleted),
		terminaldecision.ActionFact{ItemID: "item-2", CallID: "call-1", Kind: aipapi.ItemKindToolResult, Status: aipapi.ItemStatusCompleted, Name: "read_file"},
	)
	intent := mustContinue(t, safe, protocolstate.State{}, defaultLimits(), stockPlatform()).Decision.Continue
	if strings.Contains(intent.Instruction, "read_file") || strings.Contains(intent.Instruction, "call-1") {
		t.Fatal("completed ordinary tool facts must not be replayed into the instruction")
	}

	partial := withActions(normalInput(), toolAction(aipapi.ItemStatusIncomplete))
	mustStop(t, partial, protocolstate.State{}, defaultLimits(), stockPlatform(), ReasonUnsafeAction)

	ambiguous := withActions(normalInput(), toolAction(aipapi.ItemStatusInProgress))
	mustStop(t, ambiguous, protocolstate.State{}, defaultLimits(), stockPlatform(), ReasonUnsafeAction)
}

func TestCleanEmptyNormalCandidateIsNotRejectedWholesale(t *testing.T) {
	t.Parallel()

	limits := defaultLimits()
	// A clean Normal candidate with no committed output and no prose is the
	// canonical missing-signal case: it must receive the bounded repair, then
	// the following unmarked candidate must stop under the default cap of one.
	in := normalInput()
	in.Candidate.Cause = terminaldecision.CandidateCauseNormal
	in.Candidate.OutputCommitted = false
	in.Evidence.CandidateText = ""
	in.Evidence.RecentText = ""
	if err := in.Validate(); err != nil {
		t.Fatalf("fixture input must be SDK-valid: %v", err)
	}
	first := mustContinue(t, in, protocolstate.State{}, limits, stockPlatform())

	second := nextInput(in, first, 2)
	second.Candidate.OutputCommitted = false
	second.Evidence.CandidateText = ""
	stopped := mustStop(t, second, first.NextState, limits, protocolstate.Platform{Cap: 3, Attempt: 2}, ReasonBudgetExhausted)
	if stopped.NextState.Reprompts != 1 {
		t.Fatalf("second unmarked candidate must preserve the consumed total, got %d", stopped.NextState.Reprompts)
	}

	// The same clean case with committed output stays eligible as well.
	committed := normalInput()
	committed.Evidence.CandidateText = ""
	committed.Evidence.RecentText = ""
	mustContinue(t, committed, protocolstate.State{}, limits, stockPlatform())
}

func TestPostOutputInterruptionIsEligibleAndProseIsNeverHeuristic(t *testing.T) {
	t.Parallel()

	for _, cause := range []terminaldecision.CandidateCause{
		terminaldecision.CandidateCauseTransport,
		terminaldecision.CandidateCauseProviderError,
		terminaldecision.CandidateCauseLimit,
	} {
		in := normalInput()
		in.Candidate.Cause = cause
		in.Candidate.OutputCommitted = true
		mustContinue(t, in, protocolstate.State{}, defaultLimits(), stockPlatform())
	}

	// Prose that asks the user for input receives the same single bounded
	// repair; no semantic guessing is applied.
	prose := normalInput()
	prose.Evidence.CandidateText = "Please tell me which environment you want me to deploy to."
	prose.Evidence.RecentText = "I need the credentials for the staging cluster before I can continue."
	mustContinue(t, prose, protocolstate.State{}, defaultLimits(), stockPlatform())

	// Prose that claims completion is equally not special-cased here; the
	// platform-owned explicit-completion observation is the only signal.
	claims := normalInput()
	claims.Evidence.CandidateText = "All done! Everything is finished and working."
	mustContinue(t, claims, protocolstate.State{}, defaultLimits(), stockPlatform())
}

func TestMissingObjectiveOrTrajectoryStops(t *testing.T) {
	t.Parallel()

	noObjective := normalInput()
	noObjective.Evidence.Objective = "   "
	mustStop(t, noObjective, protocolstate.State{}, defaultLimits(), stockPlatform(), ReasonMissingObjective)

	noTrajectory := normalInput()
	noTrajectory.Continuation.TrajectoryRef = ""
	noTrajectory.Evidence.Lineage.TrajectoryRef = ""
	noTrajectory.Evidence.Lineage.ProgressRef = ""
	mustStop(t, noTrajectory, protocolstate.State{}, defaultLimits(), stockPlatform(), ReasonMissingTrajectory)
}

func TestFullBoundMultibyteObjectiveKeepsClausesAndValidates(t *testing.T) {
	t.Parallel()

	in := normalInput()
	// A full-bound multibyte objective: the SDK caps evidence objective text at
	// MaxEvidenceTextBytes, so this is the largest value a candidate can carry.
	in.Evidence.Objective = strings.Repeat("zażółć gęślą jaźń", 72) + strings.Repeat("ę", 88)
	if len(in.Evidence.Objective) != terminaldecision.MaxEvidenceTextBytes {
		t.Fatalf("fixture must sit exactly on the evidence bound, got %d", len(in.Evidence.Objective))
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("fixture input must be SDK-valid: %v", err)
	}
	intent, err := BuildIntent(in, mustEncodeFreshState(t, in))
	if err != nil {
		t.Fatalf("BuildIntent() unexpected error: %v", err)
	}
	if len(intent.Instruction) > terminaldecision.MaxInstructionBytes {
		t.Fatalf("instruction exceeds SDK bound: %d", len(intent.Instruction))
	}
	if !utf8.ValidString(intent.Instruction) {
		t.Fatal("instruction must remain valid UTF-8")
	}
	if err := intent.Validate(); err != nil {
		t.Fatalf("intent must be SDK-valid: %v", err)
	}
	for _, clause := range approvedClauses {
		if !strings.Contains(intent.Instruction, clause) {
			t.Fatalf("truncation must never shorten an approved clause:\n%s", clause)
		}
	}
}

func mustEncodeFreshState(t *testing.T, in terminaldecision.Input) string {
	t.Helper()
	observed, err := protocolstate.Observe(protocolstate.State{}, protocolstate.Fingerprint(in))
	if err != nil {
		t.Fatalf("Observe() unexpected error: %v", err)
	}
	proposed, err := protocolstate.Advance(observed, protocolstate.Limits{MaxReprompts: 1, NoProgressLimit: 2})
	if err != nil {
		t.Fatalf("Advance() unexpected error: %v", err)
	}
	token, err := protocolstate.Encode(proposed)
	if err != nil {
		t.Fatalf("Encode() unexpected error: %v", err)
	}
	return token
}

func TestTruncateUTF8KeepsWholeRunesOnly(t *testing.T) {
	t.Parallel()

	value := "zażółć gęślą jaźń"
	if got := truncateUTF8(value, len(value)); got != value {
		t.Fatalf("an exact budget must not change the value: %q", got)
	}
	for budget := range len(value) - 1 {
		got := truncateUTF8(value, budget)
		if len(got) > budget {
			t.Fatalf("budget %d exceeded: got %d bytes", budget, len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("budget %d produced invalid UTF-8", budget)
		}
		if !strings.HasPrefix(value, got) {
			t.Fatalf("budget %d produced a non-prefix truncation", budget)
		}
	}
	if got := truncateUTF8(value, 0); got != "" {
		t.Fatalf("zero budget must produce an empty value, got %q", got)
	}
}

// The six review counterexamples, reproduced as task-owned regressions.

func TestReservedUnsupportedFamiliesRejectBeforeAnyBootstrap(t *testing.T) {
	t.Parallel()

	// Both unsupported reserved versions must be refused even when the
	// reference is the live b-leg identifier on an initial candidate.
	for _, ref := range []string{"alg-proto-v2.future", "alg-state-v2.future"} {
		t.Run(ref, func(t *testing.T) {
			t.Parallel()
			in := normalInput()
			in.Request.BLegID = ref
			in.Evidence.Lineage.ProgressRef = ref
			if err := in.Validate(); err != nil {
				t.Fatalf("fixture input must be SDK-valid: %v", err)
			}
			if state, err := LoadState(in); !errors.Is(err, protocolstate.ErrInvalidState) {
				t.Fatalf("unsupported reserved namespace must reject before live b-leg bootstrap: state=%+v err=%v", state, err)
			}
		})
	}

	// A reserved reference that also matches the actual trajectory must not
	// bootstrap either.
	trajectoryMatch := normalInput()
	trajectoryMatch.Request.BLegID = ""
	trajectoryMatch.Continuation.TrajectoryRef = "alg-proto-v9.opaque"
	trajectoryMatch.Evidence.Lineage.TrajectoryRef = "alg-proto-v9.opaque"
	trajectoryMatch.Evidence.Lineage.ProgressRef = "alg-proto-v9.opaque"
	if state, err := LoadState(trajectoryMatch); !errors.Is(err, protocolstate.ErrInvalidState) {
		t.Fatalf("unsupported reserved namespace must reject before trajectory bootstrap: state=%+v err=%v", state, err)
	}
}

func TestPaddedPreferredTokenRejectsStrictlyEvenMatchingInitialReference(t *testing.T) {
	t.Parallel()

	token := mustEncodeFreshState(t, normalInput())
	padded := normalInput()
	padded.Evidence.Lineage.ProgressRef = " " + token + " "
	if err := padded.Validate(); err != nil {
		t.Fatalf("fixture input must be SDK-valid: %v", err)
	}
	if state, err := LoadState(padded); !errors.Is(err, protocolstate.ErrInvalidState) {
		t.Fatalf("whitespace-padded preferred token must reject: state=%+v err=%v", state, err)
	}

	// Even when the padded reference is also the live b-leg identifier, the
	// strict codec must reject it rather than bootstrap zero state.
	matching := padded
	matching.Request.BLegID = padded.Evidence.Lineage.ProgressRef
	if state, err := LoadState(matching); !errors.Is(err, protocolstate.ErrInvalidState) {
		t.Fatalf("padded preferred token must reject even when it matches the live reference: state=%+v err=%v", state, err)
	}

	embedded := normalInput()
	embedded.Evidence.Lineage.ProgressRef = protocolstate.TokenPrefix[:len(protocolstate.TokenPrefix)-1] + " " + token[len(protocolstate.TokenPrefix):]
	if state, err := LoadState(embedded); !errors.Is(err, protocolstate.ErrInvalidState) {
		t.Fatalf("noncanonical preferred token spelling must reject: state=%+v err=%v", state, err)
	}
}

func TestPaddedLiveReferenceCannotBootstrapLaterState(t *testing.T) {
	t.Parallel()

	in := normalInput()
	in.Evidence.Lineage.ProgressRef = " b-1 "
	in.Evidence.Lineage.Attempt = 4
	in.Continuation.Attempt = 4
	if err := in.Validate(); err != nil {
		t.Fatalf("fixture input must be SDK-valid: %v", err)
	}
	if state, err := LoadState(in); !errors.Is(err, protocolstate.ErrInvalidState) {
		t.Fatalf("non-exact live reference must not bootstrap later state: state=%+v err=%v", state, err)
	}
}

func TestOpaqueTrajectoryBytesArePreservedAndComparedExactly(t *testing.T) {
	t.Parallel()

	padded := normalInput()
	padded.Continuation.TrajectoryRef = " traj-1 "
	intent, err := BuildIntent(padded, mustEncodeFreshState(t, padded))
	if err != nil {
		t.Fatalf("BuildIntent() unexpected error: %v", err)
	}
	if intent.TrajectoryRef != padded.Continuation.TrajectoryRef {
		t.Fatalf("existing opaque trajectory must be preserved verbatim: got %q want %q", intent.TrajectoryRef, padded.Continuation.TrajectoryRef)
	}

	// An unpadded progress reference must not match a padded actual trajectory.
	mismatch := padded
	mismatch.Request.BLegID = ""
	mismatch.Evidence.Lineage.ProgressRef = "traj-1"
	if state, err := LoadState(mismatch); !errors.Is(err, protocolstate.ErrInvalidState) {
		t.Fatalf("non-exact actual trajectory must not bootstrap: state=%+v err=%v", state, err)
	}

	// The raw padded reference matching the raw padded trajectory is legitimate.
	exact := padded
	exact.Request.BLegID = ""
	exact.Evidence.Lineage.ProgressRef = " traj-1 "
	exact.Evidence.Lineage.Attempt = 3
	exact.Continuation.Attempt = 3
	if state, err := LoadState(exact); err != nil || state != (protocolstate.State{}) {
		t.Fatalf("exact raw trajectory fallback must bootstrap: state=%+v err=%v", state, err)
	}
}

func TestApprovedFallbacksAndValidTokenRemainAccepted(t *testing.T) {
	t.Parallel()

	// The approved actual trajectory fallback stays valid, including at an
	// initial sequence above one.
	fallback := normalInput()
	fallback.Request.BLegID = ""
	fallback.Evidence.Lineage.ProgressRef = "traj-1"
	fallback.Evidence.Lineage.Attempt = 3
	fallback.Continuation.Attempt = 3
	if state, err := LoadState(fallback); err != nil || state != (protocolstate.State{}) {
		t.Fatalf("approved actual trajectory fallback must bootstrap: state=%+v err=%v", state, err)
	}

	// Exact live b-leg retry bootstrap stays valid.
	retry := normalInput()
	retry.Evidence.Lineage.ProgressRef = "b-1"
	retry.Evidence.Lineage.Attempt = 4
	retry.Continuation.Attempt = 4
	if state, err := LoadState(retry); err != nil || state != (protocolstate.State{}) {
		t.Fatalf("exact live b-leg bootstrap must stay valid: state=%+v err=%v", state, err)
	}

	// A valid current preferred token still decodes.
	token := mustEncodeFreshState(t, normalInput())
	current := normalInput()
	current.Evidence.Lineage.ProgressRef = token
	current.Evidence.Lineage.Attempt = 2
	current.Continuation.Attempt = 2
	state, err := LoadState(current)
	if err != nil {
		t.Fatalf("valid current token must decode: %v", err)
	}
	if state.Reprompts != 1 || state.LastFingerprint == "" {
		t.Fatalf("valid current token must carry prior state, got %+v", state)
	}
}

func TestPresentBLegIDNeverBootstrapsFromTrajectoryEquality(t *testing.T) {
	t.Parallel()

	// The progress reference matches the existing trajectory byte-for-byte, but a
	// live B-leg identity is present, so only that exact raw B-leg reference may
	// bootstrap. A mismatch must stop conservatively at an initial attempt and at
	// a later attempt alike, instead of resetting protocol state.
	for _, attempt := range []uint8{0, 1, 4} {
		in := normalInput()
		in.Evidence.Lineage.ProgressRef = in.Continuation.TrajectoryRef
		in.Continuation.Attempt = attempt
		in.Evidence.Lineage.Attempt = attempt
		if err := in.Validate(); err != nil {
			t.Fatalf("fixture input at attempt %d must be SDK-valid: %v", attempt, err)
		}
		if state, err := LoadState(in); !errors.Is(err, protocolstate.ErrInvalidState) {
			t.Fatalf("present live B-leg ID must not bootstrap from trajectory equality at attempt %d: state=%+v err=%v", attempt, state, err)
		}
	}

	// Only the exact raw B-leg reference bootstraps while the B-leg is present.
	exact := normalInput()
	exact.Continuation.Attempt = 4
	exact.Evidence.Lineage.Attempt = 4
	if state, err := LoadState(exact); err != nil || state != (protocolstate.State{}) {
		t.Fatalf("exact raw B-leg retry bootstrap must stay valid: state=%+v err=%v", state, err)
	}

	// Trajectory fallback stays valid, including above attempt one, when the
	// B-leg is genuinely missing.
	fallback := normalInput()
	fallback.Request.BLegID = ""
	fallback.Evidence.Lineage.ProgressRef = fallback.Continuation.TrajectoryRef
	fallback.Continuation.Attempt = 4
	fallback.Evidence.Lineage.Attempt = 4
	if state, err := LoadState(fallback); err != nil || state != (protocolstate.State{}) {
		t.Fatalf("missing-B-leg exact trajectory fallback must stay valid: state=%+v err=%v", state, err)
	}
}

func TestExplicitCompletionWithUnsafeActionsStopsWithoutConsumingState(t *testing.T) {
	t.Parallel()

	in := withActions(normalInput(), toolAction(aipapi.ItemStatusIncomplete))
	in.Evidence.ExplicitCompletion = true
	result := mustEvaluate(t, in, protocolstate.State{}, defaultLimits(), stockPlatform())
	if result.Decision.Kind != terminaldecision.DecisionAllowStop {
		t.Fatalf("unsafe explicit completion must stop conservatively, got %q/%q", result.Decision.Kind, result.Decision.ReasonCode)
	}
	if result.Committed || result.NextState != (protocolstate.State{}) {
		t.Fatalf("conservative stop must not consume protocol state: %+v", result)
	}
}

func TestBuildIntentRejectsOversizedControlReference(t *testing.T) {
	t.Parallel()

	in := normalInput()
	if _, err := BuildIntent(in, strings.Repeat("t", terminaldecision.MaxIdentifierBytes+1)); err == nil {
		t.Fatal("oversized control reference must be rejected")
	}
}

func TestInvalidSDKInputStopsWithoutPanicOrPayloadLeak(t *testing.T) {
	t.Parallel()

	payloadMarker := "secret-marker-canonical-payload"
	in := normalInput()
	in.Candidate.Cause = "totally-unknown-cause"
	in.Evidence.CandidateText = payloadMarker

	result, err := evaluate(in, protocolstate.State{}, defaultLimits(), stockPlatform(), protocolstate.Encode)
	if err == nil {
		t.Fatal("invalid SDK input must return an error")
	}
	if strings.Contains(err.Error(), payloadMarker) {
		t.Fatal("error must not leak candidate payload")
	}
	if result.Decision.Kind != terminaldecision.DecisionAllowStop || result.Decision.ReasonCode != ReasonInvalidInput {
		t.Fatalf("expected conservative allow_stop, got %q/%q", result.Decision.Kind, result.Decision.ReasonCode)
	}
	if result.Committed || (result.NextState != protocolstate.State{}) {
		t.Fatal("invalid input must not consume a reprompt")
	}

	badLimits, err := evaluate(normalInput(), protocolstate.State{}, protocolstate.Limits{}, stockPlatform(), protocolstate.Encode)
	if err == nil {
		t.Fatal("invalid limits must return an error")
	}
	if badLimits.Decision.Kind != terminaldecision.DecisionAllowStop || badLimits.Decision.ReasonCode != ReasonInvalidInput {
		t.Fatalf("invalid limits must stop conservatively, got %q/%q", badLimits.Decision.Kind, badLimits.Decision.ReasonCode)
	}
}

func TestTerminalStateIsAbsorbing(t *testing.T) {
	t.Parallel()

	prior := protocolstate.State{Reprompts: 1, LastFingerprint: fingerprintOf(t, normalInput()), Terminal: true}
	in := normalInput()
	in.Evidence.CandidateText = "different evidence after terminal"
	mustStop(t, in, prior, defaultLimits(), stockPlatform(), ReasonProtocolTerminal)
}

func TestFailedIntentBuildDoesNotConsumeReprompt(t *testing.T) {
	t.Parallel()

	in := normalInput()
	failing := func(protocolstate.State) (string, error) {
		return "", errors.New("agent-loop-guard protocol policy: synthetic encoder failure")
	}
	result, err := evaluate(in, protocolstate.State{}, defaultLimits(), stockPlatform(), failing)
	if err != nil {
		t.Fatalf("an encoder failure must stop conservatively, not error: %v", err)
	}
	if result.Decision.Kind != terminaldecision.DecisionAllowStop {
		t.Fatalf("expected allow_stop, got %q", result.Decision.Kind)
	}
	if result.NextState.Reprompts != 0 || result.Committed {
		t.Fatalf("failed build must not consume a reprompt: %+v", result.NextState)
	}

	if _, err := evaluate(in, protocolstate.State{}, defaultLimits(), stockPlatform(), failing); err != nil {
		t.Fatalf("repeated failure must remain a conservative stop: %v", err)
	}
}
