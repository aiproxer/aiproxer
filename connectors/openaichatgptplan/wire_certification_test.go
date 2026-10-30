package openaichatgptplan_test

// Certification of the direct connector's provider surface (spec task 10.2).
//
// The per-seam proofs already exist: the Responses transport observes exactly
// one POST to the documented resource, the model-list client observes exactly
// one GET to the documented inventory, and the SIWC registration tests observe
// the auth endpoints on their own fakes. Each of those tests sees one client
// and one server, so none of them can prove the property the task names: during
// a COMPLETE direct lifecycle, the provider surface observes the auth
// endpoints, the account inventory and the Responses resource, and nothing
// else.
//
// This file supplies that single picture. Every HTTP request of the lifecycle
// travels through ONE recording transport, which forwards to ONE test server
// while preserving the public scheme, host and path the connector addressed.
// The server answers the requests and the transport records them; the
// assertions then require the observed set to EQUAL the documented set. A
// request to any other origin or path is recorded, fails the comparison, and
// names itself. The user-agent, authorization and private-header checks run
// over the SAME records, so a header added by any seam of the lifecycle is
// visible here.
//
// The lifecycle is: first SIWC registration, one account inventory read, and
// one inference request per downstream delivery mode. The registration uses
// the connector's own OIDC validator, so its discovery and key-set reads are
// part of the observed surface. The test contacts no provider, uses no real
// credential, and makes no claim about traffic outside this flow.

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The documented public origins and paths of the direct connector.
//
// The values are literals on purpose. Deriving them from the connector's own
// constants would make the observation agree with whatever the connector
// addresses, so a change of a constant to a private route would pass
// unnoticed.
const (
	wireAuthOrigin = "https://auth.openai.com"
	wireAPIOrigin  = "https://api.openai.com"

	wireAuthHost = "auth.openai.com"
	wireAPIHost  = "api.openai.com"

	wireDiscoveryPath = "/.well-known/openid-configuration"
	wireJWKSPath      = "/.well-known/jwks.json"
	wireAuthorizePath = "/api/accounts/authorize"
	wireTokenPath     = "/api/accounts/oauth/token"
	wireRevokePath    = "/api/accounts/oauth/revoke"
	wireUserInfoPath  = "/api/accounts/oauth/userinfo"
	wireModelsPath    = "/v1/models"
	wireResponsesPath = "/v1/responses"
)

const (
	wireKeyID          = "wire-key-1"
	wireIssuedClientID = "oaiapp_issued_wire"
	wireProfileID      = "acct-wire"
	wireAccessToken    = "wire-access-token-not-a-secret"
	wireRefreshToken   = "wire-refresh-token-not-a-secret"
	wireAuthorization  = "wire-authorization-code"
	wireScope          = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	wireInstructions   = "Run the harness's own tests."
	wireSystemText     = "Never invent citations."
)

// wirePrivateHeaderNames must not appear on any request of the lifecycle.
//
// The names are compared case-insensitively against the RAW header map: a
// header assigned under a non-canonical spelling is still a header on the
// wire.
var wirePrivateHeaderNames = []string{
	"originator",
	"version",
	"codex-task-type",
	"conversation_id",
	"session_id",
	"chatgpt-account-id",
	"openai-beta",
}

// wirePrivateBodyMarkers must not appear anywhere in a request URL or body.
var wirePrivateBodyMarkers = []string{
	"codex_cli_rs",
	"chatgpt.com",
	"backend-api",
	"you are codex",
	"<user_instructions>",
	"</user_instructions>",
	"chatgpt-account-id",
}

// wireSSEScript is the provider's answer, byte for byte. It carries the
// terminal event the direct contract requires; the raw transport proof is
// that the events arrive in this order with these payloads.
const wireSSEScript = "event: response.created\n" +
	"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_wire\",\"status\":\"in_progress\"}}\n\n" +
	"event: response.output_text.delta\n" +
	"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_wire\",\"content_index\":0,\"delta\":\"hello\"}\n\n" +
	"event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_wire\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_wire\",\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n"

