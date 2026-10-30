package openresponsescompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// resolveModel returns the wire model for the request from the route candidate.
func resolveModel(cand routing.AttemptCandidate) string {
	return strings.TrimSpace(cand.Primary.Model)
}

// isPrefixedWireType reports whether a discriminator is a vendor-prefixed
// extension type (containing ':' or '/'), matching the pinned profile policy.
func isPrefixedWireType(t string) bool {
	return strings.Contains(t, ":") || strings.Contains(t, "/")
}

// representableContentPartKind reports whether a canonical content part can be
// encoded to the pinned profile by the production request codec without silent
// semantic loss. Inline file_data (input_file), video references
// (input_video), and opaque prefixed extension content parts are representable;
// every other kind is rejected before any HTTP round trip rather than being
// silently text-mapped.
func representableContentPartKind(k aipapi.ContentPartKind) bool {
	switch k {
	case aipapi.ContentPartText, aipapi.ContentPartImageRef, aipapi.ContentPartRefusal,
		aipapi.ContentPartFileRef, aipapi.ContentPartVideoRef, aipapi.ContentPartExtension:
		return true
	}
	return false
}

// checkRepresentable validates that an item-authority call can be encoded to
// the pinned profile without silent semantic loss and within configured
// request limits. It must run before any HTTP round trip.
func checkRepresentable(id string, call aipapi.Call, limits RequestLimits) error {
	if len(call.Items) > limits.MaxItems {
		return fmt.Errorf("%s: %w", id, limitError("request_items", len(call.Items), limits.MaxItems))
	}
	var extensionBytes int
	for i, item := range call.Items {
		switch item.Kind {
		case aipapi.ItemKindMessage:
			if len(item.Content) > limits.MaxContentParts {
				return fmt.Errorf("%s: %w", id, limitError(fmt.Sprintf("request_items[%d].content_parts", i), len(item.Content), limits.MaxContentParts))
			}
			for j, cp := range item.Content {
				if !representableContentPartKind(cp.Kind) {
					return fmt.Errorf("%s: %w: message item %d content part %d kind %q cannot be encoded to the OpenResponses profile", id, ErrUnrepresentable, i, j, cp.Kind)
				}
			}
		case aipapi.ItemKindToolResult:
			if item.ToolResult == nil {
				return fmt.Errorf("%s: %w: tool result item %d is missing data", id, ErrUnrepresentable, i)
			}
			if len(item.ToolResult.Parts) > limits.MaxContentParts {
				return fmt.Errorf("%s: %w", id, limitError(fmt.Sprintf("request_items[%d].content_parts", i), len(item.ToolResult.Parts), limits.MaxContentParts))
			}
			for j, cp := range item.ToolResult.Parts {
				if !representableContentPartKind(cp.Kind) {
					return fmt.Errorf("%s: %w: tool result item %d part %d kind %q cannot be encoded to the OpenResponses profile", id, ErrUnrepresentable, i, j, cp.Kind)
				}
			}
		case aipapi.ItemKindReasoning:
			if item.Reasoning == nil || item.Reasoning.Reasoning == nil {
				return fmt.Errorf("%s: %w: reasoning item %d is missing its payload", id, ErrUnrepresentable, i)
			}
		case aipapi.ItemKindExtension:
			if item.Extension == nil {
				return fmt.Errorf("%s: %w: extension item %d is missing its payload", id, ErrUnrepresentable, i)
			}
			if !isPrefixedWireType(item.Extension.Type) {
				return fmt.Errorf("%s: %w: extension item %d type %q is not vendor-prefixed", id, ErrUnrepresentable, i, item.Extension.Type)
			}
			extensionBytes += len(item.Extension.Data)
		case aipapi.ItemKindToolCall:
			if item.ToolCall == nil {
				return fmt.Errorf("%s: %w: tool call item %d is missing its payload", id, ErrUnrepresentable, i)
			}
		case aipapi.ItemKindItemReference, aipapi.ItemKindCompaction:
			// Portable wired forms; EncodeItem rejects structurally invalid data.
		default:
			return fmt.Errorf("%s: %w: item %d kind %q is not representable", id, ErrUnrepresentable, i, item.Kind)
		}
	}
	if len(call.Tools) > limits.MaxTools {
		return fmt.Errorf("%s: %w", id, limitError("request_tools", len(call.Tools), limits.MaxTools))
	}
	if extensionBytes > limits.MaxExtensionBytes {
		return fmt.Errorf("%s: %w", id, limitError("request_extensions", extensionBytes, limits.MaxExtensionBytes))
	}
	return nil
}

