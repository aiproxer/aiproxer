package openaicompat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/infra/endpoint"
	"github.com/aiproxer/aiproxer/internal/infra/httpclient"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicred"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/streampeek"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

// ResolveEndpoint maps a base URL and flavor to an absolute provider target URL.
// It validates the base URL with [endpoint.ParseBaseURL] and applies the canonical
// suffix via [endpoint.Descriptor.Join].
func ResolveEndpoint(baseURL string, flavor Flavor) (string, error) {
	d, err := endpoint.ParseBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	op := endpoint.OperationOpenAIChatCompletions
	if flavor == FlavorResponses {
		op = endpoint.OperationOpenAIResponses
	}
	return d.Join(op)
}

// BuildOutboundHeaders constructs backend-owned outbound HTTP request headers.
// It sets required Content-Type, Accept, and Authorization (if secret is non-empty).
// Extra headers are sanitized to strip hop-by-hop framing, stale length/encoding,
// client auth, and frontend session/control headers (Requirement 12.2).
func BuildOutboundHeaders(apiSecret string, streaming bool, extraHeaders http.Header) http.Header {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	if streaming {
		h.Set("Accept", "text/event-stream")
	} else {
		h.Set("Accept", "application/json")
	}
	if secret := strings.TrimSpace(apiSecret); secret != "" {
		h.Set("Authorization", "Bearer "+secret)
	}
	if extraHeaders != nil {
		connTokens := parseConnectionTokens(extraHeaders)
		for k, vv := range extraHeaders {
			if isRestrictedOutboundHeader(k, connTokens) {
				continue
			}
			for _, v := range vv {
				h.Add(k, v)
			}
		}
	}
	return h
}

func parseConnectionTokens(extraHeaders http.Header) map[string]struct{} {
	if extraHeaders == nil {
		return nil
	}
	rawValues := extraHeaders["Connection"]
	if len(rawValues) == 0 {
		return nil
	}
	tokens := make(map[string]struct{})
	for _, raw := range rawValues {
		for part := range strings.SplitSeq(raw, ",") {
			token := strings.TrimSpace(part)
			if token != "" {
				tokens[strings.ToLower(token)] = struct{}{}
			}
		}
	}
	return tokens
}

func isRestrictedOutboundHeader(key string, connTokens map[string]struct{}) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if connTokens != nil {
		if _, ok := connTokens[lower]; ok {
			return true
		}
	}
	canonical := http.CanonicalHeaderKey(key)
	switch canonical {
	// Hop-by-hop headers (RFC 7230 / RFC 9110):
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Te", "Trailers", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	// Transport / body framing headers (caller/writer controls these):
	case "Content-Length", "Content-Encoding", "Expect", "Host":
		return true
	// Backend-owned content headers:
	case "Content-Type", "Accept":
		return true
	// Client auth must never leak upstream:
	case "Authorization", "X-Api-Key", "Api-Key", "X-Goog-Api-Key":
		return true
	// Frontend session / control headers must not leak upstream:
	case "X-Session-Id", "X-Resume-Token", "X-Aleg-Id", "X-Bleg-Id",
		"X-Aip-Session-Id", "X-Aip-Resume-Token", "X-Aip-Route",
		"X-Aip-A-Leg-Id", "X-Aip-Session-Hint", "X-Aip-Diagnostics-Secret",
		"X-Trace-Id":
		return true
	default:
		if strings.HasPrefix(lower, "x-aip-") ||
			strings.HasPrefix(lower, "x-session-") ||
			strings.HasPrefix(lower, "x-resume-") ||
			strings.HasPrefix(lower, "x-aleg-") ||
			strings.HasPrefix(lower, "x-bleg-") ||
			lower == "x-api-key" || lower == "api-key" || lower == "x-goog-api-key" ||
			lower == "authorization" || lower == "proxy-authorization" ||
			lower == "host" || lower == "expect" || lower == "trailer" || lower == "trailers" ||
			lower == "content-type" || lower == "accept" {
			return true
		}
		return false
	}
}

// ResolveHTTPClient returns client when non-nil; otherwise returns [httpclient.Standard]
// ensuring default TLS, proxy, HTTP/2, and timeout tuning (Requirement 12.1).
func ResolveHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return httpclient.Standard()
}

// PeekFirstEvent peeks the first event from a managed stream and returns a prepended
// stream. If the first Recv fails, the stream is closed and the error returned.
func PeekFirstEvent(ctx context.Context, es aipapi.ManagedEventStream) (aipapi.ManagedEventStream, error) {
	return streampeek.PeekFirst(ctx, es)
}

// HTTPError captures an upstream HTTP response failure (>= 400).
// It implements openaicred.HTTPStatusError so error classification works uniformly.
type HTTPError struct {
	StatusCode int
	Status     string
	Header     http.Header
	Body       []byte
	ProviderID string
}

