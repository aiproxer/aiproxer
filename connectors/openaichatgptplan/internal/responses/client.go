package responses

// The public Responses HTTP transport.
//
// The connector's inference route is the documented public Responses API:
// POST https://api.openai.com/v1/responses, authenticated with the access
// token of the EXPLICITLY SELECTED ChatGPT-plan registration, and always
// streamed. This file owns that request: it takes the exact request the
// projector/policy seam produced (WireRequest), asks the registration
// authority for the selected profile's token, sends the request, and hands
// back the raw SSE event stream for the normalizer (spec task 5.2) and the
// delivery adapter (spec task 5.3) to consume.
//
// # The credential is not part of the projection
//
// WireRequest deliberately carries no credential (payload.go). The token is
// acquired HERE, for the registration selected NOW - or, for an attempt the
// service has already authorized, it is the bound credential of exactly that
// registration. The client holds no token of its own, so a credential the store
// has already replaced is never presented and one account's request is never
// sent with another account's credential.
//
// A bound attempt never re-reads the mutable selection: the service resolves
// the attempt's registration once, validates the record that supplied the
// token, and hands the credential down through [BoundCredential]. A selection
// that moves after the admission decision therefore cannot substitute another
// registration's account between the check and the request.
//
// # One upstream path for both downstream modes
//
// The client has no downstream delivery-mode parameter. The public route
// always streams (the policy's previewStream constant is the one transport
// posture), and the downstream mode is collection over the same event
// stream: there is no provider non-stream request path to select.
//
// # Errors are non-secret and bounded
//
// Provider error text is untrusted input. It is redacted against the token
// that was sent, stripped of control characters and bounded BEFORE it can
// reach an error string. The transport never repeats a raw request or
// response body.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	// maxRequestBytes bounds the projected request document the transport
	// sends. It is generous for a long canonical history and exists so a
	// caller cannot hand the transport an unbounded document.
	maxRequestBytes = 32 << 20

	// maxErrorBodyBytes bounds the provider error document the connector
	// reads. One extra byte detects an excess, so an oversize answer is
	// refused at acquisition instead of being silently truncated.
	maxErrorBodyBytes = 64 << 10

	// maxDiagnosticRunes bounds the provider text an error carries.
	maxDiagnosticRunes = 512

	// maxErrorCodeRunes bounds the machine-readable code and type an error
	// carries.
	maxErrorCodeRunes = 128

	// maxRequestIDRunes bounds the provider request id an error carries.
	maxRequestIDRunes = 128

	// errorBodyBudget is the whole byte budget of one provider error
	// answer: the document bound plus the byte that detects an excess.
	errorBodyBudget = maxErrorBodyBytes + 1

	// responseHeaderTimeout bounds how long the provider may take to start
	// an answer. It deliberately does not bound the body: a stream lives
	// until its own end or the caller's context, and the body budgets are
	// this package's byte bounds.
	responseHeaderTimeout = 60 * time.Second
)

