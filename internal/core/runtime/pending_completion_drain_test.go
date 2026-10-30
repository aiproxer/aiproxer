// Drain, recorder, and ordinary-boundary regressions for the pending
// completion-result publication (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Completion
// Evidence and Pending Result / Concurrency and Lifecycle; requirements 6.3-6.7,
// 11.3, 11.5, 12.5).
//
// Every case drives the real prepare/publish/release seams, so a behavior
// satisfied only by a private helper fails here. Assertions read private state
// under the same locks production uses, and every ordering assertion is made from
// recorded event order rather than a sleep.
package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	secureapp "github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// pendingDrainSecretMarker is a low-entropy, obviously fake marker so secret
// scanning sees a planted test value rather than an API-key-shaped literal.
const pendingDrainSecretMarker = "PRIVATE_PRIVATE_PRIVATE"

// pendingDrainFacts is the receive-loop fact set the response seam uses.
// pendingDrainBLeg is the frozen B-leg identity a pending result travels with, so
// a case can prove the result belongs to the attempt that actually produced it.
func pendingDrainBLeg() b2bua.BLegRecord {
	return b2bua.BLegRecord{ALegID: "aleg-pending-1", BLegID: "bleg-pending-1", Seq: 1}
}

// pendingDrainBlankResultAttempt builds an attempt whose private completion is
// bounded but blank, so a case can prove a blank result prepares nothing without
// going through the normal non-empty fixture.
func pendingDrainBlankResultAttempt(t *testing.T) *attemptSession {
	t.Helper()
	attempt := newAttemptSession(attemptSessionInput{bleg: pendingDrainBLeg(), cand: controlCandidate("control-a")})
	attempt.controlCapture = newControlCallCapture(controlCaptureActivation(t))
	require.True(t, attempt.storeControlOutcome(controltool.Outcome{
		Kind: controltool.OutcomeComplete, ResultText: "   \n ", ReasonCode: "control_complete",
	}))
	_, ok := attempt.snapshotPendingCompletionText()
	assert.False(t, ok, "a blank bounded result must not be a pending completion")
	return attempt
}

// controlCaptureActivation reuses the existing interception fixture activation so
// the pending-publication fixture owns a real trusted activation and a real
// projected control tool, exactly like a live attempt does.
func controlCaptureActivation(t *testing.T) *controlToolActivation {
	t.Helper()
	return interceptActivation(t, &interceptProvider{outcome: controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "pending drain result", ReasonCode: "control_complete"}}, controltool.DefaultMaxArgsBytes)
}

// pendingCatalogFinalizer is one ordinary assembler finalizer. It exists only so
// the assembler is really enabled in the assembler-active case.
type pendingCatalogFinalizer struct{}

func (pendingCatalogFinalizer) ID() string { return "pending-catalog-finalizer" }

func (pendingCatalogFinalizer) Order() int { return 0 }

func (pendingCatalogFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

func pendingDrainFacts() recvTurnFacts {
	return testRecvTurnFacts(recvTurnFacts{
		traceID: "trace-pending-1",
		aLegID:  "aleg-pending-1",
		baseline: aipapi.Call{
			ID:    "request-pending-1",
			Route: aipapi.RouteIntent{Selector: "control-a:model-a"},
			Invocation: aipapi.Invocation{
				Operation:    aipapi.OperationOpenAIChatCompletions,
				DeliveryMode: aipapi.DeliveryModeStreaming,
			},
			Messages: testMinimalUserMessages(),
		},
		secureTurnOK: true,
	})
}

// pendingDrainAttempt builds a real attempt session holding a valid, bounded
// completion result, the same way the response interception seam leaves one
// behind for the terminal owner.
func pendingDrainAttempt(t *testing.T, result string) *attemptSession {
	t.Helper()
	attempt := newAttemptSession(attemptSessionInput{
		bleg: pendingDrainBLeg(),
		cand: controlCandidate("control-a"),
	})
	attempt.controlCapture = newControlCallCapture(controlCaptureActivation(t))
	require.True(t, attempt.storeControlOutcome(controltool.Outcome{
		Kind: controltool.OutcomeComplete, ResultText: result, ReasonCode: "control_complete",
	}), "the fixture must really hold a valid private completion result")
	text, ok := attempt.snapshotPendingCompletionText()
	require.True(t, ok, "the fixture must be snapshottable as a pending completion")
	require.Equal(t, strings.TrimSpace(result), text.result)
	return attempt
}

func pendingDrainLabels(events []aipapi.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventTextDelta:
			out = append(out, "text:"+ev.Delta)
		default:
			out = append(out, string(ev.Kind))
		}
	}
	return out
}

// pendingDrainUsageCount counts usage deltas, so a case can prove the private
// publication never fabricates an operator usage event of its own.
func pendingDrainUsageCount(events []aipapi.Event) int {
	count := 0
	for _, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta {
			count++
		}
	}
	return count
}