func (e *HTTPError) Error() string {
	msg := strings.TrimSpace(string(e.Body))
	if msg == "" {
		msg = e.Status
	}
	if e.ProviderID != "" {
		return fmt.Sprintf("%s: upstream HTTP %d: %s", e.ProviderID, e.StatusCode, msg)
	}
	return fmt.Sprintf("upstream HTTP %d: %s", e.StatusCode, msg)
}

func (e *HTTPError) HTTPStatusCode() int {
	return e.StatusCode
}

func (e *HTTPError) HTTPHeader() http.Header {
	return e.Header
}

var _ openaicred.HTTPStatusError = (*HTTPError)(nil)

// ParseStreamResponse parses an HTTP response into a managed event stream.
// On status >= 400, it reads a bounded error body and returns an [*HTTPError].
func ParseStreamResponse(providerID string, resp *http.Response, flavor Flavor, maxPending int) (aipapi.ManagedEventStream, error) {
	if resp == nil {
		return nil, errors.New("openaicompat: nil http response")
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Header:     resp.Header,
			Body:       body,
			ProviderID: providerID,
		}
	}
	dec := ssestream.NewDecoder(resp)
	switch flavor {
	case FlavorResponses:
		s := ssestream.NewStream[responses.ResponseStreamEventUnion](dec, nil)
		return NewResponsesStream(providerID, s, maxPending), nil
	default:
		s := ssestream.NewStream[openai.ChatCompletionChunk](dec, nil)
		return NewChatStream(providerID, s, maxPending), nil
	}
}

// WireOpenPrimitives bundles backend-owned transport configuration and helpers
// for executing wire requests without duplicating credential, endpoint, client,
// or response stream parsing logic (Requirements 10, 12).
type WireOpenPrimitives struct {
	ProviderID        string
	BaseURL           string
	Flavor            Flavor
	Pool              *credpool.Pool
	HTTPClient        *http.Client
	RateLimitFallback time.Duration
	// QuotaSwitchThreshold is the Retry-After duration strictly above which
	// a 429 quota-cools a key and advances pre-output. Zero selects the
	// proxy-wide default; it is bound from
	// upstream_credentials.quota_switch_retry_after at candidate build.
	QuotaSwitchThreshold time.Duration
	MaxPending           int
}

// ResolveURL resolves the target endpoint URL.
func (p WireOpenPrimitives) ResolveURL() (string, error) {
	return ResolveEndpoint(p.BaseURL, p.Flavor)
}

// BuildHeaders constructs the outbound headers for an attempt.
func (p WireOpenPrimitives) BuildHeaders(apiSecret string, streaming bool) http.Header {
	return BuildOutboundHeaders(apiSecret, streaming, nil)
}

// BuildHeadersWithExtra constructs the outbound headers including sanitized extra headers.
func (p WireOpenPrimitives) BuildHeadersWithExtra(apiSecret string, streaming bool, extraHeaders http.Header) http.Header {
	return BuildOutboundHeaders(apiSecret, streaming, extraHeaders)
}

// NewRequest constructs a secured outbound HTTP request using the resolved provider endpoint.
func (p WireOpenPrimitives) NewRequest(
	ctx context.Context,
	body io.Reader,
	contentLength int64,
	apiSecret string,
	streaming bool,
	extraHeaders http.Header,
) (*http.Request, error) {
	targetURL, err := p.ResolveURL()
	if err != nil {
		return nil, err
	}
	return NewOutboundRequest(ctx, targetURL, body, contentLength, apiSecret, streaming, extraHeaders)
}

// NewOutboundRequest constructs a secured outbound HTTP request targeting targetURL for wire execution (Requirement 12).
// It applies BuildOutboundHeaders, sets exact ContentLength when known (or -1 for streaming/chunked framing),
// clears any request trailers, keeps connection reuse enabled (Close=false), and clears req.Host so transport
// routes cleanly to targetURL without forwarding client Host.
func NewOutboundRequest(
	ctx context.Context,
	targetURL string,
	body io.Reader,
	contentLength int64,
	apiSecret string,
	streaming bool,
	extraHeaders http.Header,
) (*http.Request, error) {
	if ctx == nil {
		return nil, aipapi.ErrNilContext
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, body)
	if err != nil {
		return nil, err
	}
	req.Header = BuildOutboundHeaders(apiSecret, streaming, extraHeaders)
	if contentLength >= 0 {
		req.ContentLength = contentLength
	} else {
		req.ContentLength = -1
	}
	// Never propagate request trailers (Requirement 12.2, 12.6).
	req.Trailer = nil
	// Maintain connection reuse with shared client.
	req.Close = false
	// Derive Host header from targetURL, not untrusted client headers.
	req.Host = ""
	return req, nil
}

