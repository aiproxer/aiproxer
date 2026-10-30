package openaicompat_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/endpoint"
)

// TestEndpointBaseURLJoin_compatibleModes exercises the shared endpoint package
// from the backends tree so Task 2.2 validation (`-run Endpoint|BaseURL|Join`)
// observes OpenAI and Anthropic join contracts without factory integration.
func TestEndpointBaseURLJoin_compatibleModes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		base string
		op   endpoint.Operation
		want string
	}{
		{
			name: "openai_legacy_chat",
			base: "https://api.example.com/v1",
			op:   endpoint.OperationOpenAIChatCompletions,
			want: "https://api.example.com/v1/chat/completions",
		},
		{
			name: "openai_responses",
			base: "https://api.example.com/v1",
			op:   endpoint.OperationOpenAIResponses,
			want: "https://api.example.com/v1/responses",
		},
		{
			name: "openai_models",
			base: "https://api.example.com/v1",
			op:   endpoint.OperationOpenAIModels,
			want: "https://api.example.com/v1/models",
		},
		{
			name: "anthropic_messages_origin_policy",
			base: "https://api.example.com",
			op:   endpoint.OperationAnthropicMessages,
			want: "https://api.example.com/v1/messages",
		},
		{
			name: "anthropic_models_origin_policy",
			base: "https://api.example.com",
			op:   endpoint.OperationAnthropicModels,
			want: "https://api.example.com/v1/models",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := endpoint.ParseBaseURL(tc.base)
			if err != nil {
				t.Fatal(err)
			}
			got, err := d.Join(tc.op)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("Join = %q, want %q", got, tc.want)
			}
			if strings.Contains(strings.TrimPrefix(got, d.Scheme()+"://"), "//") {
				t.Fatalf("duplicated separators: %q", got)
			}
		})
	}
}
