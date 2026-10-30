package runtime

// Correction decision for the empty-visible-turn corrective retry (spec task 5;
// requirements 2.1, 2.2, 2.4, 4.7, 5.2, 5.4). maybeCorrectEmptyVisibleTurn
// evaluates candidacy, then the declared rails in fixed order, records the
// first decline as one bounded `skipped` decision (a disabled gate records
// nothing), and reports whether the replacement is admitted.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/stretchr/testify/require"
)

// emptyVisibleTurnRetryDecisionGate makes the completion_gates rail observable.
type emptyVisibleTurnRetryDecisionGate struct{}

func (emptyVisibleTurnRetryDecisionGate) ID() string { return "probe" }

func (emptyVisibleTurnRetryDecisionGate) Order() int { return 0 }

func (emptyVisibleTurnRetryDecisionGate) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailOpen
}

func (emptyVisibleTurnRetryDecisionGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

// emptyVisibleTurnRetryDecisionBaseSink does not implement the observer extension.
type emptyVisibleTurnRetryDecisionBaseSink struct{}

func (emptyVisibleTurnRetryDecisionBaseSink) OnAttemptRecorded(aipapi.AttemptOutcome, string) {}

func (emptyVisibleTurnRetryDecisionBaseSink) OnBackendOpenDuration(string, float64) {}

func (emptyVisibleTurnRetryDecisionBaseSink) OnTransportNegotiation(aipapi.Operation, aipapi.TransportMode, string) {
}

func (emptyVisibleTurnRetryDecisionBaseSink) OnCancellation(CancellationObservation) {}

// emptyVisibleTurnRetryDecisionSink records the optional retry observer, proving
// discovery by type assertion.
type emptyVisibleTurnRetryDecisionSink struct {
	emptyVisibleTurnRetryDecisionBaseSink
	mu        sync.Mutex
	decisions []emptyVisibleTurnRetryDecisionObservation
}

// emptyVisibleTurnRetryDecisionObservation is one optional-observer call.
type emptyVisibleTurnRetryDecisionObservation struct {
	backend string
	outcome string
}

func (s *emptyVisibleTurnRetryDecisionSink) OnEmptyVisibleTurnRetry(backend, outcome string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.decisions = append(s.decisions, emptyVisibleTurnRetryDecisionObservation{backend: backend, outcome: outcome})
}

func (s *emptyVisibleTurnRetryDecisionSink) observations() []emptyVisibleTurnRetryDecisionObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]emptyVisibleTurnRetryDecisionObservation(nil), s.decisions...)
}

// emptyVisibleTurnRetryDecisionFixture wires the real terminal, pipeline,
// accounting, and log seams with the gate pinned and the optional observer.
type emptyVisibleTurnRetryDecisionFixture struct {
	executor *Executor
	sink     *emptyVisibleTurnRetryDecisionSink
	logs     *bytes.Buffer
	context  context.Context
	stream   *retryRecvStream
	attempt  *attemptSession
	finish   aipapi.Event
}

func newEmptyVisibleTurnRetryDecisionFixture(t *testing.T, gateEnabled bool) *emptyVisibleTurnRetryDecisionFixture {
	t.Helper()
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	ex.EmptyVisibleTurnRetry = EmptyVisibleTurnRetryPolicy{Enabled: gateEnabled}
	logs := &bytes.Buffer{}
	ex.Log = slog.New(slog.NewJSONHandler(logs, nil))
	sink := &emptyVisibleTurnRetryDecisionSink{}
	ex.Metrics = sink
	base := newEmptyVisibleTurnRetryGateOffFixture(t, ex, aLegID, emptyVisibleTurnRetryGateOffCases()[0].script)
	return &emptyVisibleTurnRetryDecisionFixture{
		executor: ex,
		sink:     sink,
		logs:     logs,
		context:  context.Background(),
		stream:   base.stream,
		attempt:  base.attempt,
		finish:   emptyVisibleTurnRetryGateOffCases()[0].script[1],
	}
}

// decide runs the one decision under test and returns its admission result.
func (f *emptyVisibleTurnRetryDecisionFixture) decide(t *testing.T) bool {
	t.Helper()
	correcting, err := maybeCorrectEmptyVisibleTurn(f.context, f.stream.terminal, f.stream, f.attempt, f.finish)
	require.NoError(t, err)
	return correcting
}

// emptyVisibleTurnRetryDecisionRecords returns the captured decision records.
func emptyVisibleTurnRetryDecisionRecords(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry map[string]any
		require.NoErrorf(t, json.Unmarshal([]byte(line), &entry), "decision log line %q", line)
		if entry["msg"] == "empty_visible_turn_retry" {
			records = append(records, entry)
		}
	}
	return records
}

