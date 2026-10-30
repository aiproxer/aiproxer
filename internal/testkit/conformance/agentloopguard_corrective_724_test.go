// Corrective regressions for the PR #724 code-review findings (explicit
// completion protocol). Every cell here drives the complete production path:
// a real frontend handler, the real runtime executor, a real backend adapter,
// a scripted reference-provider origin, and the real Agent Loop Guard
// generation composed through the production feature registry. No provider
// fake is ever assigned.
//
// Finding 1 (P1): hidden-only continuations must not exceed
// max_protocol_reprompts. The deployment composes the default preferred
// strategy (max_protocol_reprompts = 1) with an unresolved platform cap, so
// the platform allowance cannot be what stops the loop: only the feature's
// own reprompt budget may bound it.
package conformance

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// algHiddenMalformedResource renders one completed upstream non-streaming
// response carrying only a private control call with malformed arguments.
// There is deliberately no assistant text and no usage payload, so the leg
// emits no client-visible text or usage event before its terminal decision.
func algHiddenMalformedResource(t *testing.T, id, itemID, callID, args string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id":         id,
		"object":     "response",
		"created_at": 1715620000,
		"status":     "completed",
		"model":      "gpt-4o-mini",
		"output": []any{map[string]any{
			"type": "function_call", "id": itemID, "call_id": callID,
			"name": algControlToolName, "arguments": args,
		}},
	})
	if err != nil {
		t.Fatalf("marshal hidden malformed resource %q: %v", id, err)
	}
	return string(raw)
}

// algHiddenEmptyResource renders one completed upstream non-streaming response
// with no output items at all: no completion signal, no text, no usage.
func algHiddenEmptyResource(t *testing.T, id string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id":         id,
		"object":     "response",
		"created_at": 1715620000,
		"status":     "completed",
		"model":      "gpt-4o-mini",
		"output":     []any{},
	})
	if err != nil {
		t.Fatalf("marshal hidden empty resource %q: %v", id, err)
	}
	return string(raw)
}

// TestCorrective724_HiddenOnlyContinuationsRespectRepromptBudget is the
// finding-1 regression: with one permitted reprompt, hidden-only malformed
// completion legs must open at most two backend attempts. The third scripted
// turn is a valid completion so that a budget bypass terminates instead of
// hanging the fixture; it must never be served.
func TestCorrective724_HiddenOnlyContinuationsRespectRepromptBudget(t *testing.T) {
	t.Parallel()

	const guardResult = "guard completion that must never publish"
	guardArgs, err := json.Marshal(map[string]string{"result": guardResult})
	if err != nil {
		t.Fatalf("marshal guard args: %v", err)
	}
	origin, upstream := algScriptedOrigin(t,
		algUpstreamTurn{JSON: algHiddenMalformedResource(t, "resp_hidden_b1", "fc_hidden_b1", "call_hidden_b1", `{"result":""}`)},
		algUpstreamTurn{JSON: algHiddenMalformedResource(t, "resp_hidden_b2", "fc_hidden_b2", "call_hidden_b2", `{"result":"   "}`)},
		algUpstreamTurn{JSON: algResourceJSON(t, "resp_hidden_guard", []any{
			algControlOutput("fc_hidden_guard", "call_hidden_guard", string(guardArgs)),
		})},
	)
	d, tr := algDeployPreferred(t, TransportJSON, origin)

	body := algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", false)
	status, frames, err := algPostCreateBody(t.Context(), d, body, false, tr)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; wire=%s", status, algWireBody(frames))
	}

	// No third backend attempt may open: the one permitted reprompt covers the
	// B1->B2 repair leg, and B2's hidden-only malformed completion must exhaust
	// the budget instead of bootstrapping it back to zero.
	algAssertBoundedUpstreamCount(t, upstream, 2)
	algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)

	// Nothing was ever publishable: the client receives no invented result and
	// no private control fact.
	if got := algWireText(frames); got != "" {
		t.Fatalf("client assistant text = %q, want empty; a hidden-only turn must invent nothing", got)
	}
	if strings.Contains(algWireBody(frames), guardResult) {
		t.Fatalf("the guard completion was published, so a third attempt opened:\n%s", algWireBody(frames))
	}
	algAssertNoPrivateControlFacts(t, frames)
}