// The failures of the public Responses transport.
//
// They are CONSTANT and value-free: the provider's documents are untrusted
// input, and a diagnostic that repeated one would put unvetted text into an
// operator- or support-facing error. The bounded, redacted diagnostic of a
// provider answer travels on HTTPError instead.
var (
	// ErrNoSelectedProfile reports that no ChatGPT-plan registration is
	// selected. The response belongs to a registration, and a request
	// without one would be an anonymous request.
	ErrNoSelectedProfile = errors.New("responses: no ChatGPT-plan registration is selected")

	// ErrRegistrationUnusable reports that the selected registration has no
	// usable access token. An empty token is REFUSED, never sent as an
	// anonymous request and never replaced with another registration's
	// token.
	ErrRegistrationUnusable = errors.New("responses: the selected ChatGPT-plan registration has no usable access token")

	// ErrInvalidWireRequest reports a request the transport must not send:
	// a non-POST method, an empty body, a body that is not a JSON object, or
	// a request that already carries a credential header.
	ErrInvalidWireRequest = errors.New("responses: the request is not a valid public Responses request")

	// ErrNotPublicEndpoint reports an endpoint other than the documented
	// public Responses route. The loopback address is the one exception, and
	// it is the local test server.
	ErrNotPublicEndpoint = errors.New("responses: the request is not addressed to the public Responses endpoint")

	// ErrRequestNotStreaming reports a request that does not ask the
	// provider to stream. The public route always streams, whatever the
	// downstream delivery mode is.
	ErrRequestNotStreaming = errors.New("responses: the public Responses request must always stream")

	// ErrRequestTooLarge reports a projected request document that exceeds
	// its bound. The transport refuses it before any credential is acquired
	// or any network request is made.
	ErrRequestTooLarge = errors.New("responses: the projected request exceeds its bound")

	// ErrUnexpectedStatus reports a provider answer this transport cannot
	// use. HTTPError carries the status, code and request id when they
	// arrived.
	ErrUnexpectedStatus = errors.New("responses: the provider did not accept the request")

	// ErrUnexpectedContentType reports a successful status whose answer is
	// not an event stream. Reading such an answer as an empty stream would
	// hide a provider-side protocol change behind a normal end of stream.
	ErrUnexpectedContentType = errors.New("responses: the provider did not answer with an event stream")

	// ErrErrorBodyTooLarge reports a provider error document that exceeds
	// its bound. It is an answer that ARRIVED and cannot be used, never an
	// absent one.
	ErrErrorBodyTooLarge = errors.New("responses: the provider error document exceeds its bound")

	// ErrErrorBodyUnreadable reports a provider error document this build
	// cannot read. The status still travels on HTTPError, so an unusable
	// answer is never reported as an absent one.
	ErrErrorBodyUnreadable = errors.New("responses: the provider error document could not be read")

	// ErrTransportFailed reports a request that never produced an answer.
	ErrTransportFailed = errors.New("responses: the public Responses request failed")
)

// ClientConfig is the configuration of the public Responses transport.
type ClientConfig struct {
	// HTTPClient performs the request. Empty means a client with a bounded
	// response-header timeout and a redirect-refusing policy.
	//
	// A SUPPLIED client keeps its own transport and timeout; only the
	// redirect policy is this connector's, because a redirect could send
	// the bearer credential to a destination of the provider's choosing.
	HTTPClient *http.Client
	// Tokens hands out the selected registration's access token. It is
	// required: the transport must never invent or borrow a credential.
	Tokens siwc.SelectedTokenProvider
}

// Client sends one public Responses request and returns its raw SSE stream.
//
// The client is safe for concurrent use: it holds no request state of its
// own.
type Client struct {
	httpClient *http.Client
	tokens     siwc.SelectedTokenProvider
}

// NewClient returns the public Responses transport.
func NewClient(config ClientConfig) (*Client, error) {
	if config.Tokens == nil {
		return nil, errors.New("responses: the public Responses transport needs the registration authority")
	}
	return &Client{
		httpClient: newResponsesHTTPClient(config.HTTPClient),
		tokens:     config.Tokens,
	}, nil
}

// OpenStream sends one projected public Responses request and returns the raw
// SSE event stream of the selected registration.
//
// The request is the exact document the projector/policy seam produced; only
// the credential header is added here. The selected registration is checked
// every time: an unusable selection is an explicit error, never an anonymous
// request and never another registration's credential.
//
// This entry point serves a caller that has no admission decision of its own
// (the transport's direct contract tests). An attempt that was already
// authorized by the connector's execution path is opened through
// [Client.OpenStreamForAttempt] instead, with the ONE credential the service
// acquired and validated for it. Admitted production attempts must use the
// bound method; the model inventory does not call this entry point (it builds
// a public model-list client instead).
func (c *Client) OpenStream(ctx context.Context, request WireRequest) (*Stream, error) {
	if ctx == nil {
		return nil, aipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.tokens == nil || c.httpClient == nil {
		return nil, errors.New("responses: the public Responses transport is not configured")
	}
	// The transport-side guards run BEFORE a credential is acquired: a request
	// this route must not send must not cost a token acquisition either.
	if err := validateWireRequest(request); err != nil {
		return nil, err
	}

	// CHECK, do not assume: the registration is selected NOW, and only that
	// registration's token is presented. A selection that cannot be read is
	// an explicit failure, not an anonymous request.
	profileID, err := c.tokens.Selected(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoSelectedProfile, err)
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil, ErrNoSelectedProfile
	}
	grant, err := c.tokens.Token(ctx, profileID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRegistrationUnusable, err)
	}
	return c.openStreamBound(ctx, request, BoundCredential{
		ProfileID:   profileID,
		AccessToken: grant.AccessToken,
		Generation:  grant.Generation,
	})
}

