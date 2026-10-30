// Behavioural proof of requirement 10.6 (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Activation State /
// Protocol State / Concurrency and Lifecycle "Withdrawal leaves no durable
// protocol row or stale overlay to clean"; requirements 10.1, 10.6, 12.5).
//
// What is proven here is a BEHAVIOUR, not an absence. The cell drives THREE
// sequential client turns through the real preferred-strategy Agent Loop Guard
// generation and then reads the REAL persistence seam the request path actually
// uses: internal/core/b2bua.Store, wrapped in a recording pass-through
// decorator.
//
// Turn one opens a session. Turn two RESUMES that same session through the real
// response-carrier resume path, so both turns provably share one durable A-leg
// row (asserted from the seam's own calls). Turn three opens a FRESH session and
// therefore a different durable row.
//
// Two independent claims fall out of that one run:
//
//  1. Activation state is bounded to the admitted request/attempt. Turn two is a
//     NEW admitted request on an existing durable session, and its activation is
//     recomputed from its OWN candidate rather than read back from stored state:
//     the control tool and the normative base instruction are projected again on
//     turn two's upstream leg even though turn one, on that same row, already
//     advertised both. Turn three, admitted on a fresh session, spends its OWN
//     full bounded repair leg: exactly two upstream legs. If the missing-signal
//     reprompt counter had been remembered per session or per process instead of
//     being bounded to the admitted request and its existing continuity facts,
//     turn three would have arrived with the budget already spent and taken a
//     single leg.
//
//     Turn two legitimately takes a single leg, but NOT because the Agent Loop
//     Guard policy returned a spent protocol budget: the policy is never consulted
//     for turn two at all. Core refuses to continue a chain whose durable attempt
//     number has reached its cap BEFORE the provider runs, and that attempt number
//     is the durable B-leg sequence already advanced twice on the resumed row.
//     The spent budget is therefore CORE's continuation chain, owned by an existing
//     continuity fact — the exception requirement 10.6 allows — and the cell
//     proves that split from the seam's own rows rather than asserting it.
//
//  2. Nothing durable was written to remember the advertisement. The store
//     decorator records every call the request path makes and every row it
//     persists; the cell asserts that the seam was genuinely LIVE (ordinary
//     continuity facts really were written, on both durable rows) and that no
//     recorded value carries any control-protocol provenance: not the control
//     tool name, not the base or repair instruction text, not the strategy
//     identity, and not the opaque protocol-state token namespace. The absence
//     claim is therefore made against a seam that demonstrably carried traffic,
//     which is what makes it behavioural rather than vacuous.
//
// The reprompt counter itself rides an EXISTING continuity fact:
// terminaldecision.Input.Evidence.Lineage.ProgressRef, which the continuation
// intent's ControlRef populates. No migration, schema, SQL, or persistence file
// is added or needed for this cell to pass.
//
// Nothing provider-specific is introduced: the assertions read the same
// `attempt_completion` name and instruction markers the rest of the acceptance
// matrix already asserts, and the generation under test is the one the
// production feature registry composes.

package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/streamrecovery"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// algDurableCall is one recorded persistence-seam operation: the method the
// request path invoked plus a bounded rendering of the values it carried. The
// rendering is what the control-provenance scan reads, so it must include every
// field a durable activation marker could plausibly hide in.
//
// This repo has more than ONE durable per-session seam, so the recording must
// cover all of them, not just the A-leg identity. Recording only the A-leg id for
// the interleaved cycle-cursor seam would leave the scan blind to precisely the
// other seam a durable activation marker could hide in, which is why both
// interleaved methods below record the full canonical interleavedstate.State
// payload instead of the row id alone.
type algDurableCall struct {
	Method string
	Values []string
}

func (c algDurableCall) render() string {
	return c.Method + "(" + strings.Join(c.Values, "|") + ")"
}

// algStoreRecorder is a pass-through decorator over the deployment's real
// b2bua.Store. It records every call and delegates to the real store, so the
// turn behaves exactly as it would without the cell, and the optional
// InterleavedStateStore and ALegRetirementObserver capabilities of the wrapped
// store are preserved rather than silently dropped.
type algStoreRecorder struct {
	inner b2bua.Store

	mu    sync.Mutex
	calls []algDurableCall
}

