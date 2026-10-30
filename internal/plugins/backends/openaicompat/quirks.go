package openaicompat

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/providerprofiles"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

// CompatibleQuirks is the small value type for strict-provider wire normalizers.
// It is built once from profile quirks in provider_profile_binding and carried
// through BackendSpec into InvokeRequest. Generic custom-compatible kinds get
// the zero value, so their behaviour is unchanged. Each quirk changes only the
// B-leg wire; canonical aipapi.Call, routing, billing and capabilities are
// untouched. Quirk off means byte-identical requests.
type CompatibleQuirks struct {
	// ForwardPromptCacheKey forwards prompt_cache_key when set.
	ForwardPromptCacheKey bool
	// ReasoningContentOnToolCalls ensures reasoning_content is present on
	// assistant messages in tools-carrying requests (Chat family only).
	ReasoningContentOnToolCalls bool
	// ToolOutputAdjacency reorders trapped items before function_call outputs
	// to just before the earliest call of the group (Responses family only).
	ToolOutputAdjacency bool
}

// QuirkObserver counts strict-provider quirk applications. It is the narrow
// metrics seam for the compatible adapters: threaded explicitly through
// BackendSpec into InvokeRequest, never a global, container, or registry. The
// nil interface means no observation, so generic custom-compatible kinds (zero
// value) stay metric-silent. Pass a nil interface when disabled; a typed-nil
// implementation would still dispatch.
type QuirkObserver interface {
	// ObserveQuirkApplied counts one quirk that actually rewrote the wire.
	// Only the bounded counted-quirk IDs are ever reported:
	// QuirkOpenAIChatReasoningContent and QuirkOpenAIResponsesToolAdjacency.
	// Refusals are never reported here: they already surface as classified
	// terminal errors with their own telemetry.
	ObserveQuirkApplied(quirk providerprofiles.QuirkID)
}

// observeQuirkApplied reports one applied quirk; a nil observer stays silent.
func observeQuirkApplied(o QuirkObserver, quirk providerprofiles.QuirkID) {
	if o != nil {
		o.ObserveQuirkApplied(quirk)
	}
}

// applyChatReasoningContentQuirk ensures every assistant message carries
// reasoning_content when the request carries tools. DeepSeek's thinking-mode
// passback applies to all turns, not only tool-call turns: "the
// reasoning_content of all previous turns should be passed back to the API".
// A missing field gains reasoning_content:"". Non-empty values are never
// overwritten. Without tools the field is ignored upstream, so tool-less
// requests are left untouched (byte-identical).
//
// An injected "" satisfies the upstream presence validator; it is not proof
// of lossless preservation. Lossless passback is proven only when canonical
// reasoning survives verbatim (see TestQuirks_ChatPreservesNonEmpty).
//
// It reports whether at least one message gained the field, so callers count
// an application only when the wire was actually rewritten.
func applyChatReasoningContentQuirk(p *openai.ChatCompletionNewParams) bool {
	if p == nil || len(p.Tools) == 0 {
		return false
	}
	applied := false
	for i := range p.Messages {
		msg := &p.Messages[i]
		if msg.OfAssistant == nil {
			continue
		}
		extras := msg.OfAssistant.ExtraFields()
		if v, ok := extras["reasoning_content"]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				continue
			}
			if s, ok := v.(string); ok && s == "" {
				continue
			}
			// Present but non-string: leave untouched (explicit, not silent).
			if _, isStr := v.(string); !isStr {
				continue
			}
		}
		merged := make(map[string]any, len(extras)+1)
		maps.Copy(merged, extras)
		merged["reasoning_content"] = ""
		msg.OfAssistant.SetExtraFields(merged)
		applied = true
	}
	return applied
}

// ErrResponsesAdjacencyRefused reports a history whose strict call/output
// adjacency cannot be restored without reordering the conversation. The
// request is refused rather than rewritten: making a strict upstream accept a
// request is not sufficient if doing so changes what the model is told
// happened.
//
// A surfaced refusal is classified for orchestration (see
// adjacencyRefusedError): it matches aipapi.ErrCapabilityReject, so
// frontends map it to 502 backend_unavailable, and it matches
// aipapi.ErrRecoverablePreOutput, so the core fails over to the next
// candidate instead of failing the request.
var ErrResponsesAdjacencyRefused = errors.New("openaicompat: strict tool-output adjacency cannot be restored without reordering conversation history")

