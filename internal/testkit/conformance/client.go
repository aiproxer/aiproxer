package conformance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// RoundTripResult is the deterministic client-visible outcome of one harness
// round trip over any client transport.
type RoundTripResult struct {
	// Text is the assembled assistant text.
	Text string
	// ResponseID is the proxy response id.
	ResponseID string
	// Status is the terminal response status ("completed", "failed", ...).
	Status string
	// Object is the response resource discriminator ("response" or
	// "response.compaction").
	Object string
	// Events is the ordered client-visible event type trajectory.
	Events []string
}

// ClientEntrypoint is the configurable client seam of the deployment harness.
// The OpenResponses family uses the harness raw wire clients (JSON/SSE/compact/
// WebSocket); existing families delegate to the repository independent reference
// clients. The Phase 8 independent OpenResponses refclient can implement this
// contract and drive the same deployment.
type ClientEntrypoint interface {
	// RoundTrip drives one create/turn and returns the deterministic result.
	RoundTrip(ctx context.Context, prompt string) (RoundTripResult, error)
	// Close releases any client-held resources (e.g. WebSocket connections).
	Close() error
}

// newOpenResponsesClient builds the transport-selected raw wire client for the
// OpenResponses frontend family. The wire model field carries the deployment's
// authoritative route selector so single cells and failover chains resolve
// through the same generic client path.
func newOpenResponsesClient(d *Deployment) ClientEntrypoint {
	model := strings.TrimSpace(d.RouteSelector)
	if model == "" {
		model = harnessDefaultModel(d.Spec.Backend)
	}
	switch d.Spec.Transport {
	case TransportSSE, TransportCompact, TransportWebSocket:
		if d.Spec.Transport == TransportWebSocket {
			ws, err := newOpenResponsesWSClient(d.Server.URL, model, d.Server.Client())
			if err != nil {
				return &closedClient{err: err}
			}
			return ws
		}
	}
	return &openResponsesHTTPClient{
		baseURL:    d.Server.URL,
		httpClient: d.Server.Client(),
		transport:  d.Spec.Transport,
		model:      model,
	}
}

type closedClient struct {
	err error
}

func (c *closedClient) RoundTrip(context.Context, string) (RoundTripResult, error) {
	return RoundTripResult{}, c.err
}

func (c *closedClient) Close() error { return nil }

// newOpenResponsesHTTPClient builds a raw wire HTTP client for the OpenResponses
// frontend with an explicit model override.
func newOpenResponsesHTTPClient(baseURL string, httpClient *http.Client, transport ClientTransport, model string) ClientEntrypoint {
	return &openResponsesHTTPClient{
		baseURL:    baseURL,
		httpClient: httpClient,
		transport:  transport,
		model:      model,
	}
}

// openResponsesHTTPClient drives the OpenResponses frontend over JSON, SSE, and
// compact transports with raw wire requests (no provider SDK).
type openResponsesHTTPClient struct {
	baseURL    string
	httpClient *http.Client
	transport  ClientTransport
	model      string
}

func (c *openResponsesHTTPClient) Close() error { return nil }

func (c *openResponsesHTTPClient) RoundTrip(ctx context.Context, prompt string) (RoundTripResult, error) {
	transport := c.transport
	if transport == "" {
		transport = TransportJSON
	}
	switch transport {
	case TransportCompact:
		return c.compact(ctx, prompt)
	case TransportSSE:
		return c.create(ctx, prompt, true)
	default:
		return c.create(ctx, prompt, false)
	}
}

func (c *openResponsesHTTPClient) createEndpoint() string {
	return strings.TrimRight(c.baseURL, "/") + "/openresponses/v1/responses"
}

func (c *openResponsesHTTPClient) compactEndpoint() string {
	return strings.TrimRight(c.baseURL, "/") + "/openresponses/v1/responses/compact"
}

func (c *openResponsesHTTPClient) createBody(prompt string, stream bool) ([]byte, error) {
	return json.Marshal(map[string]any{
		"model":  c.model,
		"input":  prompt,
		"stream": stream,
		"store":  false,
	})
}

func (c *openResponsesHTTPClient) create(ctx context.Context, prompt string, stream bool) (RoundTripResult, error) {
	body, err := c.createBody(prompt, stream)
	if err != nil {
		return RoundTripResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.createEndpoint(), bytes.NewReader(body))
	if err != nil {
		return RoundTripResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return RoundTripResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return RoundTripResult{}, fmt.Errorf("openresponses create: status %d", resp.StatusCode)
	}
	if stream {
		return parseSSEResult(resp.Body)
	}
	return parseCreateResource(resp.Body)
}

