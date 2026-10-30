package siwc

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// Tests for the public model inventory.
//
// The contract under test is requirement 7.1, 7.2, 7.3 and 7.7 of the
// migration specification and the slice of requirement 14.2 that belongs to
// this task:
//
//   - the inventory is read from the DOCUMENTED PUBLIC endpoint with the
//     selected registration's bearer credential;
//   - only the rows the provider marks for display are surfaced, and the
//     provider's ordering is preserved;
//   - the slug and the display name are preserved;
//   - the response body is bounded, and an oversize body is REFUSED.
//
// The proofs live at the wire: a reference server observes the request the
// connector makes, and the document the provider answers with is the input
// of the parser.
//
// # The authority
//
// The documentation states the request and the shape of the answer:
//
//	curl --fail-with-body --silent --show-error \
//	  https://api.openai.com/v1/models \
//	  -H "Authorization: Bearer $ACCESS_TOKEN" \
//	  | jq '[.models[] | select(.visibility == "list") | {slug, display_name}]'
//
// The proofs below rely on exactly that sentence: the public path, the
// bearer credential, the `models` array, the `list` visibility and the
// `slug` and `display_name` members.

// defaultModelsDocument is the documented model-list document.
//
// It carries the rows the documentation's example keeps AND the rows it
// drops, so that the visibility filter has something to filter.
const defaultModelsDocument = `{"models":[` +
	`{"slug":"gpt-6.1-sol","display_name":"GPT-6.1 Sol","visibility":"list"},` +
	`{"slug":"gpt-6.1-luna","display_name":"GPT-6.1 Luna","visibility":"hidden"},` +
	`{"slug":"gpt-6.1-codex","display_name":"GPT-6.1 Codex","visibility":"list"}` +
	`]}`

// recordedModelsRequest is what the reference server observed.
type recordedModelsRequest struct {
	method string
	path   string
	query  string
	header http.Header
}

// referenceModelsServer is the reference provider of the model inventory.
//
// It RECORDS the request it received, because two properties of this
// operation are properties of the TRAFFIC: the request must reach the
// documented public path, and it must carry the selected registration's
// bearer credential.
type referenceModelsServer struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []recordedModelsRequest
	status   int
	body     string
	location string
}

// newReferenceModelsServer starts the reference server.
func newReferenceModelsServer(t *testing.T) *referenceModelsServer {
	t.Helper()
	reference := &referenceModelsServer{}
	reference.server = httptest.NewServer(http.HandlerFunc(reference.route))
	t.Cleanup(reference.server.Close)
	return reference
}

