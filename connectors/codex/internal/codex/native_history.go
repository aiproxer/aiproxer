package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	nativeHistoryFingerprintDomain = "aip.codex.native-history.item.v1"
	maxNativeHistoryItems          = aipapi.MaxItems
	// The history builder has no per-connector config input. Keep its aggregate
	// bound explicit and hard; the checkpoint store applies its own entry cap.
	maxNativeHistoryBytes = 16 << 20
)

// NativeHistory is the connector-private exact input used by native-context
// policy. It is deliberately separate from the ordinary generation payload.
type NativeHistory struct {
	Items        []inputItem
	Fingerprints []string
	Boundaries   []TrajectoryBoundary
	// OpaqueMetadataTokens is aligned with Items when provider usage metadata is
	// available; non-opaque entries are ignored by the estimator.
	OpaqueMetadataTokens []int64
	TotalBytes           int64
}

// TrajectoryBoundary describes a possible split before ItemIndex. PairSafe is
// false for a boundary inside an assistant trajectory or between a call/output
// pair. The final boundary is always included.
type TrajectoryBoundary struct {
	ItemIndex      int
	UserTurnStart  bool
	AssistantStart bool
	PairSafe       bool
}

type nativeHistoryBuildError struct {
	category string
}

func (e *nativeHistoryBuildError) Error() string { return e.category }

func newNativeHistoryBuildError(category string) error {
	return &nativeHistoryBuildError{category: category}
}

func buildNativeHistory(call *aipapi.Call) (NativeHistory, error) {
	if call == nil {
		return NativeHistory{}, nativeHistoryError("malformed_history")
	}
	if err := validateNativeHistoryCall(call); err != nil {
		return NativeHistory{}, err
	}
	if estimateNativeHistoryCallBytes(call) > maxNativeHistoryBytes {
		return NativeHistory{}, nativeHistoryError("history_bounds")
	}

	var (
		items []inputItem
		err   error
	)
	if call.HasItemAuthority() {
		items, err = buildItemAuthorityInputItems(call.Items)
	} else {
		items, err = buildExactLegacyInputItems(call)
	}
	if err != nil {
		return NativeHistory{}, nativeHistoryErrorForBuild(err)
	}
	if len(items) == 0 || len(items) > maxNativeHistoryItems {
		return NativeHistory{}, nativeHistoryError("history_bounds")
	}
	if err := validateNativeInputPairs(items); err != nil {
		return NativeHistory{}, err
	}

	history := NativeHistory{
		Items:        cloneInputItems(items),
		Fingerprints: make([]string, 0, len(items)),
	}
	var totalBytes int
	for _, item := range history.Items {
		fp, err := fingerprintNativeItem(item)
		if err != nil {
			return NativeHistory{}, nativeHistoryError("fingerprint_failure")
		}
		history.Fingerprints = append(history.Fingerprints, fp)
		body, err := nativeItemJSON(item)
		if err != nil || len(body) > maxNativeHistoryBytes-totalBytes {
			return NativeHistory{}, nativeHistoryError("history_bounds")
		}
		totalBytes += len(body)
	}
	history.Boundaries = nativeTrajectoryBoundaries(history.Items)
	history.TotalBytes = int64(totalBytes)
	return history, nil
}

