package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	defaultMaxBodyBytes int64 = 8 << 20
	defaultMaxSSEBytes  int64 = 32 << 20
)

// Flavor selects the OpenAI-compatible wire endpoint family.
type Flavor string

const (
	FlavorChat      Flavor = "chat"
	FlavorResponses Flavor = "responses"
)

// Transport declares which endpoint families a caller enables.
type Transport int

const (
	TransportChatOnly Transport = iota
	TransportChatAndResponses
)

// RequestHooks let a connector inject headers or mutate the JSON body without
// embedding provider names in this support module.
type RequestHooks struct {
	// ExtraHeaders are applied after Authorization/Content-Type.
	ExtraHeaders http.Header
	// PrepareHeaders may add or replace headers after ExtraHeaders.
	PrepareHeaders func(h http.Header, call aipapi.Call, model string, flavor Flavor)
	// MutateBody may alter the decoded request object before marshal.
	MutateBody func(body map[string]any, call aipapi.Call, model string, flavor Flavor) error
	// ExtraBody merges raw JSON fields into the request body (caller-validated).
	ExtraBody map[string]json.RawMessage
}

// Client talks to an OpenAI-compatible HTTP API. HTTPClient is required.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	Transport  Transport
	Hooks      RequestHooks
	// MaxBodyBytes bounds request and non-stream response bodies (default 8MiB).
	MaxBodyBytes int64
	// MaxSSEBytes bounds total SSE payload bytes (default 32MiB).
	MaxSSEBytes int64
}

func (c *Client) maxBody() int64 {
	if c.MaxBodyBytes > 0 {
		return c.MaxBodyBytes
	}
	return defaultMaxBodyBytes
}

func (c *Client) maxSSE() int64 {
	if c.MaxSSEBytes > 0 {
		return c.MaxSSEBytes
	}
	return defaultMaxSSEBytes
}

func (c *Client) validate() error {
	if c == nil {
		return fmt.Errorf("openaicompat: nil client")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return fmt.Errorf("openaicompat: base_url is required")
	}
	if c.HTTPClient == nil {
		return fmt.Errorf("openaicompat: HTTPClient is required (caller-owned)")
	}
	return nil
}

// Open starts a managed canonical event stream for call using model.
func (c *Client) Open(ctx context.Context, call aipapi.Call, model string, flavor Flavor) (aipapi.ManagedEventStream, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, fmt.Errorf("openaicompat: %w", aipapi.ErrNilContext)
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, fmt.Errorf("openaicompat: model is required")
	}
	if flavor == "" {
		flavor = FlavorChat
	}
	if flavor == FlavorResponses && c.Transport == TransportChatOnly {
		return nil, fmt.Errorf("openaicompat: responses API is not available")
	}
	stream := call.Invocation.DeliveryMode != aipapi.DeliveryModeNonStreaming &&
		call.Invocation.TransportMode != aipapi.TransportModeNonStreaming
	body, err := buildRequestBody(call, model, flavor, stream, c.Hooks)
	if err != nil {
		return nil, err
	}
	path := "/chat/completions"
	if flavor == FlavorResponses {
		path = "/responses"
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req.Header, call, model, flavor)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: request: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, readHTTPError(resp, c.maxBody())
	}
	if stream {
		return aipapi.CloseOnlyManagedStream{Stream: newSSEStream(resp, flavor, c.maxSSE())}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	limited := io.LimitReader(resp.Body, c.maxBody())
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: read body: %w", err)
	}
	events, err := decodeNonStream(raw, flavor)
	if err != nil {
		return nil, err
	}
	return aipapi.CloseOnlyManagedStream{Stream: &sliceStream{events: events}}, nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	url := joinURL(c.BaseURL, path)
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: new request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key := strings.TrimSpace(c.APIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return req, nil
}

func (c *Client) applyHeaders(h http.Header, call aipapi.Call, model string, flavor Flavor) {
	for k, vs := range c.Hooks.ExtraHeaders {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	if c.Hooks.PrepareHeaders != nil {
		c.Hooks.PrepareHeaders(h, call, model, flavor)
	}
}

func joinURL(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}