// emptyVisibleTurnRetryDecisionAssertSkipped asserts the one bounded record
// plus its observer call.
func emptyVisibleTurnRetryDecisionAssertSkipped(t *testing.T, f *emptyVisibleTurnRetryDecisionFixture, wantReason string) {
	t.Helper()
	emptyVisibleTurnRetryDecisionAssertRecord(t, f, wantReason)
	got := f.sink.observations()
	require.Equalf(t, []emptyVisibleTurnRetryDecisionObservation{{backend: "backend-1", outcome: "skipped"}}, got, "retry observations for %q", wantReason)
}

// emptyVisibleTurnRetryDecisionAssertRecord asserts the one bounded record.
func emptyVisibleTurnRetryDecisionAssertRecord(t *testing.T, f *emptyVisibleTurnRetryDecisionFixture, wantReason string) {
	t.Helper()
	records := emptyVisibleTurnRetryDecisionRecords(t, f.logs)
	require.Lenf(t, records, 1, "decision records for %q; logs=%s", wantReason, f.logs.String())
	record := records[0]
	require.Equal(t, "skipped", record["outcome"], "record outcome")
	require.Equal(t, wantReason, record["reason"], "record reason")
	require.Equal(t, f.stream.facts.traceID, record["trace_id"], "record trace_id")
	require.Equal(t, f.attempt.bleg.BLegID, record["b_leg_id"], "record b_leg_id")
	require.NotEmpty(t, f.attempt.bleg.BLegID, "fixture attempt must carry lineage")
}

// TestEmptyVisibleTurnRetryDecision_RailsInDeclaredOrder pins each rail against
// its real owner fact, the declared precedence, and the admitted shape.
func TestEmptyVisibleTurnRetryDecision_RailsInDeclaredOrder(t *testing.T) {
	t.Parallel()
	rails := []struct {
		name       string
		wantReason string
		setup      func(t *testing.T, f *emptyVisibleTurnRetryDecisionFixture)
	}{
		{"budget_spent", "budget_spent", func(t *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			t.Helper()
			require.True(t, f.stream.terminal.claimCorrectiveRetry(), "fixture must claim the allowance")
		}},
		{"released_frames", "released_frames", func(_ *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			f.stream.terminal.noteReleasedClientFrame(aipapi.Event{Kind: aipapi.EventResponseStarted})
		}},
		{"released_frames_committed", "released_frames", func(t *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			t.Helper()
			// An outer owner can commit a prefix that never passed this seam.
			f.stream.terminal.markCommitted(f.attempt)
			require.False(t, f.stream.terminal.releasedClientFrameLatched(), "fixture must commit without latching the local seam")
		}},
		{"withholding", "withholding", func(_ *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			f.attempt.markWithholding("lookup_customer", "instruction")
		}},
		{"hidden_thinker", "hidden_thinker", func(_ *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			f.stream.terminal.setInterleavedThinker()
		}},
		{"wire_path", "wire_path", func(_ *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			f.stream.facts.wirePayload = &wireAttemptPayload{}
		}},
		{"completion_gates", "completion_gates", func(_ *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			f.context = extensions.WithRequestRuntimeSnapshot(f.context, extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
				FeaturePlanes: freezeBundle(testFeatureBundle{CompletionGates: []completion.Gate{emptyVisibleTurnRetryDecisionGate{}}}),
			}))
		}},
		{"parallel_race", "parallel_race", func(_ *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			f.attempt.cand.IsParallel = true
		}},
		{"parallel_race_selector", "parallel_race", func(t *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			t.Helper()
			// A selector listing a parallel group declines the whole route.
			require.False(t, f.attempt.cand.IsParallel, "fixture current attempt must be sequential")
			sel, err := routing.Parse("backend-1:model-1|backend-2:model-2!backend-3:model-3")
			require.NoError(t, err)
			require.True(t, slices.ContainsFunc(routing.InitialCandidates(sel), func(c routing.AttemptCandidate) bool { return c.IsParallel }), "fixture selector must list a parallel candidate")
			f.stream.recovery = &recoveryController{sel: sel}
		}},
		{"terminal_decision_provider", "terminal_decision_provider", func(t *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			t.Helper()
			f.stream.terminal.terminalDecisionProvider = terminalDecisionProviderFunc{id: "probe", fn: func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
				t.Error("a wired terminal-decision provider must never be evaluated by this decision")
				return terminaldecision.Decision{}, nil
			}}
		}},
		{"replacement_blocked", "replacement_blocked", func(_ *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			f.stream.responsePipeline.setRecordingOutcome(responseRecordingMandatoryPostCommitFailure)
		}},
		{"attempt_budget", "attempt_budget", func(_ *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			f.stream.recovery = &recoveryController{budget: &attemptBudget{max: 1, used: 1}}
		}},
		{"hold_overflow", "hold_overflow", func(t *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			t.Helper()
			f.attempt.armCorrectionHold()
			require.True(t, f.attempt.holdCorrectionFrame(emptyVisibleTurnRetryHoldPrep(emptyVisibleTurnRetryHoldText(maxWithheldBytes))), "fixture must fill the byte bound exactly")
			require.False(t, f.attempt.holdCorrectionFrame(emptyVisibleTurnRetryHoldPrep(aipapi.Event{Kind: aipapi.EventTextDelta})), "fixture must exceed the byte bound")
		}},
		{"hold_not_armed", "hold_not_armed", func(*testing.T, *emptyVisibleTurnRetryDecisionFixture) {}},
		{"first_decline_wins_when_every_rail_applies", "budget_spent", func(t *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			t.Helper()
			f.stream.terminal.claimCorrectiveRetry()
			f.stream.terminal.noteReleasedClientFrame(aipapi.Event{Kind: aipapi.EventResponseStarted})
			f.attempt.markWithholding("lookup_customer", "instruction")
			f.stream.terminal.setInterleavedThinker()
			f.stream.facts.wirePayload = &wireAttemptPayload{}
			f.attempt.cand.IsParallel = true
			f.stream.responsePipeline.setRecordingOutcome(responseRecordingMandatoryPostCommitFailure)
			f.stream.recovery = &recoveryController{budget: &attemptBudget{max: 1, used: 1}}
		}},
	}
	for _, tc := range rails {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEmptyVisibleTurnRetryDecisionFixture(t, true)
			tc.setup(t, f)
			require.Falsef(t, f.decide(t), "a %s turn must decline the corrective retry", tc.name)
			emptyVisibleTurnRetryDecisionAssertSkipped(t, f, tc.wantReason)
		})
	}
	t.Run("admitted", func(t *testing.T) {
		t.Parallel()
		f := newEmptyVisibleTurnRetryDecisionFixture(t, true)
		f.attempt.armCorrectionHold()
		require.True(t, f.decide(t), "a fully eligible empty visible turn must be admitted")
		require.Empty(t, emptyVisibleTurnRetryDecisionRecords(t, f.logs), "an admitted correction writes no record yet")
		require.Empty(t, f.sink.observations(), "an admitted correction observes no skipped outcome")
	})
}

