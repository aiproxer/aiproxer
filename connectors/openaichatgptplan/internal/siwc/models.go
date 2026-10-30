package siwc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// The public model inventory of the selected registration.
//
// # The endpoint is the documented public one
//
// The OpenAI documentation for the ChatGPT plan usage flow names the
// endpoint, the credential and the shape of the answer in one example:
//
//	curl --fail-with-body --silent --show-error \
//	  https://api.openai.com/v1/models \
//	  -H "Authorization: Bearer $ACCESS_TOKEN" \
//	  | jq '[.models[] | select(.visibility == "list") | {slug, display_name}]'
//
// This file relies on exactly that sentence:
//
//   - the path is /v1/models on the public API host, and it is NOT a
//     ChatGPT backend-api route and NOT a Codex private route;
//   - the credential is ordinary bearer authorization;
//   - the answer carries a `models` array, and the rows the provider means
//     to be shown carry the visibility "list" together with a `slug` and a
//     `display_name`.
//
// # The inventory belongs to a registration
//
// The request presents the access token of the EXPLICITLY SELECTED
// registration, and the inventory is the one of that registration. The
// client holds no token of its own: it asks the authority which
// registration is selected and for that registration's token, so a
// credential the store has already replaced is never presented, and one
// account's catalog is never read with another account's credential.
//
// This file is the client and the parser. The profile-aware cache, the
// invalidation on a profile switch and the host-facing inventory contract
// live one layer up, in internal/service/inventory.go, on the ListFor seam:
// that layer reads the selected registration itself, checks the identity the
// cache entry describes, and then asks this client for exactly that
// registration's catalog.

const (
	// DefaultModelsURL is the public model inventory endpoint of the OpenAI
	// API.
	//
	// It is the endpoint the documentation names for the ChatGPT plan
	// flow, and it is PUBLIC: it is not a ChatGPT backend-api route and it
	// is not a Codex private route.
	DefaultModelsURL = "https://api.openai.com/v1/models"

	// maxModelsResponseBytes bounds the inventory document.
	//
	// The document is a list of a few dozen model entries, and the bound
	// is generous for it. The bound exists so that a provider that answers
	// with an endless body cannot exhaust the connector's memory.
	maxModelsResponseBytes = 1 << 20

	// maxModelsBodyBytes bounds the bytes ONE answer may cost the
	// connector, whatever the answer is.
	//
	// The budget is the document bound plus the ONE byte that detects an
	// excess, and it is the WHOLE budget: the connector reads the
	// document, and it never reads past it. A refused answer is not read
	// at all, so the budget holds for a failure as well.
	maxModelsBodyBytes = maxModelsResponseBytes + 1

	// modelsMember is the member that carries the rows.
	modelsMember = "models"

	// modelsSlugMember is the member that carries a row's model id.
	modelsSlugMember = "slug"

	// modelsDisplayNameMember is the member that carries a row's display
	// label.
	modelsDisplayNameMember = "display_name"

	// modelsVisibilityMember is the member that carries a row's
	// visibility.
	modelsVisibilityMember = "visibility"

	// listVisibility is the visibility a row carries when the provider
	// means it to be shown.
	//
	// The documentation keeps the models "intended for display" and its
	// example selects them with `select(.visibility == "list")`.
	listVisibility = "list"

	// modelsUserAgent identifies this connector truthfully.
	//
	// The inventory request is an ordinary public API request. It carries
	// no Codex identity: the product that makes it names itself.
	//
	// The value is the connector's one product identity. The Responses
	// projection carries its own constant because the connector's wire
	// packages stay independent of each other.
	modelsUserAgent = "AIProxer"
)

// The failures of the model inventory.
//
// They are CONSTANT and value-free: the inventory document is untrusted
// input, and a diagnostic that repeated a part of it would put unvetted text
// into an operator- or support-facing error.
var (
	// ErrModelsUnavailable reports an inventory that could not be read.
	//
	// It covers the request that never produced an answer, the answer the
	// provider refused, and the reading that failed. The status the
	// provider sent, when it sent one, is carried by the ModelsError that
	// wraps this failure.
	ErrModelsUnavailable = errors.New("siwc: the model inventory could not be read")

	// ErrModelsTooLarge reports an inventory document that exceeds its
	// bound.
	ErrModelsTooLarge = errors.New("siwc: the model inventory document exceeds its bound")

	// ErrModelsMalformed reports an inventory document this build cannot
	// read.
	ErrModelsMalformed = errors.New("siwc: the model inventory document could not be read")

	// ErrModelsNoSelection reports that no registration is selected.
	//
	// The inventory belongs to a registration, and an inventory without
	// one would be an inventory of an account nobody chose.
	ErrModelsNoSelection = errors.New("siwc: the model inventory needs a selected registration")
)

