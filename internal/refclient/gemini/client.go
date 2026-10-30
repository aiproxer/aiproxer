// Package gemini is a reference client emulator for the Google Gemini generateContent API,
// built on google.golang.org/genai.
//
// Unlike openai-go and anthropic-sdk-go, the genai client's generateContent and
// streamGenerateContent paths issue a single HTTP round-trip per call (no built-in
// exponential retry on 429 for these RPCs). Credential tests can rely on one upstream
// hit when using refbackend ForcedHTTPStatus 429; see server tests in
// internal/refbackend/gemini.
package gemini

import (
	"context"
	"fmt"
	"iter"
	"net/http"

	"google.golang.org/genai"
)

// Config configures the official GenAI Go client for the Gemini (Google AI) backend.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// Client wraps genai.Client for scriptable generateContent calls.
type Client struct {
	inner *genai.Client
}

// New builds a Gemini API reference client (BackendGeminiAPI).
func New(ctx context.Context, cfg Config) (*Client, error) {
	cc := genai.ClientConfig{
		APIKey: cfg.APIKey,
	}
	if cfg.HTTPClient != nil {
		cc.HTTPClient = cfg.HTTPClient
	}
	if cfg.BaseURL != "" {
		cc.HTTPOptions.BaseURL = cfg.BaseURL
	}
	c, err := genai.NewClient(ctx, &cc)
	if err != nil {
		return nil, fmt.Errorf("gemini refclient: new genai client: %w", err)
	}
	return &Client{inner: c}, nil
}

// GenerateContent calls the non-streaming generateContent RPC.
func (c *Client) GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	return c.inner.Models.GenerateContent(ctx, model, contents, config)
}

// GenerateContentStream calls streamGenerateContent (?alt=sse).
func (c *Client) GenerateContentStream(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
	return c.inner.Models.GenerateContentStream(ctx, model, contents, config)
}