func (c *openResponsesHTTPClient) compact(ctx context.Context, prompt string) (RoundTripResult, error) {
	// The compact endpoint forbids store (compact resources are not
	// continuation records); model and input are required.
	body, err := json.Marshal(map[string]any{
		"model": c.model,
		"input": prompt,
	})
	if err != nil {
		return RoundTripResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.compactEndpoint(), bytes.NewReader(body))
	if err != nil {
		return RoundTripResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return RoundTripResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return RoundTripResult{}, fmt.Errorf("openresponses compact: status %d", resp.StatusCode)
	}
	return parseCompactResource(resp.Body)
}

// wireResponseResource mirrors the minimal response resource fields the harness
// client reads (independent of the production codec).
type wireResponseResource struct {
	ID     string `json:"id"`
	Object string `json:"object"`
	Status string `json:"status"`
	Model  string `json:"model"`
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

func parseCreateResource(r io.Reader) (RoundTripResult, error) {
	var res wireResponseResource
	if err := json.NewDecoder(r).Decode(&res); err != nil {
		return RoundTripResult{}, fmt.Errorf("openresponses create: decode resource: %w", err)
	}
	return roundTripFromResource(res), nil
}

func parseCompactResource(r io.Reader) (RoundTripResult, error) {
	var res wireResponseResource
	if err := json.NewDecoder(r).Decode(&res); err != nil {
		return RoundTripResult{}, fmt.Errorf("openresponses compact: decode resource: %w", err)
	}
	out := roundTripFromResource(res)
	return out, nil
}

func roundTripFromResource(res wireResponseResource) RoundTripResult {
	out := RoundTripResult{
		ResponseID: res.ID,
		Status:     res.Status,
		Object:     res.Object,
	}
	var b strings.Builder
	for _, o := range res.Output {
		if o.Type != "message" {
			continue
		}
		for _, part := range o.Content {
			if part.Type == "output_text" {
				b.WriteString(part.Text)
			}
		}
	}
	out.Text = b.String()
	return out
}

// wireStreamEvent mirrors the client-visible SSE/WebSocket event fields.
type wireStreamEvent struct {
	Type   string `json:"type"`
	Delta  string `json:"delta"`
	Text   string `json:"text"`
	Status string `json:"status"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Response *struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"response"`
}

func parseSSEResult(r io.Reader) (RoundTripResult, error) {
	out := RoundTripResult{}
	br := bufio.NewReader(r)
	var eventType string
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "[DONE]" {
			return nil
		}
		var ev wireStreamEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return fmt.Errorf("openresponses sse: decode event: %w", err)
		}
		if ev.Type == "" && eventType != "" {
			ev.Type = eventType
		}
		eventType = ""
		out.Events = append(out.Events, ev.Type)
		switch ev.Type {
		case "response.output_text.delta":
			out.Text += ev.Delta
		case "response.completed":
			out.Status = ev.Status
			if ev.Response != nil {
				out.ResponseID = ev.Response.ID
				if ev.Response.Status != "" {
					out.Status = ev.Response.Status
				}
			}
		case "response.failed":
			out.Status = "failed"
		case "response.error", "error":
			if ev.Error != nil {
				return fmt.Errorf("openresponses sse: %s", ev.Error.Message)
			}
			return fmt.Errorf("openresponses sse: upstream error event %q", ev.Type)
		}
		return nil
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return RoundTripResult{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			payload := line[len("data:"):]
			payload = strings.TrimPrefix(payload, " ")
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(payload)
		case line == "":
			if ferr := flush(); ferr != nil {
				return RoundTripResult{}, ferr
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if ferr := flush(); ferr != nil {
		return RoundTripResult{}, ferr
	}
	out.Object = "response"
	if out.Status == "" {
		return RoundTripResult{}, fmt.Errorf("openresponses sse: stream ended without a terminal event")
	}
	return out, nil
}

// openResponsesWSClient drives one WebSocket turn per RoundTrip against the
// OpenResponses frontend.
type openResponsesWSClient struct {
	wsURL      string
	model      string
	httpClient *http.Client
}

func newOpenResponsesWSClient(baseURL, model string, httpClient *http.Client) (*openResponsesWSClient, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/openresponses/v1/responses")
	if err != nil {
		return nil, err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	return &openResponsesWSClient{wsURL: u.String(), model: model, httpClient: httpClient}, nil
}

func (c *openResponsesWSClient) Close() error { return nil }

func (c *openResponsesWSClient) RoundTrip(ctx context.Context, prompt string) (RoundTripResult, error) {
	envelope, err := json.Marshal(map[string]any{
		"type":  "response.create",
		"model": c.model,
		"input": prompt,
		"store": false,
	})
	if err != nil {
		return RoundTripResult{}, err
	}
	out := RoundTripResult{}
	if err := c.run(ctx, string(envelope), func(ev wireStreamEvent, raw []byte) error {
		out.Events = append(out.Events, ev.Type)
		switch ev.Type {
		case "response.output_text.delta":
			out.Text += ev.Delta
		case "response.completed":
			if ev.Response != nil {
				out.ResponseID = ev.Response.ID
				out.Status = ev.Response.Status
			}
		case "response.failed":
			out.Status = "failed"
		}
		return nil
	}); err != nil {
		return RoundTripResult{}, err
	}
	out.Object = "response"
	if out.Status == "" {
		return RoundTripResult{}, fmt.Errorf("openresponses websocket: turn ended without a terminal event")
	}
	return out, nil
}

// sendRaw writes an arbitrary turn frame and waits for the session's verdict.
func (c *openResponsesWSClient) sendRaw(ctx context.Context, rawTurn string) error {
	return c.run(ctx, rawTurn, func(ev wireStreamEvent, raw []byte) error {
		if ev.Type == "response.failed" {
			return fmt.Errorf("openresponses websocket: turn failed")
		}
		return nil
	})
}

// run establishes one WebSocket connection, writes rawTurn, and reads frames
// until a terminal or classified error. onEvent is invoked per stream frame.
func (c *openResponsesWSClient) run(ctx context.Context, rawTurn string, onEvent func(wireStreamEvent, []byte) error) error {
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, c.wsURL, nil)
	if err != nil {
		return fmt.Errorf("openresponses websocket: dial: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("openresponses websocket: upgrade status %d", resp.StatusCode)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(rawTurn)); err != nil {
		return fmt.Errorf("openresponses websocket: write turn: %w", err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("openresponses websocket: read: %w", err)
		}
		var ev wireStreamEvent
		if err := json.Unmarshal(msg, &ev); err != nil {
			return fmt.Errorf("openresponses websocket: decode frame: %w", err)
		}
		if ev.Error != nil {
			return fmt.Errorf("openresponses websocket: %s", ev.Error.Message)
		}
		if ev.Type == "response.error" || ev.Type == "error" {
			return fmt.Errorf("openresponses websocket: upstream error event %q", ev.Type)
		}
		if err := onEvent(ev, msg); err != nil {
			return err
		}
		if ev.Type == "response.completed" || ev.Type == "response.failed" {
			return nil
		}
	}
}

// rawOpenResponsesPost posts rawBody to the OpenResponses frontend endpoint and
// returns an error when the frontend rejects the request.
func rawOpenResponsesPost(ctx context.Context, client *http.Client, endpoint, rawBody string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(rawBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openresponses post: status %d", resp.StatusCode)
	}
	return nil
}

// existingFamilyClient drives the four existing frontend families through the
// repository independent reference clients (the existing reference families).
// The reference clients abort the test on transport errors (their established
// conformance convention), so this client is used to drive positive cells; the
// harness fail-closed proofs use the raw [Deployment.RawFrontendPost] seam.
type existingFamilyClient struct {
	tb         testing.TB
	frontendID string
	deployment *Deployment
}

func (c *existingFamilyClient) Close() error { return nil }

func (c *existingFamilyClient) RoundTrip(_ context.Context, prompt string) (RoundTripResult, error) {
	proxyOrigin := c.deployment.BaseURL()
	hc := c.deployment.Server.Client()
	transport := c.deployment.Spec.Transport
	if transport == TransportSSE {
		return RoundTripResult{Text: streamAssistantText(c.tb, c.frontendID, proxyOrigin, hc), Status: "completed", Object: "response"}, nil
	}
	return RoundTripResult{Text: nonStreamAssistantText(c.tb, c.frontendID, proxyOrigin, hc), Status: "completed", Object: "response"}, nil
}
