package backendplugin

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func applyInvocationWireToCall(call *aipapi.Call, inv Invocation) error {
	if inv.Operation != "" {
		call.Invocation.Operation = aipapi.Operation(inv.Operation)
	}
	if inv.DeliveryMode != "" {
		call.Invocation.DeliveryMode = aipapi.DeliveryMode(inv.DeliveryMode)
	}
	if inv.TransportMode != "" {
		call.Invocation.TransportMode = aipapi.TransportMode(inv.TransportMode)
	}
	if pck := strings.TrimSpace(inv.PromptCacheKey); pck != "" {
		call.PromptCacheKey = pck
	}
	if !inv.ItemAuthority {
		return nil
	}
	items, err := itemsFromInvocationDTO(inv.Items)
	if err != nil {
		return err
	}
	call.Items = items
	call.Messages = nil
	call.Instructions = nil
	return nil
}

func itemsFromInvocationDTO(in []InvocationItem) ([]aipapi.Item, error) {
	out := make([]aipapi.Item, 0, len(in))
	for i, item := range in {
		mapped, err := itemFromInvocationDTO(item, fmt.Sprintf("Items[%d]", i))
		if err != nil {
			return nil, err
		}
		out = append(out, mapped)
	}
	return out, nil
}

func itemFromInvocationDTO(item InvocationItem, field string) (aipapi.Item, error) {
	out := aipapi.Item{
		Kind:   aipapi.ItemKind(item.Kind),
		ID:     item.ID,
		Status: aipapi.ItemStatus(item.Status),
		Phase:  aipapi.AssistantPhase(item.Phase),
	}
	if item.Role != "" && strings.ToLower(strings.TrimSpace(item.Kind)) != "tool_result" {
		out.Role = item.Role
	}
	for j, cp := range item.Content {
		part, err := contentPartFromInvocationDTO(cp, fmt.Sprintf("%s.Content[%d]", field, j))
		if err != nil {
			return aipapi.Item{}, err
		}
		out.Content = append(out.Content, part)
	}
	if item.ToolCall != nil {
		args := item.ToolCall.Arguments.Bytes()
		out.ToolCall = &aipapi.ToolCallItem{
			CallID:    item.ToolCall.CallID,
			Name:      item.ToolCall.Name,
			Arguments: append(json.RawMessage(nil), args...),
		}
	}
	if item.ToolResult != nil {
		tr := &aipapi.ToolResultItem{
			CallID: item.ToolResult.CallID,
			Name:   item.ToolResult.Name,
		}
		if item.ToolResult.Output != nil {
			tr.Output = *item.ToolResult.Output
		}
		for j, cp := range item.ToolResult.StructuredParts {
			part, err := contentPartFromInvocationDTO(cp, fmt.Sprintf("%s.ToolResult.StructuredParts[%d]", field, j))
			if err != nil {
				return aipapi.Item{}, err
			}
			tr.Parts = append(tr.Parts, part)
		}
		out.ToolResult = tr
	}
	if item.ItemReference != nil {
		out.Reference = &aipapi.ItemReference{ID: item.ItemReference.ID}
	}
	if item.Reasoning != nil {
		rp, err := reasoningPartFromInvocationItem(*item.Reasoning, field+".Reasoning")
		if err != nil {
			return aipapi.Item{}, err
		}
		out.Reasoning = &aipapi.ReasoningItem{Reasoning: rp}
	}
	if item.Compaction != nil {
		out.Compaction = &aipapi.CompactionItem{
			EncapsulatedID:   item.Compaction.EncapsulatedID,
			Dialect:          item.Compaction.Dialect,
			Implementor:      item.Compaction.Implementor,
			EncryptedContent: item.Compaction.EncryptedContent,
			Opaque:           append(json.RawMessage(nil), item.Compaction.Opaque.Bytes()...),
		}
	}
	if item.Extension != nil {
		out.Extension = &aipapi.OpaqueExtension{
			Namespace:   item.Extension.Namespace,
			Type:        item.Extension.Type,
			Implementor: item.Extension.Implementor,
			Direction:   item.Extension.Direction,
			Data:        append(json.RawMessage(nil), item.Extension.Opaque.Bytes()...),
		}
	}
	return out, nil
}

