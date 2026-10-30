package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// Tests for the account-scoped, profile-aware model inventory.
//
// The contract under test is requirements 7.4, 7.5, 7.6 and the inventory
// half of task 3.2:
//
//   - the catalog belongs to the SELECTED registration, and one profile's
//     catalog is never served for another;
//   - the cache is keyed by the registration it describes and is invalidated
//     when that registration changes identity, is cleared or is removed;
//   - a missing, unusable or moved selection is an explicit failure, never an
//     empty catalog and never a stale cache entry;
//   - the answer is explicitly marked when it comes from the cache.
//
// Two kinds of proof are used deliberately. The isolation proof runs the REAL
// store, manager, inventory client and HTTP endpoint, so the tokens and the
// catalogs cross the wire of the connector that ships. The policy proofs use a
// registration authority and a catalog double, so that a state the real
// operations cannot easily reach - a selection that moves during a read, a
// provider that fails after a catalog was cached - can be reached exactly.

// The model documents of the reference inventory server.
const (
	documentProfileA = `{"models":[` +
		`{"slug":"a-one","display_name":"A One","visibility":"list"},` +
		`{"slug":"a-two","display_name":"A Two","visibility":"list"}` +
		`]}`
	documentProfileB = `{"models":[` +
		`{"slug":"b-one","display_name":"B One","visibility":"list"}` +
		`]}`
	documentReplacement = `{"models":[` +
		`{"slug":"replacement-one","display_name":"Replacement One","visibility":"list"}` +
		`]}`
	documentAfterLogout = `{"models":[` +
		`{"slug":"after-logout","display_name":"After Logout","visibility":"list"}` +
		`]}`
)

// inventoryRequest is one request the reference inventory server received.
type inventoryRequest struct {
	method string
	path   string
	query  string
	token  string
}

// referenceInventoryServer answers the public model inventory from a table of
// documents per bearer token.
//
// It RECORDS the requests, because the two properties under test are
// properties of the traffic: the catalog is read with the selected
// registration's credential, and a cache hit must not produce a request.
type referenceInventoryServer struct {
	server *httptest.Server

	mu        sync.Mutex
	documents map[string]string
	fallback  string
	status    int
	requests  []inventoryRequest
	onRequest func(token string)
}

// newReferenceInventoryServer starts the reference inventory server.
func newReferenceInventoryServer(t *testing.T) *referenceInventoryServer {
	t.Helper()
	reference := &referenceInventoryServer{documents: map[string]string{}}
	reference.server = httptest.NewServer(http.HandlerFunc(reference.route))
	t.Cleanup(reference.server.Close)
	return reference
}

// route answers one inventory request.
//
// The request is recorded BEFORE the hook runs, so a test can observe exactly
// which credential reached the wire even when the hook changes the stored
// registration while the request is in flight.
func (r *referenceInventoryServer) route(w http.ResponseWriter, request *http.Request) {
	token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	r.mu.Lock()
	r.requests = append(r.requests, inventoryRequest{
		method: request.Method,
		path:   request.URL.Path,
		query:  request.URL.RawQuery,
		token:  token,
	})
	hook := r.onRequest
	r.mu.Unlock()
	if hook != nil {
		hook(token)
	}
	r.mu.Lock()
	document := r.documents[token]
	if document == "" {
		document = r.fallback
	}
	status := r.status
	r.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(document))
}

// setOnRequest installs the hook that runs after the server has received a
// request and before it answers. It is the deterministic "while the request
// is in flight" seam of the replacement proofs.
func (r *referenceInventoryServer) setOnRequest(hook func(token string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onRequest = hook
}

// answer sets the document one token receives.
func (r *referenceInventoryServer) answer(token, document string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.documents[token] = document
}

// answerAll sets the document every token receives.
func (r *referenceInventoryServer) answerAll(document string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallback = document
}

// fail makes every request answer with the given status.
func (r *referenceInventoryServer) fail(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = status
}

// requestCount returns how many requests the server received.
func (r *referenceInventoryServer) requestCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// lastRequest returns the request the server received last.
func (r *referenceInventoryServer) lastRequest(t *testing.T) inventoryRequest {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) == 0 {
		t.Fatal("the reference inventory server received no request")
	}
	return r.requests[len(r.requests)-1]
}

// usableProfile returns a stored registration that may use the plan.
func usableProfile(id, subject, accessToken string) siwc.ProfileRecord {
	return siwc.ProfileRecord{
		ID:             id,
		Label:          id,
		Subject:        subject,
		IssuedClientID: "issued-" + id,
		HostID:         "host-test",
		GrantedScopes:  []string{"offline_access", siwc.PlanUseScope},
		AccessToken:    accessToken,
		RefreshToken:   "refresh-" + accessToken,
		Expiry:         time.Now().Add(time.Hour),
	}
}

// referenceRefreshServer answers the documented token refresh with one
// replacement token set and RECORDS that it was asked.
//
// The renewal proof is about which credential the inventory request presents,
// so the renewal itself is the connector's real refresher over this endpoint:
// a hand-written renewal in the test would only prove the test.
type referenceRefreshServer struct {
	server *httptest.Server

	mu       sync.Mutex
	requests int
}