// algDeployCorrective composes the preferred-strategy deployment with extra
// feature-config YAML lines (for example, protocol limits), through the same
// production registry, merge, and snapshot path as algDeployPreferred.
func algDeployCorrective(t *testing.T, transport ClientTransport, origin http.Handler, extraConfig string) (*Deployment, *algTrace) {
	t.Helper()
	return algDeployColumn(t, algColumn{
		Strategy:             AgentLoopGuardStrategyAttemptCompletion,
		Frontend:             FrontendOpenAIResponses,
		Backend:              BackendOpenAIResponses,
		Transport:            transport,
		Origin:               origin,
		AgentLoopGuardConfig: extraConfig,
	})
}

// TestCorrective724_EmptyResponsesRespectRepromptBudget is the clean
// empty-response variant of the finding-1 regression: backend attempts that
// return no output at all must still be bounded by the same reprompt budget.
func TestCorrective724_EmptyResponsesRespectRepromptBudget(t *testing.T) {
	t.Parallel()

	const guardResult = "guard completion that must never publish"
	guardArgs, err := json.Marshal(map[string]string{"result": guardResult})
	if err != nil {
		t.Fatalf("marshal guard args: %v", err)
	}
	origin, upstream := algScriptedOrigin(t,
		algUpstreamTurn{JSON: algHiddenEmptyResource(t, "resp_empty_b1")},
		algUpstreamTurn{JSON: algHiddenEmptyResource(t, "resp_empty_b2")},
		algUpstreamTurn{JSON: algResourceJSON(t, "resp_empty_guard", []any{
			algControlOutput("fc_empty_guard", "call_empty_guard", string(guardArgs)),
		})},
	)
	d, tr := algDeployPreferred(t, TransportJSON, origin)

	body := algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", false)
	status, frames, err := algPostCreateBody(t.Context(), d, body, false, tr)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; wire=%s", status, algWireBody(frames))
	}

	algAssertBoundedUpstreamCount(t, upstream, 2)
	algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)
	if got := algWireText(frames); got != "" {
		t.Fatalf("client assistant text = %q, want empty; a hidden-only turn must invent nothing", got)
	}
	if strings.Contains(algWireBody(frames), guardResult) {
		t.Fatalf("the guard completion was published, so a third attempt opened:\n%s", algWireBody(frames))
	}
	algAssertNoPrivateControlFacts(t, frames)
}

// TestCorrective724_RepeatedAnswersStopAtNoProgressThreshold is the finding-3
// regression: successive attempts that emit the identical non-completion
// answer must be stopped by the configured no-progress threshold, not by the
// total reprompt budget. The deployment allows three reprompts with a
// no-progress limit of one, so only the no-progress breaker can stop the turn
// after the second identical leg. The fourth scripted turn is a valid
// completion so that a missed breaker terminates instead of hanging the
// fixture; it must never be served.
func TestCorrective724_RepeatedAnswersStopAtNoProgressThreshold(t *testing.T) {
	t.Parallel()

	const repeatedAnswer = "Still working."
	const guardResult = "guard completion that must never publish"
	guardArgs, err := json.Marshal(map[string]string{"result": guardResult})
	if err != nil {
		t.Fatalf("marshal guard args: %v", err)
	}
	identicalTurn := func(id string) algUpstreamTurn {
		return algUpstreamTurn{JSON: algResourceJSON(t, id, []any{
			algMessageOutput("msg_"+id, repeatedAnswer),
		})}
	}
	origin, upstream := algScriptedOrigin(t,
		identicalTurn("resp_repeat_b1"),
		identicalTurn("resp_repeat_b2"),
		identicalTurn("resp_repeat_b3"),
		algUpstreamTurn{JSON: algResourceJSON(t, "resp_repeat_guard", []any{
			algControlOutput("fc_repeat_guard", "call_repeat_guard", string(guardArgs)),
		})},
	)
	d, tr := algDeployCorrective(t, TransportJSON, origin,
		"max_protocol_reprompts: 3\nno_progress_limit: 1\n")

	body := algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", false)
	status, frames, err := algPostCreateBody(t.Context(), d, body, false, tr)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; wire=%s", status, algWireBody(frames))
	}

	// The second identical answer trips the no-progress breaker: no third
	// backend attempt may open even though the reprompt budget allows two more.
	algAssertBoundedUpstreamCount(t, upstream, 2)
	algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)

	// Both committed answers are delivered exactly once each; nothing is
	// invented on top of them.
	if got, want := algWireText(frames), repeatedAnswer+repeatedAnswer; got != want {
		t.Fatalf("client assistant text = %q, want exactly the two committed answers %q", got, want)
	}
	if strings.Contains(algWireBody(frames), guardResult) {
		t.Fatalf("the guard completion was published, so the no-progress breaker did not stop the turn:\n%s", algWireBody(frames))
	}
}