func (s *algStoreRecorder) record(method string, values ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, algDurableCall{Method: method, Values: values})
}

func (s *algStoreRecorder) observed() []algDurableCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]algDurableCall, len(s.calls))
	copy(out, s.calls)
	return out
}

func (s *algStoreRecorder) ResolveALeg(ctx context.Context, continuityKey string) (b2bua.ALegRecord, error) {
	s.record("ResolveALeg", continuityKey)
	return s.inner.ResolveALeg(ctx, continuityKey)
}

func (s *algStoreRecorder) CreateALeg(ctx context.Context, continuityKey string) (b2bua.ALegRecord, error) {
	s.record("CreateALeg", continuityKey)
	return s.inner.CreateALeg(ctx, continuityKey)
}

func (s *algStoreRecorder) FetchALeg(ctx context.Context, aLegID string) (b2bua.ALegRecord, error) {
	s.record("FetchALeg", aLegID)
	return s.inner.FetchALeg(ctx, aLegID)
}

func (s *algStoreRecorder) SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error {
	s.record("SetWeightedFirstConsumed", aLegID, fmt.Sprint(consumed))
	return s.inner.SetWeightedFirstConsumed(ctx, aLegID, consumed)
}

func (s *algStoreRecorder) NextBLeg(ctx context.Context, aLegID string) (b2bua.BLegRecord, error) {
	s.record("NextBLeg", aLegID)
	return s.inner.NextBLeg(ctx, aLegID)
}

func (s *algStoreRecorder) RecordAttempt(ctx context.Context, rec aipapi.AttemptRecord) error {
	s.record("RecordAttempt", rec.ALegID, rec.BLegID, rec.BackendID, rec.EffectiveModel,
		string(rec.Outcome), rec.Reason)
	return s.inner.RecordAttempt(ctx, rec)
}

func (s *algStoreRecorder) LoadAttempts(ctx context.Context, aLegID string) ([]aipapi.AttemptRecord, error) {
	s.record("LoadAttempts", aLegID)
	rows, err := s.inner.LoadAttempts(ctx, aLegID)
	for _, rec := range rows {
		s.record("LoadAttemptsRow", rec.ALegID, rec.BLegID, rec.BackendID, rec.EffectiveModel,
			string(rec.Outcome), rec.Reason)
	}
	return rows, err
}

// The optional capabilities of the wrapped store are delegated so the cell never
// changes which store features the request path can reach.
func (s *algStoreRecorder) SetInterleavedState(ctx context.Context, aLegID string, state interleavedstate.State) error {
	s.record("SetInterleavedState", aLegID, algRenderInterleavedState(state))
	is, ok := s.inner.(b2bua.InterleavedStateStore)
	if !ok {
		return b2bua.ErrInterleavedStateUnsupported
	}
	return is.SetInterleavedState(ctx, aLegID, state)
}

func (s *algStoreRecorder) FetchInterleavedState(ctx context.Context, aLegID string) (interleavedstate.State, error) {
	s.record("FetchInterleavedState", aLegID)
	is, ok := s.inner.(b2bua.InterleavedStateStore)
	if !ok {
		return interleavedstate.State{}, b2bua.ErrInterleavedStateUnsupported
	}
	state, err := is.FetchInterleavedState(ctx, aLegID)
	// The row that came back is recorded too, mirroring the LoadAttemptsRow
	// pattern above: a durable activation marker could equally have been
	// smuggled out through this seam's READ side.
	s.record("FetchInterleavedStateRow", aLegID, algRenderInterleavedState(state))
	return state, err
}

// algRenderInterleavedState renders the WHOLE durable payload this repo's other
// per-session seam persists. Encoding the canonical value type rather than a
// hand-picked field list keeps the provenance scan true by construction: every
// field a marker could hide in is scanned, including fields added to
// interleavedstate.State later. The failure rendering is a fixed, content-free
// sentinel because interleavedstate.State is json-encodable by construction: an
// encoding failure is a defect to notice, never a payload to render.
func algRenderInterleavedState(state interleavedstate.State) string {
	encoded, err := json.Marshal(state)
	if err != nil {
		return "interleaved_state_unencodable"
	}
	return string(encoded)
}