// pendingDrainGate records the candidate it observed and returns a scripted
// outcome, so a case can prove the gate ran once over a candidate that already
// carried the eligible result.
type pendingDrainGate struct {
	out   completion.Outcome
	calls int
	seen  []string
}

func (g *pendingDrainGate) ID() string                        { return "pending-drain-gate" }
func (g *pendingDrainGate) Order() int                        { return 0 }
func (g *pendingDrainGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (g *pendingDrainGate) Handle(_ context.Context, _ completion.Meta, buf completion.Buffered, _ completion.Services) (completion.Outcome, error) {
	g.calls++
	g.seen = nil
	for _, ev := range buf.Events() {
		if ev.Kind == aipapi.EventTextDelta {
			g.seen = append(g.seen, ev.Delta)
		}
	}
	return g.out, nil
}

// pendingTextOnlyGate returns Pass and records nothing beyond its call count,
// for cases where the candidate content is already proven elsewhere.
type pendingTextOnlyGate struct{ calls int }

func (g *pendingTextOnlyGate) ID() string                        { return "pending-text-gate" }
func (g *pendingTextOnlyGate) Order() int                        { return 0 }
func (g *pendingTextOnlyGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (g *pendingTextOnlyGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	g.calls++
	return completion.PassOriginalOutcome(), nil
}

// TestPendingDrain_replacementDiscardsSuppressedResultAndReleasesOrdinaryOutput
// pins the authoritative-replacement rule: when a gate replaces the stream, its
// output is what the client receives, so the private result is suppressed rather
// than re-appended after the replacement. Ordinary canonical output is untouched,
// and the replacement text is never matched or removed by the result value.
func TestPendingDrain_replacementDiscardsSuppressedResultAndReleasesOrdinaryOutput(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "the "+pendingDrainSecretMarker+" answer")
	replacement := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "gate replacement"},
		{Kind: aipapi.EventResponseFinished},
	}
	gate := &pendingDrainGate{out: completion.ReplaceOutcome(replacement)}
	// The buffered ordinary prefix plus the finish is what the chain must see.
	p.gateBuf = append(p.gateBuf, aipapi.Event{Kind: aipapi.EventResponseStarted}, aipapi.Event{Kind: aipapi.EventMessageStarted})

	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, []completion.Gate{gate}, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared, "a valid completion must prepare a publication value")
	assert.Zero(t, prep.ordinary.Kind, "an accepted candidate leaves no ordinary gate head to release")
	assert.Equal(t, []string{"the " + pendingDrainSecretMarker + " answer"}, gate.seen,
		"the gate must see the eligible result inside its candidate")
	assert.True(t, prep.prepared.publishing())
	// The effective replacement is authoritative: it is exactly what the client
	// receives, with the accepted finish kept by the prepared value.
	assert.Equal(t, pendingDrainLabels(replacement[:len(replacement)-1]), pendingDrainLabels(prep.prepared.events),
		"the effective gate output must be exactly what the client receives")
	assert.Equal(t, aipapi.EventResponseFinished, prep.prepared.finish.Kind,
		"the accepted finish stays with the prepared value")
}

// TestPendingDrain_replayRestoresTheAugmentedOriginal is the replay half: a gate
// that replays the original restores the augmented original candidate, including
// its eligible result, exactly as it restores ordinary original content.
func TestPendingDrain_replayRestoresTheAugmentedOriginal(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "replayed answer")
	gate := &pendingDrainGate{out: completion.Outcome{Kind: completion.OutcomeReplayOriginal}}
	p.gateBuf = append(p.gateBuf, aipapi.Event{Kind: aipapi.EventResponseStarted}, aipapi.Event{Kind: aipapi.EventMessageStarted})

	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, []completion.Gate{gate}, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)
	assert.Equal(t, 1, gate.calls)
	assert.True(t, prep.prepared.publishing(), "a replay must restore the eligible result")
	assert.Equal(t, []string{
		string(aipapi.EventResponseStarted),
		string(aipapi.EventMessageStarted),
		"text:replayed answer",
	}, pendingDrainLabels(prep.prepared.events),
		"replay must restore the augmented original candidate exactly")
	assert.Nil(t, p.gateBuf, "the prepared value owns the whole evaluated sequence")
	assert.Nil(t, p.gateDrain, "the prepared value must not install an ordinary gated drain")
}

