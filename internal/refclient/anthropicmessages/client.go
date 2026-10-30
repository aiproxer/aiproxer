// Package anthropicmessages is a reference client emulator for the Anthropic Messages API,
// built on github.com/anthropics/anthropic-sdk-go.
package anthropicmessages

import (
	"context"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

// Config configures the official Anthropic Go client. BaseURL is the API origin only
// (for example https://api.anthropic.com); the SDK posts to /v1/messages relative to it.
type Config struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	// DisableSDKRetries, when true, sets MaxRetries to 0 for deterministic refbackend errors.
	DisableSDKRetries bool
}

// Client wraps the SDK Messages surface.
type Client struct {
	sdk anthropic.Client
}

// New builds a Messages API reference client.
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
	return &Client{sdk: anthropic.NewClient(opts...)}
}

// CreateMessage calls POST /v1/messages (non-streaming).
func (c *Client) CreateMessage(ctx context.Context, body anthropic.MessageNewParams) (*anthropic.Message, error) {
	return c.sdk.Messages.New(ctx, body)
}

// CreateMessageStream calls /v1/messages with stream: true.
func (c *Client) CreateMessageStream(ctx context.Context, body anthropic.MessageNewParams) *ssestream.Stream[anthropic.MessageStreamEventUnion] {
	return c.sdk.Messages.NewStreaming(ctx, body)
}
