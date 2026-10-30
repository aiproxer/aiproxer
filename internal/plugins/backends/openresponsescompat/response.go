package openresponsescompat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// NativeEvidence captures bounded provider-native identity from a parsed
// response resource or stream. It is private attempt evidence: it is never
// forwarded to clients and never emitted on the canonical stream.
type NativeEvidence struct {
	ResponseID  string
	ItemIDs     []string
	ToolCallIDs []string
	// ExtensionTypes preserves the bounded discriminators of valid
	// vendor-prefixed output items/events accepted but not representable on
	// the canonical stream.
	ExtensionTypes []string
}

func (n *NativeEvidence) addItemID(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	n.ItemIDs = append(n.ItemIDs, id)
}

// parseResource parses one complete OpenResponses ResponseResource through the
// production codec/state semantics into canonical lifecycle events. It rejects
// trailing data, unknown output item types, unrepresentable output content, and
// non-terminal statuses. The native response ID and native item IDs are
// captured as private evidence only.
func parseResource(id string, data []byte, limits ResponseLimits) ([]aipapi.Event, NativeEvidence, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var resource proto.WireResponseResource
	if err := dec.Decode(&resource); err != nil {
		return nil, NativeEvidence{}, fmt.Errorf("%s: %w: %v", id, ErrMalformedResponse, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, NativeEvidence{}, fmt.Errorf("%s: %w: trailing data after response resource", id, ErrMalformedResponse)
	}
	if strings.TrimSpace(resource.Object) != "response" {
		return nil, NativeEvidence{}, fmt.Errorf("%s: %w: unexpected object %q in response resource", id, ErrMalformedResponse, resource.Object)
	}
	if len(resource.Output) > limits.MaxItems {
		return nil, NativeEvidence{}, fmt.Errorf("%s: %w", id, limitError("response_items", len(resource.Output), limits.MaxItems))
	}
	return resourceToEvents(id, &resource, limits)
}

func resourceToEvents(id string, resource *proto.WireResponseResource, limits ResponseLimits) ([]aipapi.Event, NativeEvidence, error) {
	native := NativeEvidence{ResponseID: strings.TrimSpace(resource.ID)}
	events := []aipapi.Event{{Kind: aipapi.EventResponseStarted}}

	var textBytes, reasoningBytes int
	for i, w := range resource.Output {
		item, err := proto.DecodeItem(w, proto.DefaultLimits())
		if err != nil {
			return nil, native, fmt.Errorf("%s: %w: output[%d]: %v", id, ErrMalformedResponse, i, err)
		}
		native.addItemID(item.ID)
		switch item.Kind {
		case aipapi.ItemKindMessage:
			evs, added, err := messageOutputEvents(id, item, aipapi.MaxContentPartsPerItem)
			if err != nil {
				return nil, native, err
			}
			events = append(events, evs...)
			textBytes += added
			if textBytes > limits.MaxTextBytes {
				return nil, native, fmt.Errorf("%s: %w", id, limitError("response_text", textBytes, limits.MaxTextBytes))
			}
		case aipapi.ItemKindToolCall:
			evs, added, err := toolCallOutputEvents(id, item)
			if err != nil {
				return nil, native, err
			}
			events = append(events, evs...)
			textBytes += added
			if textBytes > limits.MaxTextBytes {
				return nil, native, fmt.Errorf("%s: %w", id, limitError("response_text", textBytes, limits.MaxTextBytes))
			}
			if item.ToolCall != nil {
				native.ToolCallIDs = append(native.ToolCallIDs, item.ToolCall.CallID)
			}
		case aipapi.ItemKindReasoning:
			evs, added, err := reasoningOutputEvents(id, item)
			if err != nil {
				return nil, native, err
			}
			events = append(events, evs...)
			reasoningBytes += added
			if reasoningBytes > limits.MaxReasoningBytes {
				return nil, native, fmt.Errorf("%s: %w", id, limitError("response_reasoning", reasoningBytes, limits.MaxReasoningBytes))
			}
		case aipapi.ItemKindItemReference:
			// Provider-native item references are private attempt evidence and
			// are never forwarded onto the canonical stream. The native ID was
			// already captured above.
			continue
		default:
			return nil, native, fmt.Errorf("%s: %w: output[%d] item type %q is not representable in the canonical stream", id, ErrMalformedResponse, i, w.Type)
		}
	}

	if resource.UsagePresent || usagePresent(resource.Usage) {
		events = append(events, usageEventWithContext(resource.Usage, resource.ID, resource.ServiceTier))
	}

	switch resource.Status {
	case "completed":
		events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: "completed"})
	case "incomplete":
		events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: "incomplete", FinishReason: incompleteFinishReason(resource.IncompleteDetails)})
	case "failed":
		code, msg := errorFromResource(resource)
		events = append(events, aipapi.Event{Kind: aipapi.EventError, ErrorCode: code, ErrorMessage: msg})
	default:
		return nil, native, fmt.Errorf("%s: %w: unexpected resource status %q", id, ErrMalformedResponse, resource.Status)
	}
	return events, native, nil
}

