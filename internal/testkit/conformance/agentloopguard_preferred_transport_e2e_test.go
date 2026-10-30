// Transport, cancellation, and side-effect acceptance cells for slice 10.2 of
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol; design Runtime /
// Acceptance Matrix rows 6, 7, 8, 9, 10, 11, 16 and 17; requirements 8.1-8.7 and
// 12.5).
//
// Every cell here reuses the exact seam slice 10.1 deployed — the real frontend
// handler on a real httptest origin, the real runtime executor, real backend
// adapters, real reference-provider origins, and the REAL preferred-strategy
// Agent Loop Guard generation compiled through the production feature registry,
// enabled-surface merge, and production request-snapshot builder. No provider
// fake is assigned anywhere and no control provider or terminal provider is
// hand-assigned: both are the ones the production factory composes.
//
// Nothing here rebuilds task 5.2's pending-result withdrawal matrix or task
// 4.3's control-state race cleanup. Those certified the internal lifecycle of the
// private publication and of the control state. The cells below certify the
// PROTOCOL-VISIBLE consequences of transport failure, interruption,
// cancellation, refusal, and a lost parallel race: which upstream legs exist,
// which instruction text each leg carried, what the client received on the wire,
// and which candidate was or was not opened.
//
// The transport faults are produced by the scripted origin seam rather than by
// mutating the deployment under test: a leg that ends without response_finished
// is the provider's real pre-output EOF, a leg that hijacks and closes the
// connection is a real abrupt transport death, and a leg that stops producing
// bytes is a real upstream idle. No production fault switch is introduced for
// these cells.

package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/streamrecovery"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// --- planted facts for the transport matrix -----------------------------------

// algInterruptedText is the ordinary assistant text committed before a
// transport interruption. It is deliberately different from algCompletionResult so
// "the result was published" and "the committed text survived" are separately
// observable, and from algRepairText so "a continuation leg answered" is
// distinguishable from "the interrupted leg answered".
const algInterruptedText = "verifying the backfill on the last remaining table"

// algContinuationText is the ordinary assistant text the reactivated
// continuation candidate commits after an interruption.
const algContinuationText = "the interrupted leg is resumed from the retained safe point"

// algRefusalText is the provider's authoritative refusal text. It exists so the
// refusal cell can assert the client receives the provider's own words and no
// invented completion result.
const algRefusalText = "I cannot help with that request."

// algLoserResult is the bounded result the LOSING parallel candidate emits from
// its proxy-owned control call. It is a distinct sentinel so "the loser's
// completion never reached the client" is an exact-text claim rather than a
// count of frames the fixture itself produced.
const algLoserResult = "the losing candidate must never publish this result"

// algRaceContinuationText is the ordinary assistant text the WINNING parallel
// candidate commits if — and only if — the runtime admits its own bounded
// missing-signal continuation after the race. It is distinct from every other
// planted text so the client's answer can be asserted without knowing whether that
// continuation happened.
const algRaceContinuationText = "the winning candidate resumed its own turn and finished the work"

// algFailoverCandidateText is the answer the available failover candidate would
// have produced had it been opened after client-visible commitment. It is a
// distinct sentinel so "the candidate never answered" is an exact-text claim.
const algFailoverCandidateText = "the failover candidate answered after commitment"

// algCellTimeout bounds every transport cell so a hung fault can never pass as
// a bounded outcome.
const algCellTimeout = 20 * time.Second

// --- scripted transport origin -----------------------------------------------

// algLeg is one scripted upstream response. frames are written as raw SSE data
// payloads; json is written as a complete response body; status overrides the
// default 200 with a non-streaming error document; block holds the response open
// without producing bytes after the first frame, which is a real upstream idle;
// and die hijacks and closes the connection after the frames, which is a real
// abrupt transport death rather than an orderly end of stream.
type algLeg struct {
	frames []string
	json   string
	status int
	block  time.Duration
	die    bool
	// gateAfter writes the first gateAfter frames, flushes them, and then blocks
	// on gate before writing the rest. It makes "the request is admitted and its
	// B-leg is already open" an observable, deterministic event instead of a
	// timing guess, which is what an in-flight strategy-pinning cell needs.
	gate      <-chan struct{}
	gateAfter int
	// reached is closed by the origin once this leg has begun writing, so a cell
	// can synchronize on admission without sleeping.
	reached chan<- struct{}
}

// algTransportOrigin is one multi-request reference-provider origin that serves a
// scripted leg sequence and records every request body it served. Requests past
// the end of the script repeat the final leg, so an unexpected extra upstream leg
// stays observable in the count instead of becoming an error the cell cannot
// assert on.
type algTransportOrigin struct {
	mu     sync.Mutex
	legs   []algLeg
	served int
	bodies []string
}

// algServeCount returns how many upstream requests this origin served.
func (o *algTransportOrigin) algServeCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.served
}

// algServeBody returns the body of upstream request i (0-based).
func (o *algTransportOrigin) algServeBody(i int) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if i < 0 || i >= len(o.bodies) {
		return ""
	}
	return o.bodies[i]
}

func (o *algTransportOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	o.mu.Lock()
	o.served++
	idx := o.served - 1
	leg := o.legs[min(idx, len(o.legs)-1)]
	o.bodies = append(o.bodies, string(body))
	o.mu.Unlock()

	if leg.reached != nil {
		select {
		case leg.reached <- struct{}{}:
		default:
		}
	}

	if leg.block > 0 {
		select {
		case <-time.After(leg.block):
		case <-r.Context().Done():
			return
		}
	}
	if leg.status != 0 && leg.status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(leg.status)
		_, _ = w.Write([]byte(leg.json))
		return
	}
	if leg.json != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(leg.json))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	writeFrame := func(frame string) {
		_, _ = w.Write([]byte("data: " + frame + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	for i, frame := range leg.frames {
		writeFrame(frame)
		if leg.gate != nil && i == leg.gateAfter-1 {
			select {
			case <-leg.gate:
			case <-r.Context().Done():
				return
			}
		}
	}
	if leg.gate != nil && leg.gateAfter == 0 {
		select {
		case <-leg.gate:
		case <-r.Context().Done():
			return
		}
	}
	if leg.die {
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
	}
}

// algScriptTransport returns an origin that serves legs in order.
func algScriptTransport(legs ...algLeg) *algTransportOrigin {
	return &algTransportOrigin{legs: legs}
}

// --- upstream wire fragments -------------------------------------------------

// algFrame renders one upstream SSE data payload.
func algFrame(t *testing.T, payload map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal upstream frame: %v", err)
	}
	return string(raw)
}

// algResponseCreated is the upstream response lifecycle prefix.
func algResponseCreated(t *testing.T, id string, seq int) string {
	t.Helper()
	return algFrame(t, map[string]any{
		"type": "response.created", "sequence_number": seq,
		"response": map[string]any{"id": id, "object": "response", "created_at": 1715620000, "status": "in_progress", "model": "gpt-4o-mini"},
	})
}

// algResponseCompleted is the upstream terminal frame.
func algResponseCompleted(t *testing.T, id string, seq int) string {
	t.Helper()
	return algFrame(t, map[string]any{
		"type": "response.completed", "sequence_number": seq,
		"response": map[string]any{
			"id": id, "object": "response", "created_at": 1715620000, "status": "completed", "model": "gpt-4o-mini",
			"usage": map[string]any{"input_tokens": 18, "output_tokens": 7, "total_tokens": 25},
		},
	})
}

// algMessageItemPrefix is the upstream lifecycle prefix that opens an ordinary
// assistant message item, so a later text delta is accepted by the real
// item-authority mapper.
func algMessageItemPrefix(t *testing.T, itemID string, seq int) []string {
	t.Helper()
	return []string{
		algFrame(t, map[string]any{
			"type": "response.output_item.added", "sequence_number": seq, "output_index": 0,
			"item": map[string]any{
				"type": "message", "id": itemID, "status": "in_progress", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": ""}},
			},
		}),
		algFrame(t, map[string]any{
			"type": "response.content_part.added", "sequence_number": seq + 1, "item_id": itemID,
			"output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": ""},
		}),
	}
}