// TestPendingDrain_unresolvedOrdinaryBoundarySuppressesResultKeepsOutput pins the
// conservative ordinary-boundary rule: a started ordinary call that never reaches a
// completed boundary suppresses only the automatic result. The ordinary canonical
// output stays exactly what it was, and the decision supplement still reports the
// unresolved action so the deciding provider can see it.
func TestPendingDrain_unresolvedOrdinaryBoundarySuppressesResultKeepsOutput(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "held answer")
	p.gateBuf = append(p.gateBuf,
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
		aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "ordinary-1", ToolName: "get_weather"},
	)

	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared, "the prepared value still exists for its evidence")
	assert.False(t, prep.prepared.boundarySafe,
		"a started ordinary call with no completed boundary must be unsafe")
	assert.False(t, prep.prepared.publishing(),
		"an unresolved ordinary boundary must suppress the automatic result")
	// Only the synthetic original suffix is dropped. Ordinary canonical output is
	// never removed with it.
	assert.Equal(t, pendingDrainLabels([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted},
	}), pendingDrainLabels(prep.prepared.events),
		"suppression must remove only the synthetic original suffix")
	// The unresolved ordinary fact is still projected, so the deciding provider is
	// not evaluated as though the boundary did not exist.
	supplement := p.pendingActionSupplement(attempt)
	assert.True(t, prep.prepared.supplementSafe, "the supplement itself is representable")
	require.Len(t, supplement, 1)
	assert.Equal(t, "ordinary-1", supplement[0].CallID)
	assert.Equal(t, "get_weather", supplement[0].Name)
	assert.Equal(t, aipapi.ItemStatusInProgress, supplement[0].Status)
}

// TestPendingDrain_finishedOrdinaryCallInheritsItsNamedStart pins the correlation
// rule across the gate-buffer boundary: a finished call that carries no name of
// its own inherits the trusted name of the start that was already released, so a
// correlated completed lifecycle is not mistaken for an unresolved boundary.
func TestPendingDrain_finishedOrdinaryCallInheritsItsNamedStart(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "correlated answer")
	// The named start was already released, so it is in the released history.
	p.rememberClientEvent(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "ordinary-1", ToolName: "get_weather"})
	p.rememberClientEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	p.rememberClientEvent(aipapi.Event{Kind: aipapi.EventMessageStarted})
	// Only the nameless finish is still held.
	p.gateBuf = append(p.gateBuf,
		aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "ordinary-1"},
	)

	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)
	assert.True(t, prep.prepared.boundarySafe,
		"a nameless finish correlated to a released named start is a completed boundary")
	assert.True(t, prep.prepared.publishing(),
		"a fully correlated finished lifecycle must not suppress the result")
	supplement := p.pendingActionSupplement(attempt)
	require.Len(t, supplement, 1)
	assert.Equal(t, "ordinary-1", supplement[0].CallID)
	assert.Equal(t, "get_weather", supplement[0].Name,
		"the finish must inherit the trusted ordinary name")
	assert.Equal(t, aipapi.ItemStatusCompleted, supplement[0].Status,
		"a finished ordinary call is completed under the existing ActionFact convention")
}

// TestPendingDrain_orphanAndAmbiguousBoundariesFailClosed pins the conservative
// identity rules: an orphan finish and a call whose identity carries conflicting
// names are both unsafe, and neither invents a name for the orphan.
func TestPendingDrain_orphanAndAmbiguousBoundariesFailClosed(t *testing.T) {
	t.Parallel()

	t.Run("orphan_finish", func(t *testing.T) {
		t.Parallel()
		p := newResponsePipeline()
		attempt := pendingDrainAttempt(t, "orphan answer")
		p.gateBuf = append(p.gateBuf, aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "never-started"})

		prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
			aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
		require.NoError(t, err)
		require.NotNil(t, prep.prepared)
		assert.False(t, prep.prepared.boundarySafe, "an orphan finish must be unsafe")
		assert.False(t, prep.prepared.publishing(), "an orphan finish must suppress the result")
	})

	t.Run("conflicting_identity", func(t *testing.T) {
		t.Parallel()
		p := newResponsePipeline()
		attempt := pendingDrainAttempt(t, "ambiguous answer")
		p.gateBuf = append(p.gateBuf,
			aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "ordinary-1", ToolName: "get_weather"},
			aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "ordinary-1", ToolName: "get_stock"},
		)

		prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
			aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
		require.NoError(t, err)
		require.NotNil(t, prep.prepared)
		assert.False(t, prep.prepared.boundarySafe, "conflicting identities must be unsafe")
		assert.False(t, prep.prepared.publishing(), "an ambiguous identity must suppress the result")
	})

	t.Run("incomplete_item_result", func(t *testing.T) {
		t.Parallel()
		p := newResponsePipeline()
		attempt := pendingDrainAttempt(t, "incomplete answer")
		p.gateBuf = append(p.gateBuf, aipapi.Event{
			Kind: aipapi.EventItem,
			Item: &aipapi.Item{
				Kind:       aipapi.ItemKindToolResult,
				ToolResult: &aipapi.ToolResultItem{CallID: "ordinary-1", Name: "get_weather"},
				Status:     aipapi.ItemStatusInProgress,
			},
		})

		prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
			aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
		require.NoError(t, err)
		require.NotNil(t, prep.prepared)
		assert.False(t, prep.prepared.boundarySafe, "an in-progress tool result item must be unsafe")
		assert.False(t, prep.prepared.publishing(), "an incomplete ordinary result must suppress the result")
	})
}

