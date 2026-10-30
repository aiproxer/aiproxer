package upstream

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestTextOnlyRequestBuildersRejectMedia(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		call aipapi.Call
	}{
		{
			name: "image reference",
			call: aipapi.Call{Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{{Kind: aipapi.PartImageRef, ImageRef: "https://example.invalid/image.png"}},
			}}},
		},
		{
			name: "file reference",
			call: aipapi.Call{Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{{Kind: aipapi.PartFileRef, FileRef: "https://example.invalid/document.pdf"}},
			}}},
		},
		{
			name: "video item",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind:    aipapi.ItemKindMessage,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartVideoRef, VideoRef: "https://example.invalid/video.mp4"}},
			}}},
		},
		{
			name: "tool result image part",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind: aipapi.ItemKindToolResult,
				ToolResult: &aipapi.ToolResultItem{Parts: []aipapi.ContentPart{{
					Kind: aipapi.ContentPartImageRef, ImageRef: "https://example.invalid/tool-image.png",
				}}},
			}}},
		},
		{
			name: "tool result file part",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind: aipapi.ItemKindToolResult,
				ToolResult: &aipapi.ToolResultItem{Parts: []aipapi.ContentPart{{
					Kind: aipapi.ContentPartFileRef, FileRef: "https://example.invalid/tool-file.pdf",
				}}},
			}}},
		},
		{
			name: "tool result video part",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind: aipapi.ItemKindToolResult,
				ToolResult: &aipapi.ToolResultItem{Parts: []aipapi.ContentPart{{
					Kind: aipapi.ContentPartVideoRef, VideoRef: "https://example.invalid/tool-video.mp4",
				}}},
			}}},
		},
	} {
		for _, builder := range []struct {
			name  string
			build func(aipapi.Call) ([]byte, error)
		}{
			{name: "anthropic", build: func(c aipapi.Call) ([]byte, error) { return anthropicRequestBody(c, "claude") }},
			{name: "gemini", build: geminiRequestBody},
		} {
			t.Run(tc.name+"/"+builder.name, func(t *testing.T) {
				t.Parallel()
				_, err := builder.build(tc.call)
				if err == nil || !strings.Contains(err.Error(), "unsupported canonical media") {
					t.Fatalf("error=%v, want explicit unsupported canonical media error", err)
				}
			})
		}
	}
}

func TestTextOnlyRequestBuildersAcceptText(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}
	if _, err := anthropicRequestBody(call, "claude"); err != nil {
		t.Fatalf("anthropic text request: %v", err)
	}
	if _, err := geminiRequestBody(call); err != nil {
		t.Fatalf("gemini text request: %v", err)
	}
}