func (s *algStoreRecorder) SetALegRetirementObserver(fn func(string)) {
	s.record("SetALegRetirementObserver")
	if obs, ok := s.inner.(b2bua.ALegRetirementObserver); ok {
		obs.SetALegRetirementObserver(fn)
	}
}

var (
	_ b2bua.Store                  = (*algStoreRecorder)(nil)
	_ b2bua.InterleavedStateStore  = (*algStoreRecorder)(nil)
	_ b2bua.ALegRetirementObserver = (*algStoreRecorder)(nil)
)

// algControlProvenanceMarkers are the values a durable "the tool was
// advertised" marker would have to contain. The scan below fails if ANY of them
// appears in any recorded persistence-seam value.
func algControlProvenanceMarkers() []string {
	// The concrete strategy identity is deliberately NOT listed separately: in this
	// registry the preferred strategy id and the control tool name are the same
	// literal (AgentLoopGuardStrategyAttemptCompletion == algControlToolName ==
	// "attempt_completion"), so a second entry would only re-scan for a string the
	// first entry already covers and would make the list look broader than it is.
	return []string{
		algControlToolName,           // attempt_completion: the tool name AND the strategy id
		algProtocolInstructionMarker, // <task-completion-protocol>
		algRepairInstructionMarker,   // <automated-completion-protocol-repair>
		"alg-proto-v1.",              // the opaque protocol-state token namespace
	}
}

// algInstallStoreRecorder wraps the deployment's real store and returns the
// recorder. The wrapped store must be the real in-memory continuity store the
// harness wired; the cell never substitutes a fake persistence seam.
func algInstallStoreRecorder(t *testing.T, d *Deployment) *algStoreRecorder {
	t.Helper()
	if d == nil || d.Exec == nil {
		t.Fatal("alg: no deployment executor to wrap a persistence seam around")
	}
	if d.Exec.Store == nil {
		t.Fatal("alg: the deployment wired no b2bua.Store; the persistence seam would be vacuous")
	}
	if _, ok := d.Exec.Store.(*b2bua.MemoryStore); !ok {
		t.Fatalf("alg: unexpected store type %T; the cell requires the real continuity store", d.Exec.Store)
	}
	rec := &algStoreRecorder{inner: d.Exec.Store}
	d.Exec.Store = rec
	return rec
}

// algCountDurableCalls counts recorded seam calls whose method equals method.
func algCountDurableCalls(calls []algDurableCall, method string) int {
	n := 0
	for _, c := range calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

// algPostScopedTurn posts one client turn in a named session and returns the wire
// frames plus the session carriers the response published, so the next turn can
// resume the SAME session through the real resume path instead of being treated
// as an unrelated anonymous request.
func algPostScopedTurn(ctx context.Context, t *testing.T, d *Deployment, sessionID string, previous http.Header, body string) (int, []algWireFrame, http.Header, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL()+"/v1/responses", strings.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AIP-Route", d.RouteSelector)
	// The authoritative session identity is the one the proxy issued, so a
	// resuming turn must echo the previous response's carriers rather than
	// reassert a client-chosen id the secure session never minted.
	session := strings.TrimSpace(sessionID)
	if previous != nil {
		if issued := strings.TrimSpace(previous.Get("X-AIP-Session-Id")); issued != "" {
			session = issued
		}
		for _, h := range []string{"X-AIP-Resume-Token", "X-AIP-A-Leg-Id"} {
			if v := strings.TrimSpace(previous.Get(h)); v != "" {
				req.Header.Set(h, v)
			}
		}
	}
	req.Header.Set("X-AIP-Session-Id", session)
	resp, err := d.Server.Client().Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, nil, err
	}
	var frames []algWireFrame
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var decoded struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
			Text  string `json:"text"`
		}
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			return resp.StatusCode, nil, nil, fmt.Errorf("decode sse frame: %w", err)
		}
		frames = append(frames, algWireFrame{Type: decoded.Type, Delta: decoded.Delta, Text: decoded.Text, Raw: payload})
	}
	return resp.StatusCode, frames, resp.Header, nil
}