// TestPendingDrain_activeAssemblerCallSuppressesResult pins the assembler-active
// rule: an ordinary call the enabled assembler still holds produced no canonical
// boundary at all, so it cannot be represented as evidence and must fail closed.
func TestPendingDrain_activeAssemblerCallSuppressesResult(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "assembler answer")
	assembler := newToolCallAssembler([]toolcall.Finalizer{pendingCatalogFinalizer{}}, 1024,
		[]aipapi.ToolDef{{Name: "get_weather", Parameters: []byte(`{"type":"object"}`)}})
	require.NotNil(t, assembler, "the fixture must really have an enabled assembler")
	held, err := assembler.ingest(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "ordinary-1", ToolName: "get_weather"}, toolcall.Meta{}, nil)
	require.NoError(t, err)
	require.True(t, held, "the assembler must really be holding the active call")
	attempt.toolFinal = assembler

	prep, perr := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, perr)
	require.NotNil(t, prep.prepared)
	assert.False(t, prep.prepared.boundarySafe,
		"an ordinary call still held by the enabled assembler must fail closed")
	assert.False(t, prep.prepared.publishing(), "an assembler-held call must suppress the result")
}

// TestPendingDrain_priorReasoningAndToolOutputDoNotSuppress pins the eligibility
// rule: whitespace, reasoning, tool, and media output commit the attempt but never
// prove an assistant answer exists, so they must not suppress the result.
func TestPendingDrain_priorReasoningAndToolOutputDoNotSuppress(t *testing.T) {
	t.Parallel()

	for name, prior := range map[string][]aipapi.Event{
		"whitespace_text": {{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventTextDelta, Delta: "   \n\t "}},
		"reasoning":       {{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventReasoningDelta, Delta: "thinking"}},
		"tool_call":       {{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventToolCallStarted, ToolCallID: "ordinary-1", ToolName: "get_weather"}, {Kind: aipapi.EventToolCallFinished, ToolCallID: "ordinary-1", ToolName: "get_weather"}},
		"media":           {{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventAssistantImageRef, Delta: "ref"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newResponsePipeline()
			attempt := pendingDrainAttempt(t, "eligible answer")
			p.gateBuf = append(p.gateBuf, prior...)
			prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
				aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
			require.NoError(t, err)
			require.NotNil(t, prep.prepared, "a completion with no assistant answer must stay eligible")
			assert.True(t, prep.prepared.boundarySafe,
				"%s is not an unresolved ordinary boundary", name)
			assert.True(t, prep.prepared.publishing(),
				"%s must not suppress the eligible result", name)
		})
	}
}

// TestPendingDrain_priorMeaningfulTextSuppresses pins the positive half: trimmed
// released or buffered assistant text does suppress the result.
func TestPendingDrain_priorMeaningfulTextSuppresses(t *testing.T) {
	t.Parallel()

	t.Run("released", func(t *testing.T) {
		t.Parallel()
		p := newResponsePipeline()
		attempt := pendingDrainAttempt(t, "duplicate answer")
		p.rememberClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "already answered"})
		prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
			aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
		require.NoError(t, err)
		assert.Nil(t, prep.prepared, "committed assistant text must leave nothing to publish")
		assert.Zero(t, prep.ordinary.Kind, "nothing private means no ordinary gate head either")
	})

	t.Run("buffered", func(t *testing.T) {
		t.Parallel()
		p := newResponsePipeline()
		attempt := pendingDrainAttempt(t, "duplicate answer")
		p.gateBuf = append(p.gateBuf,
			aipapi.Event{Kind: aipapi.EventResponseStarted},
			aipapi.Event{Kind: aipapi.EventMessageStarted},
			aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "buffered answer"},
		)
		prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
			aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
		require.NoError(t, err)
		assert.Nil(t, prep.prepared, "post-hook buffered assistant text must suppress the result")
		assert.Zero(t, prep.ordinary.Kind, "nothing private means no ordinary gate head either")
	})
}

