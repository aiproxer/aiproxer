package standardplugins

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
)

func TestUsesOpenAINativeUsageMapper(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind string
		want bool
	}{
		{name: "essential openai-legacy", kind: openailegacy.ID, want: true},
		{name: "essential openai-responses", kind: openairesponses.ID, want: true},
		{name: "custom openai legacy compatible", kind: CustomOpenAILegacyCompatibleID, want: true},
		{name: "custom openai responses compatible", kind: CustomOpenAIResponsesCompatibleID, want: true},
		{name: "surrounding whitespace", kind: "  " + openairesponses.ID + "  ", want: true},
		{name: "anthropic essential", kind: anthropic.ID, want: false},
		{name: "gemini essential", kind: gemini.ID, want: false},
		{name: "custom anthropic compatible", kind: CustomAnthropicCompatibleID, want: false},
		{name: "custom openresponses compatible", kind: CustomOpenResponsesCompatibleID, want: false},
		{name: "arbitrary instance id is not a kind", kind: "my-arbitrary-instance", want: false},
		{name: "empty", kind: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := UsesOpenAINativeUsageMapper(tt.kind); got != tt.want {
				t.Fatalf("UsesOpenAINativeUsageMapper(%q) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}
