package service

import (
	"maps"
	"strings"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

const (
	extraBodyPrefixCommandCode = "commandcode.extra_body."
	extraBodyPrefixOpenAI      = "openai.extra_body."
)

// ProviderHooks returns Command Code OpenAI request mutation hooks (tests + production).
func ProviderHooks() openaicompat.RequestHooks {
	return openaicompat.RequestHooks{MutateBody: mutateCommandCode}
}

func resolveModel(kind string, inv backendplugin.Invocation, _ aipapi.Call) string {
	canonical := strings.TrimSpace(inv.CanonicalModelID)
	return strings.TrimPrefix(canonical, kind+"/")
}

func resolveFlavor(call aipapi.Call) openaicompat.Flavor {
	if call.Invocation.Operation == aipapi.OperationOpenAIResponses {
		return openaicompat.FlavorResponses
	}
	return openaicompat.FlavorChat
}

func mutateCommandCode(body map[string]any, call aipapi.Call, _ string, _ openaicompat.Flavor) error {
	for _, prefix := range []string{extraBodyPrefixCommandCode, extraBodyPrefixOpenAI} {
		maps.Copy(body, openaicompat.CollectPrefixedExtraBody(call.Extensions, prefix))
	}
	return nil
}