// TestPendingDrain_noValidResultPreparesNothing pins that an attempt without a
// valid completion result performs no publication work at all, so the ordinary
// response path stays byte-identical.
func TestPendingDrain_noValidResultPreparesNothing(t *testing.T) {
	t.Parallel()

	for name, outcome := range map[string]controltool.OutcomeKind{
		"invalid": controltool.OutcomeInvalid,
		"unknown": controltool.OutcomeKind(99),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newResponsePipeline()
			attempt := newAttemptSession(attemptSessionInput{bleg: pendingDrainBLeg(), cand: controlCandidate("control-a")})
			attempt.controlCapture = newControlCallCapture(controlCaptureActivation(t))
			require.True(t, attempt.storeControlOutcome(controltool.Outcome{Kind: outcome, ReasonCode: "control_" + name}))
			prepared, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
				aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
			require.NoError(t, err)
			assert.Nil(t, prepared.prepared, "an unaccepted completion outcome must prepare nothing")
			assert.Zero(t, prepared.ordinary.Kind, "an unaccepted outcome leaves no ordinary gate head")
		})
	}
}

// TestPendingDrain_releasedAttemptAndEmptyResultPrepareNothing pins the lifetime
// half of the snapshot: a released attempt and an empty bounded result both
// prepare nothing, so no lexical candidate survives the attempt that owned it.
func TestPendingDrain_releasedAttemptAndEmptyResultPrepareNothing(t *testing.T) {
	t.Parallel()

	t.Run("released", func(t *testing.T) {
		t.Parallel()
		p := newResponsePipeline()
		attempt := pendingDrainAttempt(t, "released answer")
		attempt.discardControlState()
		prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
			aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
		require.NoError(t, err)
		assert.Nil(t, prep.prepared, "a released attempt owns no result")
		assert.Zero(t, prep.ordinary.Kind, "nothing private means no ordinary gate head either")
	})

	t.Run("blank_result", func(t *testing.T) {
		t.Parallel()
		p := newResponsePipeline()
		attempt := pendingDrainBlankResultAttempt(t)
		prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
			aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
		require.NoError(t, err)
		assert.Nil(t, prep.prepared, "a blank bounded result prepares nothing")
		assert.Zero(t, prep.ordinary.Kind, "nothing private means no ordinary gate head either")
	})
}

// TestPendingDrain_repeatedPrepareReturnsTheSameValue pins the once-only
// preparation: the four finish routes must converge on one prepared value, never
// prepare a second candidate, and never append the result twice.
func TestPendingDrain_repeatedPrepareReturnsTheSameValue(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "single answer")
	first, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, first.prepared)
	second, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.Same(t, first.prepared, second.prepared, "preparation must be idempotent for one logical response")
	assert.Equal(t, 1, strings.Count(strings.Join(pendingDrainLabels(first.prepared.events), "|"), "text:single answer"),
		"the result must appear exactly once in the prepared sequence")
}

// TestPendingDrain_publicationIsClaimedExactlyOnce pins the publication claim: the
// pipeline hands out the prepared value once, so a repeated terminal call, a
// competing request command, and the generic losing GateReplacement effect
// exception cannot publish the result twice.
func TestPendingDrain_publicationIsClaimedExactlyOnce(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "claimed once")
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)
	assert.False(t, p.pendingPublicationAccepted(), "nothing is published before the accepted terminal")

	claimed, claimedOnce := p.takePendingPublication()
	require.True(t, claimedOnce, "the first claim must reserve the one publication")
	require.Same(t, prep.prepared, claimed, "the first claim must hand over the prepared value")
	assert.Same(t, prep.prepared, p.pendingPreparedSnapshot(),
		"a reservation retains the frozen candidate and its origin for the whole drain")
	assert.Equal(t, 0, pendingDrainUsageCount(prep.prepared.events),
		"no operator usage was fabricated for the publication")
	assert.True(t, p.pendingPublicationAccepted(), "the claim must be visible to the finish routes")

	again, claimedAgain := p.takePendingPublication()
	assert.False(t, claimedAgain, "a repeated terminal call must find nothing to publish")
	assert.Nil(t, again, "a repeated terminal call must not hand out the candidate twice")
}

// TestPendingDrain_closedPublicationFenceSuppressesTheResult pins the Close
// winner rule at the publication fence: a closed publication window or a finished
// request terminal means no late result, and the deferred observer is finished
// conservatively instead of being left open.
func TestPendingDrain_closedPublicationFenceSuppressesTheResult(t *testing.T) {
	t.Parallel()

	for name, fence := range map[string]func() bool{
		"publication_closed": func() bool { return false },
		"terminal_finished":  func() bool { return false },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newResponsePipeline()
			attempt := pendingDrainAttempt(t, "fenced answer")
			prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
				aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
			require.NoError(t, err)
			require.NotNil(t, prep.prepared)

			observer := pendingOpenObserver(t, attempt)
			callerCtx, cancel := context.WithCancel(context.Background())
			cancel()

			cancelled := pendingPublicationFenceOpen(callerCtx, newTurnTerminal(), fence)
			assert.False(t, cancelled, "a closed fence must suppress the publication")
			// The abandoning caller finishes the deferred observer conservatively.
			p.finishFinalStreamObservation(context.Background(), attempt, "failed")
			assert.Equal(t, 1, observer.finishCount(), "the deferred observer must be finished exactly once")
			assert.NotEmpty(t, string(observer.lastOutcome()), "the conservative outcome must be recorded")
		})
	}
}

