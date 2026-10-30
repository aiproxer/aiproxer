package service

import (
	"maps"
	"strings"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

const (
	extraBodyPrefixNVIDIA = "nvidia.extra_body."
	extraBodyPrefixOpenAI = "openai.extra_body."
)

// ProviderHooks returns NVIDIA request mutation hooks (tests + production).
func ProviderHooks() openaicompat.RequestHooks {
	return openaicompat.RequestHooks{MutateBody: mutateNVIDIA}
}

func resolveModel(kind string, inv backendplugin.Invocation, _ aipapi.Call) string {
	return strings.TrimPrefix(strings.TrimSpace(inv.CanonicalModelID), kind+"/")
}

func resolveFlavor(call aipapi.Call) openaicompat.Flavor {
	if call.Invocation.Operation == aipapi.OperationOpenAIResponses {
		return openaicompat.FlavorResponses
	}
	return openaicompat.FlavorChat
}

func mutateNVIDIA(body map[string]any, call aipapi.Call, _ string, _ openaicompat.Flavor) error {
	delete(body, "stream_options")
	if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens > 0 {
		body["max_tokens"] = *call.Options.MaxOutputTokens
		delete(body, "max_completion_tokens")
	}
	for _, prefix := range []string{extraBodyPrefixNVIDIA, extraBodyPrefixOpenAI} {
		maps.Copy(body, openaicompat.CollectPrefixedExtraBody(call.Extensions, prefix))
	}
	return nil
}