// ModelsError is a refused model inventory read.
//
// The failure carries the upstream HTTP status when the provider answered,
// and it never carries any part of the provider's document: the document is
// untrusted input, and a diagnostic that repeated it would put unvetted text
// into an operator- or support-facing error.
type ModelsError struct {
	// Status is the upstream HTTP status, or 0 when the request never
	// produced an answer.
	Status int
	// Err is the classified failure.
	Err error
}

// Error returns the classified message.
func (e *ModelsError) Error() string {
	if e == nil || e.Err == nil {
		return ErrModelsUnavailable.Error()
	}
	return e.Err.Error()
}

// Unwrap returns the classified failure, so that errors.Is finds it.
func (e *ModelsError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Model is one list-visible model of a registration's catalog.
//
// The SLUG is the provider's native model id: it is what the next request
// passes as its model, and it is preserved exactly as the provider wrote
// it. The DISPLAY NAME is the provider's own label, and it is empty when
// the provider publishes no label.
type Model struct {
	// Slug is the provider's native model id.
	Slug string
	// DisplayName is the provider's display label for the model.
	DisplayName string
}

// ModelInventory is the list-visible catalog of one registration.
//
// The PROFILE ID is the local, non-secret key of the registration the
// catalog belongs to. The CREDENTIAL GENERATION is the one-way fingerprint of
// the credential pair that authorized the read: it is the provenance of the
// answer, not a copy of the credential. The MODELS are the rows the provider
// marked for display, in the provider's own order.
type ModelInventory struct {
	// ProfileID is the local, non-secret id of the registration.
	ProfileID string
	// CredentialGeneration is the one-way fingerprint of the exact
	// credential pair the request presented. A caller that caches the
	// catalog stamps it with THIS generation, never with a generation it
	// observes after the answer arrived.
	CredentialGeneration [sha256.Size]byte
	// Models are the list-visible models, in the provider's order.
	Models []Model
}

// TokenGrant is one access-token acquisition of a registration.
//
// The token is the bearer credential the request presents. The GENERATION is
// the one-way fingerprint of the credential pair the token was acquired from;
// it is non-secret, so it may travel with the token through the catalog seam
// without becoming a second copy of a credential.
type TokenGrant struct {
	// AccessToken is the bearer token the request presents.
	AccessToken string
	// Generation is the one-way fingerprint of the credential pair the
	// token was acquired from.
	Generation [sha256.Size]byte
}

// SelectedTokenProvider hands out the access token of the explicitly
// selected registration.
//
// The interface is the seam between the public transport and the local
// registration state. The transport asks the authority which registration is
// selected and for that registration's token, and it never carries a token
// of its own: a copy of a credential is a credential the authority may
// already have replaced.
//
// The grant also carries the credential GENERATION the token was acquired
// from. The transport returns it with the catalog, so a caller that caches the
// answer can prove which credential authorized the request instead of
// inferring it from the registration state after the fact.
//
// The manager of this package implements it.
type SelectedTokenProvider interface {
	// Selected returns the id of the explicitly selected registration. An
	// empty id with no error means that no registration is selected yet.
	Selected(ctx context.Context) (string, error)
	// Token returns a usable access token of the named registration,
	// together with the generation it was acquired from. The token is
	// renewed when it is near its expiry, and the renewal is serialized per
	// registration.
	Token(ctx context.Context, profileID string) (TokenGrant, error)
}

// ModelsClientConfig is the configuration of the model inventory client.
type ModelsClientConfig struct {
	// ModelsURL is the inventory endpoint. Empty means DefaultModelsURL.
	ModelsURL string
	// HTTPClient performs the request. Empty means a client with a
	// bounded timeout.
	//
	// A SUPPLIED client keeps its own timeout, and the CALLER owns it: a
	// client with no timeout and a context with no deadline leaves the
	// request without a wall-clock bound, and this client adds none. The
	// bound this client enforces on its own is the BODY: it reads at most
	// maxModelsBodyBytes of any answer, and a refused answer is not read
	// at all. A caller that supplies a client bounds the request itself,
	// with the client's timeout or with the context's deadline.
	HTTPClient *http.Client
	// Tokens hands out the access token of the selected registration. It
	// is required: a client that cannot reach the registrations it
	// inventories would read an account nobody chose.
	Tokens SelectedTokenProvider
}

// ModelsClient reads the public model inventory of the selected
// registration.
//
// The client is safe for concurrent use: it holds no state of its own, and
// the state it reads belongs to the authority.
type ModelsClient struct {
	url        string
	httpClient *http.Client
	tokens     SelectedTokenProvider
}

// NewModelsClient returns the client of the public model inventory.
//
// The endpoint must be an absolute URL, and it must be authenticated: the
// request carries the account's bearer credential, so a plaintext source
// would publish that credential to everyone on the path. The loopback
// address is the one exception, and it is the local test server.
func NewModelsClient(config ModelsClientConfig) (*ModelsClient, error) {
	endpoint := strings.TrimSpace(config.ModelsURL)
	if endpoint == "" {
		endpoint = DefaultModelsURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return nil, errors.New("siwc: the model inventory URL must be an absolute URL")
	}
	if err := requireSecureURL(
		endpoint,
		"the model inventory URL must use HTTPS: a plaintext source would publish the access token",
	); err != nil {
		return nil, err
	}
	if config.Tokens == nil {
		return nil, errors.New("siwc: the model inventory needs the registration authority")
	}
	return &ModelsClient{
		url:        endpoint,
		httpClient: oidcHTTPClient(config.HTTPClient),
		tokens:     config.Tokens,
	}, nil
}

// List returns the list-visible models of the selected registration.
//
// The call is ACCOUNT-SCOPED: the credential is the one of the explicitly
// selected registration, and the request is the documented public endpoint.
// The provider's own ordering is preserved, and the rows the provider does
// not mark for display are not surfaced.
//
// A failure is always explicit. The client never answers with an empty
// catalog it did not read: the host reads a zero-model success as a fetched
// catalog that found no models, and the operator could not tell a
// provider-side change from an account without models.
func (c *ModelsClient) List(ctx context.Context) (ModelInventory, error) {
	if err := c.ready(ctx); err != nil {
		return ModelInventory{}, err
	}
	profileID, err := c.tokens.Selected(ctx)
	if err != nil {
		return ModelInventory{}, fmt.Errorf(
			"siwc: the model inventory needs a selected registration: %w", err,
		)
	}
	if strings.TrimSpace(profileID) == "" {
		return ModelInventory{}, ErrModelsNoSelection
	}
	return c.ListFor(ctx, profileID)
}

// ListFor returns the list-visible models of the NAMED registration.
//
// The caller names the registration when it has already decided whose catalog
// the read is about. The account-scoped inventory cache of the connector reads
// the selected registration, checks that the entry it may serve describes
// that registration, and only then asks for exactly that registration's
// catalog. A transport that re-read the selection here could answer with
// another account's catalog between that check and the request, which is the
// cross-registration boundary the inventory must not cross.
//
// The credential is still taken from the authority for the named
// registration: the caller names WHOSE catalog is read, and the authority
// decides which token that registration may use now. The returned inventory
// carries the named profile id and the credential generation the request was
// authorized with, so a caller can confirm both that the answer describes the
// registration it asked about and which generation produced it.
func (c *ModelsClient) ListFor(ctx context.Context, profileID string) (ModelInventory, error) {
	if err := c.ready(ctx); err != nil {
		return ModelInventory{}, err
	}
	id := strings.TrimSpace(profileID)
	if id == "" {
		return ModelInventory{}, ErrModelsNoSelection
	}
	grant, err := c.tokens.Token(ctx, id)
	if err != nil {
		return ModelInventory{}, fmt.Errorf(
			"siwc: the model inventory needs the access token of the selected registration: %w", err,
		)
	}
	token := strings.TrimSpace(grant.AccessToken)
	if token == "" {
		return ModelInventory{}, &ModelsError{Err: ErrModelsUnavailable}
	}
	models, err := c.fetch(ctx, token)
	if err != nil {
		return ModelInventory{}, err
	}
	return ModelInventory{
		ProfileID:            id,
		CredentialGeneration: grant.Generation,
		Models:               models,
	}, nil
}

// ready reports whether the client can read an inventory at all.
func (c *ModelsClient) ready(ctx context.Context) error {
	if ctx == nil {
		return errors.New("siwc: the model inventory needs a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.tokens == nil {
		return errors.New("siwc: the model inventory has no registration authority")
	}
	return nil
}

// fetch performs the inventory request and reads the document.
//
// The request is the documented one: a GET of the public endpoint with the
// access token as ordinary bearer authorization. It carries no other
// credential, and it identifies the product that makes it.
func (c *ModelsClient) fetch(ctx context.Context, token string) ([]Model, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, &ModelsError{Err: ErrModelsUnavailable}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", modelsUserAgent)
	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &ModelsError{Err: ErrModelsUnavailable}
	}
	// The body is CLOSED and not drained. A refused answer is untrusted
	// input the connector has no use for, and draining it would spend the
	// connector's byte budget on a document the connector refuses. The
	// connection is given up instead.
	defer func() {
		_ = response.Body.Close()
	}()
	// The answer is OBSERVED, not assumed: a status other than the
	// successful one is a failure, and the provider's body is not read at
	// all. The body is untrusted input, and the failure names no part of
	// it.
	if response.StatusCode != http.StatusOK {
		return nil, &ModelsError{Status: response.StatusCode, Err: ErrModelsUnavailable}
	}
	// The body is read within the byte budget of ONE answer, so that a
	// provider that answers with an endless body cannot exhaust the
	// connector's memory. The budget is the document bound plus the one
	// byte that detects an excess, so a document that EXCEEDS the bound
	// is DETECTED instead of silently truncated. A truncated prefix is
	// not a smaller document: a valid JSON object followed by more content
	// truncates into a COMPLETE and VALID prefix, and decoding that
	// prefix would accept a document the connector never read whole. The
	// oversize answer is therefore refused BEFORE anything is decoded,
	// with a constant failure that names no part of it.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxModelsBodyBytes))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &ModelsError{Status: response.StatusCode, Err: ErrModelsUnavailable}
	}
	if len(body) > maxModelsResponseBytes {
		return nil, &ModelsError{Status: response.StatusCode, Err: ErrModelsTooLarge}
	}
	models, err := parseModelInventory(body)
	if err != nil {
		return nil, &ModelsError{Status: response.StatusCode, Err: err}
	}
	return models, nil
}