// algCommitText returns the upstream frames that commit one ordinary assistant
// text exactly once.
func algCommitText(t *testing.T, itemID, text string, seq int) []string {
	t.Helper()
	prefix := algMessageItemPrefix(t, itemID, seq)
	frames := append([]string{}, prefix...)
	return append(frames,
		algFrame(t, map[string]any{
			"type": "response.output_text.delta", "sequence_number": seq + 2, "item_id": itemID,
			"output_index": 0, "content_index": 0, "delta": text,
		}),
		algFrame(t, map[string]any{
			"type": "response.output_text.done", "sequence_number": seq + 3, "item_id": itemID,
			"output_index": 0, "content_index": 0, "text": text,
		}),
		algFrame(t, map[string]any{
			"type": "response.output_item.done", "sequence_number": seq + 4, "output_index": 0,
			"item": map[string]any{
				"type": "message", "id": itemID, "status": "completed", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": text}},
			},
		}),
	)
}

// algFinishedTextLeg is one complete upstream leg: the lifecycle prefix, one
// ordinary assistant text committed exactly once, and the response completion.
func algFinishedTextLeg(t *testing.T, responseID, itemID, text string) []string {
	t.Helper()
	frames := append([]string{algResponseCreated(t, responseID, 1)}, algCommitText(t, itemID, text, 2)...)
	return append(frames, algResponseCompleted(t, responseID, 7))
}

// algOrdinaryToolCallFrames returns the upstream frames of ONE ordinary client
// tool call. complete selects whether the call is finalized; an unfinalized call
// is the incomplete-arguments boundary requirement 8.4 names.
func algOrdinaryToolCallFrames(t *testing.T, args, partial string, complete bool, seq int) []string {
	t.Helper()
	frames := []string{
		algFrame(t, map[string]any{
			"type": "response.output_item.added", "sequence_number": seq, "output_index": 1,
			"item": map[string]any{
				"type": "function_call", "id": algOrdinaryItemID, "call_id": algOrdinaryCallID,
				"name": algOrdinaryToolName, "arguments": "",
			},
		}),
	}
	if partial != "" {
		frames = append(frames, algFrame(t, map[string]any{
			"type":            "response.function_call_arguments.delta",
			"sequence_number": seq + 1, "item_id": algOrdinaryItemID, "output_index": 1, "delta": partial,
		}))
	}
	if !complete {
		return frames
	}
	return append(frames,
		algFrame(t, map[string]any{
			"type": "response.output_item.done", "sequence_number": seq + 2, "output_index": 1,
			"item": map[string]any{
				"type": "function_call", "id": algOrdinaryItemID, "call_id": algOrdinaryCallID,
				"name": algOrdinaryToolName, "arguments": args,
			},
		}),
	)
}

// algProxyControlCallFrames returns the upstream frames of the proxy-owned
// completion call carrying result. They are the only frames through which a
// bounded result may ever be published, and they must never reach the client.
func algProxyControlCallFrames(t *testing.T, result string, seq int) []string {
	t.Helper()
	return []string{
		algFrame(t, map[string]any{
			"type": "response.output_item.added", "sequence_number": seq, "output_index": 0,
			"item": map[string]any{
				"type": "function_call", "id": algControlItemID, "call_id": algControlCallID,
				"name": algControlToolName, "arguments": "",
			},
		}),
		algFrame(t, map[string]any{
			"type": "response.output_item.done", "sequence_number": seq + 1, "output_index": 0,
			"item": map[string]any{
				"type": "function_call", "id": algControlItemID, "call_id": algControlCallID,
				"name": algControlToolName, "arguments": algControlArgs(t, result),
			},
		}),
	}
}

// --- client drivers -----------------------------------------------------------

// algPostTransport drives one streaming client request over the real HTTP
// boundary and records the delivered frames into the shared trace, exactly like
// the slice 10.1 driver it reuses.
func algPostTransport(ctx context.Context, d *Deployment, body string, tr *algTrace) (int, []algWireFrame, error) {
	return algPostCreateAtPath(ctx, d, "/v1/responses", body, true, tr)
}

// algWireToolCallItems counts the client-visible frames that open one client tool
// call. It is the client-side duplicate detector for the side-effect invariant: a
// replayed or re-executed ordinary tool would open it more than once.
func algWireToolCallItems(frames []algWireFrame) int {
	n := 0
	for _, f := range frames {
		if f.Type == "response.output_item.added" && strings.Contains(f.Raw, `"type":"function_call"`) {
			n++
		}
	}
	return n
}

// algAssertNoCandidateOpened fails when a failover candidate origin was ever
// reached. It is the positive direction of the no-replay/no-failover invariant:
// a candidate that exists in the route and could have answered must not have
// been asked.
func algAssertNoCandidateOpened(t *testing.T, origin *algTransportOrigin) {
	t.Helper()
	if got := origin.algServeCount(); got != 0 {
		t.Fatalf("failover candidate was opened %d time(s); a committed client-visible attempt must never be replayed or failed over", got)
	}
}

// algAssertUpstreamLegLacks fails when ONE upstream request body carries text it
// must not. It is the index-scoped form of [algAssertUpstreamLacks], used where a
// marker is legitimately present on a later leg of the same turn.
func algAssertUpstreamLegLacks(t *testing.T, log *algUpstreamLog, i int, marker string) {
	t.Helper()
	if body := log.at(t, i); strings.Contains(body, marker) {
		t.Fatalf("upstream request %d carried %q although the protocol repair belongs only to an admitted continuation leg:\n%s", i, marker, body)
	}
}

// algAssertNoSemanticRepairAnywhere fails when any served upstream request
// carries the protocol's fixed recovery control text. The protocol writes that
// text on exactly one kind of leg, so its total absence is what proves the
// protocol did not create a competing recovery budget of its own.
func algAssertNoSemanticRepairAnywhere(t *testing.T, origins ...*algTransportOrigin) {
	t.Helper()
	for i, origin := range origins {
		for leg := 0; leg < origin.algServeCount(); leg++ {
			if body := origin.algServeBody(leg); strings.Contains(body, algRepairInstructionMarker) {
				t.Fatalf("origin %d leg %d carried the bounded protocol repair instruction although the existing recovery owner must stay authoritative:\n%s", i, leg, body)
			}
		}
	}
}

// algPostCompletionBody is the A-leg document of a turn whose retained input
// already contains one COMPLETED ordinary client tool call and the client's
// answer to it. That is exactly the retention fact requirement 8.3 names.
func algPostCompletionBody(t *testing.T, stream bool) string {
	t.Helper()
	return algCreateBodyWith(t, []any{
		algEchoedToolCallInput(algOrdinaryCallID, algOrdinaryToolName, algOrdinaryToolArgs),
		algToolResultInput(algOrdinaryCallID, algToolResultOutput),
		map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "apply the schema, verify the backfill, then report the result"}},
		},
	}, stream, algOrdinaryToolName)
}

// =============================================================================
// Requirement 8.1 / matrix row 8 — pre-output EOF and idle
// =============================================================================

// algRunPreOutputRecoveryCell is the shared body of the two pre-output cells.
//
// Requirement 8.1 is a negative invariant about the protocol and a positive one
// about the existing recovery: the transport fault happens BEFORE any meaningful
// output commitment, so the pre-existing pre-output recovery must still own it,
// and the completion protocol must add no competing replay budget of its own.
//
// The cell pins both directions. It asserts that the generic recovery really ran,
// by requiring the failover candidate to have been opened exactly once and its
// answer to be the client's answer; and it asserts that the protocol stayed out,
// by requiring that the fixed bounded repair instruction — production text the
// proxy writes on exactly one kind of leg — appears on NO leg of either origin,
// while the base protocol instruction was advertised on the leg that was
// interrupted, so the protocol genuinely had the opportunity to compete and
// declined. A competing replay budget would add a further leg carrying that
// marker, which both origin counts and the marker absence detect.
func algRunPreOutputRecoveryCell(t *testing.T, fault string, primary *algTransportOrigin, recovery streamrecovery.Config) {
	t.Helper()

	candidateFrames := append(
		[]string{algResponseCreated(t, "resp_alg_preout_candidate", 1)},
		algProxyControlCallFrames(t, algCompletionResult, 2)...)
	candidate := algScriptTransport(algLeg{
		frames: append(candidateFrames, algResponseCompleted(t, "resp_alg_preout_candidate", 4)),
	})

	d, tr := algDeployColumn(t, algColumn{
		Strategy:  AgentLoopGuardStrategyAttemptCompletion,
		Frontend:  FrontendOpenAIResponses,
		Backend:   BackendOpenAIResponses,
		Transport: TransportSSE,
		Origin:    primary,
		Candidates: []Candidate{
			{Backend: BackendOpenAIResponses, OriginHandler: candidate},
		},
	})
	d.Exec.StreamRecovery = recovery

	ctx, cancel := context.WithTimeout(t.Context(), algCellTimeout)
	defer cancel()
	status, frames, err := algPostTransport(ctx, d, algCreateBodyWith(t, algOrdinaryToolInput, true, algOrdinaryToolName), tr)
	if err != nil {
		t.Fatalf("%s: create stream: %v", fault, err)
	}
	if status != http.StatusOK {
		t.Fatalf("%s: status = %d, want the existing pre-output recovery to fail over to the next candidate; wire=%s", fault, status, algWireBody(frames))
	}

	// The generic recovery really ran: the interrupted candidate was retried
	// once, through the existing failover path, and the next candidate answered.
	if got := primary.algServeCount(); got != 1 {
		t.Fatalf("%s: interrupted candidate served %d requests, want exactly 1", fault, got)
	}
	if got := candidate.algServeCount(); got != 1 {
		t.Fatalf("%s: failover candidate served %d requests, want exactly 1 so the existing recovery owns the failure", fault, got)
	}
	if got := algWireText(frames); got != algCompletionResult {
		t.Fatalf("%s: client assistant text = %q, want exactly the completing candidate's bounded result %q", fault, got, algCompletionResult)
	}

	// The protocol was active on the interrupted leg, so it could have competed.
	algAssertUpstreamCarries(t, &algUpstreamLog{bodies: []string{primary.algServeBody(0)}}, 0, algProtocolInstructionMarker)

	// Requirement 8.1: it did not. No leg of either origin carries the bounded
	// repair instruction, so the protocol added no competing replay budget.
	algAssertNoSemanticRepairAnywhere(t, primary, candidate)
	algAssertNoPrivateControlStage(t, tr)
	algAssertNoPrivateControlLeak(t, frames)
}

