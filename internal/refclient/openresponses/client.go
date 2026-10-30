package openresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RequestObservation captures a raw client request for test assertions.
type RequestObservation struct {
	Method    string
	URLPath   string
	Headers   http.Header
	Body      []byte
	Timestamp time.Time
	Redacted  bool
}

// EventHandler receives each parsed streaming event.
type EventHandler func(Event) error

// Config configures the independent reference client.
type Config struct {
	// BaseURL is the server origin, e.g. http://127.0.0.1:PORT. The client appends
	// /responses and /responses/compact paths.
	BaseURL string
	// APIKey is sent as a Bearer token.
	APIKey string
	// HTTPClient overrides the default transport; nil uses http.DefaultClient.
	HTTPClient *http.Client
	// Clock, when set, supplies deterministic timestamps for observations.
	Clock Clock
	// ParseOptions bounds response parsing; zero uses defaults.
	ParseOptions ParseOptions
	// OnRequest, when set, receives each raw request (redacted headers/body).
	OnRequest func(RequestObservation)
	// SlowConsumerDelay, when non-zero, pauses after each streamed event to simulate
	// a slow consumer while remaining context-aware.
	SlowConsumerDelay time.Duration
}

// Client is the independent HTTP/SSE OpenResponses client.
type Client struct {
	baseURL   string
	apiKey    string
	hc        *http.Client
	clock     Clock
	opts      ParseOptions
	onRequest func(RequestObservation)
	slow      time.Duration

	mu         sync.Mutex
	lastReq    RequestObservation
	lastStatus int
	lastErr    error
	reqCount   atomic.Int64
}

// New constructs an independent OpenResponses client.
func New(cfg Config) *Client {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	clock := cfg.Clock
	if clock == nil {
		clock = NewClock(time.Time{})
	}
	return &Client{
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:    cfg.APIKey,
		hc:        hc,
		clock:     clock,
		opts:      cfg.ParseOptions.normalize(),
		onRequest: cfg.OnRequest,
		slow:      cfg.SlowConsumerDelay,
	}
}

// LastRequest returns the most recent captured request.
func (c *Client) LastRequest() RequestObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastReq
}

// LastStatusCode returns the most recent HTTP status.
func (c *Client) LastStatusCode() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastStatus
}

// LastError returns the most recent request error, if any.
func (c *Client) LastError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// RequestCount returns the number of requests issued by this client.
func (c *Client) RequestCount() int64 { return c.reqCount.Load() }

// HTTPError is a non-2xx response carrying the structured error object when present.
type HTTPError struct {
	StatusCode  int
	Status      string
	ErrorObject *ErrorObject
	Body        []byte
}

func (e *HTTPError) Error() string {
	if e.ErrorObject != nil {
		return fmt.Sprintf("openresponses HTTP %d: %s: %s", e.StatusCode, e.ErrorObject.Type, e.ErrorObject.Message)
	}
	return fmt.Sprintf("openresponses HTTP %d: %s", e.StatusCode, e.Status)
}

// Create performs a non-streaming POST /responses call.
func (c *Client) Create(ctx context.Context, params CreateParams) (*ResponseResource, error) {
	params.Stream = false
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("refclient/openresponses: marshal create: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, c.path("/responses"), body, "application/json", false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := readBounded(resp.Body, c.opts.MaxBodyBytes)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.lastErr = nil
	c.mu.Unlock()
	return ParseResponseResource(raw, c.opts)
}

// CreateStream performs POST /responses with stream enabled and feeds each semantic
// event to handler. It returns the terminal response resource.
func (c *Client) CreateStream(ctx context.Context, params CreateParams, handler EventHandler) (*ResponseResource, error) {
	params.Stream = true
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("refclient/openresponses: marshal create: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, c.path("/responses"), body, "application/json", true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "text/event-stream") {
		return nil, malformedf("streaming response content-type %q is not text/event-stream", ct)
	}

	p := NewSSEParser(resp.Body, c.opts)
	var terminal *ResponseResource
	for {
		if err := ctx.Err(); err != nil {
			return terminal, err
		}
		evt, err := p.Next()
		if errors.Is(err, ErrSSEDone) {
			break
		}
		if err != nil {
			c.mu.Lock()
			c.lastErr = err
			c.mu.Unlock()
			return terminal, err
		}
		if c.slow > 0 {
			if err := sleepContext(ctx, c.slow); err != nil {
				return terminal, err
			}
		}
		if handler != nil {
			if err := handler(*evt); err != nil {
				return terminal, err
			}
		}
		if evt.IsTerminal() {
			terminal = evt.Response
		}
	}
	c.mu.Lock()
	c.lastErr = nil
	c.mu.Unlock()
	return terminal, nil
}

// Compact performs a non-streaming POST /responses/compact call.
func (c *Client) Compact(ctx context.Context, params CompactParams) (*CompactResource, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("refclient/openresponses: marshal compact: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, c.path("/responses/compact"), body, "application/json", false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := readBounded(resp.Body, c.opts.MaxBodyBytes)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.lastErr = nil
	c.mu.Unlock()
	return ParseCompactResource(raw, c.opts)
}

func (c *Client) path(p string) string {
	return c.baseURL + p
}

func (c *Client) do(ctx context.Context, method, url string, body []byte, contentType string, streaming bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("refclient/openresponses: new request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if streaming {
		req.Header.Set("Accept", "text/event-stream")
	}

	c.observe(req, body)
	c.reqCount.Add(1)

	resp, err := c.hc.Do(req)
	if err != nil {
		c.mu.Lock()
		c.lastErr = err
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Lock()
	c.lastStatus = resp.StatusCode
	c.mu.Unlock()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		raw, rerr := readBounded(resp.Body, c.opts.MaxBodyBytes)
		if rerr != nil {
			return nil, rerr
		}
		httpErr := &HTTPError{StatusCode: resp.StatusCode, Status: resp.Status, Body: raw}
		var env struct {
			Error json.RawMessage `json:"error"`
		}
		if len(raw) > 0 && json.Unmarshal(raw, &env) == nil && len(env.Error) > 0 && string(env.Error) != "null" {
			var eo ErrorObject
			if err := json.Unmarshal(env.Error, &eo); err == nil {
				httpErr.ErrorObject = &eo
			}
		}
		c.mu.Lock()
		c.lastErr = httpErr
		c.mu.Unlock()
		return nil, httpErr
	}
	return resp, nil
}

func (c *Client) observe(req *http.Request, body []byte) {
	obs := RequestObservation{
		Method:    req.Method,
		URLPath:   req.URL.Path,
		Headers:   req.Header.Clone(),
		Body:      append([]byte(nil), body...),
		Timestamp: c.clock.Now(),
	}
	redacted := false
	if len(obs.Headers.Get("Authorization")) > 0 {
		obs.Headers.Set("Authorization", "[REDACTED]")
		redacted = true
	}
	obs.Redacted = redacted
	c.mu.Lock()
	c.lastReq = obs
	c.mu.Unlock()
	if c.onRequest != nil {
		c.onRequest(obs)
	}
}

// sleepContext sleeps for d or until ctx is cancelled.
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// validBaseURL validates that BaseURL is an absolute http(s) URL with a host.
func validBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("base URL requires a host")
	}
	if u.User != nil {
		return fmt.Errorf("base URL must not contain userinfo")
	}
	if u.Fragment != "" {
		return fmt.Errorf("base URL must not contain a fragment")
	}
	return nil
}

// RoundTripFunc adapts a handler func to http.RoundTripper.
type RoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper.
func (f RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
