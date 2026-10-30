package openaicompat

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	legacybackend "github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	responsesbackend "github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaiusage"
	"github.com/aiproxer/aiproxer/internal/providerprofiles"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
)

type InvokeRequest struct {
	ProviderID string
	Call       aipapi.Call
	Candidate  routing.AttemptCandidate
	SDKOptions []option.RequestOption
	Quirks     CompatibleQuirks
	// QuirkObserver counts applied strict-provider quirks; nil stays silent.
	QuirkObserver QuirkObserver
}

func OpenChat(ctx context.Context, cli openai.Client, req InvokeRequest) (aipapi.ManagedEventStream, error) {
	p, err := legacybackend.ParamsForCall(&req.Call, req.Candidate)
	if err != nil {
		return nil, err
	}
	if !req.Quirks.ForwardPromptCacheKey {
		p.PromptCacheKey = param.Opt[string]{}
	}
	if req.Quirks.ReasoningContentOnToolCalls && applyChatReasoningContentQuirk(&p) {
		observeQuirkApplied(req.QuirkObserver, providerprofiles.QuirkOpenAIChatReasoningContent)
	}
	if req.Call.Invocation.TransportMode == aipapi.TransportModeNonStreaming {
		opts := append([]option.RequestOption{}, req.SDKOptions...)
		opts = append(opts, option.WithJSONDel("stream_options"))
		comp, err := cli.Chat.Completions.New(ctx, p, opts...)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", req.ProviderID, err)
		}
		return openaiusage.NewProviderEvidenceStream(ChatCompletionEvents(*comp), "openai.chat.v2"), nil
	}
	// Empty TransportMode is the legacy default and maps to streaming.
	raw := cli.Chat.Completions.NewStreaming(ctx, p, req.SDKOptions...)
	return legacybackend.NewChatStream(raw, req.Call.MaxPendingWireEvents), nil
}

func OpenResponses(ctx context.Context, cli openai.Client, req InvokeRequest) (aipapi.ManagedEventStream, error) {
	p, err := responsesbackend.ParamsForCall(&req.Call, req.Candidate)
	if err != nil {
		return nil, err
	}
	if !req.Quirks.ForwardPromptCacheKey {
		p.PromptCacheKey = param.Opt[string]{}
	}
	if req.Quirks.ToolOutputAdjacency {
		applied, err := applyResponsesAdjacencyQuirk(&p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", req.ProviderID, err)
		}
		// A refusal is not an application: it already surfaces as a
		// classified terminal error with its own telemetry.
		if applied {
			observeQuirkApplied(req.QuirkObserver, providerprofiles.QuirkOpenAIResponsesToolAdjacency)
		}
	}
	if req.Call.Invocation.TransportMode == aipapi.TransportModeNonStreaming {
		resp, err := cli.Responses.New(ctx, p, req.SDKOptions...)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", req.ProviderID, err)
		}
		events, err := responsesbackend.CompletionEvents(*resp)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", req.ProviderID, err)
		}
		return openaiusage.NewProviderEvidenceStream(events, "openai.responses.v2"), nil
	}
	// Empty TransportMode is the legacy default and maps to streaming.
	raw := cli.Responses.NewStreaming(ctx, p, req.SDKOptions...)
	return responsesbackend.NewSDKStream(raw, req.Call.MaxPendingWireEvents), nil
}