func messageOutputEvents(id string, item aipapi.Item, maxContentParts int) ([]aipapi.Event, int, error) {
	if len(item.Content) == 0 {
		return nil, 0, fmt.Errorf("%s: %w: message output item has no content", id, ErrMalformedResponse)
	}
	if len(item.Content) > maxContentParts {
		return nil, 0, fmt.Errorf("%s: %w", id, limitError("response_content_parts", len(item.Content), maxContentParts))
	}
	events := []aipapi.Event{{Kind: aipapi.EventMessageStarted}}
	var textBytes int
	for _, cp := range item.Content {
		if cp.Kind != aipapi.ContentPartText {
			return nil, 0, fmt.Errorf("%s: %w: message output content part kind %q is not representable in the canonical stream", id, ErrMalformedResponse, cp.Kind)
		}
		if cp.Text == "" {
			continue
		}
		events = append(events, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: cp.Text})
		textBytes += len(cp.Text)
	}
	if len(events) == 1 {
		return nil, 0, fmt.Errorf("%s: %w: message output item has no text content", id, ErrMalformedResponse)
	}
	return events, textBytes, nil
}

func toolCallOutputEvents(id string, item aipapi.Item) ([]aipapi.Event, int, error) {
	if item.ToolCall == nil {
		return nil, 0, fmt.Errorf("%s: %w: function_call output item is missing its payload", id, ErrMalformedResponse)
	}
	callID := item.ToolCall.CallID
	if strings.TrimSpace(callID) == "" {
		return nil, 0, fmt.Errorf("%s: %w: function_call output item is missing call_id", id, ErrMalformedResponse)
	}
	if strings.TrimSpace(item.ToolCall.Name) == "" {
		return nil, 0, fmt.Errorf("%s: %w: function_call output item is missing its name", id, ErrMalformedResponse)
	}
	events := []aipapi.Event{{
		Kind:       aipapi.EventToolCallStarted,
		ToolCallID: callID,
		ToolName:   item.ToolCall.Name,
	}}
	delta, err := toolArgumentsDelta(item.ToolCall.Arguments)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", id, err)
	}
	if delta != "" {
		events = append(events, aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: callID, Delta: delta})
	}
	events = append(events, aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: callID})
	return events, len(delta), nil
}

func reasoningOutputEvents(id string, item aipapi.Item) ([]aipapi.Event, int, error) {
	if item.Reasoning == nil || item.Reasoning.Reasoning == nil {
		return nil, 0, fmt.Errorf("%s: %w: reasoning output item is missing its payload", id, ErrMalformedResponse)
	}
	r := item.Reasoning.Reasoning
	var events []aipapi.Event
	var bytes int
	if r.Text != "" {
		events = append(events, aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: r.Text})
		bytes += len(r.Text)
	}
	if r.Signature != "" {
		events = append(events, aipapi.Event{Kind: aipapi.EventReasoningSignatureDelta, Signature: r.Signature})
		bytes += len(r.Signature)
	}
	if len(r.Opaque) > 0 {
		events = append(events, aipapi.Event{Kind: aipapi.EventReasoningOpaqueDelta, Opaque: append([]byte(nil), r.Opaque...)})
		bytes += len(r.Opaque)
	}
	if len(events) == 0 {
		events = append(events, aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: ""})
	}
	return events, bytes, nil
}

// toolArgumentsDelta converts wire function_call arguments (a JSON string on
// the pinned profile) into the plain argument text expected by the canonical
// state machine.
func toolArgumentsDelta(raw []byte) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s, nil
		}
	}
	if !json.Valid(trimmed) {
		return "", fmt.Errorf("%w: invalid function call arguments", ErrMalformedResponse)
	}
	return string(trimmed), nil
}

// incompleteDetails carries the bounded pinned-profile reason discriminator of
// a response resource's incomplete_details payload.
type incompleteDetails struct {
	Reason string `json:"reason"`
}

// decodeIncompleteDetails parses an incomplete_details payload. Absent, null,
// and structurally invalid payloads decode to the zero value.
func decodeIncompleteDetails(raw json.RawMessage) incompleteDetails {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return incompleteDetails{}
	}
	var details incompleteDetails
	if err := json.Unmarshal(trimmed, &details); err != nil {
		return incompleteDetails{}
	}
	return details
}