// TestPreferredProtocolTransportE2E_preOutputEOFLeavesExistingRecoveryAuthoritative
// is matrix row 8's EOF half: the provider's stream ends cleanly without
// response_finished and before any output commitment.
func TestPreferredProtocolTransportE2E_preOutputEOFLeavesExistingRecoveryAuthoritative(t *testing.T) {
	t.Parallel()

	primary := algScriptTransport(algLeg{frames: []string{algResponseCreated(t, "resp_alg_preout_eof", 1)}})
	algRunPreOutputRecoveryCell(t, "pre-output EOF",
		primary,
		streamrecovery.Config{Enabled: true, EmitWarning: true})
}

// TestPreferredProtocolTransportE2E_preOutputIdleLeavesExistingRecoveryAuthoritative
// is matrix row 8's idle half: the provider opens the response and then stops
// producing bytes, so the existing idle deadline — not the protocol — must own the
// failure.
func TestPreferredProtocolTransportE2E_preOutputIdleLeavesExistingRecoveryAuthoritative(t *testing.T) {
	t.Parallel()

	primary := algScriptTransport(algLeg{
		frames: []string{algResponseCreated(t, "resp_alg_preout_idle", 1)},
		block:  400 * time.Millisecond,
	})
	algRunPreOutputRecoveryCell(t, "pre-output idle",
		primary,
		streamrecovery.Config{Enabled: true, IdleTimeout: 25 * time.Millisecond, EmitWarning: true})
}

// =============================================================================
// Requirement 8.2 / matrix row 9 — post-output interruption, active protocol
// =============================================================================

// TestPreferredProtocolTransportE2E_postOutputInterruptionContinuesWithoutReplaying
// is matrix row 9 and the first headline invariant, requirement 8.2 with 8.7.
//
// The provider commits ordinary assistant text to the client and then dies
// abruptly. Post-output continuation is enabled, the completion protocol is
// active on that leg, and no completion signal was observed, so requirement 8.2
// and the design's Transport/Continuation Interaction allow exactly one new
// continuation leg built from the retained trajectory.
//
// The cell pins the whole contract in both directions:
//
//   - the continuation was admitted: exactly two upstream legs exist and the
//     second one carries the fixed bounded repair control text;
//   - the committed attempt was never replayed: the client's assistant text is
//     the interrupted leg's text exactly once, the continuation leg's own text is
//     not a second copy of it, and the number of upstream legs is exactly the
//     one admitted continuation rather than an unbounded sequence;
//   - the bounded result was NOT surfaced on top of already committed assistant
//     text (requirement 6.4), which is what makes "no duplicate answer" exact
//     rather than approximate.
func TestPreferredProtocolTransportE2E_postOutputInterruptionContinuesWithoutReplaying(t *testing.T) {
	t.Parallel()

	interrupted := append(
		[]string{algResponseCreated(t, "resp_alg_interrupted", 1)},
		algCommitText(t, "msg_alg_interrupted", algInterruptedText, 2)...)
	resumed := algFinishedTextLeg(t, "resp_alg_resumed", "msg_alg_resumed", algContinuationText)
	origin := algScriptTransport(
		algLeg{frames: interrupted, die: true},
		algLeg{frames: resumed},
	)

	d, tr := algDeployPreferred(t, TransportSSE, origin)
	d.Exec.StreamRecovery = streamrecovery.Config{Enabled: true, AllowPostOutputContinuation: true, EmitWarning: true}

	ctx, cancel := context.WithTimeout(t.Context(), algCellTimeout)
	defer cancel()
	status, frames, err := algPostTransport(ctx, d, algCreateBodyWith(t, algOrdinaryToolInput, true, algOrdinaryToolName), tr)
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; wire=%s", status, algWireBody(frames))
	}

	// Requirement 8.2: exactly one admitted continuation leg, no unbounded
	// sequence, and the protocol's own bounded recovery intent on that leg.
	algAssertBoundedUpstreamCount(t, &algUpstreamLog{bodies: origin.algAllBodies()}, 2)
	algAssertUpstreamCarries(t, &algUpstreamLog{bodies: origin.algAllBodies()}, 1, algRepairInstructionMarker)
	algAssertUpstreamCarries(t, &algUpstreamLog{bodies: origin.algAllBodies()}, 1, algRepairSignalClause)
	// The interrupted leg advertised the base protocol, so the continuation was a
	// decision of an ACTIVE protocol rather than of an inactive one.
	algAssertUpstreamCarries(t, &algUpstreamLog{bodies: origin.algAllBodies()}, 0, algProtocolInstructionMarker)

	// The committed attempt was never replayed: the client sees the interrupted
	// leg's text exactly once and the continuation leg's text exactly once, in
	// that order, and no copy of either.
	if got, want := algWireText(frames), algInterruptedText+algContinuationText; got != want {
		t.Fatalf("client assistant text = %q, want exactly the two legs' own committed texts %q; a replayed attempt would repeat one of them", got, want)
	}
	if n := strings.Count(algWireText(frames), algInterruptedText); n != 1 {
		t.Fatalf("the interrupted attempt's committed text appears %d times on the wire, want exactly 1", n)
	}
	// Requirement 6.4: an already committed assistant answer is never duplicated by
	// the completion result.
	if strings.Contains(algWireBody(frames), algCompletionResult) {
		t.Fatalf("a completion result was surfaced on top of already committed assistant text:\n%s", algWireBody(frames))
	}
	algAssertNoPrivateControlStage(t, tr)
	algAssertNoPrivateControlLeak(t, frames)
}

// =============================================================================
// Requirement 8.3 / matrix row 10 — completed client tool/result retention
// =============================================================================

