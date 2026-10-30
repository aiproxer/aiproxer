package catalog

import "testing"

func TestInferFlavor_fromMetadata(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		entry ModelEntry
		want  Flavor
	}{
		{
			name: "openai responses package and endpoint",
			entry: ModelEntry{
				RawID:        "gpt-5.4",
				Endpoint:     "https://opencode.ai/zen/v1/responses",
				AISDKPackage: "@ai-sdk/openai",
			},
			want: FlavorOpenAIResponses,
		},
		{
			name: "openai compatible chat",
			entry: ModelEntry{
				RawID:        "deepseek-v4-flash",
				Endpoint:     "https://opencode.ai/zen/v1/chat/completions",
				AISDKPackage: "@ai-sdk/openai-compatible",
			},
			want: FlavorOpenAIChat,
		},
		{
			name: "anthropic messages",
			entry: ModelEntry{
				RawID:        "claude-sonnet-4-6",
				Endpoint:     "https://opencode.ai/zen/v1/messages",
				AISDKPackage: "@ai-sdk/anthropic",
			},
			want: FlavorAnthropicMessages,
		},
		{
			name: "google gemini",
			entry: ModelEntry{
				RawID:        "gemini-3.1-pro",
				Endpoint:     "https://generativelanguage.googleapis.com/v1beta/models/gemini-3.1-pro",
				AISDKPackage: "@ai-sdk/google",
			},
			want: FlavorGoogleGemini,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := InferFlavor(tc.entry); got != tc.want {
				t.Fatalf("InferFlavor() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInferFlavor_fromModelIDHeuristics(t *testing.T) {
	t.Parallel()

	cases := []struct {
		rawID string
		want  Flavor
	}{
		{"claude-sonnet-4-6", FlavorAnthropicMessages},
		{"gemini-3.1-pro", FlavorGoogleGemini},
		{"gpt-5.4", FlavorOpenAIResponses},
		{"glm-5.2", FlavorOpenAIChat},
	}

	for _, tc := range cases {
		t.Run(tc.rawID, func(t *testing.T) {
			t.Parallel()
			if got := InferFlavor(ModelEntry{RawID: tc.rawID}); got != tc.want {
				t.Fatalf("InferFlavor(%q) = %q, want %q", tc.rawID, got, tc.want)
			}
		})
	}
}