// TestPendingDrain_popDeliversPreparedEventsInOrderWithoutRepeatingEffects pins the
// typed drain entry: the release queue delivers the prepared sequence in canonical
// order and never re-runs a hook, a gate, or the recorder for an event whose
// effects already ran exactly once at publication.
func TestPendingDrain_popDeliversPreparedEventsInOrderWithoutRepeatingEffects(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "drained answer")
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)

	queue := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "drained answer"},
		{Kind: aipapi.EventResponseFinished},
	}
	// Queue-only fixture: reserve, stage, then activate, so the deliverable drain is
	// exactly the staged batch.
	claimPublication(t, p, prep.prepared)
	reservation, ok := p.reservePendingPublication(prep.prepared, false)
	require.True(t, ok, "the claimed candidate must be reservable")
	require.True(t, p.stageReservedPublication(reservation, attempt, queue, aipapi.Event{}, pendingCustomerAbsent),
		"the live reservation must install its own batch")
	require.True(t, p.activateReservedPendingPublication(prep.prepared, attempt),
		"an explicit activation must deliver the staged batch")

	var delivered []aipapi.Event
	for {
		ev, ok := p.popPendingCompletionRelease()
		if !ok {
			break
		}
		delivered = append(delivered, ev)
	}
	assert.Equal(t, pendingDrainLabels(queue), pendingDrainLabels(delivered),
		"the release queue must deliver the prepared sequence in canonical order")
	_, exhausted := p.popPendingCompletionRelease()
	assert.False(t, exhausted, "an exhausted release queue must not deliver a duplicate")
}

// TestPendingDrain_mandatoryRecorderFailureOnAnyEventLeavesNoResult pins the
// recorder preflight: a mandatory failure on the result, on a lifecycle frame, or
// on the finish leaves no client result, while the existing best-effort behavior
// stays exactly as it was.
func TestPendingDrain_mandatoryRecorderFailureOnAnyEventLeavesNoResult(t *testing.T) {
	t.Parallel()

	for name, failOn := range map[string]aipapi.EventKind{
		"lifecycle": aipapi.EventResponseStarted,
		"result":    aipapi.EventTextDelta,
		"finish":    aipapi.EventResponseFinished,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			recorder := &pendingFailingRecorder{failOn: failOn}
			p := newResponsePipeline()
			p.secureSessionRecorder = recorder
			p.secureRecordingMandatory = true

			attempt := pendingDrainAttempt(t, "recorded answer")
			prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
				aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
			require.NoError(t, err)
			require.NotNil(t, prep.prepared)

			observer := pendingOpenObserver(t, attempt)
			claimPublication(t, p, prep.prepared)

			terminal := newTurnTerminal()
			publishErr := terminal.stagePendingCompletion(context.Background(), attempt, p,
				pendingDrainFacts().terminalFacts(),
				pendingPublication{prepared: prep.prepared, facts: pendingDrainFacts(), callerCtx: context.Background()})
			require.Error(t, publishErr, "a mandatory recorder failure must surface")
			_, ok := p.popPendingCompletionRelease()
			assert.False(t, ok, "a mandatory recorder failure must leave no client result")
			assert.Equal(t, 1, observer.finishCount(),
				"the deferred observer must be finished conservatively, not left open")
		})
	}

	t.Run("best_effort", func(t *testing.T) {
		t.Parallel()
		recorder := &pendingFailingRecorder{failOn: aipapi.EventResponseFinished}
		p := newResponsePipeline()
		p.secureSessionRecorder = recorder
		p.secureRecordingMandatory = false

		attempt := pendingDrainAttempt(t, "best effort answer")
		prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
			aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
		require.NoError(t, err)
		require.NotNil(t, prep.prepared)

		claimPublication(t, p, prep.prepared)
		terminal := newTurnTerminal()
		require.NoError(t, terminal.stagePendingCompletion(context.Background(), attempt, p,
			pendingDrainFacts().terminalFacts(),
			pendingPublication{prepared: prep.prepared, facts: pendingDrainFacts(), callerCtx: context.Background()}),
			"the existing best-effort recording behavior must be preserved")
		require.True(t, p.activateReservedPendingPublication(prep.prepared, attempt),
			"a queue-only fixture activates the staged batch explicitly")

		var delivered int
		for {
			if _, ok := p.popPendingCompletionRelease(); !ok {
				break
			}
			delivered++
		}
		assert.Positive(t, delivered, "best-effort mode must still release the result")
	})
}