// algDualDoneCompletionSSE renders the upstream streaming wire for the
// finding-2 regression: a done-only private control call with no incremental
// argument deltas, closed by BOTH final notifications the Responses protocol
// emits for one call (response.function_call_arguments.done followed by the
// response.output_item.done snapshot of the same function_call item).
func algDualDoneCompletionSSE(t *testing.T) string {
	t.Helper()
	frame := func(name string, payload map[string]any) string {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal sse frame %s: %v", name, err)
		}
		return "event: " + name + "\ndata: " + string(raw) + "\n\n"
	}
	args := algControlArgs(t, algCompletionResult)
	var sse strings.Builder
	seq := 0
	next := func() int { seq++; return seq }
	sse.WriteString(frame("response.created", map[string]any{
		"type": "response.created", "sequence_number": next(),
		"response": map[string]any{"id": "resp_alg_dual_done", "object": "response", "created_at": 1715620000, "status": "in_progress", "model": "gpt-4o-mini"},
	}))
	sse.WriteString(frame("response.output_item.added", map[string]any{
		"type": "response.output_item.added", "sequence_number": next(), "output_index": 0,
		"item": map[string]any{
			"type": "function_call", "id": algControlItemID, "call_id": algControlCallID,
			"name": algControlToolName, "arguments": "",
		},
	}))
	sse.WriteString(frame("response.function_call_arguments.done", map[string]any{
		"type": "response.function_call_arguments.done", "sequence_number": next(),
		"item_id": algControlItemID, "output_index": 0,
		"name": algControlToolName, "arguments": args,
	}))
	sse.WriteString(frame("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "sequence_number": next(), "output_index": 0,
		"item": map[string]any{
			"type": "function_call", "id": algControlItemID, "call_id": algControlCallID,
			"name": algControlToolName, "arguments": args,
		},
	}))
	sse.WriteString(frame("response.completed", map[string]any{
		"type": "response.completed", "sequence_number": next(),
		"response": map[string]any{
			"id": "resp_alg_dual_done", "object": "response", "created_at": 1715620000, "status": "completed", "model": "gpt-4o-mini",
			"usage": map[string]any{"input_tokens": 21, "output_tokens": 9, "total_tokens": 30},
		},
	}))
	sse.WriteString("data: [DONE]\n\n")
	return sse.String()
}

// TestCorrective724_DualDoneCompletionPublishesOnceWithoutRepair is the
// finding-2 end-to-end regression: the dual final notifications for a
// done-only control call must produce one handler invocation, one published
// result, no repair continuation, and one client-facing finish. Before the
// adapter fix, the duplicated fallback arguments revoked the valid completion
// after the finish, so the turn spent its repair leg instead of publishing.
func TestCorrective724_DualDoneCompletionPublishesOnceWithoutRepair(t *testing.T) {
	t.Parallel()

	origin, upstream := algScriptedOrigin(t, algUpstreamTurn{SSE: algDualDoneCompletionSSE(t)})
	d, tr := algDeployPreferred(t, TransportSSE, origin)

	status, frames, err := algPostCreate(t.Context(), d, true, tr)
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; wire=%s", status, algWireBody(frames))
	}

	// No repair continuation: the valid completion authorizes the terminal on
	// the first leg.
	algAssertBoundedUpstreamCount(t, upstream, 1)
	if n := algCountWireType(frames, "response.completed"); n != 1 {
		t.Fatalf("response.completed frames = %d, want exactly 1; frames=%+v", n, frames)
	}

	// One published result: the bounded completion is the client's answer
	// exactly once, with no private control fact on the wire.
	if got := algWireText(frames); got != algCompletionResult {
		t.Fatalf("client assistant text = %q, want exactly %q", got, algCompletionResult)
	}
	algAssertNoPrivateControlLeak(t, frames)
	algAssertObservationOrder(t, tr, frames, algCompletionResult)
}
