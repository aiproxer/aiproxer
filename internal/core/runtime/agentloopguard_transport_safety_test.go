package runtime

// Runtime terminal and recovery integration cells for slice 10.2 of
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol; design Runtime /
// Acceptance Matrix rows 6, 7, 8, 9 and 11 plus the Transport and Continuation
// Interaction section; requirements 8.1, 8.2, 8.4, 8.5, 8.6 and 8.7).
//
// Every cell drives the ONE bounded runtime evaluator that every terminal path
// funnels through — evaluateTerminalDecision, the same seam
// [turnTerminal.sharedTerminalDecision] uses before a decision can be claimed —
// with the REAL preferred-strategy Agent Loop Guard provider produced by the
// production factory constructor. Nothing here assigns a provider fake and nothing
// here re-implements policy: the reason codes and the continuation intent under
// assertion are the ones the shipped provider emits, and the call counter below
// only records whether the runtime reached the protocol at all.
//
// The file is an INTERNAL test package on purpose. Refusal and content-filter
// candidates are declared by the SDK contract, but the runtime's own
// command-to-cause projection derives its causes exclusively from terminal
// commands, so no A-leg can produce a refusal cause end to end. The
// authoritative-cause contract is therefore certified here, at the runtime
// chokepoint where the cause is authoritative input, and the transport-visible
// consequences are certified end to end in
// internal/testkit/conformance/agentloopguard_preferred_transport_e2e_test.go.
//
// Nothing here repeats task 4.3's control-state cleanup matrix, task 5.2's
// pending-result withdrawal matrix, or task 8.3's pure provider acceptance matrix.
// Those own the private lifecycle, the publication withdrawal, and the policy
// decision table respectively. These cells own the RUNTIME OBSERVABLE contract:
// whether the runtime consulted the protocol for a transport-shaped input at all,
// which bounded reason code came back, and whether a continuation intent crossed
// the core boundary.

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/protocolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// algRuntimeToolResult is the client's answer to the completed ordinary tool call
// in the runtime cells. It exists so the "no replay" assertion can name it.
const algRuntimeToolResult = "backfill verified: 0 rows out of sync"

// algCountingProvider delegates every decision to the REAL preferred-strategy
// provider it wraps and counts the evaluations. It assigns no policy and invents no
// decision: it exists so a cell can distinguish "the protocol refused" from "the
// protocol was never asked", which is exactly the distinction requirement 8.6
// turns on. It also satisfies the task 8.3 requirement that any auxiliary
// construction or call in preferred mode be observable.
type algCountingProvider struct {
	inner terminaldecision.Provider
	calls atomic.Int32
}

func (p *algCountingProvider) ID() string { return p.inner.ID() }

func (p *algCountingProvider) Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	p.calls.Add(1)
	return p.inner.Decide(ctx, in)
}

// algRuntimeProvider returns the REAL preferred-strategy provider the production
// composition installs for `strategy: attempt_completion`, wrapped in the call
// counter. It is built through the feature's own error-returning constructor, so
// the receiver under test is the shipped one with the shipped default bounds
// (requirement 10.1: one reprompt).
func algRuntimeProvider(t *testing.T) *algCountingProvider {
	t.Helper()
	provider, err := agentloopguard.NewConfiguredProvider(agentloopguard.Config{
		Enabled:  true,
		Strategy: agentloopguard.StrategyAttemptCompletion,
	})
	if err != nil {
		t.Fatalf("compose the preferred Agent Loop Guard provider: %v", err)
	}
	if provider == nil {
		t.Fatal("the preferred strategy must compose a provider")
	}
	if id, err := terminaldecision.ProviderIdentity(provider); err != nil || id != agentloopguard.ID {
		t.Fatalf("provider identity = %q (err %v), want %q", id, err, agentloopguard.ID)
	}
	return &algCountingProvider{inner: provider}
}