// TestPreferredProtocolTransportE2E_interruptionRetainsCompletedClientToolResult
// is matrix row 10 and requirement 8.3.
//
// The A-leg already carries one COMPLETED ordinary client tool call together with
// the client's answer to it. The provider commits assistant text and then dies.
// Requirement 8.3 requires that the continuation preserve those completed
// client facts and must not request their re-execution solely because the later
// stream failed.
//
// The retention direction is asserted on the only place a retained fact can
// survive: the SECOND, separately constructed upstream request. It is an
// independent HTTP document produced by the continuation transaction's own
// conversation projection, so an exact occurrence count of the client's tool
// result and its call identity in that body is real evidence that the fact
// crossed the interruption. The re-execution direction is asserted on the client
// wire: the client must never be handed a tool call for the tool it already
// answered.
func TestPreferredProtocolTransportE2E_interruptionRetainsCompletedClientToolResult(t *testing.T) {
	t.Parallel()

	interrupted := append(
		[]string{algResponseCreated(t, "resp_alg_retain_1", 1)},
		algCommitText(t, "msg_alg_retain_1", algInterruptedText, 2)...)
	resumed := algFinishedTextLeg(t, "resp_alg_retain_2", "msg_alg_retain_2", algContinuationText)
	origin := algScriptTransport(
		algLeg{frames: interrupted, die: true},
		algLeg{frames: resumed},
	)

	d, tr := algDeployPreferred(t, TransportSSE, origin)
	d.Exec.StreamRecovery = streamrecovery.Config{Enabled: true, AllowPostOutputContinuation: true, EmitWarning: true}

	ctx, cancel := context.WithTimeout(t.Context(), algCellTimeout)
	defer cancel()
	status, frames, err := algPostTransport(ctx, d, algPostCompletionBody(t, true), tr)
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; wire=%s", status, algWireBody(frames))
	}
	upstream := &algUpstreamLog{bodies: origin.algAllBodies()}
	algAssertBoundedUpstreamCount(t, upstream, 2)
	algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)

	// Requirement 8.3, retention direction: the completed client tool call and
	// the client's answer to it are still present, exactly once each, on the
	// continuation leg's independently built request. A dropped ingress, a
	// truncated projection, or a duplicated carry-forward all change these counts.
	continuation := origin.algServeBody(1)
	if got := strings.Count(continuation, algToolResultOutput); got != 1 {
		t.Fatalf("the retained client tool result appears %d times on the continuation leg, want exactly 1; body=%s", got, continuation)
	}
	if got := strings.Count(continuation, algOrdinaryCallID); got < 1 {
		t.Fatalf("the retained completed client tool call identity is absent from the continuation leg; body=%s", continuation)
	}
	if strings.Contains(continuation, algRepairInstructionMarker+`"result"`) {
		t.Fatalf("the recovery instruction must not carry completed client tool facts as replayable work:\n%s", continuation)
	}

	// Requirement 8.3, re-execution direction: the client is never handed a tool
	// call for the tool it already answered, on either leg.
	if n := algWireToolCallItems(frames); n != 0 {
		t.Fatalf("the client was handed %d ordinary tool call(s) although it already answered that tool; re-execution was requested", n)
	}
	if strings.Contains(algWireBody(frames), algOrdinaryToolName) {
		t.Fatalf("the already answered ordinary tool reappeared on the client wire:\n%s", algWireBody(frames))
	}
	algAssertOrdinaryToolLifecycle(t, tr, algOrdinaryToolName, 0, 0)
	algAssertNoPrivateControlStage(t, tr)
	algAssertNoPrivateControlLeak(t, frames)
}

// =============================================================================
// Requirement 8.7 / matrix row 10 — no duplicate ordinary tool side effects
// =============================================================================

// TestPreferredProtocolTransportE2E_noDuplicateOrdinaryToolSideEffectsAcrossContinuation
// is the second headline invariant of this task, requirement 8.7 with 8.3.
//
// The provider commits ONE ordinary client tool call and finalizes it, then dies
// abruptly with no assistant text at all. That tool call is itself the first
// client-visible commitment, so the attempt is committed and the turn continues
// on a reactivated continuation leg that commits assistant text instead.
//
// The invariant is asserted twice and independently, because a duplicate side
// effect has two distinct observable shapes:
//
//   - internally, the real response-part hook and the real final-stream
//     observation each saw exactly one ordinary tool start and one ordinary tool
//     finish, so no second canonical lifecycle was produced at all;
//   - on the client wire, exactly one frame opened the ordinary tool call, so the
//     client is never handed the same executable tool call twice.
//
// A replay of the committed attempt, a failover to a second candidate, or a
// duplicated pending publication would each break at least one of the two, and
// the cell also asserts that the continuation leg really did run, so a pass
// cannot come from the turn simply ending early.
func TestPreferredProtocolTransportE2E_noDuplicateOrdinaryToolSideEffectsAcrossContinuation(t *testing.T) {
	t.Parallel()

	committedTool := append(
		[]string{algResponseCreated(t, "resp_alg_sideeffect_1", 1)},
		algOrdinaryToolCallFrames(t, algOrdinaryToolArgs, algOrdinaryToolArgs, true, 2)...)
	resumed := algFinishedTextLeg(t, "resp_alg_sideeffect_2", "msg_alg_sideeffect_2", algContinuationText)
	origin := algScriptTransport(
		algLeg{frames: committedTool, die: true},
		algLeg{frames: resumed},
	)

	d, tr := algDeployPreferred(t, TransportSSE, origin)
	d.Exec.StreamRecovery = streamrecovery.Config{Enabled: true, AllowPostOutputContinuation: true, EmitWarning: true}

	ctx, cancel := context.WithTimeout(t.Context(), algCellTimeout)
	defer cancel()
	status, frames, err := algPostTransport(ctx, d, algCreateBodyWith(t, algOrdinaryToolInput, true, algOrdinaryToolName), tr)
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; wire=%s", status, algWireBody(frames))
	}

	// The turn really continued, so the exactly-once claims below are about a turn
	// that spanned two upstream legs rather than about a turn that ended early.
	upstream := &algUpstreamLog{bodies: origin.algAllBodies()}
	algAssertBoundedUpstreamCount(t, upstream, 2)
	algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)

	// Internal direction: one canonical ordinary lifecycle per stage, no more.
	algAssertOrdinaryToolLifecycle(t, tr, algOrdinaryToolName, 1, 1)

	// Client direction: the executable tool call reached the client exactly once.
	if n := algWireToolCallItems(frames); n != 1 {
		t.Fatalf("client-visible ordinary tool call frames = %d, want exactly 1; a duplicate side effect would deliver the tool twice; frames=%+v", n, frames)
	}
	if got, want := algWireText(frames), algContinuationText; got != want {
		t.Fatalf("client assistant text = %q, want exactly the continuation leg's committed text %q", got, want)
	}
	// Requirement 3.5 in this cell's own vocabulary: the ordinary tool call is
	// legitimately present, but no proxy-owned control fact may appear beside it.
	algAssertNoPrivateControlStage(t, tr)
	algAssertNoPrivateControlFacts(t, frames)
	if strings.Contains(algWireBody(frames), algControlToolName) {
		t.Fatalf("the proxy-owned control tool reached the client beside an ordinary tool call:\n%s", algWireBody(frames))
	}
}

// =============================================================================
// Requirement 8.7 / matrix row 9 — no replay or failover after commitment
// =============================================================================

// TestPreferredProtocolTransportE2E_noFailoverOrReplayAfterClientVisibleCommitment
// is the first headline invariant of this task in its strongest form, requirement
// 8.7.
//
// A second real failover candidate is present in the route and would answer if it
// were opened. The primary candidate commits ordinary assistant text to the
// client and then dies abruptly. Post-output continuation is ENABLED, so the turn
// is explicitly permitted to continue — and the cell asserts that the continuation
// it takes is a continuation ON THE SAME CANDIDATE (the second leg reaches the
// primary origin and carries the protocol's own repair instruction), while the
// failover candidate origin was never reached at all.
//
// That distinction is the invariant: a continuation preserves the attempt's
// candidate identity, while a replay or failover would have opened the other
// candidate. Asserting the candidate origin's request count is zero therefore
// fails loudly on any regression that turns the post-output path back into a
// transparent retry, and it cannot pass vacuously because the cell also asserts
// the continuation leg really exists.
func TestPreferredProtocolTransportE2E_noFailoverOrReplayAfterClientVisibleCommitment(t *testing.T) {
	t.Parallel()

	interrupted := append(
		[]string{algResponseCreated(t, "resp_alg_commit_1", 1)},
		algCommitText(t, "msg_alg_commit_1", algInterruptedText, 2)...)
	resumed := algFinishedTextLeg(t, "resp_alg_commit_2", "msg_alg_commit_2", algContinuationText)
	primary := algScriptTransport(
		algLeg{frames: interrupted, die: true},
		algLeg{frames: resumed},
	)
	candidate := algScriptTransport(algLeg{
		frames: algFinishedTextLeg(t, "resp_alg_commit_candidate", "msg_alg_commit_candidate", algFailoverCandidateText),
	})

	d, tr := algDeployColumn(t, algColumn{
		Strategy:  AgentLoopGuardStrategyAttemptCompletion,
		Frontend:  FrontendOpenAIResponses,
		Backend:   BackendOpenAIResponses,
		Transport: TransportSSE,
		Origin:    primary,
		Candidates: []Candidate{
			{Backend: BackendOpenAIResponses, OriginHandler: candidate},
		},
	})
	d.Exec.StreamRecovery = streamrecovery.Config{Enabled: true, AllowPostOutputContinuation: true, EmitWarning: true}

	ctx, cancel := context.WithTimeout(t.Context(), algCellTimeout)
	defer cancel()
	status, frames, err := algPostTransport(ctx, d, algCreateBodyWith(t, algOrdinaryToolInput, true, algOrdinaryToolName), tr)
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; wire=%s", status, algWireBody(frames))
	}

	// Requirement 8.7: the second candidate exists, could have answered, and was
	// never asked.
	algAssertNoCandidateOpened(t, candidate)

	// The turn did continue — as a continuation on the committed candidate, not as
	// a replay and not as a failover.
	algAssertBoundedUpstreamCount(t, &algUpstreamLog{bodies: primary.algAllBodies()}, 2)
	algAssertUpstreamCarries(t, &algUpstreamLog{bodies: primary.algAllBodies()}, 1, algRepairInstructionMarker)

	// The candidate's answer never appeared, and the committed attempt's answer was
	// delivered exactly once.
	if strings.Contains(algWireBody(frames), algFailoverCandidateText) {
		t.Fatalf("a failover candidate answered after client-visible commitment:\n%s", algWireBody(frames))
	}
	if got, want := algWireText(frames), algInterruptedText+algContinuationText; got != want {
		t.Fatalf("client assistant text = %q, want exactly the two committed texts %q", got, want)
	}
	algAssertNoPrivateControlStage(t, tr)
	algAssertNoPrivateControlLeak(t, frames)
}

