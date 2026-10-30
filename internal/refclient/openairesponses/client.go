// Package openairesponses is a reference client emulator for the OpenAI Responses API,
// built on github.com/openai/openai-go/v3. Use it against aipstd or httptest fakes in
// integration tests.
package openairesponses

import (
	"context"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

// Config configures the official OpenAI Go client (base URL should include the /v1 prefix).
type Config struct {
	BaseURL string
	APIKey  string
	// If nil, the SDK default HTTP client is used.
	HTTPClient *http.Client
	// DisableSDKRetries, when true, sets the official client MaxRetries to 0 so the first
	// 401/429 from httptest/refbackend is visible to credential-rotation tests.
	DisableSDKRetries bool
}

// Client wraps the official SDK for scriptable Responses calls.
type Client struct {
	sdk openai.Client
}

// New constructs a Responses API client emulator.
func New(cfg Config) *Client {
	opts := []option.RequestOption{
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(cfg.APIKey),
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	if cfg.DisableSDKRetries {
		opts = append(opts, option.WithMaxRetries(0))
	}
	return &Client{sdk: openai.NewClient(opts...)}
}

// CreateResponse performs a non-streaming responses.create call.
func (c *Client) CreateResponse(ctx context.Context, body responses.ResponseNewParams) (*responses.Response, error) {
	return c.sdk.Responses.New(ctx, body)
}

// CreateResponseStream performs responses.create with stream enabled (official SDK path).
func (c *Client) CreateResponseStream(ctx context.Context, body responses.ResponseNewParams) *ssestream.Stream[responses.ResponseStreamEventUnion] {
	return c.sdk.Responses.NewStreaming(ctx, body)
}