// BoundCredential is the credential of ONE registration an attempt is already
// authorized to act for.
//
// The service resolves the attempt's registration, validates it, acquires this
// credential from it, and re-validates the record that supplied it BEFORE
// provider I/O. The transport therefore never consults the mutable selection
// for a bound attempt: a selection that moves after the admission decision
// cannot substitute another registration's account between the check and the
// request.
type BoundCredential struct {
	// ProfileID is the non-secret local id of the bound registration.
	ProfileID string
	// AccessToken is the bearer credential acquired from that registration.
	AccessToken string
	// Generation is the one-way fingerprint of the credential pair the token
	// was acquired from.
	Generation [sha256.Size]byte
}

// OpenStreamForAttempt sends one projected public Responses request with the
// credential of an ALREADY AUTHORIZED registration.
//
// The credential is the attempt's binding: the service proved, before calling
// this method, that the record which supplied the token is the registration the
// attempt was admitted for and that it still carries the direct plan-use grant.
// The transport presents exactly that credential and does not read, let alone
// follow, the mutable selection for this attempt.
func (c *Client) OpenStreamForAttempt(
	ctx context.Context,
	request WireRequest,
	credential BoundCredential,
) (*Stream, error) {
	if ctx == nil {
		return nil, aipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.httpClient == nil {
		return nil, errors.New("responses: the public Responses transport is not configured")
	}
	if strings.TrimSpace(credential.ProfileID) == "" {
		return nil, ErrNoSelectedProfile
	}
	return c.openStreamBound(ctx, request, credential)
}

// openStreamBound performs one request with one already-bound credential.
func (c *Client) openStreamBound(
	ctx context.Context,
	request WireRequest,
	credential BoundCredential,
) (*Stream, error) {
	if err := validateWireRequest(request); err != nil {
		return nil, err
	}
	token := strings.TrimSpace(credential.AccessToken)
	if token == "" {
		return nil, ErrRegistrationUnusable
	}
	// A token acquisition may have waited on a refresh; the caller may have
	// given up while it did.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	profileID := strings.TrimSpace(credential.ProfileID)

	httpRequest, err := http.NewRequestWithContext(ctx, request.Method, request.URL, bytes.NewReader(request.Body))
	if err != nil {
		return nil, ErrInvalidWireRequest
	}
	header := request.Headers.Clone()
	if header == nil {
		header = make(http.Header, 4)
	}
	header.Set("Authorization", "Bearer "+token)
	httpRequest.Header = header

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &TransportError{
			Diagnostic: safeProviderText(err.Error(), maxDiagnosticRunes, token),
			ProfileID:  safeProviderText(profileID, maxProfileIDRunes),
		}
	}
	if response.StatusCode != http.StatusOK {
		return nil, providerError(ctx, response, token, profileID)
	}
	contentType := response.Header.Get("Content-Type")
	if !isEventStream(contentType) {
		// The body is closed UNREAD: a successful answer that is not an
		// event stream is untrusted input the connector has no use for,
		// and reading it would spend bytes on a document it refuses.
		//
		// The request id is still available transport metadata, and it is
		// preserved through the same redact-before-bound path as the non-200
		// branch: discarding it here would make the refusal impossible to
		// correlate with the provider.
		requestID := firstHeader(response.Header, "x-request-id", "openai-request-id")
		_ = response.Body.Close()
		return nil, &HTTPError{
			Status:      response.StatusCode,
			RequestID:   safeProviderText(requestID, maxRequestIDRunes, token),
			ContentType: safeProviderText(contentType, maxRequestIDRunes, token),
			ProfileID:   safeProviderText(profileID, maxProfileIDRunes),
			Err:         ErrUnexpectedContentType,
		}
	}
	// A successful event-stream answer keeps the provider's correlation id.
	// Spec task 5.1 discarded it on this path (follow-up S2); capturing it here
	// lets a mid-stream failure be correlated with the provider, and it passes
	// the same redact-before-bound path as every other provider diagnostic.
	//
	// The sanitizer closes over the credential that was sent so provider text
	// arriving AFTER the headers (a stream error event, a malformed payload)
	// is redacted exactly like a transport diagnostic. It is held by the stream
	// and is never rendered.
	// Two forms of the SAME correlation id. The identity is taken before the
	// display bound so accounting can tell two answers apart; the request id is
	// the bounded, redacted diagnostic form and is what a log or an error may
	// carry.
	correlation := providerRequestIdentity(firstHeader(response.Header, "x-request-id", "openai-request-id"), token)
	return newStreamWithMetadata(ctx, response.Body, profileID, credential.Generation, answerMetadata{
		requestID:       boundRunes(correlation, maxRequestIDRunes),
		requestIdentity: correlation,
		sanitize: func(text string, limit int) string {
			return safeProviderText(text, limit, token)
		},
	}), nil
}