// TestPreferredProtocolE2E_activationIsRequestScopedAndWritesNoDurableControlState
// is the behavioural proof of requirement 10.6.
//
// One deployment, the REAL preferred Agent Loop Guard generation, and the REAL
// b2bua.Store wrapped in a recording decorator. Three sequential client turns
// stop without the completion signal: turn one and turn three each spend the
// default budget of exactly one bounded repair leg (requirement 10.1), while
// turn two resumes turn one's session and admits no further continuation because
// core's own durable continuation chain for that row is already spent. The cell
// asserts that split from the seam: turn two's leg persists NO continuation-pending
// row (the policy was never asked), while turn three's first leg does.
func TestPreferredProtocolE2E_activationIsRequestScopedAndWritesNoDurableControlState(t *testing.T) {
	t.Parallel()

	// Five upstream legs: turn one's stop + repair (2), turn two's single stop on
	// the resumed A-leg (1), then turn three's stop + repair on a FRESH session
	// (2). Each admitted request therefore has to re-derive its own activation,
	// and the two legs of each repair come from the bounded budget that request
	// owns rather than from anything remembered across the durable rows.
	origin := algScriptTransport(
		algLeg{frames: algFinishedTextLeg(t, "resp_alg_scope_1", "msg_alg_scope_1", algUnmarkedText)},
		algLeg{frames: algFinishedTextLeg(t, "resp_alg_scope_2", "msg_alg_scope_2", algRepairText)},
		algLeg{frames: algFinishedTextLeg(t, "resp_alg_scope_3", "msg_alg_scope_3", algUnmarkedText)},
		algLeg{frames: algFinishedTextLeg(t, "resp_alg_scope_4", "msg_alg_scope_4", algUnmarkedText)},
		algLeg{frames: algFinishedTextLeg(t, "resp_alg_scope_5", "msg_alg_scope_5", algRepairText)},
	)

	d := Deploy(t, DeploymentSpec{
		Frontend:               FrontendOpenAIResponses,
		Backend:                BackendOpenAIResponses,
		Transport:              TransportSSE,
		OriginHandler:          origin,
		AgentLoopGuardStrategy: AgentLoopGuardStrategyAttemptCompletion,
	})
	if d == nil {
		t.Fatal("Deploy returned nil for the activation-scope cell")
	}
	// The bounded protocol-repair leg is an ordinary continuation: it needs the
	// same continuation ports and stream-recovery policy every other preferred
	// cell enables, so the repair legs below are the production repair path and
	// not a fixture shortcut.
	algWireContinuationPorts(t, d)
	d.Exec.StreamRecovery = streamrecovery.Config{Enabled: true, AllowPostOutputContinuation: true, EmitWarning: true}
	rec := algInstallStoreRecorder(t, d)

	const sessionID = "sess-alg-activation-scope"
	body := algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", true)

	ctx, cancel := context.WithTimeout(t.Context(), algCellTimeout)
	defer cancel()

	// --- turn one --------------------------------------------------------------
	firstStatus, firstFrames, carriers, err := algPostScopedTurn(ctx, t, d, sessionID, nil, body)
	if err != nil {
		t.Fatalf("turn one create: %v", err)
	}
	if firstStatus != 200 {
		t.Fatalf("turn one status = %d, want 200; wire=%s", firstStatus, algWireBody(firstFrames))
	}
	if got, want := algWireText(firstFrames), algUnmarkedText+algRepairText; got != want {
		t.Fatalf("turn one client text = %q, want exactly the two committed model texts %q", got, want)
	}
	if served := origin.algServeCount(); served != 2 {
		t.Fatalf("turn one served %d upstream legs, want exactly 2 (its own bounded repair leg)", served)
	}
	turnOne := &algUpstreamLog{bodies: origin.algAllBodies()[:2]}
	algAssertUpstreamInjectsControlTool(t, turnOne, 0)
	algAssertUpstreamCarries(t, turnOne, 0, algProtocolInstructionMarker)
	algAssertUpstreamCarries(t, turnOne, 1, algRepairInstructionMarker)
	if strings.TrimSpace(carriers.Get("X-AIP-Resume-Token")) == "" {
		t.Fatal("turn one published no resume token; turn two could not resume the same session and the per-session claim would be vacuous")
	}

	// --- turn two: SAME session, NEW admitted request --------------------------
	afterTurnOne := origin.algServeCount()
	secondStatus, secondFrames, _, err := algPostScopedTurn(ctx, t, d, sessionID, carriers, body)
	if err != nil {
		t.Fatalf("turn two create: %v", err)
	}
	if secondStatus != 200 {
		t.Fatalf("turn two status = %d, want 200; wire=%s", secondStatus, algWireBody(secondFrames))
	}
	if got, want := algWireText(secondFrames), algUnmarkedText; got != want {
		t.Fatalf("turn two client text = %q, want exactly its own committed model text %q", got, want)
	}

	all := origin.algAllBodies()
	turnTwo := &algUpstreamLog{bodies: all[afterTurnOne:]}
	if got := len(turnTwo.all()); got != 1 {
		t.Fatalf("turn two served %d upstream legs, want exactly 1; the durable grounding asserted immediately below explains why, and it is read from the persistence seam rather than inferred", got)
	}

	// --- the REAL reason turn two takes exactly one leg ------------------------
	// Turn two's single leg is NOT a spent AGENT LOOP GUARD protocol budget. The
	// terminal policy is never consulted for turn two at all: core refuses to
	// continue a chain whose durable attempt number has already reached its cap,
	// BEFORE the provider is called (internal/core/runtime/terminal_decision.go, the
	// `input.Continuation.Attempt >= maxAttempts` gate that returns
	// `continuation_budget_exhausted` without calling provider.Decide). That attempt
	// number is the durable B-leg sequence the request path allocated on the A-leg
	// row, so the spent budget really IS an existing continuity fact — it is simply
	// owned by CORE's continuation chain, not by the protocol reprompt policy.
	//
	// The four assertions below prove exactly that from the durable seam alone, so
	// the claim rests on recorded rows rather than on this comment:
	seam := rec.observed()
	resumedRows := algDistinctFirstValues(seam, "FetchALeg")
	if len(resumedRows) == 0 {
		t.Fatalf("no durable A-leg row was resolved; the turn-two grounding would be vacuous; calls=%v", seam)
	}
	resumedRow := resumedRows[0]
	// Turn one's repair leg already advanced this row twice, so turn two's leg is
	// the row's THIRD durable B-leg allocation. That durable sequence number is the
	// attempt number core's gate reads, which is why turn two admits no further
	// continuation on this row.
	if got := algCountForRow(seam, "NextBLeg", resumedRow); got != 3 {
		t.Fatalf("the resumed A-leg row must carry exactly three durable B-leg allocations (turn one's two plus turn two's own third), got %d; turn two's leg count cannot be attributed to a spent core continuation budget without that fact; calls=%v", got, seam)
	}
	// Turn two published NO continuation: no row it wrote carries the durable
	// continuation-pending reason. That is the observable consequence of the core
	// gate refusing to continue, and it is what proves the protocol policy was
	// never asked for a verdict on turn two.
	turnTwoSeam := algCallsFromNth(seam, "FetchALeg", 2)
	turnTwoReasons := algReasonsForRow(turnTwoSeam, "RecordAttempt", resumedRow, algRecordAttemptReasonValue)
	for _, reason := range turnTwoReasons {
		if reason == algContinuationPendingReason {
			t.Fatalf("turn two published a continuation, so the terminal policy WAS consulted for it; the single-leg claim cannot be attributed to core's own continuation-budget gate; reasons=%v calls=%v", turnTwoReasons, turnTwoSeam)
		}
	}
	if got := algCountForRow(turnTwoSeam, "RecordAttempt", resumedRow); got != 1 {
		t.Fatalf("turn two must persist exactly one attempt row on the resumed A-leg row, got %d; calls=%v", got, turnTwoSeam)
	}
	// Requirement 10.6, activation half: turn two's activation is recomputed from
	// its OWN admitted request. The control tool and the normative base
	// instruction are projected again on turn two's upstream leg even though turn
	// one, on this same durable A-leg row, already advertised both. Nothing was
	// read back from stored state to suppress the activation, and no stored
	// marker was needed to reproduce it.
	algAssertUpstreamInjectsControlTool(t, turnTwo, 0)
	algAssertUpstreamCarries(t, turnTwo, 0, algProtocolInstructionMarker)
	algAssertNoPrivateControlLeak(t, secondFrames)

	// --- turn three: a DIFFERENT session spends its own budget ------------------
	// Turn two's single leg was bounded by the durable continuation chain of the
	// row it resumed, asserted from the seam above. A fresh session starts that
	// chain from nothing, so it must still get a FULL budget: if the spent budget
	// had been remembered per SESSION or in any new durable protocol row rather
	// than in the existing continuity facts that own it, turn three would inherit
	// it and take no repair leg at all. Turn three really is consulted by the
	// policy, which is the other half of the contrast asserted above.
	afterTurnTwo := origin.algServeCount()
	thirdStatus, thirdFrames, _, err := algPostScopedTurn(ctx, t, d, "sess-alg-activation-scope-fresh", nil, body)
	if err != nil {
		t.Fatalf("turn three create: %v", err)
	}
	if thirdStatus != 200 {
		t.Fatalf("turn three status = %d, want 200; wire=%s", thirdStatus, algWireBody(thirdFrames))
	}
	turnThree := &algUpstreamLog{bodies: origin.algAllBodies()[afterTurnTwo:]}
	if got := len(turnThree.all()); got != 2 {
		t.Fatalf("a fresh session served %d upstream legs, want exactly 2: the bounded repair budget must be per admitted request/attempt and its existing continuity facts, never remembered per process or in a new durable protocol row", got)
	}
	algAssertUpstreamInjectsControlTool(t, turnThree, 0)
	algAssertUpstreamCarries(t, turnThree, 0, algProtocolInstructionMarker)
	algAssertUpstreamCarries(t, turnThree, 1, algRepairInstructionMarker)
	// The contrast with turn two, from the durable seam: turn three's repair leg
	// really WAS published by the terminal policy, so its first leg carries the
	// durable continuation-pending reason that turn two's single leg never does.
	// Turn three has run by now, so its footprint is read from a fresh snapshot
	// rather than from the turn-two-time snapshot used above.
	afterAllTurns := rec.observed()
	turnThreeSeam := algCallsFromNth(afterAllTurns, "FetchALeg", 3)
	freshRows := algDistinctFirstValues(turnThreeSeam, "FetchALeg")
	if len(freshRows) != 1 {
		t.Fatalf("the fresh session must resolve exactly one durable A-leg row of its own; observed=%v", freshRows)
	}
	freshReasons := algReasonsForRow(turnThreeSeam, "RecordAttempt", freshRows[0], algRecordAttemptReasonValue)
	if !algPublishedContinuation(freshReasons) {
		t.Fatalf("the fresh session's repair leg must have been published by the terminal policy, so its durable reason must include %q; reasons=%v", algContinuationPendingReason, freshReasons)
	}
	if got, want := algWireText(thirdFrames), algUnmarkedText+algRepairText; got != want {
		t.Fatalf("turn three client text = %q, want exactly the two committed model texts %q", got, want)
	}
	algAssertNoPrivateControlLeak(t, thirdFrames)

	// --- the persistence seam was live and carried no control provenance -------
	calls := rec.observed()
	attemptRows := algCountDurableCalls(calls, "RecordAttempt")
	if attemptRows == 0 {
		t.Fatalf("no attempt row reached the persistence seam; the no-durable-state claim would be vacuous; calls=%v", calls)
	}
	if algCountDurableCalls(calls, "NextBLeg") == 0 {
		t.Fatalf("no B-leg allocation reached the persistence seam; the no-durable-state claim would be vacuous; calls=%v", calls)
	}
	// Turns one and two really shared ONE durable session row, and the fresh
	// session really got a DIFFERENT one. Without both facts the per-session half
	// of the claim would be about unrelated sessions, or turn two would silently
	// be a different session than turn one.
	legs := algDistinctFirstValues(calls, "NextBLeg")
	if len(legs) != 2 {
		t.Fatalf("want exactly two durable A-leg rows (the resumed session and the fresh one); observed=%v", legs)
	}
	resumed := algDistinctFirstValues(calls, "FetchALeg")
	if len(resumed) != 2 {
		t.Fatalf("exactly two durable A-leg rows must have been resolved (the resumed session and the fresh one); observed=%v", resumed)
	}
	if got := algCountDurableCalls(calls, "FetchALeg"); got != 3 {
		t.Fatalf("each of the three admitted turns must resolve its durable session row exactly once; got %d", got)
	}
	markers := algControlProvenanceMarkers()
	for _, c := range calls {
		rendered := c.render()
		for _, m := range markers {
			if strings.Contains(rendered, m) {
				t.Fatalf("durable persistence-seam value carries control-protocol provenance %q: %s", m, rendered)
			}
		}
	}
	// The exact inventory of durable operations this run performed. It is asserted
	// AFTER the provenance scan, because the scan is the primary claim and must
	// report a smuggled marker directly instead of a count mismatch. Pinning the
	// inventory means a future change that starts routing per-session durable
	// state through ANOTHER seam method (the interleaved cycle-cursor seam is the
	// obvious one) surfaces here as a changed count instead of silently widening
	// the gap the scan above has to cover.
	if got, want := len(calls), 13; got != want {
		t.Fatalf("the persistence seam made %d calls, want exactly %d; calls=%v", got, want, calls)
	}
	if got, want := algMethodCounts(calls), map[string]int{
		"FetchALeg":     3, // one per admitted turn
		"NextBLeg":      5, // two legs on the resumed row, one on turn two's, two on the fresh row
		"RecordAttempt": 5, // one row per upstream leg
	}; !maps.Equal(got, want) {
		t.Fatalf("recorded persistence-seam inventory = %v, want %v; calls=%v", got, want, calls)
	}
	t.Logf("requirement 10.6: three admitted turns persisted %d calls (%d attempt rows) across A-legs %v (resumed session shared %v) and zero control-protocol provenance",
		len(calls), attemptRows, legs, resumed)
}