// checkRequirements verifies the call's complete protocol requirements are
// satisfied by this instance before any upstream work. It mirrors the routing
// admission rule so unsupported semantics fail with zero round trips even when
// Open is invoked directly.
func checkRequirements(id string, call aipapi.Call, caps aipapi.BackendCaps, ds aipapi.DialectSupport) error {
	required := aipapi.DeriveProtocolRequirements(call)
	capList := make([]aipapi.Capability, 0, len(caps))
	for c := range caps {
		capList = append(capList, c)
	}
	supported := aipapi.ProtocolRequirements{
		Capabilities:       capList,
		ItemDialects:       ds.ItemDialects,
		ReasoningDialects:  ds.ReasoningDialects,
		CompactionDialects: ds.CompactionDialects,
		ExtensionTypes:     ds.ExtensionTypes,
	}
	if err := aipapi.MatchRequirements(required, supported, aipapi.ReasoningReplaySupport{}).Err(); err != nil {
		return fmt.Errorf("%s: %w: %v", id, ErrUnrepresentable, err)
	}
	return nil
}

// wireToolArguments normalizes canonical tool call arguments to the pinned
// profile's JSON-string wire form: a JSON string stays verbatim; any other
// valid JSON value is wrapped into a JSON string.
func wireToolArguments(raw []byte) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '"' {
		if json.Valid(trimmed) {
			return trimmed, nil
		}
		return nil, fmt.Errorf("%w: malformed function call arguments", ErrUnrepresentable)
	}
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("%w: function call arguments must be valid JSON", ErrUnrepresentable)
	}
	b, err := json.Marshal(string(trimmed))
	if err != nil {
		return nil, fmt.Errorf("%w: function call arguments: %v", ErrUnrepresentable, err)
	}
	return b, nil
}

// buildCreateRequest maps an item-authoritative canonical call to a
// schema-valid non-streaming create request body. It never forwards proxy IDs,
// sessions, native refs, or arbitrary call extension fields.
func buildCreateRequest(id string, spec BackendSpec, call aipapi.Call, cand routing.AttemptCandidate) ([]byte, error) {
	return buildCreateRequestBody(id, spec, call, cand, false)
}

// buildCreateRequestBody maps an item-authoritative canonical call to a
// schema-valid create request body. When stream is true the pinned profile's
// stream flag is set and the caller must negotiate SSE transport.
func buildCreateRequestBody(id string, spec BackendSpec, call aipapi.Call, cand routing.AttemptCandidate, stream bool) ([]byte, error) {
	model := resolveModel(cand)
	if model == "" {
		return nil, fmt.Errorf("%s: %w: model is required", id, ErrUnrepresentable)
	}
	if len(call.Tools) == 0 {
		call.ToolChoice = aipapi.ToolChoice{}
	}
	if err := checkRepresentable(id, call, spec.RequestLimits); err != nil {
		return nil, err
	}

	hasToolCalls := false
	for _, item := range call.Items {
		if item.Kind == aipapi.ItemKindToolCall {
			hasToolCalls = true
			break
		}
	}
	if hasToolCalls {
		newItems := make([]aipapi.Item, len(call.Items))
		copy(newItems, call.Items)
		for i, item := range newItems {
			if item.Kind == aipapi.ItemKindToolCall {
				args, err := wireToolArguments(item.ToolCall.Arguments)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", id, err)
				}
				tc := *item.ToolCall
				tc.Arguments = args
				item.ToolCall = &tc
				newItems[i] = item
			}
		}
		call.Items = newItems
	}

	for i, item := range call.Items {
		w, err := proto.EncodeItem(item)
		if err != nil {
			return nil, fmt.Errorf("%s: %w: item %d: %v", id, ErrUnrepresentable, i, err)
		}
		encoded, err := json.Marshal(w)
		if err != nil {
			return nil, fmt.Errorf("%s: %w: item %d: %v", id, ErrUnrepresentable, i, err)
		}
		if len(encoded) > spec.RequestLimits.MaxItemBytes {
			return nil, fmt.Errorf("%s: %w", id, limitError(fmt.Sprintf("request_items[%d]", i), len(encoded), spec.RequestLimits.MaxItemBytes))
		}
	}

	body, err := proto.EncodeOutboundRequest(call, proto.OutboundEncodeOptions{
		Model:             model,
		Stream:            stream,
		IncludeExtensions: false,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %v", id, ErrUnrepresentable, err)
	}

	if len(body) > proto.MaxRequestBytes {
		return nil, fmt.Errorf("%s: %w", id, limitError("request_size", len(body), proto.MaxRequestBytes))
	}
	return body, nil
}
