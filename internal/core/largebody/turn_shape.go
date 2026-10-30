package largebody

import (
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/capabilityfacts"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ErrSemanticFactBudgetExceeded indicates that normalized item/part metadata
// or fact attributes exceeded the configured semantic-fact budget, requiring
// pre-commit fallback to canonical processing (Requirement 14.4).
var ErrSemanticFactBudgetExceeded = capabilityfacts.ErrSemanticFactBudgetExceeded

// IsSemanticFactBudgetExceeded reports whether err is or wraps ErrSemanticFactBudgetExceeded.
func IsSemanticFactBudgetExceeded(err error) bool {
	return errors.Is(err, ErrSemanticFactBudgetExceeded)
}

func buildTurnShape(count int, maxFactBytes int64, buildItem func(ord int) (aipapi.ItemKind, aipapi.Role, []ClientTurnPartShape, error)) (ClientTurnShape, error) {
	if err := checkBudget(maxFactBytes); err != nil {
		return ClientTurnShape{}, err
	}
	if int64(count) > maxFactBytes {
		return ClientTurnShape{}, fmt.Errorf("%w: item count %d exceeds budget %d", ErrSemanticFactBudgetExceeded, count, maxFactBytes)
	}
	items := make([]ClientTurnItemShape, 0, count)
	var totalBytes int64
	for ord := range count {
		kind, role, parts, err := buildItem(ord)
		if err != nil {
			return ClientTurnShape{}, err
		}
		for _, p := range parts {
			totalBytes += p.ContentBytes
		}
		items = append(items, ClientTurnItemShape{Kind: kind, Role: role, Ordinal: int64(ord), Parts: parts})
	}
	shape := ClientTurnShape{Items: items, TotalContentBytes: totalBytes}
	return shape, shape.Validate(maxFactBytes)
}

// ClientTurnShapeFromMessages derives a bounded ClientTurnShape from a message slice
// without synthesizing a aipapi.Call. It maps message parts with exact equivalence
// to canonical partsToContentParts without allocating payload-sized strings.
func ClientTurnShapeFromMessages(msgs []aipapi.Message, maxFactBytes int64) (ClientTurnShape, error) {
	return buildTurnShape(len(msgs), maxFactBytes, func(ord int) (aipapi.ItemKind, aipapi.Role, []ClientTurnPartShape, error) {
		m := msgs[ord]
		parts := make([]ClientTurnPartShape, 0, len(m.Parts))
		for _, p := range m.Parts {
			if ps, keep := partShape(p); keep {
				parts = append(parts, ps)
			}
		}
		if int64(len(parts)) > maxFactBytes {
			return "", "", nil, fmt.Errorf("%w: item %d part count %d exceeds budget %d", ErrSemanticFactBudgetExceeded, ord, len(parts), maxFactBytes)
		}
		return aipapi.ItemKindMessage, m.Role, parts, nil
	})
}

// ClientTurnShapeFromItems derives a bounded ClientTurnShape from normalized items
// without retaining or materializing prompt text.
func ClientTurnShapeFromItems(items []aipapi.Item, maxFactBytes int64) (ClientTurnShape, error) {
	return buildTurnShape(len(items), maxFactBytes, func(ord int) (aipapi.ItemKind, aipapi.Role, []ClientTurnPartShape, error) {
		it := items[ord]
		var parts []ClientTurnPartShape
		if it.Kind == aipapi.ItemKindMessage {
			if int64(len(it.Content)) > maxFactBytes {
				return "", "", nil, fmt.Errorf("%w: item %d part count %d exceeds budget %d", ErrSemanticFactBudgetExceeded, ord, len(it.Content), maxFactBytes)
			}
			parts = make([]ClientTurnPartShape, 0, len(it.Content))
			for _, cp := range it.Content {
				parts = append(parts, ClientTurnPartShape{
					Kind:         cp.Kind,
					ContentBytes: contentPartByteSize(cp),
				})
			}
		} else if it.ToolResult != nil && len(it.ToolResult.Parts) > 0 {
			if int64(len(it.ToolResult.Parts)) > maxFactBytes {
				return "", "", nil, fmt.Errorf("%w: item %d tool result part count %d exceeds budget %d", ErrSemanticFactBudgetExceeded, ord, len(it.ToolResult.Parts), maxFactBytes)
			}
			parts = make([]ClientTurnPartShape, 0, len(it.ToolResult.Parts))
			for _, cp := range it.ToolResult.Parts {
				parts = append(parts, ClientTurnPartShape{
					Kind:         cp.Kind,
					ContentBytes: contentPartByteSize(cp),
				})
			}
		}
		return it.Kind, it.Role, parts, nil
	})
}

// ClientTurnShapeFromCall derives a bounded ClientTurnShape equivalent to
// aipapi.NormalizedItems for the given call without retaining or materializing
// prompt text (Requirements 14.3, 14.5).
// If call is nil or has no items, an empty ClientTurnShape is returned.
// If the derived shape exceeds maxFactBytes, it returns an error wrapping
// ErrSemanticFactBudgetExceeded (Requirement 14.4).
func ClientTurnShapeFromCall(call *aipapi.Call, maxFactBytes int64) (ClientTurnShape, error) {
	if call == nil {
		return ClientTurnShapeFromItems(nil, maxFactBytes)
	}
	return ClientTurnShapeFromItems(aipapi.NormalizedItems(*call), maxFactBytes)
}

func contentPartByteSize(cp aipapi.ContentPart) int64 {
	switch cp.Kind {
	case aipapi.ContentPartText, aipapi.ContentPartJSON, aipapi.ContentPartToolResult:
		return int64(len(cp.Text))
	case aipapi.ContentPartImageRef:
		return int64(len(cp.ImageRef) + len(cp.ImageMIME))
	case aipapi.ContentPartFileRef:
		return int64(len(cp.FileRef) + len(cp.FileData) + len(cp.FileMIME) + len(cp.FileName))
	case aipapi.ContentPartVideoRef:
		return int64(len(cp.VideoRef) + len(cp.VideoMIME))
	case aipapi.ContentPartReasoning:
		if cp.Reasoning != nil {
			return int64(len(cp.Reasoning.Text))
		}
		return 0
	case aipapi.ContentPartRefusal:
		return int64(len(cp.Refusal))
	case aipapi.ContentPartSummary:
		return int64(len(cp.Summary))
	case aipapi.ContentPartAssistantRef:
		return int64(len(cp.AssistantRef))
	case aipapi.ContentPartExtension:
		if cp.Extension != nil {
			return int64(len(cp.Extension.Data))
		}
		return 0
	default:
		return int64(len(cp.Text))
	}
}

func partShape(p aipapi.Part) (ClientTurnPartShape, bool) {
	switch p.Kind {
	case aipapi.PartText:
		return ClientTurnPartShape{Kind: aipapi.ContentPartText, ContentBytes: int64(len(p.Text))}, true
	case aipapi.PartImageRef:
		return ClientTurnPartShape{Kind: aipapi.ContentPartImageRef, ContentBytes: int64(len(p.ImageRef) + len(p.ImageMIME))}, true
	case aipapi.PartFileRef:
		return ClientTurnPartShape{Kind: aipapi.ContentPartFileRef, ContentBytes: int64(len(p.FileRef) + len(p.FileMIME) + len(p.FileName))}, true
	case aipapi.PartReasoning:
		var n int64
		if p.Reasoning != nil {
			n = int64(len(p.Reasoning.Text))
		}
		return ClientTurnPartShape{Kind: aipapi.ContentPartReasoning, ContentBytes: n}, true
	case aipapi.PartToolResult:
		return ClientTurnPartShape{Kind: aipapi.ContentPartToolResult, ContentBytes: int64(len(p.Text))}, true
	case aipapi.PartJSON:
		return ClientTurnPartShape{Kind: aipapi.ContentPartJSON, ContentBytes: int64(len(p.Content))}, true
	default:
		if p.Text != "" {
			return ClientTurnPartShape{Kind: aipapi.ContentPartText, ContentBytes: int64(len(p.Text))}, true
		}
		return ClientTurnPartShape{}, false
	}
}