// route answers the inventory request.
func (r *referenceModelsServer) route(w http.ResponseWriter, request *http.Request) {
	r.mu.Lock()
	r.requests = append(r.requests, recordedModelsRequest{
		method: request.Method,
		path:   request.URL.Path,
		query:  request.URL.RawQuery,
		header: request.Header.Clone(),
	})
	status := r.status
	body := r.body
	location := r.location
	r.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	if location != "" {
		w.Header().Set("Location", location)
		w.WriteHeader(status)
		return
	}
	if body == "" {
		body = defaultModelsDocument
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// redirect makes the server answer with a redirect to the given URL.
func (r *referenceModelsServer) redirect(target string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = http.StatusFound
	r.location = target
}

// answer replaces the answer of the reference server.
func (r *referenceModelsServer) answer(status int, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = status
	r.body = body
}

// requestCount returns how many requests the reference server received.
func (r *referenceModelsServer) requestCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// lastRequest returns the request the reference server received last.
func (r *referenceModelsServer) lastRequest(t *testing.T) recordedModelsRequest {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) == 0 {
		t.Fatal("the reference server received no request")
	}
	return r.requests[len(r.requests)-1]
}

// endpoint returns the documented public path on the reference server.
func (r *referenceModelsServer) endpoint() string {
	return r.server.URL + "/v1/models"
}

// storeRefresher answers with the record the STORE holds.
//
// It stands in for the renewal of a credential. The proofs are about WHICH
// token the connector presents, and the store is the authority for it: a
// refresher that answered with a value of its own would only prove that the
// client calls its collaborator.
type storeRefresher struct {
	store *ProfileStore
}

// Refresh answers with the stored record of the named registration.
func (r *storeRefresher) Refresh(_ context.Context, profileID string) (ProfileRecord, error) {
	return r.store.Load(profileID)
}

// newModelsAuthority returns the manager of a store that holds the named
// registrations.
//
// The authority is the REAL store and the REAL manager, so that a proof about
// the presented token is a proof about the store's own state.
func newModelsAuthority(t *testing.T, records ...ProfileRecord) *Manager {
	t.Helper()
	store := newTestProfileStore(t)
	for _, record := range records {
		saveTestProfile(t, store, record)
	}
	manager, err := NewManager(ManagerConfig{
		Profiles:  store,
		Refresher: &storeRefresher{store: store},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return manager
}

// newModelsClient returns a client that reads the reference server.
func newModelsClient(t *testing.T, reference *referenceModelsServer, authority SelectedTokenProvider) *ModelsClient {
	t.Helper()
	client, err := NewModelsClient(ModelsClientConfig{
		ModelsURL:  reference.endpoint(),
		HTTPClient: reference.server.Client(),
		Tokens:     authority,
	})
	if err != nil {
		t.Fatalf("NewModelsClient: %v", err)
	}
	return client
}

// TestModelsClient_RequestsThePublicModelsEndpoint proves the request the
// connector makes.
//
// The request must reach the documented public path, carry the selected
// registration's bearer credential and nothing else, and the answer must be
// the account's list-visible models.
func TestModelsClient_RequestsThePublicModelsEndpoint(t *testing.T) {
	reference := newReferenceModelsServer(t)
	record := testProfileRecord("acct-one")
	authority := newModelsAuthority(t, record)
	if err := authority.Select(context.Background(), "acct-one"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	client := newModelsClient(t, reference, authority)

	inventory, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if reference.requestCount() != 1 {
		t.Fatalf("the reference server received %d requests, want 1", reference.requestCount())
	}
	generation, ok := record.CredentialGeneration()
	if !ok || inventory.CredentialGeneration != generation {
		t.Error("the inventory does not carry the generation of the credential the request presented")
	}
	request := reference.lastRequest(t)
	if request.method != http.MethodGet {
		t.Errorf("the request method = %q, want %q", request.method, http.MethodGet)
	}
	if request.path != "/v1/models" {
		t.Errorf("the request path = %q, want the documented %q", request.path, "/v1/models")
	}
	if request.query != "" {
		t.Errorf("the request query = %q, want none", request.query)
	}
	values := request.header.Values("Authorization")
	if len(values) != 1 || values[0] != "Bearer "+testAccessToken {
		t.Errorf("the authorization header = %q, want exactly one %q", values, "Bearer "+testAccessToken)
	}
	// The request carries no Codex identity and no private header.
	for _, name := range []string{
		"originator", "chatgpt-account-id", "conversation_id", "session_id",
		"Codex-Task-Type", "version",
	} {
		if carried := request.header.Values(name); len(carried) != 0 {
			t.Errorf("the request carries the private header %q: %q", name, carried)
		}
	}
	if agent := request.header.Get("User-Agent"); !strings.Contains(agent, "AIProxer") {
		t.Errorf("the user agent = %q, want the truthful product identity", agent)
	}
	// The answer is the account's list-visible models, in the provider's
	// order.
	if inventory.ProfileID != "acct-one" {
		t.Errorf("the inventory names the registration %q, want %q", inventory.ProfileID, "acct-one")
	}
	want := []Model{
		{Slug: "gpt-6.1-sol", DisplayName: "GPT-6.1 Sol"},
		{Slug: "gpt-6.1-codex", DisplayName: "GPT-6.1 Codex"},
	}
	if len(inventory.Models) != len(want) {
		t.Fatalf("the inventory holds %d models, want %d", len(inventory.Models), len(want))
	}
	for index, model := range inventory.Models {
		if model != want[index] {
			t.Errorf("the model at %d = %+v, want %+v", index, model, want[index])
		}
	}
}

// TestModelsClient_UsesTheDocumentedPublicEndpointByDefault proves the
// default endpoint.
//
// The documentation names the endpoint exactly, and the default is what the
// connector uses when no other endpoint is configured.
func TestModelsClient_UsesTheDocumentedPublicEndpointByDefault(t *testing.T) {
	const want = "https://api.openai.com/v1/models"
	if DefaultModelsURL != want {
		t.Fatalf("the default inventory endpoint = %q, want %q", DefaultModelsURL, want)
	}
	parsed, err := url.Parse(DefaultModelsURL)
	if err != nil {
		t.Fatalf("the default inventory endpoint could not be read: %v", err)
	}
	if parsed.Scheme != "https" {
		t.Errorf("the default endpoint scheme = %q, want %q", parsed.Scheme, "https")
	}
	if parsed.Host != "api.openai.com" {
		t.Errorf("the default endpoint host = %q, want %q", parsed.Host, "api.openai.com")
	}
	if parsed.Path != "/v1/models" {
		t.Errorf("the default endpoint path = %q, want %q", parsed.Path, "/v1/models")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		t.Errorf("the default endpoint carries a query or a fragment: %q", DefaultModelsURL)
	}
	if parsed.User != nil {
		t.Error("the default endpoint carries credentials in its userinfo")
	}
	for _, forbidden := range []string{"backend-api", "chatgpt.com", "codex"} {
		if strings.Contains(strings.ToLower(DefaultModelsURL), forbidden) {
			t.Errorf("the default inventory endpoint names %q: %q", forbidden, DefaultModelsURL)
		}
	}
	client, err := NewModelsClient(ModelsClientConfig{Tokens: &staticAuthority{}})
	if err != nil {
		t.Fatalf("NewModelsClient: %v", err)
	}
	if client.url != DefaultModelsURL {
		t.Errorf("the client reads %q, want the documented %q", client.url, DefaultModelsURL)
	}
}

// staticAuthority is an authority that always answers with one registration.
//
// It serves the proofs that are about the request itself and not about the
// store.
type staticAuthority struct {
	profileID string
	token     string
}

// Selected answers with the prepared registration.
func (a *staticAuthority) Selected(context.Context) (string, error) {
	return a.profileID, nil
}

// Token answers with the prepared token.
func (a *staticAuthority) Token(context.Context, string) (TokenGrant, error) {
	return TokenGrant{AccessToken: a.token}, nil
}

// TestModelsClient_KeepsOnlyTheListVisibleModels proves the visibility
// filter.
//
// The documentation keeps the models "intended for display" and the example
// selects them with `select(.visibility == "list")`. A row the provider does
// not mark for display is not surfaced, and the provider's ordering is
// preserved.
func TestModelsClient_KeepsOnlyTheListVisibleModels(t *testing.T) {
	reference := newReferenceModelsServer(t)
	reference.answer(http.StatusOK, `{"models":[`+
		`{"slug":"model-visible","display_name":"Visible","visibility":"list"},`+
		`{"slug":"model-hidden","display_name":"Hidden","visibility":"hidden"},`+
		`{"slug":"model-internal","display_name":"Internal","visibility":"internal"},`+
		`{"slug":"model-empty","display_name":"Empty","visibility":""},`+
		`{"slug":"model-absent","display_name":"Absent"},`+
		`{"slug":"model-visible-two","visibility":"list"}`+
		`]}`)
	authority := newModelsAuthority(t, testProfileRecord("acct-one"))
	if err := authority.Select(context.Background(), "acct-one"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	client := newModelsClient(t, reference, authority)

	inventory, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []Model{
		{Slug: "model-visible", DisplayName: "Visible"},
		{Slug: "model-visible-two", DisplayName: ""},
	}
	if len(inventory.Models) != len(want) {
		t.Fatalf("the inventory holds %d models, want %d: %+v", len(inventory.Models), len(want), inventory.Models)
	}
	for index, model := range inventory.Models {
		if model != want[index] {
			t.Errorf("the model at %d = %+v, want %+v", index, model, want[index])
		}
	}
}

// TestModelsClient_TellsAnAbsentVisibilityMemberApartFromAnUnusableOne proves
// that the two states are not read alike.
//
// A row that carries NO visibility member is a row the provider did not mark
// for display: the member is ABSENT, and absence says nothing about the
// row. It is dropped by the filter.
//
// A row that carries a visibility member this build cannot read - a number,
// an object - is a row that ARRIVED and cannot be used. It is REFUSED, and
// it is never read as though it had not arrived: an omission is the
// permissive state, and reading an unusable value as an omission would turn
// it into one.
func TestModelsClient_TellsAnAbsentVisibilityMemberApartFromAnUnusableOne(t *testing.T) {
	cases := []struct {
		name      string
		row       string
		wantError bool
	}{
		{
			name: "an absent visibility member",
			row:  `{"slug":"model-absent","display_name":"Absent"}`,
		},
		{
			name:      "an unusable visibility member",
			row:       `{"slug":"model-unusable","display_name":"Unusable","visibility":7}`,
			wantError: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			reference := newReferenceModelsServer(t)
			reference.answer(http.StatusOK, `{"models":[`+testCase.row+`]}`)
			authority := newModelsAuthority(t, testProfileRecord("acct-one"))
			if err := authority.Select(context.Background(), "acct-one"); err != nil {
				t.Fatalf("Select: %v", err)
			}
			client := newModelsClient(t, reference, authority)

			inventory, err := client.List(context.Background())
			if testCase.wantError {
				if err == nil {
					t.Fatal("an unusable row was accepted")
				}
				if !errors.Is(err, ErrModelsMalformed) {
					t.Errorf("the failure = %v, want the unreadable-document failure", err)
				}
				if len(inventory.Models) != 0 {
					t.Errorf("the failure carried %d models, want none", len(inventory.Models))
				}
				return
			}
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(inventory.Models) != 0 {
				t.Errorf("the inventory holds %d models, want none", len(inventory.Models))
			}
		})
	}
}

// TestModelsClient_RefusesUnusableVisibleMetadata proves that a visible row
// whose payload this build cannot read is REFUSED.
//
// The SLUG of a visible row is its payload: it is the model id the next
// request passes, so a visible row without a usable slug - absent, another
// shape, or blank - is a row this build cannot represent. It is REFUSED,
// and it is never dropped: an omission is the permissive state, and a
// dropped row would turn a provider-side change into a quietly smaller
// catalog that still looks like a fetched one.
//
// The DISPLAY NAME of a visible row is OPTIONAL: the documentation says it
// is preserved "where supported", so an ABSENT display name is accepted
// and only an UNUSABLE one - a number, an object - is refused.
//
// The refusal refuses the WHOLE document: no partial catalog is returned,
// because a partial catalog reads as a catalog.
//
// Every document below carries a GOOD row as well, so that a refusal
// cannot be confused with a drop of the good row.
func TestModelsClient_RefusesUnusableVisibleMetadata(t *testing.T) {
	const goodRow = `{"slug":"gpt-6.1-sol","display_name":"GPT-6.1 Sol","visibility":"list"}`
	cases := []struct {
		name        string
		row         string
		wantRefusal bool
	}{
		{
			name:        "a visible row without a slug",
			row:         `{"display_name":"No Slug","visibility":"list"}`,
			wantRefusal: true,
		},
		{
			name:        "a visible row whose slug is a number",
			row:         `{"slug":7,"display_name":"Number","visibility":"list"}`,
			wantRefusal: true,
		},
		{
			name:        "a visible row whose slug is an object",
			row:         `{"slug":{"id":"gpt-6.1-sol"},"display_name":"Object","visibility":"list"}`,
			wantRefusal: true,
		},
		{
			name:        "a visible row whose slug is blank",
			row:         `{"slug":"   ","display_name":"Blank","visibility":"list"}`,
			wantRefusal: true,
		},
		{
			name:        "a visible row whose display name is a number",
			row:         `{"slug":"gpt-6.1-luna","display_name":7,"visibility":"list"}`,
			wantRefusal: true,
		},
		{
			name:        "a visible row whose display name is an object",
			row:         `{"slug":"gpt-6.1-luna","display_name":{"text":"Luna"},"visibility":"list"}`,
			wantRefusal: true,
		},
		{
			name: "a visible row without a display name",
			row:  `{"slug":"gpt-6.1-luna","visibility":"list"}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			reference := newReferenceModelsServer(t)
			reference.answer(http.StatusOK, `{"models":[`+goodRow+`,`+testCase.row+`]}`)
			authority := newModelsAuthority(t, testProfileRecord("acct-one"))
			if err := authority.Select(context.Background(), "acct-one"); err != nil {
				t.Fatalf("Select: %v", err)
			}
			client := newModelsClient(t, reference, authority)

			inventory, err := client.List(context.Background())
			if testCase.wantRefusal {
				if err == nil {
					t.Fatal("an unusable visible row was accepted")
				}
				if !errors.Is(err, ErrModelsMalformed) {
					t.Errorf("the failure = %v, want the unreadable-document failure", err)
				}
				if len(inventory.Models) != 0 {
					t.Errorf(
						"the failure carried %d models, want none: %+v",
						len(inventory.Models), inventory.Models,
					)
				}
				return
			}
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			want := []Model{
				{Slug: "gpt-6.1-sol", DisplayName: "GPT-6.1 Sol"},
				{Slug: "gpt-6.1-luna"},
			}
			if len(inventory.Models) != len(want) {
				t.Fatalf(
					"the inventory holds %d models, want %d: %+v",
					len(inventory.Models), len(want), inventory.Models,
				)
			}
			for index, model := range inventory.Models {
				if model != want[index] {
					t.Errorf("the model at %d = %+v, want %+v", index, model, want[index])
				}
			}
		})
	}
}

// TestModelsClient_RefusesAnOversizeDocument proves the bound.
//
// The body is read one byte past the bound, so an oversize document is
// DETECTED instead of silently truncated: a truncated prefix is not a
// smaller document. The failure is a CONSTANT one - a document is untrusted
// input, and a diagnostic that repeated a part of it would put unvetted
// text into an operator-facing error.
func TestModelsClient_RefusesAnOversizeDocument(t *testing.T) {
	reference := newReferenceModelsServer(t)
	// The document is VALID and larger than the bound, so the refusal
	// comes from the bound and not from a parse failure.
	oversize := `{"models":[{"slug":"gpt-6.1-sol","display_name":"` +
		strings.Repeat("a", maxModelsResponseBytes) + `","visibility":"list"}]}`
	reference.answer(http.StatusOK, oversize)
	authority := newModelsAuthority(t, testProfileRecord("acct-one"))
	if err := authority.Select(context.Background(), "acct-one"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	client := newModelsClient(t, reference, authority)

	_, err := client.List(context.Background())
	if err == nil {
		t.Fatal("an oversize document was accepted")
	}
	if !errors.Is(err, ErrModelsTooLarge) {
		t.Errorf("the failure = %v, want the oversize failure", err)
	}
	if strings.Contains(err.Error(), strings.Repeat("a", 32)) {
		t.Error("the failure carries a part of the document")
	}
}

// wantModelsBodyBytes is the documented byte budget of ONE answer.
//
// The number is written out HERE, on purpose: the proof compares the bytes
// the client actually consumed with a number the TEST owns, so a bound
// that grew together with the code could not grow the claim with it. The
// budget is the document bound (maxModelsResponseBytes) plus the one byte
// that detects an excess.
const wantModelsBodyBytes = 1<<20 + 1

// countingBody is a FINITE answer that reports how many bytes the client
// consumed.
//
// The answer is GENERATED and not stored, and it is several times the
// budget: a client that reads it whole spends several budgets, and a
// client that stops at the budget does not. The count is the number of
// bytes the client actually READ, which is the number the resource-safety
// claim is about.
type countingBody struct {
	remaining int
	consumed  int64
}

// Read hands out the next part of the answer.
func (b *countingBody) Read(buffer []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	size := min(len(buffer), b.remaining)
	for index := range size {
		buffer[index] = 'a'
	}
	b.remaining -= size
	b.consumed += int64(size)
	return size, nil
}

// Close ends the answer.
func (b *countingBody) Close() error { return nil }

// consumedBytes returns how many bytes the client read.
func (b *countingBody) consumedBytes() int64 { return b.consumed }

// countingTransport answers every request with a counting body.
//
// The reference server cannot report what the client CONSUMED - it can
// only report what it wrote, and the whole answer is written either way -
// and the claim under test is about the READING side. The transport
// therefore owns the body, and the body counts.
type countingTransport struct {
	status int
	body   *countingBody
}

// RoundTrip answers the request with the counting body.
func (t *countingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode:    t.status,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          t.body,
		ContentLength: -1,
		Request:       request,
	}, nil
}

// newCountingModelsClient returns a client that reads a counting answer.
//
// The endpoint is never dialled - the transport answers it - but it is a
// documented-shaped absolute HTTPS one, because the proof runs the real
// constructor and the constructor refuses a plaintext source.
func newCountingModelsClient(t *testing.T, status, size int) (*ModelsClient, *countingBody) {
	t.Helper()
	body := &countingBody{remaining: size}
	transport := &countingTransport{status: status, body: body}
	client, err := NewModelsClient(ModelsClientConfig{
		ModelsURL:  "https://models.invalid/v1/models",
		HTTPClient: &http.Client{Transport: transport},
		Tokens:     &staticAuthority{profileID: "acct-one", token: testAccessToken},
	})
	if err != nil {
		t.Fatalf("NewModelsClient: %v", err)
	}
	return client, body
}

// TestModelsClient_BoundsTheBytesOneAnswerCosts proves that the connector
// reads a BOUNDED number of bytes from one answer.
//
// The refusal of an oversize document is proved by another test; this
// proof is about the READING that happens BEFORE the refusal. A bound that
// is applied only AFTER the read does not bound what the read already
// spent, so the proof measures the bytes the client consumed and compares
// them with the documented budget.
//
// The measurement does not depend on the failure the connector reports: an
// answer that is read whole and refused afterwards costs the same budget
// as one that is refused while it is read, and the difference between the
// two is exactly the defect this proof exists to catch.
func TestModelsClient_BoundsTheBytesOneAnswerCosts(t *testing.T) {
	// The answer is FINITE and four times the budget, so a client that
	// reads it whole spends four budgets.
	client, body := newCountingModelsClient(t, http.StatusOK, 4*wantModelsBodyBytes)

	_, err := client.List(context.Background())
	if err == nil {
		t.Fatal("an oversize answer was accepted")
	}
	if consumed := body.consumedBytes(); consumed > wantModelsBodyBytes {
		t.Fatalf(
			"the client consumed %d bytes of the answer, want at most %d",
			consumed, wantModelsBodyBytes,
		)
	}
}

// TestModelsClient_DoesNotReadTheBodyOfARefusedAnswer proves that a refused
// answer is not read.
//
// The body of a refused answer is untrusted input the connector has no use
// for: the failure names the status and no part of the document. The
// answer is therefore CLOSED and not read, and the proof measures the
// bytes the client consumed instead of trusting the comment that says so.
func TestModelsClient_DoesNotReadTheBodyOfARefusedAnswer(t *testing.T) {
	client, body := newCountingModelsClient(t, http.StatusUnauthorized, 4096)

	_, err := client.List(context.Background())
	if err == nil {
		t.Fatal("a refused answer was accepted")
	}
	if !errors.Is(err, ErrModelsUnavailable) {
		t.Errorf("the failure = %v, want the unavailable failure", err)
	}
	if consumed := body.consumedBytes(); consumed != 0 {
		t.Errorf("the client consumed %d bytes of the refused answer, want none", consumed)
	}
}

// TestModelsClient_TakesTheTokenFromTheSelectedRegistration proves that the
// credential comes from the AUTHORITY.
//
// The selection is the operator's, and the store is the authority for the
// credential of the selected registration. A client that carried a copy of a
// token would present a credential the store may already have replaced, and
// it would present the WRONG account's catalog.
func TestModelsClient_TakesTheTokenFromTheSelectedRegistration(t *testing.T) {
	reference := newReferenceModelsServer(t)
	first := testProfileRecord("acct-one")
	second := testProfileRecord("acct-two")
	second.AccessToken = "second-access-token-material"
	authority := newModelsAuthority(t, first, second)
	client := newModelsClient(t, reference, authority)

	if err := authority.Select(context.Background(), "acct-one"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	inventory, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if inventory.ProfileID != "acct-one" {
		t.Errorf("the inventory names the registration %q, want %q", inventory.ProfileID, "acct-one")
	}
	request := reference.lastRequest(t)
	if got := request.header.Get("Authorization"); got != "Bearer "+first.AccessToken {
		t.Errorf("the authorization header = %q, want the selected registration's token", got)
	}

	// The operator selects the other registration, and the next read
	// presents THAT registration's credential.
	if err := authority.Select(context.Background(), "acct-two"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	inventory, err = client.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if inventory.ProfileID != "acct-two" {
		t.Errorf("the inventory names the registration %q, want %q", inventory.ProfileID, "acct-two")
	}
	request = reference.lastRequest(t)
	if got := request.header.Get("Authorization"); got != "Bearer "+second.AccessToken {
		t.Errorf("the authorization header = %q, want the newly selected registration's token", got)
	}
}

// TestModelsClient_RefusesARepeatedMember proves that a repeated member does
// not silently win.
//
// The decoder keeps the LAST value of a repeated member, so a document that
// repeats "visibility" would decide by itself whether the row is shown. The
// document is either understood in full or refused.
func TestModelsClient_RefusesARepeatedMember(t *testing.T) {
	reference := newReferenceModelsServer(t)
	reference.answer(http.StatusOK, `{"models":[`+
		`{"slug":"gpt-6.1-sol","visibility":"list","visibility":"hidden"}`+
		`]}`)
	authority := newModelsAuthority(t, testProfileRecord("acct-one"))
	if err := authority.Select(context.Background(), "acct-one"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	client := newModelsClient(t, reference, authority)

	inventory, err := client.List(context.Background())
	if err == nil {
		t.Fatal("a document that repeats a member was accepted")
	}
	if !errors.Is(err, ErrModelsMalformed) {
		t.Errorf("the failure = %v, want the unreadable-document failure", err)
	}
	if len(inventory.Models) != 0 {
		t.Errorf("the failure carried %d models, want none", len(inventory.Models))
	}
}

// TestModelsClient_RefusesADocumentWithoutTheModelsArray proves that an
// unreadable document is not an empty catalog.
//
// The host reads a zero-model success as a fetched catalog that found no
// models, so a document this build cannot read must travel the error path.
func TestModelsClient_RefusesADocumentWithoutTheModelsArray(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "no models member", body: `{"object":"list"}`},
		{name: "a models member that is not an array", body: `{"models":{"slug":"gpt-6.1-sol"}}`},
		{name: "a document that is not an object", body: `["gpt-6.1-sol"]`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			reference := newReferenceModelsServer(t)
			reference.answer(http.StatusOK, testCase.body)
			authority := newModelsAuthority(t, testProfileRecord("acct-one"))
			if err := authority.Select(context.Background(), "acct-one"); err != nil {
				t.Fatalf("Select: %v", err)
			}
			client := newModelsClient(t, reference, authority)

			inventory, err := client.List(context.Background())
			if err == nil {
				t.Fatal("an unreadable document was accepted")
			}
			if !errors.Is(err, ErrModelsMalformed) {
				t.Errorf("the failure = %v, want the unreadable-document failure", err)
			}
			if len(inventory.Models) != 0 {
				t.Errorf("the failure carried %d models, want none", len(inventory.Models))
			}
		})
	}
}

// TestModelsClient_RefusesAProviderFailure proves that a refused request is
// an explicit failure.
//
// The provider's body is NOT read: it is untrusted input, and the failure
// names no part of it.
func TestModelsClient_RefusesAProviderFailure(t *testing.T) {
	reference := newReferenceModelsServer(t)
	reference.answer(http.StatusUnauthorized, `{"error":{"message":"the-secret-marker"}}`)
	authority := newModelsAuthority(t, testProfileRecord("acct-one"))
	if err := authority.Select(context.Background(), "acct-one"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	client := newModelsClient(t, reference, authority)

	_, err := client.List(context.Background())
	if err == nil {
		t.Fatal("a refused request was accepted")
	}
	if !errors.Is(err, ErrModelsUnavailable) {
		t.Errorf("the failure = %v, want the unavailable failure", err)
	}
	if strings.Contains(err.Error(), "the-secret-marker") {
		t.Error("the failure carries a part of the provider's answer")
	}
	if strings.Contains(err.Error(), testAccessToken) {
		t.Error("the failure carries the access token")
	}
}

// TestModelsClient_DoesNotFollowARedirect proves that the credential is not
// presented to a destination of the provider's choosing.
//
// The request carries the account's bearer credential, and a redirect would
// let the provider send it somewhere else. The redirect is therefore not
// followed, and the redirected answer is a failure.
func TestModelsClient_DoesNotFollowARedirect(t *testing.T) {
	target := newReferenceModelsServer(t)
	reference := newReferenceModelsServer(t)
	reference.redirect(target.server.URL + "/v1/models")
	authority := newModelsAuthority(t, testProfileRecord("acct-one"))
	if err := authority.Select(context.Background(), "acct-one"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	client := newModelsClient(t, reference, authority)

	_, err := client.List(context.Background())
	if err == nil {
		t.Fatal("a redirected request was accepted")
	}
	if !errors.Is(err, ErrModelsUnavailable) {
		t.Errorf("the failure = %v, want the unavailable failure", err)
	}
	if target.requestCount() != 0 {
		t.Errorf("the redirect target received %d requests, want none", target.requestCount())
	}
}

// TestModelsClient_NeedsASelectedRegistration proves that the inventory
// belongs to a registration.
//
// No registration is selected, so there is no credential to present and no
// account whose catalog could be read. The read fails with the DOCUMENTED
// no-selection failure - a caller tells "nothing is selected" from "the read
// failed" by the failure itself - and the provider is not asked.
func TestModelsClient_NeedsASelectedRegistration(t *testing.T) {
	reference := newReferenceModelsServer(t)
	authority := newModelsAuthority(t, testProfileRecord("acct-one"))
	client := newModelsClient(t, reference, authority)

	_, err := client.List(context.Background())
	if err == nil {
		t.Fatal("an inventory was read without a selected registration")
	}
	if !errors.Is(err, ErrModelsNoSelection) {
		t.Errorf("the failure = %v, want the documented no-selection failure", err)
	}
	if reference.requestCount() != 0 {
		t.Errorf("the reference server received %d requests, want none", reference.requestCount())
	}
}

// TestModelsClient_RefusesAPlaintextEndpoint proves that the credential is
// not sent in the clear.
//
// The request carries the account's bearer credential, so the source must
// be authenticated. The loopback address is the one exception, and it is
// the local test server.
func TestModelsClient_RefusesAPlaintextEndpoint(t *testing.T) {
	_, err := NewModelsClient(ModelsClientConfig{
		ModelsURL: "http://api.openai.com/v1/models",
		Tokens:    &staticAuthority{},
	})
	if err == nil {
		t.Fatal("a plaintext inventory endpoint was accepted")
	}
}
