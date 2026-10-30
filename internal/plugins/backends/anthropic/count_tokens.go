package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/anthropicmessages"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type countTokensRequest struct {
	Model    any `json:"model"`
	Messages any `json:"messages"`
	System   any `json:"system,omitempty"`
	Tools    any `json:"tools,omitempty"`
}

// TokenCounter counts Anthropic Messages input tokens through /v1/messages/count_tokens.
type TokenCounter struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewTokenCounter returns an Anthropic provider-count adapter.
func NewTokenCounter(cfg Config) *TokenCounter {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &TokenCounter{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:     cfg.APIKey,
		httpClient: client,
	}
}

func (c *TokenCounter) SupportsCount(ctx context.Context, input app.ProviderCountInput) app.ProviderSupport {
	if err := ctx.Err(); err != nil {
		return app.ProviderSupport{Status: app.SupportStatusUnavailable, Err: err}
	}
	if input.Backend != ID {
		return app.ProviderSupport{Status: app.SupportStatusUnsupported, Message: "anthropic token counter only supports anthropic backend"}
	}
	if input.Kind != app.CountKindCall {
		return app.ProviderSupport{Status: app.SupportStatusUnsupported, Message: "anthropic count_tokens supports canonical calls only"}
	}
	if strings.TrimSpace(input.Model) == "" {
		return app.ProviderSupport{Status: app.SupportStatusUnsupported, Message: "anthropic model is required"}
	}
	if c == nil || c.baseURL == "" || c.apiKey == "" || c.httpClient == nil {
		return app.ProviderSupport{Status: app.SupportStatusUnavailable, Err: app.ErrProviderUnavailable}
	}
	return app.ProviderSupport{Status: app.SupportStatusSupported}
}

func (c *TokenCounter) CountText(context.Context, app.CountTextInput) (app.CountResult, error) {
	return app.CountResult{}, app.ErrProviderUnsupported
}

func (c *TokenCounter) CountOutput(context.Context, app.CountOutputInput) (app.CountResult, error) {
	return app.CountResult{}, app.ErrProviderUnsupported
}

func (c *TokenCounter) CountCall(ctx context.Context, input app.CountCallInput) (app.CountResult, error) {
	if err := ctx.Err(); err != nil {
		return app.CountResult{}, err
	}
	if support := c.SupportsCount(ctx, app.ProviderCountInput{Backend: input.Backend, Model: input.Model, Kind: app.CountKindCall}); support.Status != app.SupportStatusSupported {
		if support.Status == app.SupportStatusUnavailable {
			return app.CountResult{}, supportError(support, app.ErrProviderUnavailable)
		}
		return app.CountResult{}, supportError(support, app.ErrProviderUnsupported)
	}

	params, err := anthropicmessages.ParamsForCall(&input.Call, routing.AttemptCandidate{Primary: routing.Primary{Model: input.Model}})
	if err != nil {
		return app.CountResult{}, fmt.Errorf("%w: anthropic count_tokens: map call: %w", app.ErrProviderUnsupported, err)
	}
	body, err := json.Marshal(countTokensRequest{
		Model:    params.Model,
		Messages: params.Messages,
		System:   params.System,
		Tools:    params.Tools,
	})
	if err != nil {
		return app.CountResult{}, fmt.Errorf("%w: anthropic count_tokens: encode request: %w", app.ErrProviderUnsupported, err)
	}

	req, err := http.NewRequestWithContext(identity.WithBackgroundIdentity(ctx), http.MethodPost, c.baseURL+"/v1/messages/count_tokens", bytes.NewReader(body))
	if err != nil {
		return app.CountResult{}, fmt.Errorf("%w: anthropic count_tokens: build request: %w", app.ErrProviderUnavailable, err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	req.Header.Set("anthropic-version", APIVersion)
	req.Header.Set("x-api-key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return app.CountResult{}, ctxErr
		}
		return app.CountResult{}, fmt.Errorf("%w: anthropic count_tokens: request failed: %w", app.ErrProviderUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return app.CountResult{}, fmt.Errorf("%w: anthropic count_tokens: provider returned status %d", app.ErrProviderUnavailable, resp.StatusCode)
	}

	var out struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return app.CountResult{}, fmt.Errorf("%w: anthropic count_tokens: decode response: %w", app.ErrProviderUnavailable, err)
	}
	if out.InputTokens < 0 {
		return app.CountResult{}, fmt.Errorf("%w: anthropic count_tokens: negative input token count", app.ErrProviderUnavailable)
	}
	return app.CountResult{
		InputTokens:        out.InputTokens,
		TotalTokens:        out.InputTokens,
		TotalTokensPresent: true,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane:     aipapi.UsagePlaneProviderBillable,
			Source:    aipapi.UsageSourceProviderCountAPI,
			Authority: aipapi.UsageAuthorityAuthoritative,
			Tokenizer: aipapi.TokenizerRef{
				Type:      "provider_count_api",
				Source:    ID,
				ModelUsed: input.Model,
			},
		},
	}, nil
}

func supportError(support app.ProviderSupport, sentinel error) error {
	if support.Err == nil {
		return sentinel
	}
	return fmt.Errorf("%w: %w", sentinel, support.Err)
}