// incompleteFinishReason maps an upstream OpenResponses incomplete_details
// payload into the canonical finish reason on EventResponseFinished. The
// canonical stream supports the token-limit taxonomy (length/max_tokens) for
// incomplete responses; every other provider reason is preserved verbatim so a
// non-length truncation is never silently rewritten to "length".
func incompleteFinishReason(raw json.RawMessage) string {
	switch reason := strings.TrimSpace(decodeIncompleteDetails(raw).Reason); reason {
	case "":
		return "length"
	case "length", "max_tokens":
		return reason
	case "max_output_tokens":
		return "length"
	default:
		return truncateRuneSafe(reason, aipapi.MaxRefStringBytes)
	}
}

// resourceIncompleteReason extracts incomplete_details.reason from raw response
// bytes for resource shapes whose typed wire struct does not carry the field
// (e.g. the pinned WireCompactResource) without a protocol change.
func resourceIncompleteReason(data []byte) string {
	var probe struct {
		IncompleteDetails json.RawMessage `json:"incomplete_details"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return "length"
	}
	return incompleteFinishReason(probe.IncompleteDetails)
}

func usageEvent(u proto.WireUsage) aipapi.Event {
	return usageEventWithContext(u, "", "")
}

func usageEventWithContext(u proto.WireUsage, requestID, serviceTier string) aipapi.Event {
	raw := u.RawJSON
	if len(raw) == 0 {
		raw, _ = json.Marshal(u)
	}
	ev := aipapi.Event{
		Kind:            aipapi.EventUsageDelta,
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		TotalTokens:     u.TotalTokens,
		CacheReadTokens: u.InputTokensDetails.CachedTokens,
		ReasoningTokens: u.OutputTokensDetails.ReasoningTokens,
		RawUsageJSON:    string(raw),
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			Authority:         aipapi.UsageAuthorityAuthoritative,
			ProviderRequestID: strings.TrimSpace(requestID),
			ServiceContext:    strings.TrimSpace(serviceTier),
			DedupeKey:         "openresponses.compat.usage:stream",
		},
	}
	ev.UsagePresence = aipapi.UsagePresence{
		InputTokens:     u.InputTokensPresent || u.InputTokens != 0,
		OutputTokens:    u.OutputTokensPresent || u.OutputTokens != 0,
		CacheReadTokens: u.InputTokensDetails.CachedTokensPresent || u.InputTokensDetails.CachedTokens != 0,
		ReasoningTokens: u.OutputTokensDetails.ReasoningTokensPresent || u.OutputTokensDetails.ReasoningTokens != 0,
		TotalTokens:     u.TotalTokensPresent || u.TotalTokens != 0,
	}
	return ev
}

func usagePresent(u proto.WireUsage) bool {
	return u.InputTokens != 0 || u.OutputTokens != 0 || u.TotalTokens != 0 ||
		u.InputTokensDetails.CachedTokens != 0 || u.InputTokensDetails.TextTokens != 0 ||
		u.InputTokensDetails.AudioTokens != 0 || u.InputTokensDetails.Images != 0 ||
		u.OutputTokensDetails.ReasoningTokens != 0 || u.OutputTokensDetails.TextTokens != 0 ||
		u.OutputTokensDetails.AudioTokens != 0 || u.OutputTokensDetails.Images != 0 ||
		u.InputTokensPresent || u.OutputTokensPresent || u.TotalTokensPresent ||
		u.InputTokensDetails.CachedTokensPresent || u.InputTokensDetails.TextTokensPresent ||
		u.InputTokensDetails.AudioTokensPresent || u.InputTokensDetails.ImagesPresent ||
		u.OutputTokensDetails.ReasoningTokensPresent || u.OutputTokensDetails.TextTokensPresent ||
		u.OutputTokensDetails.AudioTokensPresent || u.OutputTokensDetails.ImagesPresent ||
		len(nativeUsageMeasures(string(u.RawJSON))) > 0
}

func errorFromResource(resource *proto.WireResponseResource) (code, message string) {
	raw := bytes.TrimSpace(resource.Error)
	if len(raw) == 0 || string(raw) == "null" {
		return "upstream_error", ""
	}
	var details proto.WireErrorDetails
	if err := json.Unmarshal(raw, &details); err != nil {
		return "upstream_error", ""
	}
	code = details.Code
	if code == "" {
		code = details.Type
	}
	if code == "" {
		code = "upstream_error"
	}
	return sanitizeErrorCode(code), sanitizeErrorMessage(details.Message)
}
