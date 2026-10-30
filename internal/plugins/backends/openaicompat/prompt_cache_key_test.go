package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
)

func TestParamsForCall_PromptCacheKey(t *testing.T) {
	t.Run("chat", func(t *testing.T) {
		testPromptCacheKeyParams(t, openailegacy.ParamsForCall, func(p openai.ChatCompletionNewParams) string { return p.PromptCacheKey.Value })
	})
	t.Run("responses", func(t *testing.T) {
		testPromptCacheKeyParams(t, openairesponses.ParamsForCall, func(p responses.ResponseNewParams) string { return p.PromptCacheKey.Value })
	})
}

func testPromptCacheKeyParams[P any](t *testing.T, build func(*aipapi.Call, routing.AttemptCandidate) (P, error), keyValue func(P) string) {
	t.Helper()
	for _, tc := range []struct {
		name, legacy, semantic, want string
		conflict                     bool
	}{
		{"legacy", "client-key", "", "client-key", false},
		{"semantic", "", "semantic-key", "semantic-key", false},
		{"empty", "", "", "", false},
		{"whitespace", "  ", "", "", false},
		{"conflict", "client-key", "other-key", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := aipapi.Call{PromptCacheKey: tc.legacy}
			if tc.semantic != "" {
				data, _ := json.Marshal(tc.semantic)
				call.SemanticExtensions = []aipapi.SemanticExtension{{
					Namespace: "aip", Type: "prompt_cache_key", Implementor: "proxy",
					Direction: aipapi.SemanticExtensionDirectionRequest, Presence: aipapi.SemanticExtensionValue, Data: data,
				}}
			}
			p, err := build(&call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
			if tc.conflict {
				var validationErr *aipapi.ValidationError
				require.ErrorAs(t, err, &validationErr)
				require.Equal(t, "PromptCacheKey", validationErr.Field)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, keyValue(p))
			raw, err := json.Marshal(p)
			require.NoError(t, err)
			var wire map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &wire))
			key, present := wire["prompt_cache_key"]
			require.Equal(t, tc.want != "", present)
			if present {
				require.Equal(t, `"`+tc.want+`"`, string(key))
			}
		})
	}
}