// validateWireRequest enforces the transport-side request contract.
//
// The policy and the projector own the wire document; this guard keeps the
// transport from becoming a private or non-streaming path for a future
// caller. The streaming flag is the policy's own constant, so the transport
// and the policy cannot disagree about the transport posture.
func validateWireRequest(request WireRequest) error {
	if len(request.Body) > maxRequestBytes {
		return ErrRequestTooLarge
	}
	if request.Method != http.MethodPost {
		return ErrInvalidWireRequest
	}
	body := bytes.TrimSpace(request.Body)
	if len(body) == 0 || !isJSONObject(body) {
		return ErrInvalidWireRequest
	}
	if err := requirePublicTarget(request.URL); err != nil {
		return err
	}
	if err := requireStreamingRequest(body); err != nil {
		return err
	}
	for name := range request.Headers {
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization") {
			// The credential is the selected registration's, added HERE.
			// A caller-supplied one would be a different account's token.
			return ErrInvalidWireRequest
		}
	}
	return nil
}

// EndpointForBaseURL returns the Responses endpoint for a REFERENCE base URL.
//
// The empty base URL is the documented public endpoint. A reference base URL
// must be either the documented public origin or a LOOPBACK IP-literal origin:
// the transport already accepts a local reference server for the connector's
// own contract tests, and this function must not become a wider seam than the
// transport. A private backend-api origin, a look-alike host name, a path or a
// query is refused.
//
// This is the validator of the connector's TEST-ONLY reference seam (the local
// reference server used by the contract tests and the staged host integration).
// It is not configuration: the shipped configuration surface names no endpoint,
// and production execution always uses the documented public resource.
func EndpointForBaseURL(base string) (string, error) {
	trimmed := strings.TrimSpace(base)
	if trimmed == "" {
		return PublicBaseURL + ResponsesPath, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil {
		return "", ErrNotPublicEndpoint
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrNotPublicEndpoint
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", ErrNotPublicEndpoint
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", ErrNotPublicEndpoint
	}
	publicOrigin := parsed.Scheme == "https" && parsed.Host == PublicBaseURL[len("https://"):]
	if !publicOrigin && !isLoopbackHost(parsed.Hostname()) {
		return "", ErrNotPublicEndpoint
	}
	return strings.TrimRight(trimmed, "/") + ResponsesPath, nil
}

// requirePublicTarget refuses any endpoint but the documented public route.
//
// The production target is the exact constant pair, so a rewrite of the
// public origin or path is refused rather than followed. The loopback
// address is the one exception, and it is narrow: the local test server, an
// IP literal rather than a name a resolver could point elsewhere.
func requirePublicTarget(target string) error {
	if target == PublicBaseURL+ResponsesPath {
		return nil
	}
	parsed, err := url.Parse(target)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil {
		return ErrNotPublicEndpoint
	}
	if parsed.Path != ResponsesPath || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ErrNotPublicEndpoint
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ErrNotPublicEndpoint
	}
	if !isLoopbackHost(parsed.Hostname()) {
		return ErrNotPublicEndpoint
	}
	return nil
}