// parseModelInventory reads the inventory document.
//
// The document is read with the same strict reader the auth domain uses: the
// members are read by their EXACT names, a document that REPEATS a member is
// refused, and a member that ARRIVED with a shape this build cannot read
// makes the document unreadable.
//
// The rows are COLLECTED, not sampled: every row of the array is read, in
// the provider's order. The rows the provider does not want shown are
// dropped by the documented visibility filter, and never by a rule of this
// build.
func parseModelInventory(body []byte) ([]Model, error) {
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return nil, ErrModelsMalformed
	}
	members, err := jsonMembers(body)
	if err != nil {
		return nil, ErrModelsMalformed
	}
	rows := memberArray(members, modelsMember)
	switch rows.State {
	case memberAbsent:
		// The documentation says the answer carries a models array. A
		// document without one is not an empty catalog: it is a document
		// this build cannot read, and reading it as an empty catalog would
		// hide a provider-side change behind a successful empty answer.
		return nil, ErrModelsMalformed
	case memberInvalid:
		return nil, ErrModelsMalformed
	}
	models := make([]Model, 0, len(rows.Value))
	for _, raw := range rows.Value {
		model, visible, err := parseModelRow(raw)
		if err != nil {
			return nil, err
		}
		if !visible {
			continue
		}
		models = append(models, model)
	}
	return models, nil
}

