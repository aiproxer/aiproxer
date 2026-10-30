package routing

import (
	"slices"
	"testing"
)

func TestFilterRoutePrefixes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{name: "empty input", input: nil, want: []string{}},
		{name: "trims whitespace", input: []string{"  openai-codex  ", "anthropic"}, want: []string{"anthropic", "openai-codex"}},
		{name: "drops empty", input: []string{"", "openai-codex", "   "}, want: []string{"openai-codex"}},
		{name: "drops colon-bearing prefix", input: []string{"openai-codex", "foo:bar"}, want: []string{"openai-codex"}},
		{name: "drops slash-bearing prefix", input: []string{"openai-codex", "foo/bar"}, want: []string{"openai-codex"}},
		{name: "dedups", input: []string{"openai-codex", "anthropic", "openai-codex"}, want: []string{"anthropic", "openai-codex"}},
		{name: "sorted output", input: []string{"ollama", "anthropic", "gemini"}, want: []string{"anthropic", "gemini", "ollama"}},
		{name: "all invalid", input: []string{"", ":", "/", "  "}, want: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FilterRoutePrefixes(tt.input)
			if !slices.Equal(got, tt.want) {
				t.Errorf("FilterRoutePrefixes(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
