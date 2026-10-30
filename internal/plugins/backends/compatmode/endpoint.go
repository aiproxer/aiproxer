// Package compatmode holds shared helpers for custom-compatible backend modes.
package compatmode

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/endpoint"
)

// ParseEndpoint validates base_url for a compatible instance.
func ParseEndpoint(cfg config.CompatibleModeConfig) (endpoint.Descriptor, error) {
	scope := strings.TrimSpace(cfg.BackendPrefix)
	if scope == "" {
		scope = "compatible"
	}
	d, err := endpoint.ParseBaseURL(cfg.BaseURL)
	if err != nil {
		return endpoint.Descriptor{}, fmt.Errorf("compatible backend %q: %w", scope, err)
	}
	return d, nil
}

// OpenAIModelsEndpoint joins the models path for OpenAI-compatible inventory.
func OpenAIModelsEndpoint(d endpoint.Descriptor) (string, error) {
	return d.Join(endpoint.OperationOpenAIModels)
}

// AnthropicModelsEndpoint joins the default models path for Anthropic-compatible inventory.
func AnthropicModelsEndpoint(d endpoint.Descriptor) (string, error) {
	return d.Join(endpoint.OperationAnthropicModels)
}

// AnthropicModelsEndpointPath joins a validated, relative model-list path to an
// Anthropic-compatible base URL. It is intentionally separate from the default
// operation join so profile quirks cannot alter execution endpoints.
func AnthropicModelsEndpointPath(d endpoint.Descriptor, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#\r\n\x00") || strings.Contains(path, "//") {
		return "", fmt.Errorf("compatible Anthropic models path: invalid path")
	}
	decodedPath, err := url.PathUnescape(path)
	if err != nil || strings.ContainsAny(decodedPath, "\r\n\x00") {
		return "", fmt.Errorf("compatible Anthropic models path: invalid path")
	}
	for segment := range strings.SplitSeq(decodedPath, "/") {
		if segment == ".." || segment == "." {
			return "", fmt.Errorf("compatible Anthropic models path: traversal is not allowed")
		}
	}
	return strings.TrimRight(d.BaseURL(), "/") + path, nil
}