// TestEmptyVisibleTurnRetryDecision_CandidacyPrecedesRails pins the candidacy
// gate: non-candidates stay silent even when a rail would decline, and the
// disabled gate records nothing.
func TestEmptyVisibleTurnRetryDecision_CandidacyPrecedesRails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		gateEnabled bool
		wantCorrect bool
		setup       func(t *testing.T, f *emptyVisibleTurnRetryDecisionFixture)
	}{
		{"clean empty finish is a candidate", true, true, func(t *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			t.Helper()
			f.attempt.armCorrectionHold()
		}},
		{"reasoning-only finish is a candidate", true, true, func(t *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			t.Helper()
			f.attempt.observeProducedOutput(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "thinking"})
			f.attempt.armCorrectionHold()
		}},
		{"truncated finish never reaches the rails", true, false, func(_ *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			f.finish = aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: "incomplete", FinishReason: "max_tokens"}
			f.stream.terminal.claimCorrectiveRetry() // a rail that would decline if candidacy did not run first
		}},
		{"refusal finish never reaches the rails", true, false, func(_ *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			f.finish = aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "content_filter"}
			f.stream.terminal.claimCorrectiveRetry()
		}},
		{"produced visible output never reaches the rails", true, false, func(_ *testing.T, f *emptyVisibleTurnRetryDecisionFixture) {
			f.attempt.observeProducedOutput(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"})
			f.stream.terminal.claimCorrectiveRetry()
		}},
		{"disabled gate records nothing", false, false, func(*testing.T, *emptyVisibleTurnRetryDecisionFixture) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEmptyVisibleTurnRetryDecisionFixture(t, tc.gateEnabled)
			tc.setup(t, f)
			require.Equalf(t, tc.wantCorrect, f.decide(t), "correcting for %q", tc.name)
			require.Empty(t, emptyVisibleTurnRetryDecisionRecords(t, f.logs), "a non-decision shape must stay silent")
			require.Empty(t, f.sink.observations(), "a non-decision shape must observe nothing")
		})
	}
}

// TestEmptyVisibleTurnRetryDecision_RecordCarriesLineageOutcomeReasonOnly pins
// the bounded record: lineage, outcome, and reason only - no content, tool name,
// model identifier, or candidate key.
func TestEmptyVisibleTurnRetryDecision_RecordCarriesLineageOutcomeReasonOnly(t *testing.T) {
	t.Parallel()
	f := newEmptyVisibleTurnRetryDecisionFixture(t, true)
	f.attempt.markWithholding("lookup_customer", "SECRET-INSTRUCTION-TEXT")
	require.False(t, f.decide(t), "a withholding turn must decline")
	emptyVisibleTurnRetryDecisionAssertRecord(t, f, "withholding")
	records := emptyVisibleTurnRetryDecisionRecords(t, f.logs)
	require.Len(t, records[0], 7, "record carries time/level/msg/trace_id/b_leg_id/outcome/reason only")
	for _, banned := range []string{"model-1", "backend-1", "lookup_customer", "SECRET-INSTRUCTION-TEXT", "backend-1:model-1"} {
		require.NotContainsf(t, f.logs.String(), banned, "decision record leaks %q", banned)
	}
}