// =============================================================================
// Requirement 8.4 / matrix row 11 — incomplete client tool arguments
// =============================================================================

// TestPreferredProtocolTransportE2E_incompleteClientToolArgsStopConservatively is
// matrix row 11 and requirement 8.4.
//
// The provider opens ONE ordinary client tool call and streams a partial
// argument fragment, then dies without ever finalizing the call. That is the
// unsafe boundary requirement 8.4 names: an incomplete ordinary tool argument,
// which makes safe continuation impossible.
//
// The cell pins the conservative stop. The unresolved boundary itself is client
// visible (the real tool lifecycle started and the partial arguments crossed the
// boundary), yet the protocol must not read the resulting interruption as missing
// completion work: no continuation leg exists, the bounded repair instruction
// appears on no leg, and no completion result is published on top of an
// unresolved tool boundary. Every one of those is a claim about production
// behaviour, not about the fixture: the leg count, the instruction text, and the
// published result are all produced by the protocol and the runtime, never by the
// scripted origin.
func TestPreferredProtocolTransportE2E_incompleteClientToolArgsStopConservatively(t *testing.T) {
	t.Parallel()

	partial := algOrdinaryToolCallFrames(t, "", `{"scope":"ful`, false, 2)
	origin := algScriptTransport(
		algLeg{frames: append([]string{algResponseCreated(t, "resp_alg_partial", 1)}, partial...), die: true},
		algLeg{frames: algFinishedTextLeg(t, "resp_alg_partial_2", "msg_alg_partial_2", algContinuationText)},
	)

	d, tr := algDeployPreferred(t, TransportSSE, origin)
	d.Exec.StreamRecovery = streamrecovery.Config{Enabled: true, AllowPostOutputContinuation: true, EmitWarning: true}

	ctx, cancel := context.WithTimeout(t.Context(), algCellTimeout)
	defer cancel()
	status, frames, err := algPostTransport(ctx, d, algCreateBodyWith(t, algOrdinaryToolInput, true, algOrdinaryToolName), tr)
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want the conservative stop; wire=%s", status, algWireBody(frames))
	}

	// The unresolved boundary really is client visible, so the stop below is a
	// decision about a live tool boundary rather than about a quiet failure.
	algOrdinaryToolStartedAndLeftUnfinished(t, tr, frames, algOrdinaryToolName)

	// Requirement 8.4: conservative stop. No continuation leg, no repair
	// instruction anywhere, no invented result.
	upstream := &algUpstreamLog{bodies: origin.algAllBodies()}
	algAssertBoundedUpstreamCount(t, upstream, 1)
	algAssertNoSemanticRepairAnywhere(t, origin)
	if strings.Contains(algWireBody(frames), algCompletionResult) {
		t.Fatalf("a completion result was published on top of an unresolved ordinary tool boundary:\n%s", algWireBody(frames))
	}
	// Requirement 3.5 in this cell's own vocabulary: the ordinary tool call is
	// legitimately present on the wire, but no proxy-owned control fact may appear
	// beside it and the protocol must not have executed a control call either.
	algAssertNoPrivateControlStage(t, tr)
	algAssertNoPrivateControlFacts(t, frames)
	if strings.Contains(algWireBody(frames), algControlToolName) {
		t.Fatalf("the proxy-owned control tool reached the client:\n%s", algWireBody(frames))
	}
}

// algOrdinaryToolStartedAndLeftUnfinished proves the interrupted ordinary tool
// call crossed both internal stages and the client boundary as a START and was
// never finalized. It is the positive form of the unsafe boundary requirement 8.4
// names: the provider opened the call and streamed a partial argument fragment,
// and nothing in the pipeline may present it as a completed executable tool.
func algOrdinaryToolStartedAndLeftUnfinished(t *testing.T, tr *algTrace, frames []algWireFrame, name string) {
	t.Helper()
	items := tr.snapshot()
	hookStart, obsStart := -1, -1
	for _, it := range items {
		if it.kind != string(aipapi.EventToolCallStarted) || it.tool != name {
			continue
		}
		switch it.stage {
		case algTraceStageHook:
			if hookStart < 0 {
				hookStart = it.seq
			}
		case algTraceStageObserve:
			if obsStart < 0 {
				obsStart = it.seq
			}
		}
	}
	if hookStart < 0 || obsStart < 0 {
		t.Fatalf("the interrupted tool start never crossed both internal stages; trace=%+v", items)
	}
	if hookStart >= obsStart {
		t.Fatalf("the tool start must pass the response-part hook before the observation stage; hook=%d observe=%d trace=%+v", hookStart, obsStart, items)
	}
	starts, finishes := algOrdinaryLifecycleCounts(t, tr, name)
	if starts != 1 {
		t.Fatalf("ordinary tool starts observed = %d, want exactly 1", starts)
	}
	if finishes != 0 {
		t.Fatalf("ordinary tool finishes observed = %d, want 0; the provider never finalized the call, so a finish here would mean the pipeline invented one; trace=%+v", finishes, items)
	}
	if n := algWireToolCallItems(frames); n != 1 {
		t.Fatalf("client-visible ordinary tool call frames = %d, want exactly 1; frames=%+v", n, frames)
	}
	if !strings.Contains(algWireBody(frames), `"name":"`+name+`"`) {
		t.Fatalf("the interrupted ordinary tool call never reached the client as an ordinary tool event; wire=%s", algWireBody(frames))
	}
}

// algOrdinaryLifecycleCounts returns the ordinary tool lifecycle starts and
// finishes the real final-stream observation stage recorded for name.
func algOrdinaryLifecycleCounts(t *testing.T, tr *algTrace, name string) (starts, finishes int) {
	t.Helper()
	for _, it := range tr.snapshot() {
		if it.stage != algTraceStageObserve {
			continue
		}
		switch it.kind {
		case string(aipapi.EventToolCallStarted):
			if it.tool == name {
				starts++
			}
		case string(aipapi.EventToolCallFinished):
			finishes++
		}
	}
	return starts, finishes
}

// =============================================================================
// Requirement 8.5 / matrix row 6 — client cancellation
// =============================================================================

// algRunCancellationCell is the shared body of the two cancellation cells.
//
// Requirement 8.5 is a negative invariant: when the client cancels, the proxy must
// never convert the absent `attempt_completion` into automatic continuation. The
// protocol is active for the whole leg, so it has both the expectation and the
// budget to request a bounded repair; it must not.
//
// The cell asserts that no continuation leg was ever opened on either origin and
// that the fixed repair instruction — which only the protocol's own recovery
// intent writes — appears on no leg. That is a claim about production behaviour
// derived from the protocol's own control text, so it cannot pass because the
// fixture chose not to script a second leg.
func algRunCancellationCell(t *testing.T, name string, toolLeg algLeg) {
	t.Helper()

	origin := algScriptTransport(toolLeg, algLeg{
		frames: algFinishedTextLeg(t, "resp_alg_cancel_second", "msg_alg_cancel_second", algContinuationText),
	})

	d, tr := algDeployPreferred(t, TransportSSE, origin)
	d.Exec.StreamRecovery = streamrecovery.Config{Enabled: true, AllowPostOutputContinuation: true, EmitWarning: true}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		time.Sleep(250 * time.Millisecond)
		cancel()
	}()

	_, frames, err := algPostTransport(ctx, d, algCreateBodyWith(t, algOrdinaryToolInput, true, algOrdinaryToolName), tr)
	if err == nil {
		t.Fatalf("%s: a cancelled client request must not complete normally; wire=%s", name, algWireBody(frames))
	}
	if !strings.Contains(err.Error(), "context canceled") && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("%s: cancellation error = %v, want the client-observed cancellation", name, err)
	}

	// Give any wrongly admitted continuation leg a chance to appear before the
	// count is asserted, so a late continuation cannot hide behind the client
	// already being gone.
	time.Sleep(200 * time.Millisecond)

	algAssertBoundedUpstreamCount(t, &algUpstreamLog{bodies: origin.algAllBodies()}, 1)
	algAssertNoSemanticRepairAnywhere(t, origin)
	// Requirement 8.5 with requirement 4.6: no completion signal was invented for
	// the cancelled turn.
	if strings.Contains(algWireBody(frames), algCompletionResult) {
		t.Fatalf("%s: a completion result was published for a cancelled turn:\n%s", name, algWireBody(frames))
	}
	algAssertNoPrivateControlStage(t, tr)
}