func buildExactLegacyInputItems(call *aipapi.Call) ([]inputItem, error) {
	out := make([]inputItem, 0, len(call.Messages))
	for _, message := range call.Messages {
		// System messages are represented by the connector instructions field and
		// are not legal Responses input items.
		if message.Role == aipapi.RoleSystem {
			continue
		}
		if message.Role == aipapi.RoleTool {
			for _, part := range message.Parts {
				if part.Kind != aipapi.PartToolResult {
					return nil, newNativeHistoryBuildError("unsupported_item")
				}
				out = append(out, functionCallOutputItem{
					Type:   "function_call_output",
					CallID: strings.TrimSpace(part.ToolCallID),
					Output: toolResultString(part),
				})
			}
			continue
		}
		if message.Role == aipapi.RoleAssistant {
			items, structured, err := assistantFunctionCallItems(message.Parts, true)
			if err != nil {
				return nil, err
			}
			if structured {
				out = append(out, items...)
				continue
			}
		}
		item, err := messageToInputItem(message)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func validateNativeHistoryCall(call *aipapi.Call) error {
	if call.HasItemAuthority() {
		if len(call.Items) == 0 || len(call.Items) > maxNativeHistoryItems {
			return nativeHistoryError("history_bounds")
		}
		seenCalls := make(map[string]struct{}, len(call.Items))
		seenOutputs := make(map[string]struct{}, len(call.Items))
		for _, item := range call.Items {
			if item.Status == aipapi.ItemStatusInProgress || item.Status == aipapi.ItemStatusIncomplete {
				return nativeHistoryError("incomplete_item")
			}
			if item.Kind == aipapi.ItemKindMessage && item.Role == aipapi.RoleSystem {
				return nativeHistoryError("illegal_role")
			}
			if item.Kind == aipapi.ItemKindMessage && item.Role == aipapi.RoleTool {
				return nativeHistoryError("illegal_role")
			}
			if item.Kind == aipapi.ItemKindToolCall {
				if item.ToolCall == nil || strings.TrimSpace(item.ToolCall.CallID) == "" {
					return nativeHistoryError("missing_call_id")
				}
				id := strings.TrimSpace(item.ToolCall.CallID)
				if _, exists := seenCalls[id]; exists {
					return nativeHistoryError("duplicate_call_id")
				}
				seenCalls[id] = struct{}{}
			}
			if item.Kind == aipapi.ItemKindToolResult {
				if item.ToolResult == nil || strings.TrimSpace(item.ToolResult.CallID) == "" {
					return nativeHistoryError("missing_call_id")
				}
				id := strings.TrimSpace(item.ToolResult.CallID)
				if _, exists := seenCalls[id]; !exists {
					return nativeHistoryError("orphan_output")
				}
				if _, exists := seenOutputs[id]; exists {
					return nativeHistoryError("duplicate_output")
				}
				seenOutputs[id] = struct{}{}
			}
			if item.Kind == aipapi.ItemKindReasoning && item.Reasoning != nil && item.Reasoning.Reasoning != nil {
				if item.Reasoning.Reasoning.Dialect != aipapi.ReasoningDialectOpenAIResponsesItemV1 {
					return nativeHistoryError("unsupported_dialect")
				}
				if len(item.Reasoning.Reasoning.Opaque) > aipapi.MaxReasoningOpaqueBytes {
					return nativeHistoryError("oversized_opaque")
				}
			}
			if item.Kind == aipapi.ItemKindCompaction && item.Compaction != nil && len(item.Compaction.Opaque) > aipapi.MaxPartJSONBytes {
				return nativeHistoryError("oversized_opaque")
			}
		}
		if err := call.Validate(); err != nil {
			return nativeHistoryErrorForValidation(err)
		}
		return nil
	}

	if len(call.Messages) == 0 || len(call.Messages) > aipapi.MaxMessages {
		return nativeHistoryError("history_bounds")
	}
	seenCalls := make(map[string]struct{})
	seenOutputs := make(map[string]struct{})
	for _, message := range call.Messages {
		if message.Role == "" {
			return nativeHistoryError("illegal_role")
		}
		if message.Role == aipapi.RoleSystem {
			continue
		}
		if message.Role != aipapi.RoleUser && message.Role != aipapi.RoleDeveloper && message.Role != aipapi.RoleAssistant && message.Role != aipapi.RoleTool {
			return nativeHistoryError("illegal_role")
		}
		for _, part := range message.Parts {
			switch part.Kind {
			case aipapi.PartReasoning:
				if part.Reasoning == nil || part.Reasoning.Dialect != aipapi.ReasoningDialectOpenAIResponsesItemV1 {
					return nativeHistoryError("unsupported_dialect")
				}
				if len(part.Reasoning.Opaque) > aipapi.MaxReasoningOpaqueBytes {
					return nativeHistoryError("oversized_opaque")
				}
			case aipapi.PartToolResult:
				if message.Role != aipapi.RoleTool || strings.TrimSpace(part.ToolCallID) == "" {
					return nativeHistoryError("missing_call_id")
				}
				id := strings.TrimSpace(part.ToolCallID)
				if _, exists := seenCalls[id]; !exists {
					return nativeHistoryError("orphan_output")
				}
				if _, exists := seenOutputs[id]; exists {
					return nativeHistoryError("duplicate_output")
				}
				seenOutputs[id] = struct{}{}
			case aipapi.PartJSON:
				if len(part.Content) > aipapi.MaxPartJSONBytes {
					return nativeHistoryError("oversized_opaque")
				}
				if message.Role == aipapi.RoleAssistant {
					if item, ok, err := partToFunctionCallItem(part); err != nil {
						return nativeHistoryError("malformed_item")
					} else if ok {
						fnCall, isFnCall := item.(functionCallItem)
						if !isFnCall {
							return nativeHistoryError("malformed_item")
						}
						id := strings.TrimSpace(fnCall.CallID)
						if _, exists := seenCalls[id]; exists {
							return nativeHistoryError("duplicate_call_id")
						}
						seenCalls[id] = struct{}{}
					} else {
						var header struct {
							Type string `json:"type"`
						}
						if json.Unmarshal(part.Content, &header) == nil && header.Type == "compaction" {
							continue
						}
						return nativeHistoryError("unsupported_item")
					}
				}
			}
		}
	}
	if err := call.Validate(); err != nil {
		return nativeHistoryErrorForValidation(err)
	}
	return nil
}

func estimateNativeHistoryCallBytes(call *aipapi.Call) int {
	if call == nil {
		return 0
	}
	total := 0
	add := func(n int) {
		if n > maxNativeHistoryBytes-total {
			total = maxNativeHistoryBytes
			return
		}
		total += n
	}
	if call.HasItemAuthority() {
		for _, item := range call.Items {
			body, err := json.Marshal(item)
			if err != nil {
				return maxNativeHistoryBytes
			}
			add(len(body))
		}
		return total
	}
	for _, message := range call.Messages {
		body, err := json.Marshal(message.Parts)
		if err != nil {
			return maxNativeHistoryBytes
		}
		add(len(body))
	}
	return total
}

func validateNativeInputPairs(items []inputItem) error {
	seenCalls := make(map[string]struct{})
	seenOutputs := make(map[string]struct{})
	for _, item := range items {
		switch value := item.(type) {
		case functionCallItem:
			id := strings.TrimSpace(value.CallID)
			if id == "" {
				return nativeHistoryError("missing_call_id")
			}
			if _, exists := seenCalls[id]; exists {
				return nativeHistoryError("duplicate_call_id")
			}
			seenCalls[id] = struct{}{}
		case functionCallOutputItem:
			id := strings.TrimSpace(value.CallID)
			if _, exists := seenCalls[id]; !exists {
				return nativeHistoryError("output_before_call")
			}
			if _, exists := seenOutputs[id]; exists {
				return nativeHistoryError("duplicate_output")
			}
			seenOutputs[id] = struct{}{}
		}
	}
	return nil
}

func fingerprintNativeItem(item inputItem) (string, error) {
	body, err := nativeItemJSON(item)
	if err != nil {
		return "", err
	}
	if len(body) > aipapi.MaxPartJSONBytes+aipapi.MaxReasoningOpaqueBytes {
		return "", fmt.Errorf("item too large")
	}
	h := sha256.New()
	_, _ = h.Write([]byte(nativeHistoryFingerprintDomain))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(itemDiscriminator(item)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func nativeItemJSON(item inputItem) ([]byte, error) {
	if opaque, ok := item.(opaqueResponseItem); ok {
		return canonicalOpaqueJSON(opaque.raw)
	}
	return json.Marshal(item)
}

func canonicalOpaqueJSON(raw []byte) ([]byte, error) {
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing JSON")
	}
	return json.Marshal(value)
}

func itemDiscriminator(item inputItem) string {
	switch item.(type) {
	case textMessageItem, richMessageItem:
		return "message"
	case functionCallItem:
		return "function_call"
	case functionCallOutputItem:
		return "function_call_output"
	case opaqueResponseItem:
		return "opaque_response_item"
	default:
		return "unknown"
	}
}

func (h NativeHistory) SafeSplitIndices() []int {
	out := make([]int, 0, len(h.Boundaries))
	for _, boundary := range h.Boundaries {
		if boundary.PairSafe {
			out = append(out, boundary.ItemIndex)
		}
	}
	return out
}

func nativeTrajectoryBoundaries(items []inputItem) []TrajectoryBoundary {
	out := make([]TrajectoryBoundary, len(items)+1)
	for i := range out {
		out[i] = TrajectoryBoundary{ItemIndex: i, PairSafe: true}
	}
	// Calls stay outstanding until their matching output. The second condition
	// keeps adjacent assistant/action items in one indivisible trajectory.
	outstanding := make(map[string]struct{})
	for i, item := range items {
		out[i].PairSafe = !hasOutstandingCalls(outstanding)
		if message, ok := messageItemRole(item); ok {
			out[i].UserTurnStart = message == "user"
			out[i].AssistantStart = message == "assistant" && !assistantLike(items, i-1)
			if message == "assistant" && assistantLike(items, i-1) {
				out[i].PairSafe = false
			}
		}
		switch value := item.(type) {
		case functionCallItem:
			out[i].AssistantStart = !assistantLike(items, i-1)
			out[i].PairSafe = out[i].AssistantStart && !hasOutstandingCalls(outstanding)
			outstanding[strings.TrimSpace(value.CallID)] = struct{}{}
		case functionCallOutputItem:
			out[i].PairSafe = false
			delete(outstanding, strings.TrimSpace(value.CallID))
		case opaqueResponseItem:
			out[i].AssistantStart = !assistantLike(items, i-1)
			out[i].PairSafe = out[i].AssistantStart && !hasOutstandingCalls(outstanding)
		}
	}
	out[len(items)].PairSafe = !hasOutstandingCalls(outstanding)
	return out
}

func hasOutstandingCalls(calls map[string]struct{}) bool { return len(calls) != 0 }

func assistantLike(items []inputItem, index int) bool {
	if index < 0 || index >= len(items) {
		return false
	}
	switch item := items[index].(type) {
	case functionCallItem, opaqueResponseItem:
		return true
	case textMessageItem:
		return item.Role == "assistant"
	case richMessageItem:
		return item.Role == "assistant"
	default:
		return false
	}
}

func messageItemRole(item inputItem) (string, bool) {
	switch value := item.(type) {
	case textMessageItem:
		return value.Role, true
	case richMessageItem:
		return value.Role, true
	default:
		return "", false
	}
}

func cloneInputItems(items []inputItem) []inputItem {
	out := make([]inputItem, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case textMessageItem:
			out = append(out, value)
		case richMessageItem:
			content := make([]contentBlock, len(value.Content))
			for i, part := range value.Content {
				switch p := part.(type) {
				case inputTextPart:
					content[i] = p
				case inputImagePart:
					content[i] = p
				case inputFilePart:
					content[i] = p
				default:
					content[i] = part
				}
			}
			out = append(out, richMessageItem{Type: value.Type, Role: value.Role, Content: content, phase: value.phase})
		case functionCallItem:
			out = append(out, value)
		case functionCallOutputItem:
			out = append(out, value)
		case opaqueResponseItem:
			out = append(out, opaqueResponseItem{raw: append(json.RawMessage(nil), value.raw...)})
		default:
			out = append(out, item)
		}
	}
	return out
}

func nativeHistoryError(category string) error {
	return fmt.Errorf("codex native history: %s", category)
}

func nativeHistoryErrorForBuild(err error) error {
	if err == nil {
		return nil
	}
	if typed, ok := errors.AsType[*nativeHistoryBuildError](err); ok {
		return nativeHistoryError(typed.category)
	}
	return nativeHistoryError("malformed_item")
}

func nativeHistoryErrorForValidation(err error) error {
	if err == nil {
		return nil
	}
	var validation *aipapi.ValidationError
	if !errors.As(err, &validation) {
		return nativeHistoryError("malformed_history")
	}
	field := strings.ToLower(validation.Field)
	message := strings.ToLower(validation.Message)
	switch {
	case strings.Contains(field, "status") || strings.Contains(message, "status"):
		return nativeHistoryError("illegal_status")
	case strings.Contains(field, "role") || strings.Contains(message, "role"):
		return nativeHistoryError("illegal_role")
	case strings.Contains(field, "reasoning") && strings.Contains(message, "dialect"):
		return nativeHistoryError("unsupported_dialect")
	case strings.Contains(field, "opaque"), strings.Contains(message, "must be valid json"):
		return nativeHistoryError("invalid_opaque")
	case strings.Contains(message, "exceeds"):
		return nativeHistoryError("oversized_opaque")
	default:
		return nativeHistoryError("malformed_history")
	}
}
