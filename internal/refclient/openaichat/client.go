// Package openaichat is a reference client emulator for the OpenAI Chat Completions API
// (legacy OpenAI-compatible chat surface), using github.com/openai/openai-go/v3.
package openaichat

import (
	"context"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

// Config configures the official OpenAI Go client (base URL must include the /v1 prefix).
type Config struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	// DisableSDKRetries, when true, sets MaxRetries to 0 for deterministic refbackend errors.
	DisableSDKRetries bool
}

// Client wraps the SDK chat completions surface.
type Client struct {
	sdk openai.Client
}

// New builds a Chat Completions reference client.
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

// CreateChatCompletion calls POST /v1/chat/completions (non-streaming).
func (c *Client) CreateChatCompletion(ctx context.Context, body openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	return c.sdk.Chat.Completions.New(ctx, body)
}

// CreateChatCompletionStream calls chat/completions with stream: true.
func (c *Client) CreateChatCompletionStream(ctx context.Context, body openai.ChatCompletionNewParams) *ssestream.Stream[openai.ChatCompletionChunk] {
	return c.sdk.Chat.Completions.NewStreaming(ctx, body)
}