// algActiveProtocolInput returns a terminal-decision input for a candidate whose
// completion protocol WAS successfully active on this attempt and which therefore
// carries every fact a bounded repair needs: a live objective, a resumable
// trajectory, a fresh protocol state, a configured and platform-unexhausted budget,
// and no observed completion signal.
//
// Only the fields named by mutate differ between the cells below, so each cell's
// single deviation from a fully eligible candidate is exactly the input its
// transport situation produces.
func algActiveProtocolInput(t *testing.T, cause terminaldecision.CandidateCause, committed bool, mutate func(*terminaldecision.Input)) terminaldecision.Input {
	t.Helper()
	in := terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{
			Cause:           cause,
			Reference:       "bleg-runtime-transport",
			OutputCommitted: committed,
		},
		Request: terminaldecision.RequestIdentity{
			RequestID: "request-runtime-transport",
			TraceID:   "trace-runtime-transport",
			ALegID:    "aleg-runtime-transport",
			BLegID:    "bleg-runtime-transport",
		},
		Policy: terminaldecision.PolicySnapshot{Revision: "policy-runtime-transport", MaxContinuationAttempts: 3},
		Continuation: terminaldecision.ContinuationEvidence{
			TrajectoryRef: "trajectory-runtime-transport",
			Attempt:       1,
		},
		Evidence: terminaldecision.Evidence{
			Objective:  "apply the schema, verify the backfill, then report the result",
			RecentText: "apply the schema, verify the backfill, then report the result",
			// The attempt's frozen activation: the protocol really was active here.
			ExplicitCompletionExpected: true,
			// The live B-leg identity is the opaque initial protocol reference,
			// exactly as projectTerminalDecisionEvidence projects it.
			Lineage: terminaldecision.EvidenceLineage{
				TrajectoryRef: "trajectory-runtime-transport",
				ProgressRef:   "bleg-runtime-transport",
				Attempt:       1,
			},
		},
		Deadline: time.Now().Add(time.Minute),
	}
	if mutate != nil {
		mutate(&in)
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("the transport-shaped terminal input must be SDK-valid: %v", err)
	}
	return in
}

// algWithCompletedOrdinaryTool adds one COMPLETED ordinary client tool call and
// its completed result: the retained facts requirement 8.3 preserves.
func algWithCompletedOrdinaryTool(in *terminaldecision.Input) {
	in.Evidence.ActionCount = 2
	in.Evidence.Actions[0] = terminaldecision.ActionFact{
		CallID: "call_runtime_completed",
		Kind:   aipapi.ItemKindToolCall,
		Status: aipapi.ItemStatusCompleted,
		Name:   "run_backfill_check",
	}
	in.Evidence.Actions[1] = terminaldecision.ActionFact{
		CallID: "call_runtime_completed",
		Kind:   aipapi.ItemKindToolResult,
		Status: aipapi.ItemStatusCompleted,
		Name:   "run_backfill_check",
	}
}

// TestAgentLoopGuardTransport_authoritativeCausesNeverBecomeMissingSignalRecovery
// is matrix row 7 and requirement 8.6 at the runtime chokepoint.
//
// Every input below is a FULLY ELIGIBLE active-protocol candidate: the protocol
// expected a completion, the objective and trajectory are present, the ordinary
// tool facts are complete, the budget is fresh, and no completion signal was
// observed, with client-visible output already committed. The ONLY deviation is
// the authoritative non-recoverable cause. A continuation intent here would be
// exactly the "authoritative terminal reinterpreted as missing completion work"
// defect requirement 8.6 forbids, so the cell asserts the whole shape of the
// refusal: the typed pass-through reason, the absence of any continuation intent,
// AND that the protocol was never consulted at all — the strongest available proof,
// because a provider that is never asked cannot reinterpret anything.
//
// Refusal and content filter are included alongside cancellation and authority
// denial because requirement 8.6 names refusal and content filtering explicitly and
// the runtime's own command-to-cause projection does not derive them.
//
// The cell additionally asks the REAL provider the same question directly, so both
// owners of the rule are pinned: the runtime that refuses to route the cause to the
// protocol, and the protocol that would refuse anyway.
func TestAgentLoopGuardTransport_authoritativeCausesNeverBecomeMissingSignalRecovery(t *testing.T) {
	t.Parallel()

	causes := []struct {
		name  string
		cause terminaldecision.CandidateCause
	}{
		{name: "refusal", cause: terminaldecision.CandidateCauseRefusal},
		{name: "content_filter", cause: terminaldecision.CandidateCauseContentFilter},
		{name: "cancellation", cause: terminaldecision.CandidateCauseCancellation},
		{name: "authority_denied", cause: terminaldecision.CandidateCauseAuthorityDenied},
	}
	for _, tc := range causes {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := algRuntimeProvider(t)
			in := algActiveProtocolInput(t, tc.cause, true, algWithCompletedOrdinaryTool)
			got := evaluateTerminalDecision(t.Context(), provider, in)

			if got.Decision.Kind != terminaldecision.DecisionAllowStop || got.Decision.Continue != nil {
				t.Fatalf("authoritative cause %q produced decision kind %q with intent %v; an authoritative non-recoverable terminal must never become missing completion work",
					tc.cause, got.Decision.Kind, got.Decision.Continue)
			}
			if got.Decision.ReasonCode != string(tc.cause) {
				t.Fatalf("authoritative cause %q reason = %q, want the typed pass-through %q", tc.cause, got.Decision.ReasonCode, string(tc.cause))
			}
			if got.Decision.ReasonCode == protocolpolicy.ReasonMissingSignal {
				t.Fatal("an authoritative cause was reported as missing completion work")
			}
			if n := provider.calls.Load(); n != 0 {
				t.Fatalf("the preferred protocol was consulted %d time(s) for the authoritative cause %q; the runtime must take the typed pass-through itself", n, tc.cause)
			}

			// Defence in depth: the real protocol refuses the same input even when
			// it is asked directly.
			direct, err := provider.inner.Decide(t.Context(), in)
			if err != nil {
				t.Fatalf("the preferred protocol refused to answer for %q: %v", tc.cause, err)
			}
			if direct.Kind != terminaldecision.DecisionAllowStop || direct.Continue != nil {
				t.Fatalf("the preferred protocol turned the authoritative cause %q into decision kind %q with intent %v", tc.cause, direct.Kind, direct.Continue)
			}
			if direct.ReasonCode != protocolpolicy.ReasonAuthoritative {
				t.Fatalf("the preferred protocol reason for %q = %q, want %q", tc.cause, direct.ReasonCode, protocolpolicy.ReasonAuthoritative)
			}
		})
	}
}

