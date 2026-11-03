package openresponses

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestDecodeReplayItem_OnlyEmptyAssistantTextIsNormalized locks the replay
// normalization for the proxy's own tool-only message placeholder: empty
// assistant text parts disappear, a message left with no content is dropped,
// and every nonempty or non-text part survives in order.
func TestDecodeReplayItem_OnlyEmptyAssistantTextIsNormalized(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		role      string
		phase     string
		content   string
		wantKeep  bool
		wantParts []aipapi.ContentPart
	}{
		{
			name:     "empty assistant message is dropped",
			role:     "assistant",
			content:  `[{"type":"output_text","text":""}]`,
			wantKeep: false,
		},
		{
			name:     "empty assistant text before nonempty text is removed",
			role:     "assistant",
			content:  `[{"type":"output_text","text":""},{"type":"output_text","text":"kept"}]`,
			wantKeep: true,
			wantParts: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "kept"},
			},
		},
		{
			name:     "empty assistant text before an image is removed",
			role:     "assistant",
			content:  `[{"type":"output_text","text":""},{"type":"input_image","image_url":"https://example.com/page.png"}]`,
			wantKeep: true,
			wantParts: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartImageRef, ImageRef: "https://example.com/page.png"},
			},
		},
		{
			name:     "empty user message is not normalized",
			role:     "user",
			content:  `[{"type":"input_text","text":""}]`,
			wantKeep: true,
			wantParts: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: ""},
			},
		},
		{
			name:     "empty phased assistant message is not normalized",
			role:     "assistant",
			phase:    "commentary",
			content:  `[{"type":"output_text","text":""}]`,
			wantKeep: true,
			wantParts: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: ""},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			item, keep, err := DecodeReplayItem(WireItem{
				Type:    "message",
				Role:    tc.role,
				Phase:   tc.phase,
				Content: json.RawMessage(tc.content),
			}, DefaultLimits())
			if err != nil {
				t.Fatalf("DecodeReplayItem: %v", err)
			}
			if keep != tc.wantKeep {
				t.Fatalf("keep=%v, want %v", keep, tc.wantKeep)
			}
			if !keep {
				return
			}
			if len(item.Content) != len(tc.wantParts) {
				t.Fatalf("content=%+v, want %+v", item.Content, tc.wantParts)
			}
			for i := range tc.wantParts {
				if item.Content[i] != tc.wantParts[i] {
					t.Fatalf("content[%d]=%+v, want %+v", i, item.Content[i], tc.wantParts[i])
				}
			}
		})
	}
}
