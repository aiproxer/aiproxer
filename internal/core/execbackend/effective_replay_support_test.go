package execbackend_test

import (
	"context"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestEffectiveReplaySupport_normalizesDedupesSortsOmitsEmpty(t *testing.T) {
	t.Parallel()
	be := execbackend.Backend{
		ReplaySupport: aipapi.ReasoningReplaySupport{
			Dialects: []aipapi.ReasoningDialect{
				"  OpenAI.Chat.Reasoning_Text.V1 ",
				"",
				"   ",
				aipapi.ReasoningDialectAnthropicThinkingV1,
				aipapi.ReasoningDialectOpenAIChatTextV1,
				aipapi.ReasoningDialectAnthropicThinkingV1,
			},
		},
	}
	got := execbackend.EffectiveReplaySupport(context.Background(), be, aipapi.Call{}, routing.AttemptCandidate{})
	want := []aipapi.ReasoningDialect{
		aipapi.ReasoningDialectAnthropicThinkingV1,
		aipapi.ReasoningDialectOpenAIChatTextV1,
	}
	if !slices.Equal(got.Dialects, want) {
		t.Fatalf("got %#v want %#v", got.Dialects, want)
	}
}

func TestEffectiveReplaySupport_resolvePathNormalized(t *testing.T) {
	t.Parallel()
	be := execbackend.Backend{
		ResolveReplaySupport: func(context.Context, aipapi.Call, routing.AttemptCandidate) aipapi.ReasoningReplaySupport {
			return aipapi.ReasoningReplaySupport{
				Dialects: []aipapi.ReasoningDialect{" Anthropic.Thinking.V1 ", ""},
			}
		},
	}
	got := execbackend.EffectiveReplaySupport(context.Background(), be, aipapi.Call{}, routing.AttemptCandidate{})
	if len(got.Dialects) != 1 || got.Dialects[0] != aipapi.ReasoningDialectAnthropicThinkingV1 {
		t.Fatalf("got %#v", got.Dialects)
	}
}