// TestAgentLoopGuardTransport_preOutputProviderFailureNeverCompetesWithGenericRecovery
// is matrix row 8's runtime half and requirement 8.1.
//
// A pre-output transport, provider, or limit failure is the situation the EXISTING
// pre-output recovery owner already handles. Requirement 8.1 and the design's
// Decision Order step 4 forbid the completion protocol from adding a competing
// replay budget for it, so the runtime's evaluator must receive a conservative
// allow-stop and never a repair intent however fresh the protocol budget is.
//
// The call count is asserted as exactly one, which makes the cell non-vacuous: the
// protocol really was asked and really declined, rather than being bypassed the way
// an authoritative cause is.
func TestAgentLoopGuardTransport_preOutputProviderFailureNeverCompetesWithGenericRecovery(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		cause terminaldecision.CandidateCause
	}{
		{name: "transport", cause: terminaldecision.CandidateCauseTransport},
		{name: "provider_error", cause: terminaldecision.CandidateCauseProviderError},
		{name: "limit", cause: terminaldecision.CandidateCauseLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := algRuntimeProvider(t)
			in := algActiveProtocolInput(t, tc.cause, false, algWithCompletedOrdinaryTool)
			got := evaluateTerminalDecision(t.Context(), provider, in)
			if n := provider.calls.Load(); n != 1 {
				t.Fatalf("the preferred protocol was consulted %d time(s), want exactly 1 so the decline is the protocol's own decision", n)
			}
			if got.Decision.Kind != terminaldecision.DecisionAllowStop || got.Decision.Continue != nil {
				t.Fatalf("pre-output cause %q produced kind %q with intent %v; the existing recovery owner must stay authoritative and no competing replay budget may be added",
					tc.cause, got.Decision.Kind, got.Decision.Continue)
			}
			if got.Decision.ReasonCode != protocolpolicy.ReasonPreOutputFailure {
				t.Fatalf("pre-output cause %q reason = %q, want %q", tc.cause, got.Decision.ReasonCode, protocolpolicy.ReasonPreOutputFailure)
			}
			if got.Decision.ReasonCode == protocolpolicy.ReasonMissingSignal {
				t.Fatalf("a pre-output failure was reported as missing completion work")
			}
			if got.OutputCommitted {
				t.Fatal("the evaluator changed output commitment for a pre-output candidate")
			}
		})
	}
}

