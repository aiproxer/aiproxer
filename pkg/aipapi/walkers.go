package aipapi

import "fmt"

// NormalizedItems returns the item trajectory for c.
// If c has item authority (len(c.Items) > 0), it returns c.Items.
// Otherwise, it projects legacy c.Instructions and c.Messages into a normalized slice of Item.
func NormalizedItems(c Call) []Item {
	return normalizedItems(c, 0)
}

// NormalizedItemsCapped returns the first max entries of the normalized item
// trajectory for c, without materializing unselected legacy items. A
// non-positive max returns the full trajectory, like NormalizedItems. For
// item-authoritative calls it slices the existing items without copying;
// for legacy calls it projects instructions first, then messages, stopping
// once max entries are produced. Callers that hash or compare a bounded
// prefix (for example content-free digest feeds) must use this instead of
// slicing NormalizedItems, so huge trajectories pay only for the retained
// prefix.
func NormalizedItemsCapped(c Call, max int) []Item {
	return normalizedItems(c, max)
}

func normalizedItems(c Call, max int) []Item {
	if c.HasItemAuthority() {
		if max > 0 && len(c.Items) > max {
			return c.Items[:max]
		}
		return c.Items
	}

	capped := max > 0
	var items []Item
	for i, msg := range c.Instructions {
		if capped && len(items) == max {
			return items
		}
		item := Item{
			Kind:   ItemKindMessage,
			ID:     fmt.Sprintf("inst-%d", i),
			Status: ItemStatusCompleted,
			Role:   msg.Role,
		}
		if item.Role == "" {
			item.Role = RoleSystem
		}
		item.Content = partsToContentParts(msg.Parts)
		items = append(items, item)
	}

	for i, msg := range c.Messages {
		if capped && len(items) == max {
			return items
		}
		item := Item{
			Kind:   ItemKindMessage,
			ID:     fmt.Sprintf("msg-%d", i),
			Status: ItemStatusCompleted,
			Role:   msg.Role,
		}
		item.Content = partsToContentParts(msg.Parts)
		items = append(items, item)
	}

	return items
}

func partsToContentParts(parts []Part) []ContentPart {
	out := make([]ContentPart, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case PartText:
			out = append(out, ContentPart{Kind: ContentPartText, Text: p.Text})
		case PartImageRef:
			var ann *AnnotationPart
			if len(p.Content) > 0 {
				ann = &AnnotationPart{Type: "image_detail", Data: p.Content}
			}
			out = append(out, ContentPart{Kind: ContentPartImageRef, ImageRef: p.ImageRef, ImageMIME: p.ImageMIME, Annotation: ann})
		case PartFileRef:
			out = append(out, ContentPart{Kind: ContentPartFileRef, FileRef: p.FileRef, FileMIME: p.FileMIME, FileName: p.FileName})
		case PartReasoning:
			out = append(out, ContentPart{Kind: ContentPartReasoning, Reasoning: p.Reasoning})
		case PartToolResult:
			out = append(out, ContentPart{
				Kind: ContentPartToolResult,
				Text: p.Text,
			})
		case PartJSON:
			var ann *AnnotationPart
			if len(p.Content) > 0 {
				ann = &AnnotationPart{Type: "json_content", Data: p.Content}
			}
			out = append(out, ContentPart{
				Kind:       ContentPartJSON,
				Text:       string(p.Content),
				Annotation: ann,
			})
		default:
			if p.Text != "" {
				out = append(out, ContentPart{Kind: ContentPartText, Text: p.Text})
			}
		}
	}
	return out
}

// WalkCallItems visits every Item in c's normalized trajectory.
func WalkCallItems(c Call, fn func(item Item) error) error {
	items := NormalizedItems(c)
	for _, item := range items {
		if err := fn(item); err != nil {
			return err
		}
	}
	return nil
}