// newReferenceRefreshServer starts the reference token endpoint.
func newReferenceRefreshServer(t *testing.T) *referenceRefreshServer {
	t.Helper()
	reference := &referenceRefreshServer{}
	reference.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reference.mu.Lock()
		reference.requests++
		reference.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"access_token":"token-renewed",` +
				`"refresh_token":"refresh-renewed",` +
				`"expires_in":3600,"token_type":"Bearer"}`,
		))
	}))
	t.Cleanup(reference.server.Close)
	return reference
}

// requestCount returns how many refresh requests the provider received.
func (r *referenceRefreshServer) requestCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests
}

// realInventory is the connector's real inventory over a real registration
// store and a reference inventory endpoint.
type realInventory struct {
	inventory *profileInventory
	store     *siwc.ProfileStore
	manager   *siwc.Manager
	server    *referenceInventoryServer
	refresh   *referenceRefreshServer
	now       time.Time

	mu            sync.Mutex
	hookErr       error
	logoutCleared bool
}

// recordHookError records a failure of an in-flight hook for the test
// goroutine to assert; the hook runs on the reference server's goroutine.
func (r *realInventory) recordHookError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hookErr == nil {
		r.hookErr = err
	}
}

// inFlightError returns the failure an in-flight hook recorded.
func (r *realInventory) inFlightError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hookErr
}

// recordLogoutCleared records that the real sign-out cleared the local
// tokens during the in-flight hook.
func (r *realInventory) recordLogoutCleared() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logoutCleared = true
}

// logoutWasCleared reports whether the in-flight sign-out cleared the local
// tokens.
func (r *realInventory) logoutWasCleared() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.logoutCleared
}

// clock returns the test clock.
func (r *realInventory) clock() time.Time { return r.now }

// advance moves the test clock forward.
func (r *realInventory) advance(by time.Duration) { r.now = r.now.Add(by) }

// newRealInventory builds the real store, manager, client and cache.
//
// The manager uses the connector's own refresher. The tokens of the test
// records are fresh, so the refresher returns them without a provider call:
// the inventory proof observes the token the STORE holds, not a stand-in.
func newRealInventory(t *testing.T, records ...siwc.ProfileRecord) *realInventory {
	t.Helper()
	storePath := t.TempDir()
	store, err := siwc.NewProfileStore(storePath)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	for _, record := range records {
		if err := store.SaveNew(record); err != nil {
			t.Fatalf("save profile %q: %v", record.ID, err)
		}
	}
	refresh := newReferenceRefreshServer(t)
	refresher, err := siwc.NewRefresher(siwc.RefresherConfig{
		Profiles:      store,
		TokenEndpoint: refresh.server.URL,
		HTTPClient:    refresh.server.Client(),
	})
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}
	manager, err := siwc.NewManager(siwc.ManagerConfig{
		Profiles:  store,
		Refresher: refresher,
		Revoker:   newTestRevoker(t, store),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	authority := &selectedProfiles{store: store, manager: manager}
	server := newReferenceInventoryServer(t)
	client, err := siwc.NewModelsClient(siwc.ModelsClientConfig{
		ModelsURL:  server.server.URL + "/v1/models",
		HTTPClient: server.server.Client(),
		Tokens:     authority,
	})
	if err != nil {
		t.Fatalf("NewModelsClient: %v", err)
	}
	real := &realInventory{
		store:   store,
		manager: manager,
		server:  server,
		refresh: refresh,
		now:     time.Now(),
	}
	inventory, err := newProfileInventory(inventoryConfig{
		Authority: authority,
		Catalog:   client,
		Now:       real.clock,
		TTL:       time.Minute,
	})
	if err != nil {
		t.Fatalf("newProfileInventory: %v", err)
	}
	real.inventory = inventory
	return real
}

// newTestRevoker returns the connector's real sign-out over a reference
// provider.
//
// The provider publishes a discovery document whose revocation endpoint
// confirms an empty HTTP 200. The sign-out itself is the connector's own: the
// inventory proof observes the persisted state a real logout leaves behind,
// not a hand-written imitation of it.
func newTestRevoker(t *testing.T, store *siwc.ProfileStore) *siwc.Revoker {
	t.Helper()
	mux := http.NewServeMux()
	var provider *httptest.Server
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(
			w,
			`{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q,"revocation_endpoint":%q}`,
			provider.URL,
			provider.URL+"/authorize",
			provider.URL+"/token",
			provider.URL+"/jwks.json",
			provider.URL+"/revoke",
		)
	})
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	provider = httptest.NewServer(mux)
	t.Cleanup(provider.Close)

	oidc, err := siwc.NewOIDCClient(siwc.OIDCClientConfig{
		DiscoveryURL: provider.URL + "/.well-known/openid-configuration",
		HTTPClient:   provider.Client(),
	})
	if err != nil {
		t.Fatalf("NewOIDCClient: %v", err)
	}
	revoker, err := siwc.NewRevoker(siwc.RevokerConfig{
		Profiles:   store,
		OIDC:       oidc,
		HTTPClient: provider.Client(),
		Sleep:      func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewRevoker: %v", err)
	}
	return revoker
}

// list calls the connector's ListModels through the configured instance.
func (r *realInventory) list(t *testing.T) backendplugin.ListModelsResponse {
	t.Helper()
	instance := &instance{inventory: r.inventory}
	response, err := instance.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	return response
}

// nativeIDs returns the native model ids of an answer, in order.
func nativeIDs(response backendplugin.ListModelsResponse) []string {
	ids := make([]string, 0, len(response.Models))
	for _, model := range response.Models {
		ids = append(ids, model.NativeModelID)
	}
	return ids
}

// TestInventory_TwoProfilesAreIsolated proves requirement 7.4 on the wire.
//
// Profile A is read, profile B is selected and read, and profile A is
// selected again. Each read presents the selected registration's own
// credential, and profile A's catalog is never replaced by - or confused
// with - profile B's. The third read is served from profile A's own entry,
// which is the SEGREGATION half of the requirement: the entry is keyed by the
// registration it describes, so selecting another profile neither leaks that
// profile's catalog into A nor discards A's entry.
func TestInventory_TwoProfilesAreIsolated(t *testing.T) {
	t.Parallel()
	first := usableProfile("acct-a", "subject-a", "token-a")
	second := usableProfile("acct-b", "subject-b", "token-b")
	real := newRealInventory(t, first, second)
	real.server.answer("token-a", documentProfileA)
	real.server.answer("token-b", documentProfileB)
	if err := real.manager.Select(context.Background(), first.ID); err != nil {
		t.Fatalf("select %q: %v", first.ID, err)
	}

	firstResponse := real.list(t)
	if got := nativeIDs(firstResponse); !slices.Equal(got, []string{"a-one", "a-two"}) {
		t.Fatalf("profile A's catalog = %v, want [a-one a-two]", got)
	}
	if firstResponse.InventorySource != inventorySourceLive {
		t.Errorf("the first read's source = %q, want %q", firstResponse.InventorySource, inventorySourceLive)
	}
	request := real.server.lastRequest(t)
	if request.method != http.MethodGet || request.path != "/v1/models" || request.query != "" {
		t.Errorf("the inventory request = %s %s?%s, want GET /v1/models", request.method, request.path, request.query)
	}
	if request.token != "token-a" {
		t.Errorf("profile A's read presented the credential %q, want profile A's token", request.token)
	}

	if err := real.manager.Select(context.Background(), second.ID); err != nil {
		t.Fatalf("select %q: %v", second.ID, err)
	}
	secondResponse := real.list(t)
	if got := nativeIDs(secondResponse); !slices.Equal(got, []string{"b-one"}) {
		t.Fatalf("profile B's catalog = %v, want [b-one]", got)
	}
	if secondResponse.InventorySource != inventorySourceLive {
		t.Errorf("profile B's first read source = %q, want %q", secondResponse.InventorySource, inventorySourceLive)
	}
	request = real.server.lastRequest(t)
	if request.token != "token-b" {
		t.Errorf("profile B's read presented the credential %q, want profile B's token", request.token)
	}
	if requests := real.server.requestCount(); requests != 2 {
		t.Fatalf("the server received %d requests after two profile reads, want 2", requests)
	}

	// Re-select profile A. The answer must be A's catalog, and it must be
	// served from A's own entry: a cache keyed by anything but the
	// registration would have discarded A's entry when B was read.
	if err := real.manager.Select(context.Background(), first.ID); err != nil {
		t.Fatalf("re-select %q: %v", first.ID, err)
	}
	again := real.list(t)
	if got := nativeIDs(again); !slices.Equal(got, []string{"a-one", "a-two"}) {
		t.Fatalf("profile A after profile B = %v, want A's own catalog [a-one a-two]", got)
	}
	if again.InventorySource != inventorySourceCache {
		t.Errorf("profile A's repeated read source = %q, want the cache source %q", again.InventorySource, inventorySourceCache)
	}
	if requests := real.server.requestCount(); requests != 2 {
		t.Fatalf("profile A's cached read produced a request: %d requests, want 2", requests)
	}
}

// TestInventory_InvalidatesOnCredentialReplacement proves the credential half
// of the invalidation policy.
//
// A returning authorization replaces the session of the same account. The
// account mapping may not change at all, but the credential generation does,
// and a cached catalog of the previous session must not be served from the
// new one. The test does not consult the inventory between the old and the
// new credential: the change must be observed by the entry's identity check,
// not by having watched the operation happen.
func TestInventory_InvalidatesOnCredentialReplacement(t *testing.T) {
	t.Parallel()
	record := usableProfile("acct-a", "subject-a", "token-a")
	real := newRealInventory(t, record)
	real.server.answer("token-a", documentProfileA)
	real.server.answer("token-after", documentReplacement)
	if err := real.manager.Select(context.Background(), record.ID); err != nil {
		t.Fatalf("select: %v", err)
	}
	if got := nativeIDs(real.list(t)); !slices.Equal(got, []string{"a-one", "a-two"}) {
		t.Fatalf("the first catalog = %v, want [a-one a-two]", got)
	}

	// The reauthorization persists a new session for the same registration.
	replacement := record
	replacement.AccessToken = "token-after"
	replacement.RefreshToken = "refresh-after"
	replacement.Expiry = time.Now().Add(2 * time.Hour)
	if _, err := real.store.Publish(record.ID, func(current siwc.ProfileRecord) (siwc.ProfileRecord, error) {
		updated := current
		updated.AccessToken = replacement.AccessToken
		updated.RefreshToken = replacement.RefreshToken
		updated.Expiry = replacement.Expiry
		return updated, nil
	}); err != nil {
		t.Fatalf("publish the replacement credential: %v", err)
	}

	response := real.list(t)
	if got := nativeIDs(response); !slices.Equal(got, []string{"replacement-one"}) {
		t.Fatalf("the catalog after the credential replacement = %v, want the new session's catalog", got)
	}
	if response.InventorySource != inventorySourceLive {
		t.Errorf("the catalog after the credential replacement came from %q, want %q", response.InventorySource, inventorySourceLive)
	}
	if requests := real.server.requestCount(); requests != 2 {
		t.Fatalf("the server received %d requests, want a fresh read after the replacement", requests)
	}
	if request := real.server.lastRequest(t); request.token != "token-after" {
		t.Errorf("the fresh read presented %q, want the replacement session's token", request.token)
	}
}

// TestInventory_ReplacementDuringTheFetchIsNotServedToTheNextCall proves that
// a catalog read with an old session's credential is never published under the
// replacement session.
//
// The reference server publishes the replacement AFTER it has received the
// in-flight request and BEFORE it answers it. The answer of that first call
// still describes the old session, which is permitted: the request was
// authorized with the credential that was current when it was sent. What is
// forbidden is keeping that answer for the NEW session. The second,
// non-overlapping call must reach the provider with the replacement token
// instead of taking the old catalog from profile-cache.
func TestInventory_ReplacementDuringTheFetchIsNotServedToTheNextCall(t *testing.T) {
	t.Parallel()
	record := usableProfile("acct-a", "subject-a", "token-a")
	real := newRealInventory(t, record)
	real.server.answer("token-a", documentProfileA)
	real.server.answer("token-after", documentReplacement)
	if err := real.manager.Select(context.Background(), record.ID); err != nil {
		t.Fatalf("select: %v", err)
	}
	real.server.setOnRequest(func(token string) {
		if token != "token-a" {
			return
		}
		if _, err := real.store.Publish(record.ID, func(current siwc.ProfileRecord) (siwc.ProfileRecord, error) {
			replacement := current
			replacement.AccessToken = "token-after"
			replacement.RefreshToken = "refresh-after"
			replacement.Expiry = time.Now().Add(2 * time.Hour)
			return replacement, nil
		}); err != nil {
			real.recordHookError(fmt.Errorf("publish the replacement during the fetch: %w", err))
		}
	})

	// The in-flight call is authorized with token-a. Its answer may still be
	// the old session's catalog.
	real.list(t)
	if err := real.inFlightError(); err != nil {
		t.Fatalf("the in-flight replacement failed: %v", err)
	}
	if requests := real.server.requestCount(); requests != 1 {
		t.Fatalf("the first read produced %d provider requests, want 1", requests)
	}
	if request := real.server.lastRequest(t); request.token != "token-a" {
		t.Fatalf("the in-flight read presented %q, want the credential that was current then", request.token)
	}

	// The next call is NOT overlapping the replacement. It must ask the new
	// session, and it must never receive the old catalog from profile-cache.
	next := real.list(t)
	if got := nativeIDs(next); !slices.Equal(got, []string{"replacement-one"}) {
		t.Fatalf("the catalog after the in-flight replacement = %v (source %q), want the replacement session's catalog",
			got, next.InventorySource)
	}
	if next.InventorySource != inventorySourceLive {
		t.Errorf("the catalog after the in-flight replacement came from %q, want %q",
			next.InventorySource, inventorySourceLive)
	}
	if requests := real.server.requestCount(); requests != 2 {
		t.Fatalf("the server received %d requests, want a fresh read after the replacement", requests)
	}
	if request := real.server.lastRequest(t); request.token != "token-after" {
		t.Errorf("the read after the replacement presented %q, want the replacement session's token", request.token)
	}
}

// TestInventory_InvalidatesOnRegistrationIdentityChange proves the identity
// half of the invalidation policy.
//
// A returning authorization that binds another account changes the subject,
// the host provenance and the granted scopes. The credential may be the same,
// and the answer is refused as a catalog of the old identity until it is read
// again for the new one.
func TestInventory_InvalidatesOnRegistrationIdentityChange(t *testing.T) {
	t.Parallel()
	record := usableProfile("acct-a", "subject-a", "token-a")
	real := newRealInventory(t, record)
	real.server.answer("token-a", documentProfileA)
	if err := real.manager.Select(context.Background(), record.ID); err != nil {
		t.Fatalf("select: %v", err)
	}
	if got := nativeIDs(real.list(t)); !slices.Equal(got, []string{"a-one", "a-two"}) {
		t.Fatalf("the first catalog = %v, want [a-one a-two]", got)
	}
	real.server.answer("token-a", documentReplacement)

	if _, err := real.store.Publish(record.ID, func(current siwc.ProfileRecord) (siwc.ProfileRecord, error) {
		updated := current
		updated.Subject = "subject-replacement"
		updated.HostID = "host-replacement"
		updated.GrantedScopes = []string{siwc.PlanUseScope, "offline_access", "api.read"}
		return updated, nil
	}); err != nil {
		t.Fatalf("publish the replaced identity: %v", err)
	}

	response := real.list(t)
	if got := nativeIDs(response); !slices.Equal(got, []string{"replacement-one"}) {
		t.Fatalf("the catalog after the identity change = %v, want a fresh read", got)
	}
	if response.InventorySource != inventorySourceLive {
		t.Errorf("the catalog after the identity change came from %q, want %q", response.InventorySource, inventorySourceLive)
	}
	if requests := real.server.requestCount(); requests != 2 {
		t.Fatalf("the server received %d requests, want a fresh read after the identity change", requests)
	}
}

// TestInventory_LogoutInvalidatesAndReauthorizationRefetches proves the
// cleared-registration half of the invalidation policy.
//
// A signed-out registration cannot supply an inventory, so the read fails
// explicitly and the cached catalog is not served. The entry is DROPPED, not
// merely bypassed: after the registration is authorized again with the same
// account mapping, a catalog read before the sign-out must still not be
// served.
func TestInventory_LogoutInvalidatesAndReauthorizationRefetches(t *testing.T) {
	t.Parallel()
	record := usableProfile("acct-a", "subject-a", "token-a")
	real := newRealInventory(t, record)
	real.server.answer("token-a", documentProfileA)
	real.server.answer("token-after-logout", documentAfterLogout)
	if err := real.manager.Select(context.Background(), record.ID); err != nil {
		t.Fatalf("select: %v", err)
	}
	if got := nativeIDs(real.list(t)); !slices.Equal(got, []string{"a-one", "a-two"}) {
		t.Fatalf("the first catalog = %v, want [a-one a-two]", got)
	}

	// The connector's own sign-out clears the tokens and marks the
	// registration as needing a new authorization.
	result, err := real.manager.Logout(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !result.LocalTokensCleared {
		t.Fatalf("the sign-out did not clear the local tokens: %+v", result)
	}

	instance := &instance{inventory: real.inventory}
	response, err := instance.ListModels(context.Background(), 0)
	if err == nil {
		t.Fatalf("a signed-out registration answered with %d models (source %q)", len(response.Models), response.InventorySource)
	}
	if !errors.Is(err, errInventoryRegistrationUnusable) {
		t.Errorf("the sign-out failure = %v, want the unusable-registration failure", err)
	}
	if requests := real.server.requestCount(); requests != 1 {
		t.Errorf("the signed-out read produced a provider request: %d requests, want 1", requests)
	}

	// A reauthorization of the same registration establishes a new session.
	if _, err := real.store.Publish(record.ID, func(current siwc.ProfileRecord) (siwc.ProfileRecord, error) {
		authorized := current
		authorized.AccessToken = "token-after-logout"
		authorized.RefreshToken = "refresh-after-logout"
		authorized.Expiry = time.Now().Add(time.Hour)
		authorized.NeedsReauth = false
		return authorized, nil
	}); err != nil {
		t.Fatalf("publish the reauthorization: %v", err)
	}

	response = real.list(t)
	if got := nativeIDs(response); !slices.Equal(got, []string{"after-logout"}) {
		t.Fatalf("the catalog after the reauthorization = %v, want a fresh read", got)
	}
	if response.InventorySource != inventorySourceLive {
		t.Errorf("the catalog after the reauthorization came from %q, want %q", response.InventorySource, inventorySourceLive)
	}
	if requests := real.server.requestCount(); requests != 2 {
		t.Fatalf("the server received %d requests, want a fresh read after the reauthorization", requests)
	}
}

// TestInventory_LogoutAndReauthorizationDuringTheFetchIsNotServedToTheNextCall
// proves the same generation boundary across the connector's real sign-out.
//
// The in-flight request carries token-a. While it is in flight the real
// Manager.Logout clears the local tokens, and the same registration is then
// authorized again with token-after-logout. The answer of the first call may
// still be the catalog of the signed-out session, but it must not be published
// as the reauthorized registration's catalog: the next call must read the new
// session with its own credential.
func TestInventory_LogoutAndReauthorizationDuringTheFetchIsNotServedToTheNextCall(t *testing.T) {
	t.Parallel()
	record := usableProfile("acct-a", "subject-a", "token-a")
	real := newRealInventory(t, record)
	real.server.answer("token-a", documentProfileA)
	real.server.answer("token-after-logout", documentAfterLogout)
	if err := real.manager.Select(context.Background(), record.ID); err != nil {
		t.Fatalf("select: %v", err)
	}
	real.server.setOnRequest(func(token string) {
		if token != "token-a" {
			return
		}
		result, err := real.manager.Logout(context.Background(), record.ID)
		if err != nil {
			real.recordHookError(fmt.Errorf("sign out during the fetch: %w", err))
			return
		}
		if !result.LocalTokensCleared {
			real.recordHookError(errors.New("the sign-out did not clear the local tokens"))
			return
		}
		real.recordLogoutCleared()
		if _, err := real.store.Publish(record.ID, func(current siwc.ProfileRecord) (siwc.ProfileRecord, error) {
			authorized := current
			authorized.AccessToken = "token-after-logout"
			authorized.RefreshToken = "refresh-after-logout"
			authorized.Expiry = time.Now().Add(time.Hour)
			authorized.NeedsReauth = false
			return authorized, nil
		}); err != nil {
			real.recordHookError(fmt.Errorf("publish the reauthorization during the fetch: %w", err))
		}
	})

	real.list(t)
	if err := real.inFlightError(); err != nil {
		t.Fatalf("the in-flight sign-out and reauthorization failed: %v", err)
	}
	if !real.logoutWasCleared() {
		t.Fatal("the in-flight sign-out did not report that it cleared the local tokens")
	}
	if requests := real.server.requestCount(); requests != 1 {
		t.Fatalf("the first read produced %d provider requests, want 1", requests)
	}
	if request := real.server.lastRequest(t); request.token != "token-a" {
		t.Fatalf("the in-flight read presented %q, want the credential that was current then", request.token)
	}

	next := real.list(t)
	if got := nativeIDs(next); !slices.Equal(got, []string{"after-logout"}) {
		t.Fatalf("the catalog after the in-flight reauthorization = %v (source %q), want the new session's catalog",
			got, next.InventorySource)
	}
	if next.InventorySource != inventorySourceLive {
		t.Errorf("the catalog after the in-flight reauthorization came from %q, want %q",
			next.InventorySource, inventorySourceLive)
	}
	if requests := real.server.requestCount(); requests != 2 {
		t.Fatalf("the server received %d requests, want a fresh read after the reauthorization", requests)
	}
	if request := real.server.lastRequest(t); request.token != "token-after-logout" {
		t.Errorf("the read after the reauthorization presented %q, want the new session's token", request.token)
	}
}

// TestInventory_RefusesAnUnusableRegistrationWithoutServingTheCache proves
// that an arrived-but-unusable registration is neither absent nor cached.
//
// The registration keeps its credential here and is only marked as needing a
// new authorization: the state is "present and unusable", and it must be
// refused instead of being read as "no profile selected" or answered from the
// entry of the session that is no longer valid.
func TestInventory_RefusesAnUnusableRegistrationWithoutServingTheCache(t *testing.T) {
	t.Parallel()
	record := usableProfile("acct-a", "subject-a", "token-a")
	real := newRealInventory(t, record)
	real.server.answer("token-a", documentProfileA)
	if err := real.manager.Select(context.Background(), record.ID); err != nil {
		t.Fatalf("select: %v", err)
	}
	if got := nativeIDs(real.list(t)); !slices.Equal(got, []string{"a-one", "a-two"}) {
		t.Fatalf("the first catalog = %v, want [a-one a-two]", got)
	}

	if _, err := real.store.Publish(record.ID, func(current siwc.ProfileRecord) (siwc.ProfileRecord, error) {
		unusable := current
		unusable.NeedsReauth = true
		return unusable, nil
	}); err != nil {
		t.Fatalf("publish the unusable state: %v", err)
	}

	instance := &instance{inventory: real.inventory}
	response, err := instance.ListModels(context.Background(), 0)
	if err == nil {
		t.Fatalf("an unusable registration answered with %d models (source %q)", len(response.Models), response.InventorySource)
	}
	if !errors.Is(err, errInventoryRegistrationUnusable) {
		t.Errorf("the failure = %v, want the unusable-registration failure", err)
	}
	if requests := real.server.requestCount(); requests != 1 {
		t.Errorf("the unusable read produced a provider request: %d requests, want 1", requests)
	}

	// Clear the marker without changing the credential: a bypassed entry
	// would now match again. The entry was dropped, so the next read must
	// reach the provider.
	if _, err := real.store.Publish(record.ID, func(current siwc.ProfileRecord) (siwc.ProfileRecord, error) {
		usable := current
		usable.NeedsReauth = false
		return usable, nil
	}); err != nil {
		t.Fatalf("publish the usable state: %v", err)
	}
	real.server.answer("token-a", documentReplacement)
	if got := nativeIDs(real.list(t)); !slices.Equal(got, []string{"replacement-one"}) {
		t.Fatalf("the catalog after the registration became usable again = %v, want a fresh read", got)
	}
	if requests := real.server.requestCount(); requests != 2 {
		t.Fatalf("the server received %d requests, want a fresh read after the unusable state", requests)
	}
}

// TestInventory_RemovalIsNotServedFromCache proves that a removed
// registration is a failure, not its old catalog.
func TestInventory_RemovalIsNotServedFromCache(t *testing.T) {
	t.Parallel()
	record := usableProfile("acct-a", "subject-a", "token-a")
	real := newRealInventory(t, record)
	real.server.answer("token-a", documentProfileA)
	if err := real.manager.Select(context.Background(), record.ID); err != nil {
		t.Fatalf("select: %v", err)
	}
	if got := nativeIDs(real.list(t)); !slices.Equal(got, []string{"a-one", "a-two"}) {
		t.Fatalf("the first catalog = %v, want [a-one a-two]", got)
	}

	// The store's documented record name. Removing a saved registration is
	// a store-directory operation; the inventory must observe that the
	// record is gone instead of serving the catalog it remembers.
	if err := os.Remove(filepath.Join(real.store.Path(), "profile-"+record.ID+".json")); err != nil {
		t.Fatalf("remove the registration: %v", err)
	}

	instance := &instance{inventory: real.inventory}
	response, err := instance.ListModels(context.Background(), 0)
	if err == nil {
		t.Fatalf("a removed registration answered with %d models (source %q)", len(response.Models), response.InventorySource)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the removal failure = %v, want the not-exist failure", err)
	}
	if requests := real.server.requestCount(); requests != 1 {
		t.Errorf("the removed registration produced a provider request: %d requests, want 1", requests)
	}
}

// TestInventory_RefusesAReadThatOutlivesTheSelection proves the post-read
// check: an answer for one registration is never attributed to another
// selection.
//
// The catalog double moves the selection while it answers, which is the race
// between an active-profile switch and an inventory read. The answer is
// refused, and it is not cached under either registration.
func TestInventory_RefusesAReadThatOutlivesTheSelection(t *testing.T) {
	t.Parallel()
	authority := newStubRegistrations(
		usableProfile("acct-a", "subject-a", "token-a"),
		usableProfile("acct-b", "subject-b", "token-b"),
	)
	authority.selectProfile("acct-a")
	catalog := &stubCatalog{models: map[string][]siwc.Model{
		"acct-a": {{Slug: "a-one"}},
		"acct-b": {{Slug: "b-one"}},
	}}
	catalog.setOnCall(func(string) { authority.selectProfile("acct-b") })
	inventory := newStubInventory(t, authority, catalog)

	instance := &instance{inventory: inventory}
	if _, err := instance.ListModels(context.Background(), 0); err == nil {
		t.Fatal("a read that outlived its selection was accepted")
	} else if !errors.Is(err, errInventorySelectionChanged) {
		t.Errorf("the failure = %v, want the selection-changed failure", err)
	}
	if calls := catalog.callsFor(); !slices.Equal(calls, []string{"acct-a"}) {
		t.Errorf("the catalog was read for %v, want only the selection that started the read", calls)
	}

	// The answer of the previous selection is not waiting under the new
	// one: the next read is a fresh read for profile B.
	catalog.setOnCall(nil)
	if got := stubIDs(t, inventory); !slices.Equal(got, []string{"b-one"}) {
		t.Fatalf("the catalog after the selection moved = %v, want [b-one]", got)
	}
	if calls := catalog.callsFor(); !slices.Equal(calls, []string{"acct-a", "acct-b"}) {
		t.Errorf("the catalog reads = %v, want one read per selection", calls)
	}
}

// TestInventory_RefusesACatalogThatNamesAnotherRegistration proves the
// defensive owner check and that a refused answer leaves no cache entry.
//
// The production client always echoes the registration it was asked about, so
// the branch is a guard rather than an observed provider behaviour. The stub
// exercises it directly: an answer that claims another owner is refused, and
// it is not stored for either registration.
func TestInventory_RefusesACatalogThatNamesAnotherRegistration(t *testing.T) {
	t.Parallel()
	authority := newStubRegistrations(usableProfile("acct-a", "subject-a", "token-a"))
	authority.selectProfile("acct-a")
	catalog := &stubCatalog{models: map[string][]siwc.Model{
		"acct-a": {{Slug: "a-one"}},
	}}
	catalog.setReportedFor("acct-b")
	inventory := newStubInventory(t, authority, catalog)

	_, err := inventory.list(context.Background(), 0)
	if err == nil {
		t.Fatal("a catalog that names another registration was accepted")
	}
	if !errors.Is(err, errInventoryCatalogMismatch) {
		t.Errorf("the failure = %v, want the catalog-mismatch failure", err)
	}

	catalog.setReportedFor("")
	response, err := inventory.list(context.Background(), 0)
	if err != nil {
		t.Fatalf("the read after the refused answer: %v", err)
	}
	if got := nativeIDs(response); !slices.Equal(got, []string{"a-one"}) {
		t.Fatalf("the catalog after the refused answer = %v, want [a-one]", got)
	}
	if response.InventorySource != inventorySourceLive {
		t.Errorf("the read after the refused answer came from %q, want %q",
			response.InventorySource, inventorySourceLive)
	}
	if calls := catalog.callsFor(); !slices.Equal(calls, []string{"acct-a", "acct-a"}) {
		t.Errorf("the catalog reads = %v, want the refused answer to leave no cache entry", calls)
	}
}

// TestInventory_KeepsAPreRequestRenewalAndCachesItsGeneration proves the
// legitimate refresh-before-request behaviour on the wire.
//
// The stored access token is inside the refresher's renewal margin, so the
// connector's real refresher renews the credential BEFORE the inventory
// request is sent. The reference server asserts which credential it actually
// received, and the repeated read may be a cache hit for that renewed
// generation. This is the schedule that must keep working: the renewal is part
// of the acquisition whose generation the entry carries.
func TestInventory_KeepsAPreRequestRenewalAndCachesItsGeneration(t *testing.T) {
	t.Parallel()
	record := usableProfile("acct-a", "subject-a", "token-a")
	record.Expiry = time.Now().Add(30 * time.Second)
	real := newRealInventory(t, record)
	real.server.answer("token-renewed", documentReplacement)
	if err := real.manager.Select(context.Background(), record.ID); err != nil {
		t.Fatalf("select: %v", err)
	}

	first := real.list(t)
	if got := nativeIDs(first); !slices.Equal(got, []string{"replacement-one"}) {
		t.Fatalf("the catalog after the pre-request renewal = %v, want the renewed session's catalog", got)
	}
	if first.InventorySource != inventorySourceLive {
		t.Errorf("the first read came from %q, want %q", first.InventorySource, inventorySourceLive)
	}
	if requests := real.refresh.requestCount(); requests != 1 {
		t.Fatalf("the refresher made %d provider requests, want the one renewal before the fetch", requests)
	}
	request := real.server.lastRequest(t)
	if request.token != "token-renewed" {
		t.Fatalf("the inventory request presented %q, want the renewed credential", request.token)
	}
	stored, err := real.store.Load(record.ID)
	if err != nil {
		t.Fatalf("load the renewed registration: %v", err)
	}
	if stored.AccessToken != "token-renewed" {
		t.Fatalf("the store holds %q, want the renewed credential", stored.AccessToken)
	}

	// The renewed generation is the one the read used, so the repeated read
	// may be served from the cache without another provider request.
	again := real.list(t)
	if got := nativeIDs(again); !slices.Equal(got, []string{"replacement-one"}) {
		t.Fatalf("the repeated catalog = %v, want the renewed session's catalog", got)
	}
	if again.InventorySource != inventorySourceCache {
		t.Errorf("the repeated read came from %q, want the cache source %q",
			again.InventorySource, inventorySourceCache)
	}
	if requests := real.server.requestCount(); requests != 1 {
		t.Fatalf("the repeated read produced %d provider requests, want 1", requests)
	}
}

// TestInventory_DoesNotServeTheCacheForAFailedRead proves that the cache is
// not a fallback.
//
// The premise is established first: the entry genuinely exists, matches the
// registration and is served from the cache while it is inside its lifetime.
// Then it ages out and the provider fails. The failure is explicit: a
// successful stale catalog would hide a provider-side change behind an answer
// that looks fetched.
func TestInventory_DoesNotServeTheCacheForAFailedRead(t *testing.T) {
	t.Parallel()
	authority := newStubRegistrations(usableProfile("acct-a", "subject-a", "token-a"))
	authority.selectProfile("acct-a")
	catalog := &stubCatalog{models: map[string][]siwc.Model{
		"acct-a": {{Slug: "a-one"}},
	}}
	catalog.generation = stubGeneration(authority)
	now := time.Now()
	inventory, err := newProfileInventory(inventoryConfig{
		Authority: authority,
		Catalog:   catalog,
		Now:       func() time.Time { return now },
		TTL:       time.Minute,
	})
	if err != nil {
		t.Fatalf("newProfileInventory: %v", err)
	}
	if _, err := inventory.list(context.Background(), 0); err != nil {
		t.Fatalf("the first read: %v", err)
	}

	// The premise of the proof: the first read was actually cached. A repeat
	// inside the lifetime is a cache hit and produces no provider request.
	cached, err := inventory.list(context.Background(), 0)
	if err != nil {
		t.Fatalf("the repeated read inside the lifetime: %v", err)
	}
	if cached.InventorySource != inventorySourceCache {
		t.Fatalf("the repeated read came from %q after %d catalog reads, want the cached catalog %q: the first read was never cached",
			cached.InventorySource, len(catalog.callsFor()), inventorySourceCache)
	}
	if requests := catalog.callsFor(); !slices.Equal(requests, []string{"acct-a"}) {
		t.Fatalf("the repeated read produced the catalog reads %v, want the one entry the first read cached", requests)
	}

	now = now.Add(2 * time.Minute)
	providerFailure := errors.New("the provider is unavailable")
	catalog.setErr(providerFailure)

	response, err := inventory.list(context.Background(), 0)
	if err == nil {
		t.Fatalf("a failed read was answered from the expired cache with %d models (source %q)",
			len(response.Models), response.InventorySource)
	}
	if !errors.Is(err, providerFailure) {
		t.Errorf("the failed read = %v, want the provider's failure", err)
	}
	if requests := catalog.callsFor(); !slices.Equal(requests, []string{"acct-a", "acct-a"}) {
		t.Errorf("the catalog reads = %v, want the aged-out entry to be re-read", requests)
	}
}

// TestInventory_AFailedProfileBReadNeverFallsBackToACachedProfileA proves the
// cross-registration half of requirement 7.6.
//
// Profile A's catalog is cached. Profile B is selected and its read fails.
// The failure is returned as a failure with no models: profile A's catalog is
// not a last-known answer for profile B, because that would be another
// account's inventory presented as this one's.
func TestInventory_AFailedProfileBReadNeverFallsBackToACachedProfileA(t *testing.T) {
	t.Parallel()
	authority := newStubRegistrations(
		usableProfile("acct-a", "subject-a", "token-a"),
		usableProfile("acct-b", "subject-b", "token-b"),
	)
	authority.selectProfile("acct-a")
	catalog := &stubCatalog{models: map[string][]siwc.Model{
		"acct-a": {{Slug: "a-one"}},
		"acct-b": {{Slug: "b-one"}},
	}}
	inventory := newStubInventory(t, authority, catalog)
	if got := stubIDs(t, inventory); !slices.Equal(got, []string{"a-one"}) {
		t.Fatalf("profile A's catalog = %v, want [a-one]", got)
	}

	authority.selectProfile("acct-b")
	catalog.setErr(errors.New("profile B's provider read failed"))
	response, err := inventory.list(context.Background(), 0)
	if err == nil {
		t.Fatalf("a failed profile B read answered with %d models (source %q)", len(response.Models), response.InventorySource)
	}
	if len(response.Models) != 0 {
		t.Errorf("the failed read carried %d models, want none", len(response.Models))
	}
	if calls := catalog.callsFor(); !slices.Equal(calls, []string{"acct-a", "acct-b"}) {
		t.Errorf("the catalog reads = %v, want one read per profile", calls)
	}
}

// TestInventory_CacheIsBoundedAndMarked proves the two properties requirement
// 7.6 names for a last-known cache: it is profile-local and explicitly
// marked.
//
// The mark is asserted on every cache hit; the bound is asserted by reading
// more registrations than the cache can hold and observing that the oldest
// entry was dropped while the answer stayed correct.
func TestInventory_CacheIsBoundedAndMarked(t *testing.T) {
	t.Parallel()
	records := make([]siwc.ProfileRecord, 0, maxProfileInventories+1)
	models := map[string][]siwc.Model{}
	for index := 0; index <= maxProfileInventories; index++ {
		id := "acct-" + string(rune('a'+index))
		records = append(records, usableProfile(id, "subject-"+id, "token-"+id))
		models[id] = []siwc.Model{{Slug: id + "-model"}}
	}
	authority := newStubRegistrations(records...)
	catalog := &stubCatalog{models: models}
	inventory := newStubInventory(t, authority, catalog)

	for _, record := range records {
		authority.selectProfile(record.ID)
		response, err := inventory.list(context.Background(), 0)
		if err != nil {
			t.Fatalf("read %q: %v", record.ID, err)
		}
		if response.InventorySource != inventorySourceLive {
			t.Errorf("the first read of %q came from %q, want %q", record.ID, response.InventorySource, inventorySourceLive)
		}
	}

	// The cache holds at most maxProfileInventories entries. Re-selecting
	// the OLDEST registration must reach the catalog again, because its
	// entry was evicted by the later reads.
	authority.selectProfile(records[0].ID)
	response, err := inventory.list(context.Background(), 0)
	if err != nil {
		t.Fatalf("read the oldest registration: %v", err)
	}
	if response.InventorySource != inventorySourceLive {
		t.Errorf("the evicted entry was served from %q, want a fresh read", response.InventorySource)
	}
	// The most recent registration is still cached.
	authority.selectProfile(records[len(records)-1].ID)
	response, err = inventory.list(context.Background(), 0)
	if err != nil {
		t.Fatalf("read the most recent registration: %v", err)
	}
	if response.InventorySource != inventorySourceCache {
		t.Errorf("the most recent entry came from %q, want the cache source %q", response.InventorySource, inventorySourceCache)
	}
}

// TestInventory_LimitPreservesProviderOrder proves the bounded answer keeps
// the provider's order: the limit truncates, it does not sort or sample.
func TestInventory_LimitPreservesProviderOrder(t *testing.T) {
	t.Parallel()
	authority := newStubRegistrations(usableProfile("acct-a", "subject-a", "token-a"))
	authority.selectProfile("acct-a")
	catalog := &stubCatalog{models: map[string][]siwc.Model{
		"acct-a": {{Slug: "third"}, {Slug: "first"}, {Slug: "second"}},
	}}
	inventory := newStubInventory(t, authority, catalog)

	response, err := inventory.list(context.Background(), 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := nativeIDs(response); !slices.Equal(got, []string{"third", "first"}) {
		t.Fatalf("the limited catalog = %v, want the provider's first two rows", got)
	}
	if response.Models[0].CanonicalModelID != FactoryKind+"/third" {
		t.Errorf("the canonical model id = %q, want the connector route plus the native slug", response.Models[0].CanonicalModelID)
	}
}

// TestListModels_NoStoreIsAnExplicitOperationalFailure proves the missing
// configuration path.
func TestListModels_NoStoreIsAnExplicitOperationalFailure(t *testing.T) {
	t.Parallel()
	instance := &instance{}
	response, err := instance.ListModels(context.Background(), 0)
	if err == nil {
		t.Fatalf("an instance without a store answered with %d models", len(response.Models))
	}
	if !errors.Is(err, errInventoryNoStore) {
		t.Errorf("the failure = %v, want the missing-store failure", err)
	}
	if !modelinventory.IsOperational(err) {
		t.Errorf("the missing-store failure is not marked operational: %v", err)
	}
}

// TestListModels_ReportsTheMissingSelectionNotTheBuildGap is the first
// failing assertion of the inventory slice, kept as the wiring proof.
func TestListModels_ReportsTheMissingSelectionNotTheBuildGap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storePath := t.TempDir()
	inst, err := New().Configure(ctx, backendplugin.ConfigureRequest{
		InstanceID:  "inventory-selection",
		FactoryKind: FactoryKind,
		ConfigYAML:  fmt.Appendf(nil, "profile_store_path: %q\n", storePath),
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	resp, err := inst.ListModels(ctx, 10)
	if err == nil {
		t.Fatalf("ListModels succeeded with %d models and no selected registration", len(resp.Models))
	}
	if strings.Contains(err.Error(), "not part of this connector build") {
		t.Fatalf("the inventory still reports the build gap instead of the missing selection: %v", err)
	}
	if !strings.Contains(err.Error(), "selected registration") {
		t.Fatalf("the inventory failure does not name the missing selected registration: %v", err)
	}
	if resp.InventorySource != "" {
		t.Errorf("a failed inventory carries the inventory source %q", resp.InventorySource)
	}
}

// TestStubCatalog_ReportsTheGenerationTheReadPresented pins the premise the
// policy proofs rely on: the catalog double reads the credential generation
// BEFORE its in-flight hook runs.
//
// Production acquires the credential and then fetches, so a read reports the
// generation it presented. If the double read the generation after the hook, a
// hook that replaced the credential would attribute the preselected catalog to
// the replacement session - the defect the real-wire J1 proofs refuse. This is
// a fixture-premise proof, not a claim about production behaviour.
func TestStubCatalog_ReportsTheGenerationTheReadPresented(t *testing.T) {
	t.Parallel()
	authority := newStubRegistrations(usableProfile("acct-a", "subject-a", "token-a"))
	presented, err := authority.Load("acct-a")
	if err != nil {
		t.Fatalf("load the registration: %v", err)
	}
	presentedGeneration, _ := presented.CredentialGeneration()

	catalog := &stubCatalog{
		models:     map[string][]siwc.Model{"acct-a": {{Slug: "a-one"}}},
		generation: stubGeneration(authority),
	}
	catalog.setOnCall(func(string) {
		authority.mutate("acct-a", func(record siwc.ProfileRecord) siwc.ProfileRecord {
			record.AccessToken = "token-replacement"
			record.RefreshToken = "refresh-replacement"
			return record
		})
	})

	inventory, err := catalog.ListFor(context.Background(), "acct-a")
	if err != nil {
		t.Fatalf("ListFor: %v", err)
	}
	if inventory.CredentialGeneration != presentedGeneration {
		t.Fatalf("the double reported the generation %x, want the credential the read presented (%x)",
			inventory.CredentialGeneration, presentedGeneration)
	}
	after, err := authority.Load("acct-a")
	if err != nil {
		t.Fatalf("load the replaced registration: %v", err)
	}
	afterGeneration, _ := after.CredentialGeneration()
	if afterGeneration == presentedGeneration {
		t.Fatal("the in-flight hook did not replace the credential: this proof's premise is not established")
	}
}

// stubRegistrations is the registration authority of the policy proofs.
//
// It answers from a map, and the test can move the selection while a catalog
// read is in flight.
type stubRegistrations struct {
	mu       sync.Mutex
	selected string
	records  map[string]siwc.ProfileRecord
}

// newStubRegistrations returns the authority of the given records.
func newStubRegistrations(records ...siwc.ProfileRecord) *stubRegistrations {
	stub := &stubRegistrations{records: map[string]siwc.ProfileRecord{}}
	for _, record := range records {
		stub.records[record.ID] = record
	}
	return stub
}

// Selected returns the current selection.
func (s *stubRegistrations) Selected(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selected, nil
}

// Load returns the record of the named registration.
func (s *stubRegistrations) Load(profileID string) (siwc.ProfileRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[profileID]
	if !ok {
		return siwc.ProfileRecord{}, fs.ErrNotExist
	}
	return record, nil
}

// selectProfile records the selection.
func (s *stubRegistrations) selectProfile(profileID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selected = profileID
}

// mutate applies a change to one stored record, the way a credential renewal
// or an authorization publishes one.
func (s *stubRegistrations) mutate(
	profileID string,
	change func(siwc.ProfileRecord) siwc.ProfileRecord,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[profileID]
	if !ok {
		return
	}
	s.records[profileID] = change(record)
}

// stubCatalog is the catalog of the policy proofs.
type stubCatalog struct {
	mu          sync.Mutex
	models      map[string][]siwc.Model
	err         error
	calls       []string
	onCall      func(profileID string)
	reportedFor string
	generation  func(profileID string) [sha256.Size]byte
}

// ListFor returns the prepared catalog of the named registration.
//
// The generation is read BEFORE the in-flight hook runs. Production acquires
// the credential and then fetches, so the generation a real client reports is
// the one the request presented; a hook that mutates the registration while
// the read is in flight must not make the preselected catalog describe
// credentials this read never presented.
func (s *stubCatalog) ListFor(_ context.Context, profileID string) (siwc.ModelInventory, error) {
	s.mu.Lock()
	s.calls = append(s.calls, profileID)
	err := s.err
	models := append([]siwc.Model(nil), s.models[profileID]...)
	onCall := s.onCall
	reportedFor := s.reportedFor
	generation := s.generation
	s.mu.Unlock()
	var credentialGeneration [sha256.Size]byte
	if generation != nil {
		credentialGeneration = generation(profileID)
	}
	if onCall != nil {
		onCall(profileID)
	}
	if err != nil {
		return siwc.ModelInventory{}, err
	}
	inventory := siwc.ModelInventory{ProfileID: profileID, Models: models}
	if generation != nil {
		inventory.CredentialGeneration = credentialGeneration
	}
	if reportedFor != "" {
		inventory.ProfileID = reportedFor
	}
	return inventory, nil
}

// setErr makes every read fail.
func (s *stubCatalog) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// setOnCall installs the hook that runs while one read is in flight, AFTER
// the generation the read presents has been read.
func (s *stubCatalog) setOnCall(hook func(profileID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onCall = hook
}

// setReportedFor makes the catalog name another registration than the one it
// was read for.
func (s *stubCatalog) setReportedFor(profileID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reportedFor = profileID
}

// callsFor returns the registrations the catalog was read for.
func (s *stubCatalog) callsFor() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// newStubInventory returns an inventory over the doubles.
//
// The stub catalog reports the generation of the authority's current record,
// the way the real client reports the generation of the token it acquired.
// The proofs that are about cache policy therefore keep exercising the real
// publication rule without inventing a credential.
func newStubInventory(
	t *testing.T,
	authority registrationAuthority,
	catalog *stubCatalog,
) *profileInventory {
	t.Helper()
	catalog.generation = stubGeneration(authority)
	inventory, err := newProfileInventory(inventoryConfig{
		Authority: authority,
		Catalog:   catalog,
	})
	if err != nil {
		t.Fatalf("newProfileInventory: %v", err)
	}
	return inventory
}

// stubGeneration returns the generation callback of the policy double: the
// generation of the credential pair the authority's CURRENT record holds, the
// way the real client reports the generation of the token it acquired.
//
// Every policy fixture that builds an inventory directly must install it, or
// its first answer is stamped with an all-zero generation, mismatches the
// stored record and is never cached.
func stubGeneration(authority registrationAuthority) func(profileID string) [sha256.Size]byte {
	return func(profileID string) [sha256.Size]byte {
		record, err := authority.Load(profileID)
		if err != nil {
			return [sha256.Size]byte{}
		}
		generation, _ := record.CredentialGeneration()
		return generation
	}
}

// stubIDs reads the inventory of the current selection.
func stubIDs(t *testing.T, inventory *profileInventory) []string {
	t.Helper()
	response, err := inventory.list(context.Background(), 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return nativeIDs(response)
}