// Client returns the effective HTTP client.
func (p WireOpenPrimitives) Client() *http.Client {
	return ResolveHTTPClient(p.HTTPClient)
}

// Execute executes openFn within the credential-rotation loop.
func (p WireOpenPrimitives) Execute(
	ctx context.Context,
	openFn func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error),
) (aipapi.ManagedEventStream, error) {
	// The quota-switch threshold selects long quota exhaustion; it is bound
	// from the candidate snapshot's upstream_credentials policy (zero selects
	// the coordinator default).
	return openaicred.ExecuteWithCredentialPool(ctx, p.ProviderID, p.Pool, p.QuotaSwitchThreshold, openFn)
}

// ParseAndPeekStream parses an HTTP response stream and peeks the first event.
func (p WireOpenPrimitives) ParseAndPeekStream(ctx context.Context, resp *http.Response) (aipapi.ManagedEventStream, error) {
	es, err := ParseStreamResponse(p.ProviderID, resp, p.Flavor, p.MaxPending)
	if err != nil {
		return nil, err
	}
	return PeekFirstEvent(ctx, es)
}

// OpenWire opens a backend attempt directly from a wire request (Requirement 8, 12).
func (p WireOpenPrimitives) OpenWire(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, aipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	targetURL, err := p.ResolveURL()
	if err != nil {
		return nil, err
	}

	// With a proven exact-bytes replay factory, rotate across credentials
	// through the shared coordinator before any output or effect; without
	// one, make a single attempt and return the classified error.
	if p.Pool != nil && req.Replay.Valid() {
		return p.openWireReplay(ctx, targetURL, req)
	}

	var cred credpool.Credential
	if p.Pool != nil {
		now := time.Now()
		c, aerr := p.Pool.Acquire(now, nil)
		if aerr != nil {
			if errors.Is(aerr, credpool.ErrNoUsableCredential) {
				return nil, aipapi.RecoverablePreOutputError(aerr)
			}
			return nil, fmt.Errorf("%s: %w", p.ProviderID, aerr)
		}
		cred = c
	}

	streaming := req.WireRequest.Delivery == aipapi.DeliveryModeStreaming
	httpReq, err := NewOutboundRequest(ctx, targetURL, req.Body, req.ContentLength, cred.Secret, streaming, req.Header)
	if err != nil {
		return nil, err
	}
	// The wire lane has no canonical Call. Record only the bounded final
	// request-byte representation before the HTTP client commits upstream
	// bytes; adapters with richer post-transform facts may replace this summary.
	coremetering.ObservePreparedInput(ctx, coremetering.PreparedInputSummary{
		PayloadBytes: req.ContentLength, PayloadBytesPresent: req.ContentLength >= 0,
		MethodRef: "adapter:openaicompat.wire_payload.v1",
	})

	client := p.Client()
	resp, err := client.Do(httpReq)
	if err != nil {
		if p.Pool != nil && cred.ID != "" {
			kind, retryAfter := openaicred.ClassifyOpenAIAPIError(err)
			now := time.Now()
			switch kind {
			case openaicred.FailureAuthInvalid:
				p.Pool.MarkAuthInvalid(cred.ID)
			case openaicred.FailureRateLimited:
				until := credpool.CooldownFromRetryAfterOrFallback(retryAfter, now, p.RateLimitFallback)
				p.Pool.MarkRateLimited(cred.ID, until)
			}
		}
		kind, _ := openaicred.ClassifyOpenAIAPIError(err)
		if kind == openaicred.FailureRetryable || kind == openaicred.FailureRateLimited || kind == openaicred.FailureAuthInvalid {
			return nil, aipapi.RecoverablePreOutputError(err)
		}
		return nil, err
	}

	stream, err := p.ParseAndPeekStream(ctx, resp)
	if err != nil {
		if p.Pool != nil && cred.ID != "" {
			kind, retryAfter := openaicred.ClassifyOpenAIAPIError(err)
			now := time.Now()
			switch kind {
			case openaicred.FailureAuthInvalid:
				p.Pool.MarkAuthInvalid(cred.ID)
			case openaicred.FailureRateLimited:
				until := credpool.CooldownFromRetryAfterOrFallback(retryAfter, now, p.RateLimitFallback)
				p.Pool.MarkRateLimited(cred.ID, until)
			}
		}
		kind, _ := openaicred.ClassifyOpenAIAPIError(err)
		if kind == openaicred.FailureRetryable || kind == openaicred.FailureRateLimited || kind == openaicred.FailureAuthInvalid {
			return nil, aipapi.RecoverablePreOutputError(err)
		}
		return nil, err
	}
	return stream, nil
}