// WalkCallContentParts visits every ContentPart in c's normalized item trajectory.
func WalkCallContentParts(c Call, fn func(item Item, part ContentPart) error) error {
	return WalkCallItems(c, func(item Item) error {
		for _, cp := range item.Content {
			if err := fn(item, cp); err != nil {
				return err
			}
		}
		if item.Kind == ItemKindToolResult && item.ToolResult != nil {
			for _, cp := range item.ToolResult.Parts {
				if err := fn(item, cp); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// WalkCallTexts visits every text payload in c's trajectory (legacy or item authority).
// Uses NormalizedItems as the single unified traversal path for audit, redaction, and secrets scanning.
func WalkCallTexts(c Call, fn func(field string, text string) error) error {
	items := NormalizedItems(c)
	for i, item := range items {
		fieldPrefix := fmt.Sprintf("Items[%d]", i)
		for j, cp := range item.Content {
			cpField := fmt.Sprintf("%s.Content[%d]", fieldPrefix, j)
			if cp.Text != "" {
				if err := fn(cpField+".Text", cp.Text); err != nil {
					return err
				}
			}
			if cp.Refusal != "" {
				if err := fn(cpField+".Refusal", cp.Refusal); err != nil {
					return err
				}
			}
			if cp.Summary != "" {
				if err := fn(cpField+".Summary", cp.Summary); err != nil {
					return err
				}
			}
			if cp.Reasoning != nil && cp.Reasoning.Text != "" {
				if err := fn(cpField+".Reasoning.Text", cp.Reasoning.Text); err != nil {
					return err
				}
			}
		}
		if item.ToolResult != nil {
			if item.ToolResult.Output != "" {
				if err := fn(fmt.Sprintf("%s.ToolResult.Output", fieldPrefix), item.ToolResult.Output); err != nil {
					return err
				}
			}
			for j, cp := range item.ToolResult.Parts {
				cpField := fmt.Sprintf("%s.ToolResult.Parts[%d]", fieldPrefix, j)
				if cp.Text != "" {
					if err := fn(cpField+".Text", cp.Text); err != nil {
						return err
					}
				}
				if cp.Refusal != "" {
					if err := fn(cpField+".Refusal", cp.Refusal); err != nil {
						return err
					}
				}
				if cp.Summary != "" {
					if err := fn(cpField+".Summary", cp.Summary); err != nil {
						return err
					}
				}
				if cp.Reasoning != nil && cp.Reasoning.Text != "" {
					if err := fn(cpField+".Reasoning.Text", cp.Reasoning.Text); err != nil {
						return err
					}
				}
			}
		}
		if item.Reasoning != nil && item.Reasoning.Reasoning != nil && item.Reasoning.Reasoning.Text != "" {
			if err := fn(fmt.Sprintf("%s.Reasoning.Text", fieldPrefix), item.Reasoning.Reasoning.Text); err != nil {
				return err
			}
		}
	}
	return nil
}

// WalkCallOpaqueData visits all raw JSON/opaque data blobs across items/extensions for redaction and inspection.
func WalkCallOpaqueData(c Call, fn func(field string, data []byte) error) error {
	return WalkCallItems(c, func(item Item) error {
		if item.Extension != nil && len(item.Extension.Data) > 0 {
			if err := fn("Extension.Data", item.Extension.Data); err != nil {
				return err
			}
		}
		if item.Compaction != nil && len(item.Compaction.Opaque) > 0 {
			if err := fn("Compaction.Opaque", item.Compaction.Opaque); err != nil {
				return err
			}
		}
		if item.ToolCall != nil && len(item.ToolCall.Arguments) > 0 {
			if err := fn("ToolCall.Arguments", item.ToolCall.Arguments); err != nil {
				return err
			}
		}
		if item.Reasoning != nil && item.Reasoning.Reasoning != nil && len(item.Reasoning.Reasoning.Opaque) > 0 {
			if err := fn("Reasoning.Opaque", item.Reasoning.Reasoning.Opaque); err != nil {
				return err
			}
		}
		for _, cp := range item.Content {
			if cp.Annotation != nil && len(cp.Annotation.Data) > 0 {
				if err := fn("Annotation.Data", cp.Annotation.Data); err != nil {
					return err
				}
			}
			if cp.Extension != nil && len(cp.Extension.Data) > 0 {
				if err := fn("ExtensionContent.Data", cp.Extension.Data); err != nil {
					return err
				}
			}
		}
		if item.ToolResult != nil {
			for _, cp := range item.ToolResult.Parts {
				if cp.Extension != nil && len(cp.Extension.Data) > 0 {
					if err := fn("ToolResult.Parts.ExtensionContent.Data", cp.Extension.Data); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}
