package siwc

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// The regressions of the two structural boundaries of this package.
//
// The package expresses the difference between the CALLER's interest and
// the OPERATION's obligation at two places, and the proofs below are about
// those two places:
//
//   - the RECEIPT BOUNDARY of the sign-in flows: everything before the
//     provider's complete answer is the caller's, and everything after it
//     is the connector's. The validation of a credential set the provider
//     has already issued is therefore not contingent on the caller's
//     context; and
//   - the RECONCILIATION of the refresh: a commit and its one
//     generation-checked fallback share ONE budget, and the outcome of the
//     commit is visible to the operator.
//
// The proofs are black box on purpose: they drive the flows and they read
// the store, so they state the CONTRACT and not the shape of the code that
// implements it.

// ---------------------------------------------------------------------------
// The mechanisms the proofs use.
// ---------------------------------------------------------------------------

// answerBody is a response body that cancels the caller's context when the
// WHOLE body has been delivered.
//
// The cancellation happens at the EOF read, so the caller has received
// every byte of the provider's answer and the context is already done when
// the connector reaches the step that follows it. The mechanism is
// deterministic and it sleeps for nothing.
type answerBody struct {
	data   []byte
	offset int
	cancel context.CancelFunc
	once   sync.Once
}

func (b *answerBody) Read(p []byte) (int, error) {
	if b.offset < len(b.data) {
		n := copy(p, b.data[b.offset:])
		b.offset += n
		return n, nil
	}
	b.once.Do(func() {
		if b.cancel != nil {
			b.cancel()
		}
	})
	return 0, io.EOF
}

func (b *answerBody) Close() error { return nil }

// answerTransport replaces the body of the token endpoint's answer with a
// REAL signed ID token, and optionally cancels the caller's context at EOF.
//
// The ID token is signed with the fake provider's key, carries the
// attempt's ACTUAL nonce and the issued client id the token must be
// addressed to, and names the account the registration belongs to. The
// proof therefore drives the REAL validator against a REAL discovery
// document and key set: a fake validator would not reach the network at
// all, and the defect the proof is about lives exactly there.
type answerTransport struct {
	t      *testing.T
	base   *http.Client
	issuer string
	key    *rsa.PrivateKey
	keyID  string
	// audience is the issued client id the token must be addressed to.
	audience string
	subject  string
	// nonceOf returns the nonce of the attempt that is in flight.
	nonceOf func() string
	cancel  context.CancelFunc

	mu    sync.Mutex
	calls int
}

func (t *answerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	_ = body
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	nonce := ""
	if t.nonceOf != nil {
		nonce = t.nonceOf()
	}
	now := time.Now()
	claims := map[string]any{
		"iss":   t.issuer,
		"sub":   t.subject,
		"aud":   t.audience,
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Add(-time.Minute).Unix(),
		"nonce": nonce,
		"email": "user@example.com",
	}
	signed := signRS256(t.t, t.key, map[string]any{"alg": "RS256", "kid": t.keyID, "typ": "JWT"}, claims)
	payload := map[string]any{
		"access_token":  "reauthorized-access-token",
		"refresh_token": "reauthorized-refresh-token",
		"id_token":      signed,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"scope":         "chatgpt.tokens.use.direct email offline_access openid profile resource.invoke",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp.Body = &answerBody{data: raw, cancel: t.cancel}
	return resp, nil
}

// revocationTransport answers the first attempts with a 503 and cancels the
// caller's context on the final attempt.
//
// The cancellation is the caller's WITHDRAWAL of the sign-out, and it
// arrives on the attempt that would have been the last one.
type revocationTransport struct {
	mu       sync.Mutex
	calls    int
	failAt   int
	cancel   context.CancelFunc
	lastForm string
}

func (t *revocationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	form := ""
	if req.Body != nil {
		data, err := io.ReadAll(req.Body)
		if err == nil {
			form = string(data)
		}
	}
	t.mu.Lock()
	t.calls++
	calls := t.calls
	t.lastForm = form
	t.mu.Unlock()
	if calls >= t.failAt {
		if t.cancel != nil {
			t.cancel()
		}
		return nil, context.Canceled
	}
	return &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(&answerBody{data: []byte(`{"error":"server_error"}`)}),
		Request:    req,
	}, nil
}