// TestPreferredProtocolTransportE2E_clientCancellationStartsNoProtocolRecovery is
// matrix row 6's active-protocol half: the provider holds the stream open after
// committing assistant text, and the client cancels mid-stream.
func TestPreferredProtocolTransportE2E_clientCancellationStartsNoProtocolRecovery(t *testing.T) {
	t.Parallel()

	committed := append(
		[]string{algResponseCreated(t, "resp_alg_cancel", 1)},
		algCommitText(t, "msg_alg_cancel", algInterruptedText, 2)...)
	algRunCancellationCell(t, "cancellation with committed output",
		algLeg{frames: committed, block: 3 * time.Second})
}

// TestPreferredProtocolTransportE2E_clientCancellationWithdrawsPendingResult is
// matrix row 6's pending-result half: the provider delivers a VALID proxy-owned
// completion call and then holds the stream open, so a bounded result is pending
// publication when the client cancels.
//
// Task 5.2 certified the internal withdrawal of that pending publication on close,
// caller cancel, shared A-leg cancel, deadline, and continuation. This cell adds
// the PROTOCOL-VISIBLE half that 5.2 does not own: a cancelled turn must publish
// no result, open no continuation leg, and carry no repair instruction.
func TestPreferredProtocolTransportE2E_clientCancellationWithdrawsPendingResult(t *testing.T) {
	t.Parallel()

	completedControl := append(
		[]string{algResponseCreated(t, "resp_alg_cancel_pending", 1)},
		algProxyControlCallFrames(t, algCompletionResult, 2)...)
	algRunCancellationCell(t, "cancellation with a pending result",
		algLeg{frames: completedControl, block: 3 * time.Second})
}

// =============================================================================
// Requirement 8.6 / matrix row 7 — refusal and content filtering
// =============================================================================

// TestPreferredProtocolTransportE2E_refusalIsNotReinterpretedAsMissingCompletionWork
// is matrix row 7 and requirement 8.6 at the transport boundary.
//
// The provider refuses the request with an authoritative, non-recoverable content
// filter rejection before producing any output. The completion protocol is active
// for that leg, so an absent completion signal is exactly the condition that would
// otherwise trigger the bounded missing-signal repair. Requirement 8.6 forbids
// reading this authoritative non-recoverable terminal as missing completion work.
//
// The cell pins the whole outcome: the protocol WAS active on the refused leg (the
// base instruction and the frozen control tool were advertised, so it had both the
// expectation and the budget), and it nevertheless declined to continue — no
// continuation leg exists, no leg carries the bounded repair instruction, no
// completion result was invented, and the client received the provider's own
// authoritative failure rather than a fabricated answer.
//
// The client-visible status is the frontend's generic mapping of a surfaced
// non-recoverable upstream failure. This cell's subject is NOT that mapping: it is
// that the runtime and the protocol let the authoritative refusal terminate the
// turn, which is why the assertion is on the absence of a fabricated 200 answer,
// of a continuation leg, of the repair instruction, and of any published result.
func TestPreferredProtocolTransportE2E_refusalIsNotReinterpretedAsMissingCompletionWork(t *testing.T) {
	t.Parallel()

	origin := algScriptTransport(algLeg{
		status: http.StatusBadRequest,
		json:   `{"error":{"message":"` + algRefusalText + `","type":"invalid_request_error","code":"content_filter_violation"}}`,
	})

	d, tr := algDeployPreferred(t, TransportSSE, origin)
	d.Exec.StreamRecovery = streamrecovery.Config{Enabled: true, AllowPostOutputContinuation: true, EmitWarning: true}

	ctx, cancel := context.WithTimeout(t.Context(), algCellTimeout)
	defer cancel()
	status, frames, err := algPostTransport(ctx, d, algCreateBodyWith(t, algOrdinaryToolInput, true, algOrdinaryToolName), tr)
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if status == http.StatusOK {
		t.Fatalf("status = %d, want the authoritative provider refusal surfaced as a failure rather than converted into an invented answer; wire=%s", status, algWireBody(frames))
	}
	if got := algWireText(frames); got != "" {
		t.Fatalf("client assistant text = %q, want none: a refusal is the provider's authoritative terminal and must not be answered", got)
	}

	upstream := &algUpstreamLog{bodies: origin.algAllBodies()}
	// The protocol really was active on the refused leg, so it could have
	// reinterpreted the missing signal as incomplete work.
	algAssertBoundedUpstreamCount(t, upstream, 1)
	algAssertUpstreamCarries(t, upstream, 0, algProtocolInstructionMarker)
	algAssertUpstreamInjectsControlTool(t, upstream, 0)

	// Requirement 8.6: it did not reinterpret the refusal as missing completion
	// work. No continuation leg, no repair instruction, no invented result.
	algAssertNoSemanticRepairAnywhere(t, origin)
	if strings.Contains(algWireBody(frames), algCompletionResult) {
		t.Fatalf("a completion result was invented for an authoritative refusal:\n%s", algWireBody(frames))
	}
	algAssertNoPrivateControlStage(t, tr)
	algAssertNoPrivateControlLeak(t, frames)
}

// =============================================================================
// Design Concurrency and Lifecycle / matrix row 16 — parallel race loser
// =============================================================================