// isLoopbackHost reports whether the host is a loopback IP literal.
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

// requireStreamingRequest enforces the mandatory upstream transport flag.
//
// The value is read from the policy's constant, never re-hardcoded here: the
// policy decides the transport posture, and the transport proves the request
// carries it.
func requireStreamingRequest(body []byte) error {
	document := make(map[string]json.RawMessage)
	if err := json.Unmarshal(body, &document); err != nil || document == nil {
		return ErrInvalidWireRequest
	}
	raw, ok := document["stream"]
	if !ok {
		return ErrRequestNotStreaming
	}
	var stream bool
	if err := json.Unmarshal(raw, &stream); err != nil {
		return ErrRequestNotStreaming
	}
	// The expected value is the policy's constant, read through a local so
	// the transport and the policy cannot disagree about the posture.
	want := previewStream
	if stream != want {
		return ErrRequestNotStreaming
	}
	return nil
}

// isEventStream reports whether a content type is the SSE media type.
func isEventStream(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "text/event-stream"
}

// providerError turns a refused provider answer into an HTTPError.
//
// The answer is read within ONE bounded budget, so an endless error document
// cannot exhaust the connector. An oversize or unreadable document is still
// an ANSWER: the status is preserved and the failure says which part could
// not be used, instead of reporting the answer as absent.
func providerError(ctx context.Context, response *http.Response, token, profileID string) error {
	defer func() { _ = response.Body.Close() }()
	requestID := firstHeader(response.Header, "x-request-id", "openai-request-id")
	httpErr := &HTTPError{
		Status:    response.StatusCode,
		RequestID: safeProviderText(requestID, maxRequestIDRunes, token),
		ProfileID: safeProviderText(profileID, maxProfileIDRunes),
		Err:       ErrUnexpectedStatus,
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, errorBodyBudget))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		httpErr.Err = errors.Join(ErrUnexpectedStatus, ErrErrorBodyUnreadable)
		return httpErr
	}
	if len(body) > maxErrorBodyBytes {
		httpErr.Err = errors.Join(ErrUnexpectedStatus, ErrErrorBodyTooLarge)
		return httpErr
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return httpErr
	}
	var document struct {
		Error struct {
			Message string          `json:"message"`
			Type    string          `json:"type"`
			Code    json.RawMessage `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		httpErr.Err = errors.Join(ErrUnexpectedStatus, ErrErrorBodyUnreadable)
		return httpErr
	}
	httpErr.Type = safeProviderText(document.Error.Type, maxErrorCodeRunes, token)
	httpErr.Code = safeProviderText(rawString(document.Error.Code), maxErrorCodeRunes, token)
	httpErr.Diagnostic = safeProviderText(document.Error.Message, maxDiagnosticRunes, token)
	if httpErr.Type == "" && httpErr.Code == "" && httpErr.Diagnostic == "" {
		// A JSON answer with no usable error object arrived and cannot be
		// used. It is not an absent answer.
		httpErr.Err = errors.Join(ErrUnexpectedStatus, ErrErrorBodyUnreadable)
	}
	return httpErr
}

// rawString returns a JSON string member, or empty for any other value.
//
// A code that arrived as a number, object or null is not a documented
// machine-readable code; the status and the diagnostic still identify the
// answer.
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

// firstHeader returns the first non-empty value of the named headers.
func firstHeader(header http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

// providerRequestIdentity redacts and sanitises the provider's correlation text
// WITHOUT bounding it.
//
// Redaction still runs FIRST, so a credential that a provider echoed into its
// correlation header cannot survive into the identity. The length bound is
// deliberately not applied here: a bounded value cannot distinguish two
// correlation ids that differ only after the bound, and the accounting layer
// needs the distinction. Callers that render or log the value bound it
// themselves (safeProviderText).
func providerRequestIdentity(text string, secrets ...string) string {
	redacted := oauthcred.Redact(text, secrets...)
	sanitized := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, redacted)
	fields := strings.Fields(sanitized)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

// safeProviderText redacts, sanitises and bounds untrusted provider text.
//
// The order is the security property: REDACTION runs before the length bound,
// so a secret crossing the bound cannot keep a visible prefix. Control
// characters are replaced so a provider message cannot forge a log line, and
// the bound is applied to characters so a truncated diagnostic stays valid
// text.
func safeProviderText(text string, limit int, secrets ...string) string {
	return boundRunes(providerRequestIdentity(text, secrets...), limit)
}

// boundRunes shortens a text to at most limit characters.
func boundRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

// HTTPError is a provider answer this transport could not use.
//
// Status, Code, Type and RequestID are the provider's own, non-secret
// identities. Diagnostic is the provider's message after redaction and
// bounding; it never carries the access token or a raw body. ContentType is
// set only when a successful status carried an unusable content type.
type HTTPError struct {
	// Status is the upstream HTTP status.
	Status int
	// Code is the machine-readable provider error code, when it arrived as
	// a string.
	Code string
	// Type is the provider error type, when it arrived.
	Type string
	// RequestID is the provider request id, when it arrived.
	RequestID string
	// ContentType is the unusable content type of a successful answer.
	ContentType string
	// Diagnostic is the bounded, redacted provider message.
	Diagnostic string
	// ProfileID is the non-secret local id of the registration that
	// authorized the request.
	ProfileID string
	// Err is the classified cause.
	Err error
}

// Error renders the provider identity and the bounded diagnostic.
func (e *HTTPError) Error() string {
	if e == nil {
		return ErrUnexpectedStatus.Error()
	}
	msg := ErrUnexpectedStatus.Error()
	if errors.Is(e.Err, ErrUnexpectedContentType) {
		msg = ErrUnexpectedContentType.Error()
	}
	if e.Status != 0 {
		msg += " (status " + strconv.Itoa(e.Status) + ")"
	}
	if e.Code != "" {
		msg += " (code " + strconv.Quote(e.Code) + ")"
	}
	if e.RequestID != "" {
		msg += " (request id " + strconv.Quote(e.RequestID) + ")"
	}
	if e.ContentType != "" {
		msg += " (content type " + strconv.Quote(e.ContentType) + ")"
	}
	if e.ProfileID != "" {
		msg += " (profile " + strconv.Quote(e.ProfileID) + ")"
	}
	if e.Diagnostic != "" {
		msg += ": " + e.Diagnostic
	}
	return msg
}

// Unwrap returns the classified cause.
func (e *HTTPError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// TransportError is a request that never produced a provider answer.
//
// The diagnostic is the transport's own text after redaction and bounding; it
// carries no credential and no request body.
type TransportError struct {
	// Diagnostic is the bounded, redacted transport text.
	Diagnostic string
	// ProfileID is the non-secret local id of the registration the request
	// was bound to.
	ProfileID string
}

// Error renders the bounded diagnostic.
func (e *TransportError) Error() string {
	if e == nil || e.Diagnostic == "" {
		return ErrTransportFailed.Error()
	}
	return ErrTransportFailed.Error() + ": " + e.Diagnostic
}

// Unwrap returns the classified cause.
func (e *TransportError) Unwrap() error { return ErrTransportFailed }

// newResponsesHTTPClient returns the client the Responses requests use.
//
// The redirect policy is always this connector's: a redirect is refused,
// because following one would replay the account's bearer credential to a
// destination the provider chose.
func newResponsesHTTPClient(caller *http.Client) *http.Client {
	refuseRedirect := func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if caller == nil {
		transport := http.DefaultTransport
		if base, ok := http.DefaultTransport.(*http.Transport); ok {
			cloned := base.Clone()
			cloned.ResponseHeaderTimeout = responseHeaderTimeout
			transport = cloned
		}
		return &http.Client{Transport: transport, CheckRedirect: refuseRedirect}
	}
	return &http.Client{
		Transport:     caller.Transport,
		Timeout:       caller.Timeout,
		Jar:           caller.Jar,
		CheckRedirect: refuseRedirect,
	}
}