func contentPartFromInvocationDTO(cp InvocationContentPart, field string) (aipapi.ContentPart, error) {
	out := aipapi.ContentPart{Kind: mapContentPartKindToAipapi(cp.Kind)}
	switch cp.Kind {
	case PartKindText:
		if cp.Text == nil {
			return aipapi.ContentPart{}, fmt.Errorf("%w: %s requires text", ErrInvalidInvocation, field)
		}
		out.Text = *cp.Text
	case PartKindJSON:
		if cp.Text == nil {
			return aipapi.ContentPart{}, fmt.Errorf("%w: %s requires json text", ErrInvalidInvocation, field)
		}
		out.Text = *cp.Text
	case PartKindToolResult:
		if cp.Text == nil {
			return aipapi.ContentPart{}, fmt.Errorf("%w: %s requires tool result text", ErrInvalidInvocation, field)
		}
		out.Text = *cp.Text
	case PartKindImageRef:
		if cp.ImageRef == nil {
			return aipapi.ContentPart{}, fmt.Errorf("%w: %s requires image_ref", ErrInvalidInvocation, field)
		}
		out.ImageRef = *cp.ImageRef
		if cp.ImageMIME != nil {
			out.ImageMIME = *cp.ImageMIME
		}
	case PartKindFileRef:
		if cp.FileRef == nil && cp.FileData == nil {
			return aipapi.ContentPart{}, fmt.Errorf("%w: %s requires file_ref or file_data", ErrInvalidInvocation, field)
		}
		if cp.FileRef != nil {
			out.FileRef = *cp.FileRef
		}
		if cp.FileMIME != nil {
			out.FileMIME = *cp.FileMIME
		}
		if cp.FileName != nil {
			out.FileName = *cp.FileName
		}
		if cp.FileData != nil {
			out.FileData = *cp.FileData
		}
	case PartKindVideoRef:
		if cp.VideoRef == nil {
			return aipapi.ContentPart{}, fmt.Errorf("%w: %s requires video_ref", ErrInvalidInvocation, field)
		}
		out.VideoRef = *cp.VideoRef
		if cp.VideoMIME != nil {
			out.VideoMIME = *cp.VideoMIME
		}
	case PartKindRefusal:
		if cp.Refusal == nil {
			return aipapi.ContentPart{}, fmt.Errorf("%w: %s requires refusal", ErrInvalidInvocation, field)
		}
		out.Refusal = *cp.Refusal
	case PartKindSummary:
		if cp.Summary == nil {
			return aipapi.ContentPart{}, fmt.Errorf("%w: %s requires summary", ErrInvalidInvocation, field)
		}
		out.Summary = *cp.Summary
	case PartKindAssistantRef:
		if cp.AssistantRef == nil {
			return aipapi.ContentPart{}, fmt.Errorf("%w: %s requires assistant_ref", ErrInvalidInvocation, field)
		}
		out.AssistantRef = *cp.AssistantRef
	case PartKindReasoning:
		if cp.Reasoning == nil {
			return aipapi.ContentPart{}, fmt.Errorf("%w: %s requires reasoning", ErrInvalidInvocation, field)
		}
		rp, err := reasoningPartFromInvocationPart(*cp.Reasoning, field+".Reasoning")
		if err != nil {
			return aipapi.ContentPart{}, err
		}
		out.Reasoning = rp
	case PartKindAnnotation:
		if cp.AnnotationType == nil {
			return aipapi.ContentPart{}, fmt.Errorf("%w: %s requires annotation type", ErrInvalidInvocation, field)
		}
		ann := &aipapi.AnnotationPart{Type: *cp.AnnotationType}
		if cp.AnnotationData.State() == RawJSONValue {
			ann.Data = append(json.RawMessage(nil), cp.AnnotationData.Bytes()...)
		}
		out.Annotation = ann
	case PartKindExtension:
		if cp.ExtensionType == nil {
			return aipapi.ContentPart{}, fmt.Errorf("%w: %s requires extension type", ErrInvalidInvocation, field)
		}
		ext := &aipapi.ExtensionContentPart{Type: *cp.ExtensionType}
		if cp.ExtensionNamespace != nil {
			ext.Namespace = *cp.ExtensionNamespace
		}
		if cp.ExtensionImplementor != nil {
			ext.Implementor = *cp.ExtensionImplementor
		}
		if cp.ExtensionData.State() == RawJSONValue {
			ext.Data = append(json.RawMessage(nil), cp.ExtensionData.Bytes()...)
		}
		out.Extension = ext
	default:
		return aipapi.ContentPart{}, fmt.Errorf("%w: %s unsupported kind %q", ErrInvalidInvocation, field, cp.Kind)
	}
	return out, nil
}

func reasoningPartFromInvocationItem(r InvocationReasoningItem, field string) (*aipapi.ReasoningPart, error) {
	out := &aipapi.ReasoningPart{}
	if r.Dialect != nil {
		out.Dialect = aipapi.ReasoningDialect(*r.Dialect)
	}
	if r.Text != nil {
		out.Text = *r.Text
	}
	if r.Signature != nil {
		out.Signature = *r.Signature
	}
	if r.Opaque.State() == RawJSONValue {
		out.Opaque = append([]byte(nil), r.Opaque.Bytes()...)
	}
	applyReasoningExactFields(r.Summary, r.Content, r.EncryptedContent, out)
	return out, nil
}

func reasoningPartFromInvocationPart(r InvocationReasoningPart, field string) (*aipapi.ReasoningPart, error) {
	out := &aipapi.ReasoningPart{}
	if r.Dialect != nil {
		out.Dialect = aipapi.ReasoningDialect(*r.Dialect)
	}
	if r.Text != nil {
		out.Text = *r.Text
	}
	if r.Signature != nil {
		out.Signature = *r.Signature
	}
	if r.Opaque.State() == RawJSONValue {
		out.Opaque = append([]byte(nil), r.Opaque.Bytes()...)
	}
	applyReasoningExactFields(r.Summary, r.Content, r.EncryptedContent, out)
	return out, nil
}

func mapContentPartKindToAipapi(k PartKind) aipapi.ContentPartKind {
	switch k {
	case PartKindText:
		return aipapi.ContentPartText
	case PartKindJSON:
		return aipapi.ContentPartJSON
	case PartKindToolResult:
		return aipapi.ContentPartToolResult
	case PartKindImageRef:
		return aipapi.ContentPartImageRef
	case PartKindFileRef:
		return aipapi.ContentPartFileRef
	case PartKindVideoRef:
		return aipapi.ContentPartVideoRef
	case PartKindReasoning:
		return aipapi.ContentPartReasoning
	case PartKindRefusal:
		return aipapi.ContentPartRefusal
	case PartKindSummary:
		return aipapi.ContentPartSummary
	case PartKindAnnotation:
		return aipapi.ContentPartAnnotation
	case PartKindAssistantRef:
		return aipapi.ContentPartAssistantRef
	case PartKindExtension:
		return aipapi.ContentPartExtension
	default:
		return aipapi.ContentPartKind(k)
	}
}
