package openairesponses

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openaiwire"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func WriteNonStreamJSON(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts EncodeOptions) error {
	resp, err := buildWireResponse(ctx, call, es, opts)
	if err != nil {
		return err
	}
	sessionwire.WriteResponseCarriers(w, call)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(resp)
}

// WireWriteNonStreamJSON encodes a completed canonical stream as OpenAI Responses JSON for wire fast-path execution.
// It is deterministic-only: creation time comes from the response context
// seeds and it never consults an executor clock. Clock-selected wire encoding
// goes through the handler closure into wireWriteNonStreamJSONWithOptions.
func WireWriteNonStreamJSON(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream, exposeExt bool) error {
	return wireWriteNonStreamJSONWithOptions(ctx, w, rc, es, EncodeOptions{ExposeAipUsageExtensions: exposeExt})
}

// wireWriteNonStreamJSONWithOptions encodes a completed canonical stream as
// OpenAI Responses JSON using caller-selected encode options, sharing the
// existing encoder core. Zero option fields fall back to the response context
// deterministic seeds, preserving canonical zero/default normalization.
func wireWriteNonStreamJSONWithOptions(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream, opts EncodeOptions) error {
	model := rc.ClientModel()
	if model == "" {
		model = rc.EffectiveModel()
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	rid := opts.ResponseID
	if rid == "" {
		rid = rc.OpenAIResponseID()
	}
	mid := opts.MessageID
	if mid == "" {
		mid = rc.OpenAIMessageID()
	}
	ts := opts.CreatedAt
	if ts == 0 {
		ts = rc.DeterministicTimestamp()
	}
	if ts == 0 {
		ts = time.Now().Unix()
	}
	resp, err := buildWireResponseInternal(ctx, es, model, rid, mid, ts, opts.ExposeAipUsageExtensions, nil)
	if err != nil {
		return err
	}
	rc.WriteSessionHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(resp)
}

func buildWireResponse(ctx context.Context, call *aipapi.Call, es aipapi.EventStream, opts EncodeOptions) (wireResponse, error) {
	model := ModelFromCall(call)
	if model == "" {
		model = "gpt-4o-mini"
	}
	opts = defaultEncodeOptions(call, opts)
	return buildWireResponseInternal(ctx, es, model, opts.ResponseID, opts.MessageID, opts.CreatedAt, opts.ExposeAipUsageExtensions, opts.namespaceTools)
}

func buildWireResponseInternal(ctx context.Context, es aipapi.EventStream, model, rid, mid string, ts int64, exposeExt bool, namespaceTools *namespaceToolMapping) (wireResponse, error) {
	order := &nonstreamOutputOrder{}
	tee := openaiwire.TeeTerminal(es)
	col, err := aipapi.Collect(ctx, &orderTeeStream{inner: tee, order: order})
	if err != nil {
		return wireResponse{}, err
	}
	text := col.Text.String()
	msgOut := map[string]any{
		"type":    "message",
		"id":      mid,
		"status":  "completed",
		"role":    "assistant",
		"content": wireMessageContentParts(text, col.AssistantMedia),
	}
	responsesExact := make([]aipapi.ReasoningPart, 0, len(col.ReasoningParts))
	for i := range col.ReasoningParts {
		if aipapi.NormalizeReasoningDialect(col.ReasoningParts[i].Dialect) == aipapi.ReasoningDialectOpenAIResponsesItemV1 {
			responsesExact = append(responsesExact, col.ReasoningParts[i])
		}
	}
	toolsByID := make(map[string]aipapi.ToolCallSummary, len(col.ToolCallOrder))
	for _, tc := range col.OrderedToolCalls() {
		toolsByID[tc.ID] = tc
	}
	out := make([]any, 0, len(order.markers)+1)
	if order.exactCount > 0 {
		for _, m := range order.markers {
			switch m.kind {
			case nonstreamOrderMessage:
				if text != "" || len(col.AssistantMedia) > 0 {
					out = append(out, msgOut)
				}
			case nonstreamOrderExactReasoning:
				if m.exactOrdinal < 0 || m.exactOrdinal >= len(responsesExact) {
					return wireResponse{}, fmt.Errorf("openairesponses: invalid reasoning item")
				}
				item, err := exactReasoningWireObject(&responsesExact[m.exactOrdinal])
				if err != nil {
					return wireResponse{}, err
				}
				out = append(out, item)
			case nonstreamOrderTool:
				tc, ok := toolsByID[m.toolCallID]
				if !ok {
					continue
				}
				out = append(out, functionCallWireObject(tc, namespaceTools))
			}
		}
	} else {
		if reasoning := col.Reasoning.String(); reasoning != "" {
			out = append(out, map[string]any{
				"type":    "reasoning",
				"id":      "rs_" + rid,
				"status":  "completed",
				"summary": []any{map[string]any{"type": "summary_text", "text": reasoning}},
			})
		}
		if text != "" || len(col.AssistantMedia) > 0 {
			out = append(out, msgOut)
		}
		for _, tc := range col.OrderedToolCalls() {
			out = append(out, functionCallWireObject(tc, namespaceTools))
		}
	}
	resp := wireResponse{
		ID:        rid,
		Object:    "response",
		CreatedAt: ts,
		Status:    "completed",
		Model:     model,
		Output:    out,
	}
	if tee.IsOutputLimitTerminal(col) {
		resp.Status = "incomplete"
		resp.IncompleteDetails = &wireIncomplete{Reason: "max_output_tokens"}
	} else if col.FinishReason == "content_filter" {
		resp.Status = "incomplete"
		resp.IncompleteDetails = &wireIncomplete{Reason: "content_filter"}
	}
	resp.Usage = wireResponsesUsage(col, exposeExt)
	return resp, nil
}

// functionCallWireObject emits one completed function_call item. A projected
// namespace member is restored to the client's original bare name plus its
// namespace; ordinary and unknown names pass through unchanged.
func functionCallWireObject(tc aipapi.ToolCallSummary, namespaceTools *namespaceToolMapping) map[string]any {
	name, namespace := restoreNamespaceToolName(namespaceTools, tc.Name)
	item := map[string]any{
		"type":      "function_call",
		"id":        fcItemID(tc.ID),
		"call_id":   tc.ID,
		"name":      name,
		"arguments": tc.Arguments,
		"status":    "completed",
	}
	if namespace != "" {
		item["namespace"] = namespace
	}
	return item
}