// algRecordAttemptReasonValue is the position of the durable reason inside a
// recorded RecordAttempt value list (the decorator records the A-leg id, B-leg
// id, backend id, effective model, outcome, then reason). It is the durable
// evidence of whether core published a continuation for that leg.
const algRecordAttemptReasonValue = 5

// algContinuationPendingReason is the durable reason core records for an attempt
// whose terminal decision admitted a continuation
// (internal/core/runtime/terminal_decision_continuation.go). It is the observable
// trace of a policy consultation that produced a repair leg: a leg that published
// NO continuation never records it.
const algContinuationPendingReason = "terminal_decision_continuation_pending"

// algCallsFromNth returns the recorded seam calls from the nth (1-based)
// occurrence of method onward, so a cell can isolate one admitted turn's own
// durable footprint. Each admitted turn resolves its A-leg row exactly once, so
// the nth FetchALeg starts the nth turn.
func algCallsFromNth(calls []algDurableCall, method string, nth int) []algDurableCall {
	seen := 0
	for i, c := range calls {
		if c.Method != method {
			continue
		}
		seen++
		if seen == nth {
			return calls[i:]
		}
	}
	return nil
}

// algCountForRow counts recorded method calls whose first recorded value is the
// durable row row.
func algCountForRow(calls []algDurableCall, method, row string) int {
	n := 0
	for _, c := range calls {
		if c.Method == method && len(c.Values) > 0 && c.Values[0] == row {
			n++
		}
	}
	return n
}