// TestPendingDrain_recorderSeesResultBeforeFinishInCanonicalOrder pins the
// canonical recording order: the recorder sees the complete prepared batch with
// the result strictly before the finish, and it runs once per event.
func TestPendingDrain_recorderSeesResultBeforeFinishInCanonicalOrder(t *testing.T) {
	t.Parallel()

	recorder := &pendingFailingRecorder{}
	p := newResponsePipeline()
	p.secureSessionRecorder = recorder

	attempt := pendingDrainAttempt(t, "ordered answer")
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)
	claimPublication(t, p, prep.prepared)

	terminal := newTurnTerminal()
	require.NoError(t, terminal.stagePendingCompletion(context.Background(), attempt, p,
		pendingDrainFacts().terminalFacts(),
		pendingPublication{prepared: prep.prepared, facts: pendingDrainFacts(), callerCtx: context.Background()}))

	recorded := recorder.recordedKinds()
	require.NotEmpty(t, recorded)
	textAt, finishAt := -1, -1
	for i, kind := range recorded {
		switch kind {
		case aipapi.EventTextDelta:
			if textAt < 0 {
				textAt = i
			}
		case aipapi.EventResponseFinished:
			if finishAt < 0 {
				finishAt = i
			}
		}
	}
	require.GreaterOrEqual(t, textAt, 0, "the recorder must see the result")
	require.GreaterOrEqual(t, finishAt, 0, "the recorder must see the finish")
	assert.Less(t, textAt, finishAt, "the result must be recorded strictly before the finish")
	assert.Equal(t, 1, recorder.countOf(aipapi.EventResponseFinished),
		"the finish must be recorded exactly once")
}

// TestPendingDrain_observerSeesResultBeforeUsageAndFinish pins the observation
// order the seam review requires: the attempt-owned final-stream observer sees the
// actual post-hook, post-gate result, then the finish, and its Finish runs exactly
// once afterwards.
func TestPendingDrain_observerSeesResultBeforeUsageAndFinish(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "observed answer")
	observer := pendingOpenObserver(t, attempt)
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)
	claimPublication(t, p, prep.prepared)

	terminal := newTurnTerminal()
	require.NoError(t, terminal.stagePendingCompletion(context.Background(), attempt, p,
		pendingDrainFacts().terminalFacts(),
		pendingPublication{prepared: prep.prepared, facts: pendingDrainFacts(), callerCtx: context.Background()}))

	observed := observer.observedKinds()
	textAt, finishAt := -1, -1
	for i, kind := range observed {
		switch kind {
		case aipapi.EventTextDelta:
			if textAt < 0 {
				textAt = i
			}
		case aipapi.EventResponseFinished:
			if finishAt < 0 {
				finishAt = i
			}
		}
	}
	require.GreaterOrEqual(t, textAt, 0, "the observer must see the published result")
	require.GreaterOrEqual(t, finishAt, 0, "the observer must see the finish")
	assert.Less(t, textAt, finishAt, "the observer must see the result before the finish")
	assert.Zero(t, observer.finishCount(),
		"staging only preflights the observer; its Finish is deferred to the real delivery boundary")
	assert.Equal(t, 1, observer.countOf(aipapi.EventResponseFinished),
		"the observer must see the finish exactly once")
}

// TestPendingDrain_earlyAbandonedObserverIsFinishedConservatively pins the
// losing/errored cleanup half: when the accepted publication never runs, the
// deferred observer is still finished exactly once with a conservative outcome so
// no observer session is left open.
func TestPendingDrain_earlyAbandonedObserverIsFinishedConservatively(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "abandoned answer")
	observer := pendingOpenObserver(t, attempt)
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)

	terminal := newTurnTerminal()
	terminal.abandonDeferredFinalObservation(p, attempt)
	assert.Equal(t, 1, observer.finishCount(), "an abandoning caller must finish the deferred observer")
	assert.NotEmpty(t, string(observer.lastOutcome()), "the conservative outcome must be recorded")

	// A second abandon is a no-op: the observer is never reopened.
	terminal.abandonDeferredFinalObservation(p, attempt)
	assert.Equal(t, 1, observer.finishCount(), "the observer must never be reopened")
	_, ok := p.popPendingCompletionRelease()
	assert.False(t, ok, "an abandoned publication must leave no client result")
	assert.Nil(t, p.pendingPreparedSnapshot(),
		"an abandoning caller must drop the owned candidate even before any reservation")
}

var errPendingRecorderMandatory = errors.New("pending recorder mandatory failure")

// pendingFailingRecorder is the secure-session stream recorder. It records the
// canonical order it was asked to persist and fails on one configured kind, so a
// case can prove preflight order and mandatory-versus-best-effort behavior.
type pendingFailingRecorder struct {
	mu      sync.Mutex
	failOn  aipapi.EventKind
	kinds   []aipapi.EventKind
	payload []string
}