// adjacencyRefusedError classifies a refused adjacency rewrite for the core
// and the frontends. RejectError.Unwrap exposes only ErrCapabilityReject, so
// a dedicated type carries both roots: capability rejection for the 502
// mapping, and recoverable pre-output for candidate failover. The wrapped
// cause preserves ErrResponsesAdjacencyRefused identity and the refusing
// detail (item indexes only, never conversation content).
type adjacencyRefusedError struct{ cause error }

func (e *adjacencyRefusedError) Error() string {
	if e == nil || e.cause == nil {
		return ErrResponsesAdjacencyRefused.Error()
	}
	return e.cause.Error()
}

func (e *adjacencyRefusedError) Unwrap() []error {
	if e == nil || e.cause == nil {
		return []error{aipapi.ErrCapabilityReject, aipapi.ErrRecoverablePreOutput}
	}
	return []error{aipapi.ErrCapabilityReject, aipapi.ErrRecoverablePreOutput, e.cause}
}

// applyResponsesAdjacencyQuirk restores call/output adjacency by delaying
// non-assistant conversation messages, never model-authored items.
//
// Strict upstreams require each function_call_output to follow its
// function_call with only sibling calls and outputs in between. The only
// rewrite that does not fabricate history is to delay the interleaved
// message past the result it was interleaved with: a user, developer or
// system message cannot observe tool state, so moving it later asserts
// nothing false. Everything the model authored — a later tool call, an exact
// reasoning item, an assistant message, another tool result that completed
// earlier — is causal evidence the model would have seen in order, so an
// output is never moved across it.
//
// When a violation cannot be fixed within that rule, the request is refused
// with ErrResponsesAdjacencyRefused and sent unchanged.
//
// Invariants (covered by tests in quirks_test.go):
//   - Relative order of every non-output item is preserved exactly.
//   - An output only moves earlier, never later, and never before its call.
//   - Output completion order is preserved; a move that would reverse it is
//     refused rather than applied.
//   - Healthy input is byte-identical: no detected violation, no rewrite.
//   - Every reliably paired violation is resolved or refused; no rewrite is
//     ever applied half-way.
//   - Ambiguous pairing (a repeated call_id or output id) is not a violation
//     this quirk can establish: the request is returned unchanged.
//
// Cost: one indexing pass plus one backward pass, then O(1) per call.
//
// It reports whether the input was actually reordered, so callers count an
// application only when the wire was rewritten. A refusal (non-nil error)
// leaves the input unchanged and is not an application.
func applyResponsesAdjacencyQuirk(p *responses.ResponseNewParams) (bool, error) {
	if p == nil {
		return false, nil
	}
	items := p.Input.OfInputItemList
	if len(items) == 0 {
		return false, nil
	}
	reordered, applied, err := reorderResponsesAdjacency(items)
	if err != nil {
		return false, &adjacencyRefusedError{cause: err}
	}
	p.Input.OfInputItemList = reordered
	return applied, nil
}

func isResponsesCall(item responses.ResponseInputItemUnionParam) (string, bool) {
	if item.OfFunctionCall == nil {
		return "", false
	}
	id := strings.TrimSpace(item.OfFunctionCall.CallID)
	if id == "" {
		return "", false
	}
	return id, true
}

func isResponsesOutput(item responses.ResponseInputItemUnionParam) (string, bool) {
	if item.OfFunctionCallOutput == nil {
		return "", false
	}
	if !item.OfFunctionCallOutput.CallID.Valid() {
		return "", false
	}
	id := strings.TrimSpace(item.OfFunctionCallOutput.CallID.Value)
	if id == "" {
		return "", false
	}
	return id, true
}