// wireExpectedEvents is what the transport must deliver for wireSSEScript.
var wireExpectedEvents = []responses.Event{
	{
		Name: "response.created",
		Data: []byte(`{"type":"response.created","response":{"id":"resp_wire","status":"in_progress"}}`),
	},
	{
		Name: "response.output_text.delta",
		Data: []byte(`{"type":"response.output_text.delta","item_id":"msg_wire","content_index":0,"delta":"hello"}`),
	},
	{
		Name: "response.completed",
		Data: []byte(`{"type":"response.completed","response":{"id":"resp_wire","status":"completed","output":[{"id":"msg_wire","type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`),
	},
}

// wireObservedRequest is one request the recording transport saw, with the
// public target the connector addressed.
type wireObservedRequest struct {
	method string
	scheme string
	host   string
	path   string
	query  string
	header http.Header
	body   []byte
}

// wireTestServer is the provider surface of the lifecycle.
//
// It answers the documented endpoints for the public hosts. Any other host or
// path is still recorded by the transport and answered with 404, so the
// endpoint-set assertion can name it.
type wireTestServer struct {
	server *httptest.Server
	key    *rsa.PrivateKey

	mu       sync.Mutex
	requests []wireObservedRequest
	nonce    string
}

var (
	wireKeyOnce sync.Once
	wireKey     *rsa.PrivateKey
	wireKeyErr  error
)

// wireTestKey returns the key the fake provider signs its ID token with.
//
// The key is generated once for the test binary: a 2048-bit key would
// otherwise dominate the run.
func wireTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	wireKeyOnce.Do(func() {
		wireKey, wireKeyErr = rsa.GenerateKey(rand.Reader, 2048)
	})
	if wireKeyErr != nil {
		t.Fatalf("generate the test key: %v", wireKeyErr)
	}
	return wireKey
}

// newWireTestServer starts the provider surface.
func newWireTestServer(t *testing.T) *wireTestServer {
	t.Helper()
	provider := &wireTestServer{key: wireTestKey(t)}
	provider.server = httptest.NewServer(http.HandlerFunc(provider.handle))
	t.Cleanup(provider.server.Close)
	return provider
}

// client returns the one HTTP client of the lifecycle.
//
// Every connector collaborator receives this client, so every request it makes
// is recorded, whatever origin it addresses.
func (s *wireTestServer) client() *http.Client {
	target, err := url.Parse(s.server.URL)
	if err != nil {
		panic("the test server URL is not a URL: " + err.Error())
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &wireRecordingTransport{
			provider: s,
			target:   target,
			base:     s.server.Client().Transport,
		},
	}
}

// handle answers the documented provider documents.
func (s *wireTestServer) handle(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Host, wireAuthHost) && !strings.EqualFold(r.Host, wireAPIHost) {
		http.NotFound(w, r)
		return
	}
	path := r.URL.Path
	switch {
	case strings.EqualFold(r.Host, wireAuthHost) && path == wireDiscoveryPath:
		s.serveDiscovery(w)
	case strings.EqualFold(r.Host, wireAuthHost) && path == wireJWKSPath:
		s.serveKeySet(w)
	case strings.EqualFold(r.Host, wireAuthHost) && path == wireAuthorizePath:
		s.serveAuthorize(w, r)
	case strings.EqualFold(r.Host, wireAuthHost) && path == wireTokenPath:
		s.serveToken(w, r)
	case strings.EqualFold(r.Host, wireAPIHost) && path == wireModelsPath:
		s.serveModels(w)
	case strings.EqualFold(r.Host, wireAPIHost) && path == wireResponsesPath:
		s.serveResponses(w)
	default:
		http.NotFound(w, r)
	}
}

// serveDiscovery publishes the provider's OIDC configuration.
func (s *wireTestServer) serveDiscovery(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w,
		`{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q,"revocation_endpoint":%q,"userinfo_endpoint":%q}`,
		wireAuthOrigin,
		wireAuthOrigin+wireAuthorizePath,
		wireAuthOrigin+wireTokenPath,
		wireAuthOrigin+wireJWKSPath,
		wireAuthOrigin+wireRevokePath,
		wireAuthOrigin+wireUserInfoPath,
	)
}

// serveKeySet publishes the key the ID token is signed with.
func (s *wireTestServer) serveKeySet(w http.ResponseWriter) {
	exponent := big.NewInt(int64(s.key.E)).Bytes()
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w,
		`{"keys":[{"kty":"RSA","kid":%q,"use":"sig","alg":"RS256","n":%q,"e":%q}]}`,
		wireKeyID,
		base64.RawURLEncoding.EncodeToString(s.key.N.Bytes()),
		base64.RawURLEncoding.EncodeToString(exponent),
	)
}