func (r *pendingFailingRecorder) RecordClientTurnAfterGate(context.Context, secureapp.ClientTurnRecordInput) error {
	return nil
}

func (r *pendingFailingRecorder) RecordPostHookStreamEvent(_ context.Context, in secureapp.StreamEventRecordInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kinds = append(r.kinds, aipapi.EventKind(in.EventKind))
	r.payload = append(r.payload, in.EventPayloadJSON)
	if r.failOn != "" && aipapi.EventKind(in.EventKind) == r.failOn {
		return errPendingRecorderMandatory
	}
	return nil
}

func (r *pendingFailingRecorder) recordedKinds() []aipapi.EventKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]aipapi.EventKind, len(r.kinds))
	copy(out, r.kinds)
	return out
}

func (r *pendingFailingRecorder) countOf(kind aipapi.EventKind) int {
	count := 0
	for _, got := range r.recordedKinds() {
		if got == kind {
			count++
		}
	}
	return count
}

// pendingObserver is one real final-stream observation participant. It records the
// order it observed and how often it was finished.
type pendingObserver struct {
	mu        sync.Mutex
	observedK []aipapi.EventKind
	failOn    aipapi.EventKind
	onObserve func(context.Context, aipapi.Event)
	finishes  int
	outcome   response.StreamOutcome
	closed    chan struct{}
}

// pendingOpenObserver opens one real attempt-owned final-stream observation
// session with a single recording participant and returns that participant, so a
// case reads the same observer production runs rather than a stub.
func pendingOpenObserver(t *testing.T, attempt *attemptSession) *pendingObserver {
	t.Helper()
	factory := &pendingStreamObserverFactory{}
	session := &extensions.FinalStreamObservationSession{}
	require.NoError(t, session.Open(context.Background(),
		[]response.StreamObserverFactory{factory},
		response.StreamMeta{TraceID: "trace-pending-1", ALegID: "aleg-pending-1", BLegID: "bleg-pending-1"},
		response.Services{}), "the observation fixture must open")
	attempt.finalStreamObs = session
	return factory.instance()
}

// pendingStreamObserverFactory opens the one observer this file records with.
type pendingStreamObserverFactory struct{ obs *pendingObserver }

// instance returns the opened participant. It is only read after Open has run, so
// the caller never races the factory.
func (f *pendingStreamObserverFactory) instance() *pendingObserver {
	if f.obs == nil {
		f.obs = &pendingObserver{}
	}
	return f.obs
}

func (f *pendingStreamObserverFactory) ID() string                        { return "pending-observer" }
func (f *pendingStreamObserverFactory) Order() int                        { return 0 }
func (f *pendingStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (f *pendingStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	if f.obs == nil {
		f.obs = &pendingObserver{}
	}
	return f.obs, nil
}

func (o *pendingObserver) Observe(ctx context.Context, ev aipapi.Event) error {
	if o.onObserve != nil {
		o.onObserve(ctx, ev)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.observedK = append(o.observedK, ev.Kind)
	if ev.Kind == o.failOn {
		return errPendingLifetimeBoom
	}
	return nil
}

func (o *pendingObserver) Finish(_ context.Context, outcome response.StreamOutcome) error {
	o.mu.Lock()
	if o.closed == nil {
		o.closed = make(chan struct{})
	}
	if o.finishes == 0 {
		close(o.closed)
	}
	o.finishes++
	o.outcome = outcome
	o.mu.Unlock()
	return nil
}

// finished returns a channel closed by the FIRST Finish, so a case can order a
// concurrent withdrawal against work it holds blocked without polling or sleeping.
func (o *pendingObserver) finished() <-chan struct{} {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed == nil {
		o.closed = make(chan struct{})
	}
	return o.closed
}

func (o *pendingObserver) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (o *pendingObserver) observedKinds() []aipapi.EventKind {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]aipapi.EventKind, len(o.observedK))
	copy(out, o.observedK)
	return out
}

func (o *pendingObserver) countOf(kind aipapi.EventKind) int {
	count := 0
	for _, got := range o.observedKinds() {
		if got == kind {
			count++
		}
	}
	return count
}

func (o *pendingObserver) finishCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.finishes
}

func (o *pendingObserver) lastOutcome() response.StreamOutcome {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.outcome
}

func claimPublication(t *testing.T, p *responsePipeline, prepared *pendingCompletion) {
	t.Helper()
	claimed, ok := p.takePendingPublication()
	require.True(t, ok, "the publication must be reservable exactly once")
	require.Same(t, prepared, claimed, "the publication must be claimable exactly once")
}

// pendingFenceEvents is the minimal legal canonical sequence one bounded result
// publishes: a lifecycle pair, the assistant text, and the accepted finish.
func pendingFenceEvents(result string) []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: result},
		{Kind: aipapi.EventResponseFinished},
	}
}