// withdrawingTransport cancels the caller's context and fails the request.
//
// It is the mechanism of a cancellation that arrives while the provider's
// configuration is being read: the request never produced an answer, and
// the caller is gone.
type withdrawingTransport struct {
	cancel context.CancelFunc
	err    error
}

func (t *withdrawingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if t.cancel != nil {
		t.cancel()
	}
	if t.err != nil {
		return nil, t.err
	}
	return nil, context.Canceled
}

// cannedTransport answers every request with the same canned response.
type cannedTransport struct {
	status int
	body   string

	mu    sync.Mutex
	forms []string
}

func (t *cannedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	form := ""
	if req.Body != nil {
		data, err := io.ReadAll(req.Body)
		if err == nil {
			form = string(data)
		}
	}
	t.mu.Lock()
	t.forms = append(t.forms, form)
	t.mu.Unlock()
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(&answerBody{data: []byte(t.body)}),
		Request:    req,
	}, nil
}

// ---------------------------------------------------------------------------
// The receipt boundary of the returning authorization.
// ---------------------------------------------------------------------------

// TestReauthorization_CancellationAfterTheTokenSetValidatesAndPublishesIt
// proves that the validation of a credential set the provider has already
// issued survives a cancellation that arrives after the answer.
//
// The provider has issued a complete token set, and the connector has to
// validate it and publish it. The validation is the FIRST step of the
// post-receipt reconciliation, and it runs under the OWNED, bounded
// context: the caller's cancellation governs the work that had not yet
// produced a side effect, and not the work that has.
//
// The proof drives the REAL validator against a REAL discovery document
// and key set, with a correctly signed ID token: the existing proof of
// this shape uses a fake validator whose token set carries no ID token at
// all, and it therefore cannot reach the defect.
func TestReauthorization_CancellationAfterTheTokenSetValidatesAndPublishesIt(t *testing.T) {
	run := func(t *testing.T, cancelAtEOF bool) (ProfileRecord, int, int, error) {
		t.Helper()
		// A FRESH fake provider and a FRESH validator per run: a
		// validator that already holds the discovery document and the key
		// set would not need the network, and the cold cache is the case
		// under test.
		fake := newFakeOIDCServer(t)
		validator := newTestValidator(t, fake, fake.server.URL)
		harness := newHarness(t)
		harness.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
		registered, err := harness.authorizer.Register(context.Background(), RegisterRequest{
			ProfileID: "acct-one",
			Label:     "work",
		})
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		transport := &answerTransport{
			t:        t,
			base:     harness.fake.server.Client(),
			issuer:   fake.server.URL,
			key:      testRSAKey(t),
			keyID:    fake.keyID,
			audience: registered.IssuedClientID,
			subject:  registered.Subject,
			nonceOf: func() string {
				requests := harness.fake.authorizationRequests()
				if len(requests) == 0 {
					return ""
				}
				return requests[len(requests)-1].Get("nonce")
			},
		}
		if cancelAtEOF {
			transport.cancel = cancel
		}
		authorizer, err := NewAuthorizer(AuthorizerConfig{
			AuthorizationEndpoint: harness.fake.server.URL + "/api/accounts/authorize",
			TokenEndpoint:         harness.fake.server.URL + "/api/accounts/oauth/token",
			HTTPClient:            &http.Client{Transport: transport},
			BrowserOpener:         harness.authorizer.config.BrowserOpener,
			Profiles:              harness.profiles,
			Hosts:                 harness.hosts,
			Validator:             validator,
		})
		if err != nil {
			t.Fatalf("NewAuthorizer: %v", err)
		}
		record, err := authorizer.Reauthorize(ctx, ReauthorizeRequest{ProfileID: registered.ID})
		return record, fake.discoveryFetches(), fake.jwksFetches(), err
	}

	t.Run("control", func(t *testing.T) {
		record, discovery, jwks, err := run(t, false)
		if err != nil {
			t.Fatalf("CONTROL FAILED: the returning authorization failed without a cancellation: %v", err)
		}
		if record.RefreshToken != "reauthorized-refresh-token" {
			t.Errorf("CONTROL: the fresh credential set was not persisted: %q", record.RefreshToken)
		}
		t.Logf("CONTROL: ok, discovery=%d jwks=%d", discovery, jwks)
	})

	t.Run("cancelled", func(t *testing.T) {
		record, discovery, jwks, err := run(t, true)
		t.Logf("CASE: err=%v discovery=%d jwks=%d refresh=%q access=%q",
			err, discovery, jwks, record.RefreshToken, record.AccessToken)
		if err != nil {
			t.Errorf("CASE: the returning authorization FAILED after the provider issued the token set: %v", err)
		}
		if record.RefreshToken != "reauthorized-refresh-token" {
			t.Errorf("CASE: the fresh credential set was not persisted: %q", record.RefreshToken)
		}
		if discovery == 0 || jwks == 0 {
			t.Errorf("CASE: the validation did not reach the provider: discovery=%d jwks=%d", discovery, jwks)
		}
	})
}