// serveAuthorize stands in for the browser's authorization request.
func (s *wireTestServer) serveAuthorize(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.nonce = r.URL.Query().Get("nonce")
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, "<html><body>authorize</body></html>")
}

// serveToken answers the code exchange with a complete token set.
func (s *wireTestServer) serveToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "unreadable exchange", http.StatusBadRequest)
		return
	}
	audience := strings.TrimSpace(r.PostForm.Get("client_id"))
	if audience == "" {
		audience = wireIssuedClientID
	}
	s.mu.Lock()
	nonce := s.nonce
	s.mu.Unlock()
	idToken, err := s.signIDToken(audience, nonce)
	if err != nil {
		http.Error(w, "the token could not be signed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  wireAccessToken,
		"refresh_token": wireRefreshToken,
		"id_token":      idToken,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"scope":         wireScope,
	})
}

// serveModels answers the account inventory.
func (s *wireTestServer) serveModels(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"models":[`+
		`{"slug":"gpt-6.1-sol","display_name":"GPT-6.1 Sol","visibility":"list"},`+
		`{"slug":"gpt-6.1-codex","display_name":"GPT-6.1 Codex","visibility":"list"},`+
		`{"slug":"gpt-6.1-hidden","display_name":"Hidden","visibility":"hidden"}`+
		`]}`)
}

// serveResponses answers the streaming inference resource.
func (s *wireTestServer) serveResponses(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, wireSSEScript)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// signIDToken signs the ID token the validator will verify.
func (s *wireTestServer) signIDToken(audience, nonce string) (string, error) {
	if strings.TrimSpace(nonce) == "" {
		return "", errors.New("the authorization request carried no nonce")
	}
	now := time.Now()
	header := map[string]any{"alg": "RS256", "kid": wireKeyID}
	claims := map[string]any{
		"iss":   wireAuthOrigin,
		"aud":   audience,
		"sub":   "subject-wire",
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Unix(),
		"nonce": nonce,
	}
	signingInput := encodeJWTSegment(header) + "." + encodeJWTSegment(claims)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// encodeJWTSegment encodes one JWT segment.
func encodeJWTSegment(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic("the test fixture is not encodable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// browse performs the browser's part of the registration: it requests the
// authorization URL the connector generated and delivers the provider's
// callback to the loopback listener.
func (s *wireTestServer) browse(client *http.Client, authorizationURL string) error {
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		return err
	}
	if parsed.Host != wireAuthHost || parsed.Path != wireAuthorizePath {
		return fmt.Errorf("the connector generated the authorization URL %q, want the public %s%s",
			parsed.Redacted(), wireAuthOrigin, wireAuthorizePath)
	}
	authorization, err := client.Get(authorizationURL)
	if err != nil {
		return err
	}
	_ = authorization.Body.Close()
	query := parsed.Query()
	callback := url.Values{}
	callback.Set("state", query.Get("state"))
	callback.Set("code", wireAuthorization)
	callback.Set("client_id", wireIssuedClientID)
	answer, err := http.Get(query.Get("redirect_uri") + "?" + callback.Encode())
	if err != nil {
		return err
	}
	_ = answer.Body.Close()
	return nil
}

// recorded returns the requests the transport observed.
func (s *wireTestServer) recorded() []wireObservedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]wireObservedRequest(nil), s.requests...)
}

// wireRecordingTransport records every request and forwards it to the test
// server while keeping the public target visible to the server.
type wireRecordingTransport struct {
	provider *wireTestServer
	target   *url.URL
	base     http.RoundTripper
}

// RoundTrip records the request the connector made, then forwards it.
func (t *wireRecordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	if request.Body != nil {
		var err error
		body, err = io.ReadAll(io.LimitReader(request.Body, 1<<20))
		if err != nil {
			return nil, err
		}
		_ = request.Body.Close()
	}
	t.provider.mu.Lock()
	t.provider.requests = append(t.provider.requests, wireObservedRequest{
		method: request.Method,
		scheme: request.URL.Scheme,
		host:   request.URL.Host,
		path:   request.URL.Path,
		query:  request.URL.RawQuery,
		header: request.Header.Clone(),
		body:   body,
	})
	t.provider.mu.Unlock()

	forwarded := request.Clone(request.Context())
	forwarded.URL.Scheme = t.target.Scheme
	forwarded.URL.Host = t.target.Host
	// The server observes the public Host the connector addressed, not the
	// loopback address of the test.
	forwarded.Host = request.URL.Host
	if body != nil {
		forwarded.Body = io.NopCloser(bytes.NewReader(body))
		forwarded.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
		forwarded.ContentLength = int64(len(body))
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(forwarded)
}

// wireTokenAuthority is the selected registration of the lifecycle.
type wireTokenAuthority struct {
	profileID   string
	accessToken string
}

// Selected answers with the prepared registration.
func (a wireTokenAuthority) Selected(context.Context) (string, error) { return a.profileID, nil }

// Token answers with the registration's credential.
func (a wireTokenAuthority) Token(context.Context, string) (siwc.TokenGrant, error) {
	return siwc.TokenGrant{AccessToken: a.accessToken}, nil
}

// TestDirectConnectorObservesOnlyDocumentedProviderEndpoints is the task 10.2
// certification.
//
// The test runs the direct lifecycle against one recording provider surface:
// first registration (discovery, key set, authorization, token exchange), one
// account inventory read, and one inference request per downstream delivery
// mode. It then requires the observed method/origin/path set to EQUAL the
// documented set, and checks every recorded request for the private Codex
// headers, the credential's placement and the truthful product identity.
func TestDirectConnectorObservesOnlyDocumentedProviderEndpoints(t *testing.T) {
	t.Parallel()
	provider := newWireTestServer(t)
	client := provider.client()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	directory := t.TempDir()
	hosts, err := siwc.NewHostStore(filepath.Join(directory, "state", "host.json"))
	if err != nil {
		t.Fatalf("NewHostStore: %v", err)
	}
	profiles, err := siwc.NewProfileStore(filepath.Join(directory, "state", "profiles"))
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	validator, err := siwc.NewOIDCValidator(siwc.OIDCValidatorConfig{
		OIDCClientConfig: siwc.OIDCClientConfig{HTTPClient: client},
	})
	if err != nil {
		t.Fatalf("NewOIDCValidator: %v", err)
	}

	browserDone := make(chan error, 1)
	authorizer, err := siwc.NewAuthorizer(siwc.AuthorizerConfig{
		HTTPClient: client,
		BrowserOpener: func(_ context.Context, authorizationURL string) error {
			go func() { browserDone <- provider.browse(client, authorizationURL) }()
			return nil
		},
		Profiles:  profiles,
		Hosts:     hosts,
		Validator: validator,
	})
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	record, err := authorizer.Register(ctx, siwc.RegisterRequest{ProfileID: wireProfileID, Label: "wire"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	select {
	case browserErr := <-browserDone:
		if browserErr != nil {
			t.Fatalf("the simulated browser failed: %v", browserErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the simulated browser did not finish")
	}
	if record.AccessToken != wireAccessToken || record.RefreshToken != wireRefreshToken {
		t.Fatal("the registration did not persist the token set the provider issued")
	}
	if err := profiles.Select(record.ID); err != nil {
		t.Fatalf("Select: %v", err)
	}

	authority := wireTokenAuthority{profileID: record.ID, accessToken: record.AccessToken}
	modelsClient, err := siwc.NewModelsClient(siwc.ModelsClientConfig{HTTPClient: client, Tokens: authority})
	if err != nil {
		t.Fatalf("NewModelsClient: %v", err)
	}
	inventory, err := modelsClient.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	wantModels := []siwc.Model{
		{Slug: "gpt-6.1-sol", DisplayName: "GPT-6.1 Sol"},
		{Slug: "gpt-6.1-codex", DisplayName: "GPT-6.1 Codex"},
	}
	if inventory.ProfileID != record.ID || !slices.Equal(inventory.Models, wantModels) {
		t.Fatalf("the inventory = %+v, want the selected registration %q and the list-visible models %+v in provider order",
			inventory, record.ID, wantModels)
	}

	responsesClient, err := responses.NewClient(responses.ClientConfig{HTTPClient: client, Tokens: authority})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	var inferenceBodies [][]byte
	for _, mode := range []aipapi.DeliveryMode{aipapi.DeliveryModeStreaming, aipapi.DeliveryModeNonStreaming} {
		admission, err := responses.AdmitCall(responses.ProjectInput{
			Call:  wireCanonicalCall(mode),
			Model: "gpt-6.1-codex",
		}, responses.PolicyModeInteroperable)
		if err != nil {
			t.Fatalf("AdmitCall(%s): %v", mode, err)
		}
		if admission.Request.URL != wireAPIOrigin+wireResponsesPath {
			t.Fatalf("the %s admission addresses %q, want the public %s%s",
				mode, admission.Request.URL, wireAPIOrigin, wireResponsesPath)
		}
		stream, err := responsesClient.OpenStream(ctx, admission.Request)
		if err != nil {
			t.Fatalf("OpenStream(%s): %v", mode, err)
		}
		events := collectWireEvents(t, stream)
		if !slices.EqualFunc(events, wireExpectedEvents, sameWireEvent) {
			t.Fatalf("the %s events = %#v, want the provider's events %#v", mode, events, wireExpectedEvents)
		}
		inferenceBodies = append(inferenceBodies, admission.Request.Body)
	}
	if !bytes.Equal(inferenceBodies[0], inferenceBodies[1]) {
		t.Fatalf("the downstream delivery mode changed the upstream request:\n%s\n---\n%s",
			inferenceBodies[0], inferenceBodies[1])
	}

	observed := provider.recorded()
	assertWireEndpointSet(t, observed)
	assertWireRequestSurface(t, observed)
	if countWireResponses(observed) != 2 {
		t.Fatalf("the provider observed %d inference requests, want exactly one per downstream mode and no retry",
			countWireResponses(observed))
	}
	bodies := wireInferenceBodies(t, observed)
	if !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("the two inference requests differ on the wire:\n%s\n---\n%s", bodies[0], bodies[1])
	}
}

// wireCanonicalCall builds the canonical request of the lifecycle.
//
// The instruction block and the system-role message exercise the documented
// instruction mapping: the harness's own text is preserved, the system text
// is folded into instructions, and no Codex identity prompt is added.
func wireCanonicalCall(mode aipapi.DeliveryMode) aipapi.Call {
	return aipapi.Call{
		Instructions: []aipapi.Message{{
			Role:  aipapi.RoleDeveloper,
			Parts: []aipapi.Part{aipapi.TextPart(wireInstructions)},
		}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}},
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart(wireSystemText)}},
		},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIResponses,
			DeliveryMode: mode,
		},
	}
}

// collectWireEvents reads a raw provider stream to its end.
func collectWireEvents(t *testing.T, stream *responses.Stream) []responses.Event {
	t.Helper()
	defer func() { _ = stream.Close() }()
	var events []responses.Event
	for {
		event, err := stream.Next()
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		events = append(events, event)
	}
}

// sameWireEvent compares two raw events.
func sameWireEvent(left, right responses.Event) bool {
	return left.Name == right.Name && bytes.Equal(left.Data, right.Data)
}

// assertWireEndpointSet requires the observed endpoints to EQUAL the
// documented set.
func assertWireEndpointSet(t *testing.T, observed []wireObservedRequest) {
	t.Helper()
	got := map[string]int{}
	for _, request := range observed {
		got[request.method+" "+request.scheme+"://"+request.host+request.path]++
	}
	want := map[string]int{
		"GET " + wireAuthOrigin + wireDiscoveryPath: 1,
		"GET " + wireAuthOrigin + wireJWKSPath:      1,
		"GET " + wireAuthOrigin + wireAuthorizePath: 1,
		"POST " + wireAuthOrigin + wireTokenPath:    1,
		"GET " + wireAPIOrigin + wireModelsPath:     1,
		"POST " + wireAPIOrigin + wireResponsesPath: 2,
	}
	if maps.Equal(got, want) {
		return
	}
	t.Fatalf("the provider surface observed %v, want exactly %v (observed requests: %v)",
		got, want, wireDescribeRequests(observed))
}

// assertWireRequestSurface checks every recorded request.
func assertWireRequestSurface(t *testing.T, observed []wireObservedRequest) {
	t.Helper()
	for _, request := range observed {
		label := request.method + " " + request.scheme + "://" + request.host + request.path
		for name := range request.header {
			lower := strings.ToLower(name)
			if slices.Contains(wirePrivateHeaderNames, lower) || strings.HasPrefix(lower, "x-codex") {
				t.Errorf("%s carries the private Codex header %q", label, name)
			}
		}
		text := strings.ToLower(request.query + " " + string(request.body))
		for _, marker := range wirePrivateBodyMarkers {
			if strings.Contains(text, marker) {
				t.Errorf("%s carries the forbidden marker %q", label, marker)
			}
		}
		if request.query != "" && !strings.HasPrefix(request.path, wireAuthorizePath) {
			t.Errorf("%s carries the unexpected query %q", label, request.query)
		}
		switch request.path + "|" + request.host {
		case wireModelsPath + "|" + wireAPIHost:
			assertWireCredential(t, label, request, wireAccessToken)
			if request.method != http.MethodGet {
				t.Errorf("%s is a %s, want GET", label, request.method)
			}
			if accept := request.header.Get("Accept"); accept != "application/json" {
				t.Errorf("%s carries Accept %q, want application/json", label, accept)
			}
			if agent := request.header.Get("User-Agent"); !strings.Contains(agent, "AIProxer") {
				t.Errorf("%s user-agent = %q, want the truthful product identity", label, agent)
			}
		case wireResponsesPath + "|" + wireAPIHost:
			assertWireCredential(t, label, request, wireAccessToken)
			if request.method != http.MethodPost {
				t.Errorf("%s is a %s, want POST", label, request.method)
			}
			if agent := request.header.Get("User-Agent"); !strings.Contains(agent, "AIProxer") {
				t.Errorf("%s user-agent = %q, want the truthful product identity", label, agent)
			}
			assertWireInferenceBody(t, label, request.body)
		default:
			if values := request.header.Values("Authorization"); len(values) != 0 {
				t.Errorf("%s carries an authorization header it must not carry", label)
			}
		}
	}
}

// assertWireCredential requires exactly the selected registration's bearer.
func assertWireCredential(t *testing.T, label string, request wireObservedRequest, token string) {
	t.Helper()
	values := request.header.Values("Authorization")
	if len(values) != 1 || values[0] != "Bearer "+token {
		t.Errorf("%s authorization = %q, want exactly one %q", label, values, "Bearer "+token)
	}
}

// assertWireInferenceBody checks the mandatory transport posture and the
// instruction mapping of one inference request.
func assertWireInferenceBody(t *testing.T, label string, body []byte) {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("%s body is not a JSON object: %v", label, err)
	}
	if stream, ok := document["stream"].(bool); !ok || !stream {
		t.Errorf("%s stream = %v, want true", label, document["stream"])
	}
	if store, ok := document["store"].(bool); !ok || store {
		t.Errorf("%s store = %v, want false", label, document["store"])
	}
	if _, ok := document["previous_response_id"]; ok {
		t.Errorf("%s carries previous_response_id", label)
	}
	if model, _ := document["model"].(string); model != "gpt-6.1-codex" {
		t.Errorf("%s model = %q, want the native slug", label, model)
	}
	wantInstructions := wireInstructions + "\n\n" + wireSystemText
	if instructions, _ := document["instructions"].(string); instructions != wantInstructions {
		t.Errorf("%s instructions = %q, want the harness's own text with the system text folded in (%q)",
			label, instructions, wantInstructions)
	}
}

// countWireResponses counts the inference requests.
func countWireResponses(observed []wireObservedRequest) int {
	count := 0
	for _, request := range observed {
		if request.host == wireAPIHost && request.path == wireResponsesPath {
			count++
		}
	}
	return count
}

// wireInferenceBodies returns the bodies of the inference requests in order.
func wireInferenceBodies(t *testing.T, observed []wireObservedRequest) [][]byte {
	t.Helper()
	var bodies [][]byte
	for _, request := range observed {
		if request.host == wireAPIHost && request.path == wireResponsesPath {
			bodies = append(bodies, request.body)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("the provider observed %d inference request(s), want 2", len(bodies))
	}
	return bodies
}

// wireDescribeRequests renders the observed requests for a failure message.
func wireDescribeRequests(observed []wireObservedRequest) []string {
	described := make([]string, 0, len(observed))
	for _, request := range observed {
		described = append(described, request.method+" "+request.scheme+"://"+request.host+request.path)
	}
	return described
}