// TestPreferredProtocolTransportE2E_parallelLoserCompletionNeverReachesTheClient
// is matrix row 16 and the design's Concurrency and Lifecycle rule that "only the
// winning attempt's evidence may affect the logical response" and "a losing
// attempt's control result must be discarded with that attempt".
//
// Two real backends race in parallel. The winner commits ordinary assistant text
// and stops cleanly WITHOUT the completion signal. The loser holds its response
// back and only then emits a VALID proxy-owned completion call carrying its own
// distinct bounded result.
//
// The cell asserts the winner's decision governs and the loser's completion is
// discarded with the loser:
//
//   - the winner's origin served exactly two requests, the first its unmarked stop
//     and the second its own admitted continuation carrying the repair
//     control tool and the normative instruction, so its control call was a real
//     trusted-provenance proxy completion and not an unrelated tool event;
//   - the loser's bounded result sentinel appears nowhere on the client wire and in
//     no internal observation stage, so the client can never act on a result from
//     the discarded attempt and no observer ever saw it;
//   - the client's answer is exactly the winning lineage's own committed output.
//
// The loser's completion is emitted immediately while the winning arm is held back,
// so both arms obtain a real B-leg and a real upstream request before either writes
// a frame; the winning lineage still wins because the proxy-owned control call is
// private and therefore never publishes client-visible output, so the race is
// decided by the only arm that commits something the client can see. The sentinel is
// a different string from every other planted fact, so each wire assertion is an
// exact-text claim rather than a count of something the fixture itself produced.
func TestPreferredProtocolTransportE2E_parallelLoserCompletionNeverReachesTheClient(t *testing.T) {
	t.Parallel()

	winnerStop := append(
		[]string{algResponseCreated(t, "resp_alg_race_w1", 1)},
		algCommitText(t, "msg_alg_race_w1", algInterruptedText, 2)...)
	winnerStop = append(winnerStop, algResponseCompleted(t, "resp_alg_race_w1", 7))
	winnerRepair := algFinishedTextLeg(t, "resp_alg_race_w2", "msg_alg_race_w2", algRaceContinuationText)
	// The WINNING arm is the one held back: both arms therefore obtain a real
	// upstream request before either writes a frame, so the cell never degenerates
	// into a race where the loser was never opened at all.
	winner := algScriptTransport(
		algLeg{frames: winnerStop, block: 250 * time.Millisecond},
		algLeg{frames: winnerRepair, block: 250 * time.Millisecond},
	)
	loserCompletion := append(
		[]string{algResponseCreated(t, "resp_alg_race_l", 1)},
		algProxyControlCallFrames(t, algLoserResult, 2)...)
	loserCompletion = append(loserCompletion, algResponseCompleted(t, "resp_alg_race_l", 4))
	loser := algScriptTransport(algLeg{frames: loserCompletion})

	d, tr := algDeployColumn(t, algColumn{
		Strategy:  AgentLoopGuardStrategyAttemptCompletion,
		Frontend:  FrontendOpenAIResponses,
		Backend:   BackendOpenAIResponses,
		Transport: TransportSSE,
		Origin:    winner,
		Candidates: []Candidate{
			{Backend: BackendOpenAIResponses, OriginHandler: loser},
		},
	})
	// The parallel selector races both real candidates; the ordinary failover
	// separator Deploy composes is replaced by the parallel one.
	d.RouteSelector = RouteSelector(BackendOpenAIResponses, "gpt-4o-mini") + "!" +
		RouteSelector(candidateBackendKey(BackendOpenAIResponses, 0), "gpt-4o-mini")
	d.Exec.StreamRecovery = streamrecovery.Config{Enabled: true, EmitWarning: true}

	ctx, cancel := context.WithTimeout(t.Context(), algCellTimeout)
	defer cancel()
	status, frames, err := algPostTransport(ctx, d, algCreateBodyWith(t, algOrdinaryToolInput, true, algOrdinaryToolName), tr)
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; wire=%s", status, algWireBody(frames))
	}

	// The loser really raced: it obtained a real B-leg and really emitted a real
	// proxy-owned completion call. Without that request the discard claims below
	// would be vacuous.
	loserBodies := loser.algAllBodies()
	if got := len(loserBodies); got != 1 {
		t.Fatalf("losing candidate served %d upstream requests, want exactly 1 so the discard claims are about a real losing attempt", got)
	}
	// The winner really served its own unmarked stop, on its own candidate.
	if got := len(winner.algAllBodies()); got < 1 {
		t.Fatal("the winning candidate served no upstream request")
	}
	// The protocol was active for the LOSER too, so the loser's control call was a
	// real trusted-provenance proxy completion and not an unrelated tool event.
	loserUpstream := &algUpstreamLog{bodies: loserBodies}
	algAssertUpstreamCarries(t, loserUpstream, 0, algProtocolInstructionMarker)
	algAssertUpstreamInjectsControlTool(t, loserUpstream, 0)

	// The loser's completion was discarded with the loser: its bounded result
	// sentinel reached neither the client wire nor any internal stage, and the
	// proxy-owned control identity never crossed the client boundary at all.
	if strings.Contains(algWireBody(frames), algLoserResult) {
		t.Fatalf("the losing candidate's completion result reached the client:\n%s", algWireBody(frames))
	}
	for _, it := range tr.snapshot() {
		if strings.Contains(it.text, algLoserResult) {
			t.Fatalf("%s observed the losing candidate's completion result at seq %d: %+v", it.stage, it.seq, it)
		}
	}
	algAssertNoPrivateControlLeak(t, frames)

	// The winner's decision governs: the winning lineage's committed text reaches
	// the client exactly once, nothing derived from the losing attempt contributed
	// to the client's answer, and no proxy completion result was published on top
	// of the winning lineage's already committed text.
	//
	// This cell deliberately does not assert HOW MANY legs the winner itself
	// consumes. Whether the winner's own bounded missing-signal continuation is
	// admitted after a parallel race is a separate behaviour, certified by the
	// missing-signal and reactivation cells, and the runtime does not admit it
	// deterministically under a parallel plan; asserting it here would make this
	// cell a flake detector rather than a certification of the race-loser
	// contract, which is what this row actually owns.
	clientText := algWireText(frames)
	if n := strings.Count(clientText, algInterruptedText); n != 1 {
		t.Fatalf("the winning lineage's committed text appears %d times in the client answer, want exactly 1; client answer=%q", n, clientText)
	}
	if !strings.HasPrefix(clientText, algInterruptedText) {
		t.Fatalf("client answer = %q, want the winning lineage's committed text first", clientText)
	}
	if strings.Contains(clientText, algCompletionResult) {
		t.Fatalf("a proxy completion result was published on top of the winning lineage's committed text:\n%s", algWireBody(frames))
	}
	if strings.Contains(algWireBody(frames), algCompletionResult) {
		t.Fatalf("a proxy completion result reached the client although no committed winning attempt completed:\n%s", algWireBody(frames))
	}
}

// =============================================================================
// Design Transport and Continuation Interaction — continuation reactivation
// =============================================================================

// TestPreferredProtocolTransportE2E_continuationCandidateReactivationPublishesResult
// is the design's Transport and Continuation Interaction rule that "the same
// control-tool provider is projected again if the continuation candidate remains
// eligible, so the worker can complete the handshake on the repair turn".
//
// The first leg produces no output and no completion signal at all, so the protocol
// admits its bounded repair. The reactivated continuation candidate then completes
// the handshake by emitting a valid proxy-owned completion call.
//
// The cell asserts that the reactivation was real and admitted on its own facts:
//
//   - both legs advertise the frozen proxy-owned control tool definition and the
//     normative base instruction, so the continuation candidate genuinely had the
//     handshake available and the control provider was genuinely re-projected;
//   - the repair leg additionally carries the fixed bounded recovery instruction
//     with its fixed clauses;
//   - because no meaningful assistant text had been committed on either leg, the
//     bounded result is published through the ordinary client release path exactly
//     once as the client's assistant answer;
//   - the turn is bounded at exactly two upstream legs.
//
// That last pair of assertions is what distinguishes a reactivated candidate from
// a turn that merely continued: a continuation that had not been re-projected could
// not have completed the handshake, and a handshake that did not complete would
// have published nothing.
func TestPreferredProtocolTransportE2E_continuationCandidateReactivationPublishesResult(t *testing.T) {
	t.Parallel()

	firstLegFrames := []string{
		algResponseCreated(t, "resp_alg_reactivate_1", 1),
		algResponseCompleted(t, "resp_alg_reactivate_1", 2),
	}
	secondLegFrames := append(
		[]string{algResponseCreated(t, "resp_alg_reactivate_2", 1)},
		algProxyControlCallFrames(t, algCompletionResult, 2)...)
	secondLegFrames = append(secondLegFrames, algResponseCompleted(t, "resp_alg_reactivate_2", 4))
	origin := algScriptTransport(
		algLeg{frames: firstLegFrames},
		algLeg{frames: secondLegFrames},
	)

	d, tr := algDeployPreferred(t, TransportSSE, origin)
	d.Exec.StreamRecovery = streamrecovery.Config{Enabled: true, AllowPostOutputContinuation: true, EmitWarning: true}

	ctx, cancel := context.WithTimeout(t.Context(), algCellTimeout)
	defer cancel()
	status, frames, err := algPostTransport(ctx, d, algCreateBodyWith(t, algOrdinaryToolInput, true, algOrdinaryToolName), tr)
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; wire=%s", status, algWireBody(frames))
	}

	upstream := &algUpstreamLog{bodies: origin.algAllBodies()}
	// Bounded: the first leg plus exactly one reactivated continuation candidate.
	algAssertBoundedUpstreamCount(t, upstream, 2)

	// The control provider was projected on the FIRST leg and projected AGAIN on
	// the reactivated continuation candidate, together with the normative base
	// instruction, so the continuation candidate could complete the handshake.
	algAssertUpstreamInjectsControlTool(t, upstream, 0)
	algAssertUpstreamInjectsControlTool(t, upstream, 1)
	algAssertUpstreamCarries(t, upstream, 0, algProtocolInstructionMarker)
	algAssertUpstreamCarries(t, upstream, 1, algProtocolInstructionMarker)
	// Requirement 7.2: the reactivated leg additionally carries the fixed bounded
	// recovery control text, and the first leg carries only the base protocol.
	algAssertUpstreamLegLacks(t, upstream, 0, algRepairInstructionMarker)
	algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)
	algAssertUpstreamCarries(t, upstream, 1, algRepairSignalClause)

	// The handshake completed on the repair turn, and because nothing meaningful
	// had been committed the bounded result became the client's answer exactly
	// once through the ordinary release path.
	if got := algWireText(frames); got != algCompletionResult {
		t.Fatalf("client assistant text = %q, want exactly the reactivated leg's bounded result %q", got, algCompletionResult)
	}
	if n := strings.Count(algWireText(frames), algCompletionResult); n != 1 {
		t.Fatalf("the bounded result appears %d times on the wire, want exactly 1", n)
	}
	algAssertNoPrivateControlStage(t, tr)
	algAssertNoPrivateControlLeak(t, frames)
}