// ---------------------------------------------------------------------------
// The receipt boundary of the first registration.
// ---------------------------------------------------------------------------

// TestRegistration_CancellationAfterTheTokenSetValidatesAndPersistsIt
// proves that the validation of a first registration's credential set
// survives a cancellation that arrives after the answer.
//
// The first registration has the same receipt boundary as the returning
// authorization: the provider has issued the token set, and the connector
// has to validate it and persist it.
func TestRegistration_CancellationAfterTheTokenSetValidatesAndPersistsIt(t *testing.T) {
	run := func(t *testing.T, cancelAtEOF bool) (ProfileRecord, int, int, error) {
		t.Helper()
		fake := newFakeOIDCServer(t)
		validator := newTestValidator(t, fake, fake.server.URL)
		harness := newHarness(t)
		harness.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		transport := &answerTransport{
			t:        t,
			base:     harness.fake.server.Client(),
			issuer:   fake.server.URL,
			key:      testRSAKey(t),
			keyID:    fake.keyID,
			audience: "oaiapp_issued_1",
			subject:  "subject-1",
			nonceOf: func() string {
				requests := harness.fake.authorizationRequests()
				if len(requests) == 0 {
					return ""
				}
				return requests[len(requests)-1].Get("nonce")
			},
		}
		if cancelAtEOF {
			transport.cancel = cancel
		}
		authorizer, err := NewAuthorizer(AuthorizerConfig{
			AuthorizationEndpoint: harness.fake.server.URL + "/api/accounts/authorize",
			TokenEndpoint:         harness.fake.server.URL + "/api/accounts/oauth/token",
			HTTPClient:            &http.Client{Transport: transport},
			BrowserOpener:         harness.authorizer.config.BrowserOpener,
			Profiles:              harness.profiles,
			Hosts:                 harness.hosts,
			Validator:             validator,
		})
		if err != nil {
			t.Fatalf("NewAuthorizer: %v", err)
		}
		record, err := authorizer.Register(ctx, RegisterRequest{ProfileID: "acct-one"})
		return record, fake.discoveryFetches(), fake.jwksFetches(), err
	}

	t.Run("control", func(t *testing.T) {
		record, discovery, jwks, err := run(t, false)
		if err != nil {
			t.Fatalf("CONTROL FAILED: %v", err)
		}
		if record.RefreshToken != "reauthorized-refresh-token" {
			t.Errorf("CONTROL: the credential set was not persisted: %q", record.RefreshToken)
		}
		t.Logf("CONTROL: ok discovery=%d jwks=%d", discovery, jwks)
	})
	t.Run("cancelled", func(t *testing.T) {
		record, discovery, jwks, err := run(t, true)
		t.Logf("CASE: err=%v discovery=%d jwks=%d refresh=%q", err, discovery, jwks, record.RefreshToken)
		if err != nil {
			t.Errorf("CASE: the registration FAILED after the provider issued the token set: %v", err)
		}
		if record.RefreshToken != "reauthorized-refresh-token" {
			t.Errorf("CASE: the credential set was not persisted: %q", record.RefreshToken)
		}
		if discovery == 0 || jwks == 0 {
			t.Errorf("CASE: the validation did not reach the provider: discovery=%d jwks=%d", discovery, jwks)
		}
	})
}

// ---------------------------------------------------------------------------
// The withdrawal of a sign-out.
// ---------------------------------------------------------------------------