// TestAgentLoopGuardTransport_postOutputFailureWithActiveProtocolRequestsOneBoundedContinuation
// is matrix row 9's runtime half with requirement 8.2 and 8.7.
//
// The same candidate as the pre-output cell, but the client has already seen
// committed output. Requirement 8.2 permits exactly one bounded continuation leg
// built from the retained trajectory and forbids replaying the committed attempt;
// the design fixes that leg's instruction text. The cell asserts that the runtime
// evaluator hands the platform an intent whose instruction is the approved repair
// control text and whose control reference is a preferred protocol token, and then
// that the NEXT candidate carrying that accepted token is refused because the
// requirement 10.1 default budget of one reprompt is spent.
//
// The second half is what makes this a lifecycle assertion rather than a single
// decision check: it proves the bounded state travels on the accepted reference, so
// the immutable total cap survives the continuation instead of being reset by it.
func TestAgentLoopGuardTransport_postOutputFailureWithActiveProtocolRequestsOneBoundedContinuation(t *testing.T) {
	t.Parallel()

	provider := algRuntimeProvider(t)
	in := algActiveProtocolInput(t, terminaldecision.CandidateCauseTransport, true, algWithCompletedOrdinaryTool)
	got := evaluateTerminalDecision(t.Context(), provider, in)
	if got.Decision.Kind != terminaldecision.DecisionContinue {
		t.Fatalf("post-output interruption with an active protocol produced kind %q (reason %q), want a bounded continuation",
			got.Decision.Kind, got.Decision.ReasonCode)
	}
	intent := got.Decision.Continue
	if intent == nil {
		t.Fatal("the continuation decision carried no intent")
	}
	if intent.ReasonCode != protocolpolicy.ReasonMissingSignal {
		t.Fatalf("continuation reason = %q, want %q", intent.ReasonCode, protocolpolicy.ReasonMissingSignal)
	}
	if !strings.HasPrefix(intent.Instruction, "<automated-completion-protocol-repair>") ||
		!strings.HasSuffix(intent.Instruction, "</automated-completion-protocol-repair>") {
		t.Fatalf("the continuation instruction is not the fixed bounded recovery control text: %q", intent.Instruction)
	}
	if !strings.Contains(intent.Instruction, "The previous model turn ended without the required `attempt_completion` signal.") {
		t.Fatalf("the continuation instruction lost its fixed missing-signal clause: %q", intent.Instruction)
	}
	if !strings.HasPrefix(intent.ControlRef, "alg-proto-v1.") {
		t.Fatalf("continuation control reference = %q, want the preferred protocol token family", intent.ControlRef)
	}
	// Requirement 8.3 with 8.2: the recovery intent never replays the committed
	// attempt's ordinary client tool facts.
	if strings.Contains(intent.Instruction, "run_backfill_check") || strings.Contains(intent.Instruction, algRuntimeToolResult) {
		t.Fatalf("the recovery intent replayed ordinary client tool facts:\n%s", intent.Instruction)
	}

	// The accepted control reference travels on the next candidate, and the
	// requirement 10.1 default cap of one reprompt refuses a second repair.
	next := algActiveProtocolInput(t, terminaldecision.CandidateCauseTransport, true, func(in *terminaldecision.Input) {
		algWithCompletedOrdinaryTool(in)
		in.Continuation.Attempt = 2
		in.Evidence.Lineage.Attempt = 2
		in.Evidence.Lineage.ProgressRef = intent.ControlRef
	})
	second := evaluateTerminalDecision(t.Context(), provider, next)
	if n := provider.calls.Load(); n != 2 {
		t.Fatalf("the preferred protocol was consulted %d time(s) across the two candidates, want exactly 2", n)
	}
	if second.Decision.Kind != terminaldecision.DecisionAllowStop || second.Decision.Continue != nil {
		t.Fatalf("a candidate carrying the accepted protocol reference produced kind %q with intent %v; the immutable total cap must refuse a second repair",
			second.Decision.Kind, second.Decision.Continue)
	}
	if second.Decision.ReasonCode != protocolpolicy.ReasonBudgetExhausted {
		t.Fatalf("exhausted-protocol reason = %q, want %q", second.Decision.ReasonCode, protocolpolicy.ReasonBudgetExhausted)
	}
}