// parseModelRow reads one row of the inventory array.
//
// The row answers two questions: whether the provider wants it SHOWN, and
// what the model is called.
//
// The VISIBILITY member answers the first. A row that carries no visibility
// member is a row the provider did not mark for display: the member is
// ABSENT, and absence says nothing about the row, so the row is dropped by
// the filter. A row that carries a visibility member this build cannot read
// - a number, an object - is a row that ARRIVED and cannot be used: it is
// REFUSED, because reading it as though it had not arrived would turn an
// unusable value into an omission, and an omission is the permissive state.
//
// The SLUG answers the second, and it is REQUIRED once the row is visible:
// the slug is the model id the next request passes, so a visible row
// without one is a row this build cannot represent. Unlike the visibility
// member, the slug is not a filter input but the payload itself, and its
// absence is therefore a refusal and not a drop.
//
// The DISPLAY NAME is preserved as the provider wrote it, and its absence
// is not a refusal: the documentation says the display name is preserved
// "where supported".
func parseModelRow(raw json.RawMessage) (Model, bool, error) {
	members, err := jsonMembers(raw)
	if err != nil {
		return Model{}, false, ErrModelsMalformed
	}
	visibility := memberString(members, modelsVisibilityMember)
	switch visibility.State {
	case memberInvalid:
		return Model{}, false, ErrModelsMalformed
	case memberAbsent:
		return Model{}, false, nil
	}
	if visibility.Value != listVisibility {
		return Model{}, false, nil
	}
	slug := memberString(members, modelsSlugMember)
	if slug.State != memberUsable || strings.TrimSpace(slug.Value) == "" {
		return Model{}, false, ErrModelsMalformed
	}
	displayName := memberString(members, modelsDisplayNameMember)
	if displayName.State == memberInvalid {
		return Model{}, false, ErrModelsMalformed
	}
	return Model{Slug: slug.Value, DisplayName: displayName.Value}, true, nil
}