// TestLogout_CancellationDuringTheFinalRevocationPreservesTheCredential
// proves that a cancellation on the LAST revocation attempt aborts the
// sign-out instead of completing it.
//
// A cancellation is the caller's WITHDRAWAL and not a network failure: the
// provider refused nothing, nothing was confirmed, and the local
// credential must stay. The failure is the caller's own, and the local
// clearing never happens.
func TestLogout_CancellationDuringTheFinalRevocationPreservesTheCredential(t *testing.T) {
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)

	oidcFake := newFakeOIDCServer(t)
	oidcClient, err := NewOIDCClient(OIDCClientConfig{
		DiscoveryURL:  oidcFake.server.URL + "/.well-known/openid-configuration",
		HTTPClient:    oidcFake.server.Client(),
		RenewInterval: -1,
	})
	if err != nil {
		t.Fatalf("NewOIDCClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &revocationTransport{failAt: 3, cancel: cancel}
	revoker, err := NewRevoker(RevokerConfig{
		Profiles:   store,
		OIDC:       oidcClient,
		HTTPClient: &http.Client{Transport: transport},
		Sleep:      func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewRevoker: %v", err)
	}

	result, err := revoker.Logout(ctx, record.ID)
	stored, loadErr := store.Load(record.ID)
	t.Logf("attempts=%d err=%v confirmed=%v cleared=%v outcome=%q guidance=%q",
		transport.calls, err, result.RemoteRevocationConfirmed, result.LocalTokensCleared,
		result.RevocationOutcome, result.Guidance)
	t.Logf("stored: refresh=%q access=%q needsReauth=%v loadErr=%v",
		stored.RefreshToken, stored.AccessToken, stored.NeedsReauth, loadErr)
	if err == nil {
		t.Errorf("a cancelled final attempt reported SUCCESS with no error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the failure is not the caller's cancellation: %v", err)
	}
	if result.LocalTokensCleared {
		t.Errorf("the local sign-out was reported as completed although the caller withdrew")
	}
	if stored.RefreshToken == "" {
		t.Errorf("the saved refresh token was erased although no remote outcome was confirmed")
	}
	if stored.NeedsReauth {
		t.Errorf("the registration was marked although the caller withdrew")
	}
}

// TestLogout_CancellationDuringTheDiscoveryAbortsTheSignOut proves that a
// cancellation that arrives while the provider's configuration is read
// aborts the sign-out.
//
// The discovery is the FIRST step of the revocation, and a cancellation
// there is a withdrawal like any other: nothing was confirmed, and the
// local credential must stay.
func TestLogout_CancellationDuringTheDiscoveryAbortsTheSignOut(t *testing.T) {
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oidcClient, err := NewOIDCClient(OIDCClientConfig{
		DiscoveryURL: "https://provider.invalid/.well-known/openid-configuration",
		HTTPClient: &http.Client{Transport: &withdrawingTransport{
			cancel: cancel,
			err:    context.Canceled,
		}},
		RenewInterval: -1,
	})
	if err != nil {
		t.Fatalf("NewOIDCClient: %v", err)
	}
	revoker, err := NewRevoker(RevokerConfig{
		Profiles:   store,
		OIDC:       oidcClient,
		HTTPClient: &http.Client{Transport: &cannedTransport{status: http.StatusOK, body: ""}},
		Sleep:      func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewRevoker: %v", err)
	}

	result, err := revoker.Logout(ctx, record.ID)
	stored, loadErr := store.Load(record.ID)
	t.Logf("err=%v cleared=%v outcome=%q", err, result.LocalTokensCleared, result.RevocationOutcome)
	t.Logf("stored: refresh=%q needsReauth=%v loadErr=%v",
		stored.RefreshToken, stored.NeedsReauth, loadErr)
	if err == nil {
		t.Fatalf("a cancelled discovery reported SUCCESS with no error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the failure is not the caller's cancellation: %v", err)
	}
	if result.LocalTokensCleared {
		t.Errorf("the local sign-out was reported as completed although the caller withdrew")
	}
	if stored.RefreshToken == "" {
		t.Errorf("the saved refresh token was erased although no remote outcome was confirmed")
	}
}

// ---------------------------------------------------------------------------
// The reconciliation of the refresh.
// ---------------------------------------------------------------------------

// TestRefresh_TheFailedCommitIsVisibleToTheOperator proves that the
// operator can tell a COMMITTED quarantine from a FAILED one.
//
// The class of a refresh failure is the same in both cases - the provider
// refused the token - and the class alone therefore does not say whether
// the local record of that refusal was written. The rendered failure says
// it, in constant text that names no credential.
func TestRefresh_TheFailedCommitIsVisibleToTheOperator(t *testing.T) {
	// The rotation failed AND the quarantine failed: the publication lock
	// is held, so every publication of the registration fails inside the
	// store's budget.
	failedStore := newTestProfileStore(t)
	failedStore.lockTimeout = 40 * time.Millisecond
	failedRecord := expiredTestProfile("acct-one")
	saveTestProfile(t, failedStore, failedRecord)
	failedDir, err := failedStore.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	release, err := failedStore.acquireProfileLock(
		context.Background(), failedDir, failedRecord.ID, publicationLockKind, 5*time.Second,
	)
	if err != nil {
		t.Fatalf("acquire the publication lock: %v", err)
	}
	defer release()
	failedTransport := &cannedTransport{body: `{"access_token":"refreshed-access-token",` +
		`"refresh_token":"rotated-refresh-token","token_type":"bearer",` +
		`"expires_in":3600,"scope":"openid offline_access chatgpt.tokens.use.direct"}`}
	failedRefresher := newTestRefresher(t, failedStore, "https://provider.invalid/token", &http.Client{
		Transport: failedTransport,
	})
	_, failedErr := failedRefresher.Refresh(context.Background(), failedRecord.ID)
	if failedErr == nil {
		t.Fatalf("the failed commit was reported as a success")
	}
	failedRendered := failedErr.Error()
	t.Logf("failed quarantine: %q", failedRendered)
	failedStored, loadErr := failedStore.Load(failedRecord.ID)
	if loadErr != nil {
		t.Fatalf("load the registration: %v", loadErr)
	}
	t.Logf("failed quarantine: stored refresh=%q needsReauth=%v",
		failedStored.RefreshToken, failedStored.NeedsReauth)

	// The provider refused the token and the quarantine WAS committed.
	committedStore := newTestProfileStore(t)
	committedRecord := expiredTestProfile("acct-one")
	saveTestProfile(t, committedStore, committedRecord)
	committedTransport := &cannedTransport{
		status: http.StatusBadRequest,
		body:   `{"error":"refresh_token_reused"}`,
	}
	committedRefresher := newTestRefresher(t, committedStore, "https://provider.invalid/token", &http.Client{
		Transport: committedTransport,
	})
	_, committedErr := committedRefresher.Refresh(context.Background(), committedRecord.ID)
	if committedErr == nil {
		t.Fatalf("the refused token was reported as a success")
	}
	committedRendered := committedErr.Error()
	t.Logf("committed quarantine: %q", committedRendered)
	committedStored, loadErr := committedStore.Load(committedRecord.ID)
	if loadErr != nil {
		t.Fatalf("load the registration: %v", loadErr)
	}
	t.Logf("committed quarantine: stored refresh=%q needsReauth=%v",
		committedStored.RefreshToken, committedStored.NeedsReauth)

	// The rendered failure names the stale credential and the failed
	// quarantine, in constant text.
	lower := strings.ToLower(failedRendered)
	for _, want := range []string{"stale", "quarantine"} {
		if !strings.Contains(lower, want) {
			t.Errorf("the rendered failure does not mention %q: %q", want, failedRendered)
		}
	}
	// The state the FAILED case established is a state that was really
	// established: the publication lock was held, so NOTHING was
	// published, the stored record is the one that was there before, and
	// the registration is NOT marked. The failure may therefore say so.
	//
	// The commit outcome is the closed vocabulary's name of that state,
	// and it is asserted as a literal: the VALUE is the operator-facing
	// contract, and not the name of the constant that carries it.
	var failedClassified *RefreshError
	if !errors.As(failedErr, &failedClassified) {
		t.Fatalf("the failed quarantine is not a classified refresh failure: %v", failedErr)
	}
	if failedClassified.Commit != "quarantine-failed" {
		t.Errorf("the commit outcome = %q, want %q", failedClassified.Commit, "quarantine-failed")
	}
	if !strings.Contains(failedRendered, "NOT marked") {
		t.Errorf("the rendered failure does not say that the registration is not marked: %q", failedRendered)
	}
	// The failed quarantine leaves the UNSAFE local state, and the failure
	// says so: the consumed token is still on disk and the registration is
	// not marked.
	if failedStored.RefreshToken != failedRecord.RefreshToken {
		t.Errorf("the consumed token is not on disk: %q", failedStored.RefreshToken)
	}
	if failedStored.NeedsReauth {
		t.Errorf("the registration is marked although the quarantine was not committed")
	}
	// The committed quarantine leaves the SAFE local state.
	if committedStored.RefreshToken != "" {
		t.Errorf("the unusable refresh token was kept: %q", committedStored.RefreshToken)
	}
	if !committedStored.NeedsReauth {
		t.Errorf("the registration is not marked although the quarantine was committed")
	}
	// The state the COMMITTED case established is the opposite one: the
	// quarantine was published and its durability was confirmed.
	var committedClassified *RefreshError
	if !errors.As(committedErr, &committedClassified) {
		t.Fatalf("the committed quarantine is not a classified refresh failure: %v", committedErr)
	}
	if committedClassified.Commit != "quarantined" {
		t.Errorf("the commit outcome = %q, want %q", committedClassified.Commit, "quarantined")
	}
	// The two outcomes are DISTINGUISHABLE.
	if failedRendered == committedRendered {
		t.Errorf(
			"a committed quarantine and a failed one are reported the same way: %q",
			failedRendered,
		)
	}
	if strings.Contains(committedRendered, "NOT marked") {
		t.Errorf("a committed quarantine is reported as a failed one: %q", committedRendered)
	}
	// The rendering is the same through the formatter: the raw cause is
	// never rendered, and the operator sees the commit outcome.
	if formatted := fmt.Sprintf("%v", failedErr); formatted != failedRendered {
		t.Errorf("the formatted failure differs from the rendered one: %q", formatted)
	}
}

// TestRefresh_TheFailedCommitAndItsQuarantineShareOneBudget proves that a
// commit and its one generation-checked fallback share ONE budget.
//
// The commit and the fallback are two publications, and the store's budget
// is the TOTAL of the reconciliation they belong to: the fallback has what
// is left of the commit's budget, and a failed commit does not buy a
// second full budget.
func TestRefresh_TheFailedCommitAndItsQuarantineShareOneBudget(t *testing.T) {
	const budget = 40 * time.Millisecond
	run := func(t *testing.T, terminal bool) time.Duration {
		t.Helper()
		store := newTestProfileStore(t)
		store.lockTimeout = budget
		record := expiredTestProfile("acct-one")
		saveTestProfile(t, store, record)

		dir, err := store.resolve()
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		release, err := store.acquireProfileLock(
			context.Background(), dir, record.ID, publicationLockKind, 5*time.Second,
		)
		if err != nil {
			t.Fatalf("acquire the publication lock: %v", err)
		}
		defer release()

		body := `{"access_token":"refreshed-access-token",` +
			`"refresh_token":"rotated-refresh-token","token_type":"bearer",` +
			`"expires_in":3600,"scope":"openid offline_access chatgpt.tokens.use.direct"}`
		status := http.StatusOK
		if terminal {
			body = `{"error":"refresh_token_reused"}`
			status = http.StatusBadRequest
		}
		transport := &cannedTransport{status: status, body: body}
		refresher := newTestRefresher(t, store, "https://provider.invalid/token", &http.Client{
			Transport: transport,
		})
		started := time.Now()
		_, _ = refresher.Refresh(context.Background(), record.ID)
		return time.Since(started)
	}

	rotation := run(t, false)
	terminal := run(t, true)
	t.Logf("failed-rotation subcase: %v (store budget %v)", rotation, budget)
	t.Logf("terminal-quarantine subcase: %v (store budget %v)", terminal, budget)
	// The failed rotation waits for the commit AND for the quarantine, and
	// the two share the one budget.
	if rotation > 70*time.Millisecond {
		t.Errorf("the failed rotation waited %v, which is about TWO store budgets", rotation)
	}
	if rotation < budget/2 {
		t.Errorf("the failed rotation waited %v, which is less than the store's budget", rotation)
	}
	// The terminal quarantine is ONE publication, and it waits for the one
	// budget.
	if terminal > 70*time.Millisecond {
		t.Errorf("the terminal quarantine waited %v, which is about TWO store budgets", terminal)
	}
}

// ---------------------------------------------------------------------------
// The identity of a published registration.
// ---------------------------------------------------------------------------

// TestStore_PublishRejectsAChangeThatAltersTheProfileID proves that a
// change that moves a registration to another profile id publishes
// nothing.
//
// The profile id is the registration's identity: it is the name of the
// file the record lives in and the key every other operation of the store
// uses. A change that altered it would publish into ANOTHER registration's
// file and leave a second record behind.
func TestStore_PublishRejectsAChangeThatAltersTheProfileID(t *testing.T) {
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)

	updated, err := store.Publish("acct-one", func(current ProfileRecord) (ProfileRecord, error) {
		changed := current
		changed.ID = "acct-two"
		return changed, nil
	})
	if err == nil {
		t.Fatalf("a change that altered the profile id was accepted: %q", updated.ID)
	}
	t.Logf("the refusal: %q", err.Error())
	// The failure is a CONSTANT and it names no id.
	for _, id := range []string{"acct-one", "acct-two"} {
		if strings.Contains(err.Error(), id) {
			t.Errorf("the failure names the profile id %q: %q", id, err.Error())
		}
	}
	// No second record was created.
	if _, err := store.Load("acct-two"); err == nil {
		t.Errorf("a change that altered the profile id created a second record")
	}
	// The registration itself is unchanged.
	first, err := store.Load("acct-one")
	if err != nil {
		t.Fatalf("load the registration: %v", err)
	}
	if first.RefreshToken != record.RefreshToken {
		t.Errorf("the refused publication changed the record: %q", first.RefreshToken)
	}
	if first.NeedsReauth != record.NeedsReauth {
		t.Errorf("the refused publication changed the state of the record")
	}
}

// ---------------------------------------------------------------------------
// The reports of the manager.
// ---------------------------------------------------------------------------

// TestManager_NoticesAndErrorsNeverCarryTheSubject proves the shape of the
// manager's reports.
//
// The NOTICES and the ERRORS identify a registration through its non-secret
// local key, and they never carry the subject or the email hint of the
// account. The LISTING is the one report that carries the subject, and it
// carries it as the store's own summary: the subject is stored state, and
// the summary is the store's non-secret view of it.
func TestManager_NoticesAndErrorsNeverCarryTheSubject(t *testing.T) {
	store := newTestProfileStore(t)
	record := testProfileRecord("acct-one")
	saveTestProfile(t, store, record)

	var notices []string
	manager, err := NewManager(ManagerConfig{
		Profiles: store,
		Revoker: &fakeRevoker{result: LogoutResult{
			ProfileID:          "acct-one",
			RevocationOutcome:  RevocationOutcomeUnavailable,
			Guidance:           RevocationGuidanceUnconfirmed,
			LocalTokensCleared: true,
		}},
		Notice: func(message string) {
			notices = append(notices, message)
		},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// The notice of an unconfirmed revocation.
	if _, err := manager.Logout(context.Background(), "acct-one"); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if len(notices) == 0 {
		t.Fatalf("the manager reported no notice")
	}
	for _, notice := range notices {
		if strings.Contains(notice, record.Subject) {
			t.Errorf("a notice carries the subject: %q", notice)
		}
		if strings.Contains(notice, record.EmailHint) {
			t.Errorf("a notice carries the email hint: %q", notice)
		}
	}
	// The error of a manager that has no selected registration.
	if _, err := manager.Active(context.Background()); err == nil {
		t.Errorf("an unselected registration was reported as active")
	} else if strings.Contains(err.Error(), record.Subject) {
		t.Errorf("an error carries the subject: %q", err.Error())
	}
	// The rendering of a registration names the local key.
	rendered := DescribeRegistration(record)
	if strings.Contains(rendered, record.Subject) {
		t.Errorf("the rendering carries the subject: %q", rendered)
	}
	if strings.Contains(rendered, record.EmailHint) {
		t.Errorf("the rendering carries the email hint: %q", rendered)
	}
	// The LISTING carries the subject, as the store's own summary.
	summaries, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("the listing holds %d registrations, want 1", len(summaries))
	}
	if summaries[0].Subject != record.Subject {
		t.Errorf("the listing does not carry the subject: %q", summaries[0].Subject)
	}
}