// isDelayableConversationMessage reports whether an item is a message the
// model did not author, and which therefore carries no observation of tool
// state. Only these may be delayed past a tool result. Reasoning items,
// assistant messages and every other tool-call shape are model-authored.
func isDelayableConversationMessage(item responses.ResponseInputItemUnionParam) bool {
	switch {
	case item.OfMessage != nil:
		return item.OfMessage.Role != responses.EasyInputMessageRoleAssistant
	case item.OfInputMessage != nil:
		return item.OfInputMessage.Role != string(aipapi.RoleAssistant)
	case item.OfOutputMessage != nil:
		return false
	default:
		return false
	}
}

// reorderResponsesAdjacency delays interleaved non-assistant messages past the
// tool results they were interleaved with. The applied flag is true only when
// at least one output moved; every no-op path returns the input unchanged
// with applied=false.
func reorderResponsesAdjacency(items []responses.ResponseInputItemUnionParam) ([]responses.ResponseInputItemUnionParam, bool, error) {
	// Any repeated call_id or output id makes pairing ambiguous: return the
	// request unchanged rather than rewriting history on a guess.
	callAt := make(map[string]int, len(items))
	outputAt := make(map[string]int, len(items))
	for i, it := range items {
		if id, ok := isResponsesCall(it); ok {
			if _, dup := callAt[id]; dup {
				return items, false, nil
			}
			callAt[id] = i
		}
		if id, ok := isResponsesOutput(it); ok {
			if _, dup := outputAt[id]; dup {
				return items, false, nil
			}
			outputAt[id] = i
		}
	}

	// nextConversation[i] is the index of the first conversation item after i,
	// or len(items) when the tail is all calls and outputs. One backward pass
	// keeps per-call target lookup O(1) regardless of history length.
	nextConversation := make([]int, len(items)+1)
	nextConversation[len(items)] = len(items)
	for i := len(items) - 1; i >= 0; i-- {
		nextConversation[i] = nextConversation[i+1]
		if _, isCall := isResponsesCall(items[i]); isCall {
			continue
		}
		if _, isOut := isResponsesOutput(items[i]); isOut {
			continue
		}
		nextConversation[i] = i
	}

	// Each violated output is scheduled immediately before the first
	// conversation item that follows its call. Several outputs can target one
	// item; they are emitted in their original order so completion order is
	// never reversed.
	targets := map[int][]int{}
	for id, callIdx := range callAt {
		outputIdx, ok := outputAt[id]
		if !ok || outputIdx <= callIdx {
			// A call with no output, or an inverted pair (output before its
			// call), is not a call/output adjacency violation this quirk can
			// establish.
			continue
		}
		target := nextConversation[callIdx+1]
		if target >= outputIdx {
			continue
		}
		targets[target] = append(targets[target], outputIdx)
	}
	moved := make(map[int]bool)
	for _, due := range targets {
		slices.Sort(due)
		for _, idx := range due {
			moved[idx] = true
		}
	}
	if len(moved) == 0 {
		return items, false, nil
	}

	// Validate every scheduled move before applying any of them: a partially
	// applied plan would rewrite history without restoring adjacency.
	//
	// Everything from the target up to the last moved output is delayed past
	// the result. That is safe only while all of it is non-assistant
	// conversation content. A tool call in the gap would assert the model saw
	// this result before generating a later call; a reasoning item or an
	// assistant message is model-authored evidence of what the model knew;
	// another output that completed earlier would be reordered after one that
	// completed later. Any of those is refused rather than relocated.
	for target, due := range targets {
		for k := target; k < due[len(due)-1]; k++ {
			if slices.Contains(due, k) {
				continue
			}
			if !isDelayableConversationMessage(items[k]) {
				return items, false, fmt.Errorf("%w: delaying item %d would move a tool output across model-authored item %d", ErrResponsesAdjacencyRefused, target, k)
			}
		}
	}

	out := make([]responses.ResponseInputItemUnionParam, 0, len(items))
	for i, it := range items {
		for _, outputIdx := range targets[i] {
			out = append(out, items[outputIdx])
		}
		if moved[i] {
			continue
		}
		out = append(out, it)
	}
	return out, true, nil
}
