package routeselect

import (
	"testing"
)

func TestInlineOrDefault(t *testing.T) {
	t.Parallel()
	prefixes := NewPrefixSet([]string{
		"anthropic",
		"huggingface",
		"llamacpp",
		"lmstudio",
		"ollama-cloud",
		"openai-codex",
		"opencode-go",
		"opencode-zen",
		"stub",
		"vllm",
	})

	tests := []struct {
		name         string
		model        string
		defaultRoute string
		want         string
	}{
		{
			name:         "known prefix with model",
			model:        "anthropic:claude-3-5-sonnet",
			defaultRoute: "openai-legacy:gpt-4o",
			want:         "anthropic:claude-3-5-sonnet",
		},
		{
			name:         "ollama cloud prefix with canonical model",
			model:        "ollama-cloud:google/gemma3:4b",
			defaultRoute: "openai-legacy:gpt-4o",
			want:         "ollama-cloud:google/gemma3:4b",
		},
		{
			name:         "llamacpp prefix with local model",
			model:        "llamacpp:local-model",
			defaultRoute: "openai-legacy:gpt-4o",
			want:         "llamacpp:local-model",
		},
		{
			name:         "unknown prefix",
			model:        "xyz:claude-3-5-sonnet",
			defaultRoute: "openai-legacy:gpt-4o",
			want:         "openai-legacy:gpt-4o",
		},
		{
			name:         "test stub prefix",
			model:        "stub:claude-3-5-sonnet",
			defaultRoute: "openai-legacy:gpt-4o",
			want:         "stub:claude-3-5-sonnet",
		},
		{
			name:         "no prefix",
			model:        "claude-3-5-sonnet",
			defaultRoute: "openai-legacy:gpt-4o",
			want:         "openai-legacy:gpt-4o",
		},
		{
			name:         "empty model",
			model:        "",
			defaultRoute: "openai-legacy:gpt-4o",
			want:         "openai-legacy:gpt-4o",
		},
		{
			name:         "whitespace trimmed",
			model:        "  anthropic:claude-3-5-sonnet  ",
			defaultRoute: "  openai-legacy:gpt-4o  ",
			want:         "anthropic:claude-3-5-sonnet",
		},
		// openai-codex: arbitrary model + optional URI params must override the default route.
		{
			name:         "openai-codex prefix with reasoning_effort param",
			model:        "openai-codex:gpt-5.5?reasoning_effort=low",
			defaultRoute: "openai-codex:gpt-5.5",
			want:         "openai-codex:gpt-5.5?reasoning_effort=low",
		},
		{
			name:         "openai-codex prefix gpt-5.4",
			model:        "openai-codex:gpt-5.4",
			defaultRoute: "openai-codex:gpt-5.5",
			want:         "openai-codex:gpt-5.4",
		},
		{
			name:         "openai-codex prefix gpt-5.4-mini",
			model:        "openai-codex:gpt-5.4-mini",
			defaultRoute: "openai-codex:gpt-5.5",
			want:         "openai-codex:gpt-5.4-mini",
		},
		{
			name:         "openai-codex prefix arbitrary model not in static inventory",
			model:        "openai-codex:gpt-5.3-codex-spark",
			defaultRoute: "openai-codex:gpt-5.5",
			want:         "openai-codex:gpt-5.3-codex-spark",
		},
		// Other standard backends missing from the allowlist must also route from the body model.
		{
			name:         "opencode-go prefix",
			model:        "opencode-go:zen-go-1",
			defaultRoute: "openai-codex:gpt-5.5",
			want:         "opencode-go:zen-go-1",
		},
		{
			name:         "opencode-zen prefix",
			model:        "opencode-zen:zen-1",
			defaultRoute: "openai-codex:gpt-5.5",
			want:         "opencode-zen:zen-1",
		},
		{
			name:         "huggingface prefix",
			model:        "huggingface:Qwen/Qwen3",
			defaultRoute: "openai-codex:gpt-5.5",
			want:         "huggingface:Qwen/Qwen3",
		},
		{
			name:         "vllm prefix",
			model:        "vllm:llama-3",
			defaultRoute: "openai-codex:gpt-5.5",
			want:         "vllm:llama-3",
		},
		{
			name:         "lmstudio prefix",
			model:        "lmstudio:local-model",
			defaultRoute: "openai-codex:gpt-5.5",
			want:         "lmstudio:local-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := prefixes.InlineOrDefault(tt.model, tt.defaultRoute)
			if got != tt.want {
				t.Errorf("InlineOrDefault(%q, %q) = %q, want %q", tt.model, tt.defaultRoute, got, tt.want)
			}
		})
	}
}

func TestFromModelOrDefault(t *testing.T) {
	t.Parallel()
	prefixes := NewPrefixSet([]string{"anthropic", "openai-codex"})

	tests := []struct {
		name         string
		body         string
		defaultRoute string
		want         string
	}{
		{
			name:         "valid json with known inline model",
			body:         `{"model": "anthropic:claude-3-5"}`,
			defaultRoute: "openai-responses:gpt-4o",
			want:         "anthropic:claude-3-5",
		},
		{
			name:         "valid json with unknown inline model",
			body:         `{"model": "foo:bar"}`,
			defaultRoute: "openai-responses:gpt-4o",
			want:         "openai-responses:gpt-4o",
		},
		{
			name:         "valid json with no model",
			body:         `{"messages": []}`,
			defaultRoute: "openai-responses:gpt-4o",
			want:         "openai-responses:gpt-4o",
		},
		{
			name:         "invalid json",
			body:         `{invalid`,
			defaultRoute: "openai-responses:gpt-4o",
			want:         "openai-responses:gpt-4o",
		},
		{
			name:         "empty body",
			body:         "",
			defaultRoute: "openai-responses:gpt-4o",
			want:         "openai-responses:gpt-4o",
		},
		// openai-codex: the body model (with optional URI params) must override the default route.
		{
			name:         "openai-codex inline model with reasoning_effort param",
			body:         `{"model": "openai-codex:gpt-5.5?reasoning_effort=low"}`,
			defaultRoute: "openai-codex:gpt-5.5",
			want:         "openai-codex:gpt-5.5?reasoning_effort=low",
		},
		{
			name:         "openai-codex inline model no params",
			body:         `{"model": "openai-codex:gpt-5.4"}`,
			defaultRoute: "openai-codex:gpt-5.5",
			want:         "openai-codex:gpt-5.4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := prefixes.FromModelOrDefault([]byte(tt.body), tt.defaultRoute)
			if got != tt.want {
				t.Errorf("FromModelOrDefault(%q, %q) = %q, want %q", tt.body, tt.defaultRoute, got, tt.want)
			}
		})
	}
}
