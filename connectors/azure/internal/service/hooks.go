package service

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ProviderHooks returns openaicompat request hooks for Azure OpenAI.
func ProviderHooks(Config) openaicompat.RequestHooks {
	return openaicompat.RequestHooks{}
}

// ResolveFlavor selects the endpoint flavor: Responses preferred.
// Ambiguous or empty operations default to Responses; only explicit chat completions uses chat.
func ResolveFlavor(call aipapi.Call) openaicompat.Flavor {
	if call.Invocation.Operation == aipapi.OperationOpenAIChatCompletions {
		return openaicompat.FlavorChat
	}
	return openaicompat.FlavorResponses
}

func resolveDeployment(cfg Config, kind string, canonicalModelID string) (string, error) {
	name := strings.TrimSpace(canonicalModelID)
	if kind != "" {
		name = strings.TrimPrefix(name, kind+"/")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("azure-openai: model is required; route via %s/<deployment-name>", kind)
	}
	if _, ok := cfg.Deployments[name]; !ok {
		return "", fmt.Errorf("azure-openai: unknown deployment %q; configured deployments: %s", name, strings.Join(sortedDeploymentNames(cfg.Deployments), ", "))
	}
	return name, nil
}

// sortedDeploymentNames returns configured deployment names in stable order.
func sortedDeploymentNames(deployments map[string]string) []string {
	names := make([]string, 0, len(deployments))
	for name := range deployments {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// isResponsesCapableModel filters inventory to Responses-capable text/coding models.
// It drops embedding, rerank, image, audio, and empty/whitespace model IDs.
func isResponsesCapableModel(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	lower := strings.ToLower(id)
	dropPatterns := []string{
		"embed",
		"bge",
		"rerank",
		"image",
		"flux",
		"diffusion",
		"dall-e",
		"whisper",
		"audio",
		"tts",
		"speech",
		"music",
		"asr",
		"video",
	}
	for _, pattern := range dropPatterns {
		if strings.Contains(lower, pattern) {
			return false
		}
	}
	return true
}