// algReasonsForRow returns the distinct value at valueIndex of every recorded
// method call whose first recorded value is the durable row row.
func algReasonsForRow(calls []algDurableCall, method, row string, valueIndex int) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range calls {
		if c.Method != method || len(c.Values) <= valueIndex || c.Values[0] != row || seen[c.Values[valueIndex]] {
			continue
		}
		seen[c.Values[valueIndex]] = true
		out = append(out, c.Values[valueIndex])
	}
	return out
}

// algPublishedContinuation reports whether any recorded durable reason is the
// continuation-pending reason, i.e. whether core really published a continuation
// for that turn and therefore really consulted the terminal policy.
func algPublishedContinuation(reasons []string) bool {
	return slices.Contains(reasons, algContinuationPendingReason)
}

// algMethodCounts counts recorded seam calls per method, so a cell can pin the
// exact inventory of durable operations one run performed.
func algMethodCounts(calls []algDurableCall) map[string]int {
	counts := make(map[string]int, len(calls))
	for _, c := range calls {
		counts[c.Method]++
	}
	return counts
}

// algDistinctFirstValues returns the distinct first recorded value of method, in
// first-seen order, so a cell can prove which durable rows a run actually used.
func algDistinctFirstValues(calls []algDurableCall, method string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range calls {
		if c.Method != method || len(c.Values) == 0 || seen[c.Values[0]] {
			continue
		}
		seen[c.Values[0]] = true
		out = append(out, c.Values[0])
	}
	return out
}