// algAllBodies returns every request body this origin served, in arrival order,
// so a cell can hand the recorded legs to the shared upstream assertions.
func (o *algTransportOrigin) algAllBodies() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.bodies...)
}

// Compile-time guard: the scripted origin must remain a plain
// net/http.Handler so it can be injected through the existing harness seam
// without any new production fault switch.
var _ http.Handler = (*algTransportOrigin)(nil)

// fmt keeps the import set stable for the exact-count diagnostics below.
var _ = fmt.Sprintf

// =============================================================================
// Requirement 10.3 / matrix row 17 — reload between strategies
// =============================================================================

// algPublishStrategy publishes one REAL Agent Loop Guard generation for the named
// strategy onto the deployment's request-runtime snapshot, through the same
// production feature registry, enabled-surface merge, and snapshot builder the
// harness already uses. It is the cell-visible form of a strategy reload.
//
// It must only be called while NO request is in flight: Executor.RuntimeSnapshot
// is a plain field with no synchronized reload accessor, so swapping it under a
// live request is a harness-side data race rather than a production reload path.
func algPublishStrategy(t *testing.T, d *Deployment, strategy string, bus *hooks.Bus) {
	t.Helper()
	planes, err := AgentLoopGuardFeaturePlanes(t, strategy)
	if err != nil {
		t.Fatalf("AgentLoopGuardFeaturePlanes(%q): %v", strategy, err)
	}
	cs := aipfeature.NewContributionSet()
	if err := planes.ReplayTo(cs, "alg-reload-generation"); err != nil {
		t.Fatalf("ReplayTo: %v", err)
	}
	d.Exec.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{FeaturePlanes: cs.Freeze()})
}

// TestPreferredProtocolTransportE2E_reloadSwitchesStrategyForNewlyAdmittedTurns
// is matrix row 17's observable half with requirement 10.3: newly admitted turns
// use the newly published generation, and the newly published generation is the
// one whose behaviour is then observed on the wire.
//
// One deployment serves both turns. The first is admitted on the LEGACY
// semantic-verifier generation and stops unmarked; the second is admitted after
// the deployment has published the PREFERRED attempt_completion generation and
// stops the same way. The two turns differ in exactly the ways requirement 1.3 and
// 10.3 require, and every difference is read off request bodies the production
// path built itself:
//
//   - legacy turn: one upstream leg, NO proxy-owned control tool advertised, NO
//     normative base instruction, NO bounded repair leg, and the client's answer is
//     the model's own committed text;
//   - preferred turn: two upstream legs, the control tool and the base instruction
//     advertised on its first leg, the fixed bounded repair instruction on the
//     continuation leg, and the client's answer is both committed model texts.
//
// The IN-FLIGHT half of requirement 10.3 — a turn that was already admitted when
// the reload happened — is deliberately NOT asserted here. The harness seam for a
// reload is Executor.RuntimeSnapshot, a plain field with no synchronized reload
// accessor, so swapping it while a request is in flight is a harness-side data
// race (confirmed with -race) rather than a production reload. That half is owned
// by the committed assembly-time pin in internal/core/runtime/executor_assemble_stream.go
// and its existing test
// internal/core/runtime/control_tool_activation_seams_test.go:TestControlToolProjection_reloadPinsActivationToItsOwnGeneration,
// plus the in-flight pin cell
// internal/core/runtime/control_tool_inflight_generation_pin_test.go:TestControlToolProjection_inFlightRequestKeepsAdmittedGenerationAcrossReload.
func TestPreferredProtocolTransportE2E_reloadSwitchesStrategyForNewlyAdmittedTurns(t *testing.T) {
	t.Parallel()

	stopFrames := algFinishedTextLeg(t, "resp_alg_reload_stop", "msg_alg_reload_stop", algUnmarkedText)
	repairFrames := algFinishedTextLeg(t, "resp_alg_reload_repair", "msg_alg_reload_repair", algRepairText)
	origin := algScriptTransport(
		algLeg{frames: stopFrames},
		algLeg{frames: stopFrames},
		algLeg{frames: repairFrames},
	)

	bus := hooks.New(hooks.Config{})
	d := Deploy(t, DeploymentSpec{
		Frontend:               FrontendOpenAIResponses,
		Backend:                BackendOpenAIResponses,
		Transport:              TransportSSE,
		OriginHandler:          origin,
		AgentLoopGuardStrategy: AgentLoopGuardStrategySemanticVerifier,
	})
	if d == nil {
		t.Fatal("Deploy returned nil for the reload cell")
	}
	d.Exec.Bus = bus
	algWireContinuationPorts(t, d)
	d.Exec.StreamRecovery = streamrecovery.Config{Enabled: true, AllowPostOutputContinuation: true, EmitWarning: true}

	ctx, cancel := context.WithTimeout(t.Context(), algCellTimeout)
	defer cancel()
	body := algCreateBodyWith(t, algOrdinaryToolInput, true, algOrdinaryToolName)

	// --- turn one: admitted on the legacy generation -------------------------
	legacyStatus, legacyFrames, err := algPostCreateAtPath(ctx, d, "/v1/responses", body, true, nil)
	if err != nil {
		t.Fatalf("legacy create stream: %v", err)
	}
	if legacyStatus != http.StatusOK {
		t.Fatalf("legacy status = %d, want 200; wire=%s", legacyStatus, algWireBody(legacyFrames))
	}
	servedAfterLegacy := origin.algServeCount()
	if servedAfterLegacy != 1 {
		t.Fatalf("legacy turn served %d upstream requests, want exactly 1; requirement 1.3 keeps the legacy strategy free of any proxy-owned protocol", servedAfterLegacy)
	}
	legacyUpstream := &algUpstreamLog{bodies: origin.algAllBodies()}
	algAssertUpstreamDoesNotInjectControlTool(t, legacyUpstream)
	algAssertUpstreamLacks(t, legacyUpstream, algProtocolInstructionMarker)
	algAssertUpstreamLacks(t, legacyUpstream, algRepairInstructionMarker)
	if got := algWireText(legacyFrames); got != algUnmarkedText {
		t.Fatalf("legacy client assistant text = %q, want the model's own committed text %q", got, algUnmarkedText)
	}

	// --- reload: publish the preferred generation, no request in flight -------
	algPublishStrategy(t, d, AgentLoopGuardStrategyAttemptCompletion, bus)

	// --- turn two: admitted on the newly published generation -----------------
	preferredStatus, preferredFrames, err := algPostCreateAtPath(ctx, d, "/v1/responses", body, true, nil)
	if err != nil {
		t.Fatalf("preferred create stream: %v", err)
	}
	if preferredStatus != http.StatusOK {
		t.Fatalf("preferred status = %d, want 200; wire=%s", preferredStatus, algWireBody(preferredFrames))
	}
	servedAfterPreferred := origin.algServeCount()
	if servedAfterPreferred-servedAfterLegacy != 2 {
		t.Fatalf("preferred turn served %d upstream requests, want exactly 2; the newly published generation must spend its one bounded repair leg",
			servedAfterPreferred-servedAfterLegacy)
	}
	preferredUpstream := &algUpstreamLog{bodies: origin.algAllBodies()[servedAfterLegacy:]}
	algAssertUpstreamInjectsControlTool(t, preferredUpstream, 0)
	algAssertUpstreamCarries(t, preferredUpstream, 0, algProtocolInstructionMarker)
	algAssertUpstreamCarries(t, preferredUpstream, 1, algRepairInstructionMarker)
	if got, want := algWireText(preferredFrames), algUnmarkedText+algRepairText; got != want {
		t.Fatalf("preferred client assistant text = %q, want exactly the two committed model texts %q", got, want)
	}
	algAssertNoPrivateControlLeak(t, preferredFrames)

	// The reload really changed the decision, and it changed it by exactly the
	// specified amount: one extra upstream leg and no change to the model text the
	// client already saw.
	if preferredUpstream.count() == legacyUpstream.count() {
		t.Fatal("the reload produced no observable change in the admitted turn's decisions")
	}
	if algWireText(legacyFrames) == "" || !strings.HasPrefix(algWireText(preferredFrames), algWireText(legacyFrames)) {
		t.Fatalf("the reload changed the client's answer shape: legacy=%q preferred=%q", algWireText(legacyFrames), algWireText(preferredFrames))
	}
}