// TestAgentLoopGuardTransport_unsafeOrdinaryToolBoundaryStopsInsteadOfContinuing
// is matrix row 11's runtime half and requirement 8.4.
//
// The retained trajectory carries an ordinary client tool boundary the provider
// never finalized: an in-progress or explicitly incomplete tool call, or an
// unfinished or incomplete tool result. Requirement 8.4 makes that an unsafe
// boundary, so continuation must stop conservatively even though the protocol is
// active, the budget is fresh, no completion signal was observed, and client-visible
// output was already committed.
//
// Each unsafe shape is listed separately because the projection keeps the CURRENT
// boundary visible ahead of older facts: an assertion that only ever held for one of
// them would not pin the contract.
func TestAgentLoopGuardTransport_unsafeOrdinaryToolBoundaryStopsInsteadOfContinuing(t *testing.T) {
	t.Parallel()

	unsafeAction := func(callID string, kind aipapi.ItemKind, status aipapi.ItemStatus) func(*terminaldecision.Input) {
		return func(in *terminaldecision.Input) {
			algWithCompletedOrdinaryTool(in)
			in.Evidence.ActionCount = 3
			in.Evidence.Actions[2] = terminaldecision.ActionFact{
				CallID: callID,
				Kind:   kind,
				Status: status,
				Name:   "run_backfill_check",
			}
		}
	}
	cases := []struct {
		name   string
		mutate func(*terminaldecision.Input)
	}{
		{
			name:   "unfinished_tool_call",
			mutate: unsafeAction("call_runtime_partial", aipapi.ItemKindToolCall, aipapi.ItemStatusInProgress),
		},
		{
			name:   "incomplete_tool_call",
			mutate: unsafeAction("call_runtime_partial", aipapi.ItemKindToolCall, aipapi.ItemStatusIncomplete),
		},
		{
			name:   "unfinished_tool_result",
			mutate: unsafeAction("call_runtime_partial", aipapi.ItemKindToolResult, aipapi.ItemStatusInProgress),
		},
		{
			name:   "incomplete_tool_result",
			mutate: unsafeAction("call_runtime_partial", aipapi.ItemKindToolResult, aipapi.ItemStatusIncomplete),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := algRuntimeProvider(t)
			in := algActiveProtocolInput(t, terminaldecision.CandidateCauseTransport, true, tc.mutate)
			got := evaluateTerminalDecision(t.Context(), provider, in)
			if n := provider.calls.Load(); n != 1 {
				t.Fatalf("the preferred protocol was consulted %d time(s), want exactly 1 so the conservative stop is the protocol's own decision", n)
			}
			if got.Decision.Kind != terminaldecision.DecisionAllowStop || got.Decision.Continue != nil {
				t.Fatalf("an unsafe ordinary tool boundary produced kind %q with intent %v; requirement 8.4 requires a conservative stop",
					got.Decision.Kind, got.Decision.Continue)
			}
			if got.Decision.ReasonCode != protocolpolicy.ReasonUnsafeAction {
				t.Fatalf("unsafe-boundary reason = %q, want %q", got.Decision.ReasonCode, protocolpolicy.ReasonUnsafeAction)
			}
		})
	}
}

// TestAgentLoopGuardTransport_canceledContextNeverRequestsProtocolRecovery is
// matrix row 6's runtime half with requirement 8.5.
//
// A cancelled or expired request context must stop conservatively with a bounded
// reason and without any continuation intent. The candidate cause is deliberately a
// non-authoritative NORMAL completion so the runtime does NOT take its typed
// authoritative pass-through: the protocol is genuinely consulted and genuinely
// declines. That makes the cell the strongest available form of requirement 8.5's
// "the proxy shall never convert absence of attempt_completion into automatic
// continuation", because a provider that is asked and refuses cannot be bypassed by
// a bypass to hide a defect.
func TestAgentLoopGuardTransport_canceledContextNeverRequestsProtocolRecovery(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		prepare func() (context.Context, context.CancelFunc)
	}{
		{
			name: "canceled",
			prepare: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
		},
		{
			name: "deadline_exceeded",
			prepare: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := algRuntimeProvider(t)
			ctx, cancel := tc.prepare()
			defer cancel()
			in := algActiveProtocolInput(t, terminaldecision.CandidateCauseNormal, true, algWithCompletedOrdinaryTool)
			got := evaluateTerminalDecision(ctx, provider, in)
			if n := provider.calls.Load(); n != 1 {
				t.Fatalf("the preferred protocol was consulted %d time(s), want exactly 1 so the decline is the protocol's own decision", n)
			}
			if got.Decision.Kind != terminaldecision.DecisionAllowStop || got.Decision.Continue != nil {
				t.Fatalf("a %s request context produced kind %q with intent %v; requirement 8.5 forbids protocol recovery on client cancellation",
					tc.name, got.Decision.Kind, got.Decision.Continue)
			}
			if got.Decision.ReasonCode == protocolpolicy.ReasonMissingSignal {
				t.Fatal("client cancellation was reported as missing completion work")
			}
			if got.Decision.ReasonCode == protocolpolicy.ReasonBudgetExhausted ||
				got.Decision.ReasonCode == protocolpolicy.ReasonUnsafeAction ||
				got.Decision.ReasonCode == protocolpolicy.ReasonMissingObjective ||
				got.Decision.ReasonCode == protocolpolicy.ReasonMissingTrajectory {
				t.Fatalf("a %s request context was decided on candidate evidence (reason %q); it must stop on the context itself", tc.name, got.Decision.ReasonCode)
			}
		})
	}
}
