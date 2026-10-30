package siwc

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The PARSING of the provider's callback.
//
// A callback is a query the provider controls, and every parameter of it
// may arrive more than once. The listener therefore COLLECTS the arrived
// values instead of sampling them, refuses a parameter that arrived more
// than once, and builds its redaction set from every arrived value.
//
// The regressions below drive the real listener over a real HTTP round trip,
// and the flow regressions drive the real authorization flow against the
// fake provider.

// rawCallbackOpener returns a browser opener that delivers a callback whose
// query is written RAW.
//
// The query is raw because the defect is about a parameter that arrives more
// than once: url.Values.Encode would drop the repetition.
func rawCallbackOpener(t *testing.T, rawQuery func(state string) string) func(context.Context, string) error {
	t.Helper()
	return func(_ context.Context, authorizationURL string) error {
		parsed, err := url.Parse(authorizationURL)
		if err != nil {
			return err
		}
		query := parsed.Query()
		target := query.Get("redirect_uri") + "?" + rawQuery(query.Get("state"))
		response, err := http.Get(target)
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
}

// deliverRawCallback delivers a raw query to a fresh listener and returns
// the outcome it produced.
func deliverRawCallback(t *testing.T, state, rawQuery string) (callbackOutcome, *callbackListener) {
	t.Helper()
	listener, err := newCallbackListener(state, time.Minute)
	if err != nil {
		t.Fatalf("newCallbackListener: %v", err)
	}
	t.Cleanup(func() { _ = listener.close() })
	response, err := http.Get(listener.redirectURI() + "?" + rawQuery)
	if err != nil {
		t.Fatalf("GET the callback: %v", err)
	}
	_ = response.Body.Close()
	select {
	case outcome := <-listener.outcomes:
		return outcome, listener
	case <-time.After(10 * time.Second):
		t.Fatal("the callback produced no outcome")
		return callbackOutcome{}, listener
	}
}

// TestCallback_DuplicateCodeCannotDiscloseTheCredential proves that a
// callback whose code parameter arrives twice cannot put the credential
// into an operator- or support-facing error.
//
// The defect was a SAMPLING defect: url.Values.Get answers the first value
// only, so a callback that carried an empty code and a credential in its
// second code looked like a PURE error. The mixed-callback refusal was
// skipped, and the credential was absent from the redaction set, so it
// reached the error description, the error text and the command's stderr.
//
// The refusal is the ambiguity refusal, the class of the provider's failure
// stays visible, and nothing is exchanged.
func TestCallback_DuplicateCodeCannotDiscloseTheCredential(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		credential string
		codeQuery  func(credential string) string
	}{
		{
			name:       "an empty first code and a credential in the second",
			credential: "synthetic-duplicate-callback-code-0123456789",
			codeQuery: func(credential string) string {
				return "&code=&code=" + url.QueryEscape(credential)
			},
		},
		{
			name:       "a short credential in the second code",
			credential: "synthetic-short-duplicate-code-abc",
			codeQuery: func(credential string) string {
				return "&code=&code=" + url.QueryEscape(credential)
			},
		},
		{
			name:       "a credential that crosses the description bound",
			credential: strings.Repeat("synthetic-bound-crossing-duplicate-code-", 12),
			codeQuery: func(credential string) string {
				return "&code=&code=" + url.QueryEscape(credential)
			},
		},
		{
			// RFC 6749 section 3.1 forbids the repetition outright, and
			// comparing the values would mean trusting the provider's
			// repetition.
			name:       "the same credential twice",
			credential: "synthetic-repeated-identical-code-0123456789",
			codeQuery: func(credential string) string {
				return "&code=" + url.QueryEscape(credential) + "&code=" + url.QueryEscape(credential)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			h := newHarness(t)
			h.authorizer.config.BrowserOpener = rawCallbackOpener(t, func(state string) string {
				return "state=" + url.QueryEscape(state) +
					testCase.codeQuery(testCase.credential) +
					"&error=access_denied" +
					"&error_description=" + url.QueryEscape("rejected: "+testCase.credential)
			})

			_, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
			if err == nil {
				t.Fatal("the registration succeeded against an ambiguous callback")
			}
			if !errors.Is(err, ErrAmbiguousCallback) {
				t.Errorf("the registration failed with %v, want the ambiguity refusal", err)
			}
			// The refusal is connector-owned: it must not travel under
			// the provider's error type, whose description field is the
			// provider's own text.
			var callbackErr *CallbackError
			if errors.As(err, &callbackErr) && callbackErr.Description != "" {
				t.Errorf("the refusal reused the provider's error type with a description: %q", callbackErr.Description)
			}
			// The credential is not disclosed, not even as a prefix.
			if strings.Contains(err.Error(), testCase.credential) {
				t.Errorf("the error discloses the credential: %q", err.Error())
			}
			if len(testCase.credential) >= 26 && strings.Contains(err.Error(), testCase.credential[:26]) {
				t.Errorf("the error discloses a prefix of the credential: %q", err.Error())
			}
			// The class of the failure stays visible.
			if !strings.Contains(err.Error(), "access_denied") {
				t.Errorf("the error does not name the provider's class: %q", err.Error())
			}
			// Nothing was exchanged.
			if exchanges := h.fake.exchangeRequests(); len(exchanges) != 0 {
				t.Errorf("the token endpoint received %d exchanges, want 0", len(exchanges))
			}
		})
	}
}

// TestCallback_DuplicateClientIDCannotDiscloseTheCredential proves the same
// property for the client id.
//
// The issued client id is a public protocol identifier, but the VALUE that
// arrives with it is the provider's, and a value that arrives in a second
// occurrence must not reach an operator-facing error either.
func TestCallback_DuplicateClientIDCannotDiscloseTheCredential(t *testing.T) {
	const credential = "synthetic-duplicate-client-id-value-0123456789"
	h := newHarness(t)
	h.authorizer.config.BrowserOpener = rawCallbackOpener(t, func(state string) string {
		return "state=" + url.QueryEscape(state) +
			"&error=access_denied" +
			"&error_description=" + url.QueryEscape("rejected for client "+credential) +
			"&client_id=oaiapp_issued_1&client_id=" + url.QueryEscape(credential)
	})

	_, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	if err == nil {
		t.Fatal("the registration succeeded against an ambiguous callback")
	}
	if !errors.Is(err, ErrAmbiguousCallback) {
		t.Errorf("the registration failed with %v, want the ambiguity refusal", err)
	}
	if strings.Contains(err.Error(), credential) {
		t.Errorf("the error discloses the client id: %q", err.Error())
	}
	if len(credential) >= 26 && strings.Contains(err.Error(), credential[:26]) {
		t.Errorf("the error discloses a prefix of the client id: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("the error does not name the provider's class: %q", err.Error())
	}
	if exchanges := h.fake.exchangeRequests(); len(exchanges) != 0 {
		t.Errorf("the token endpoint received %d exchanges, want 0", len(exchanges))
	}
}

// TestCallback_DuplicateStateIsRefused proves the precedence of the state
// check and the refusal of a repeated state.
//
// The state is validated FIRST, and it keeps that precedence: a callback
// whose FIRST state is wrong is a state mismatch, even when a later
// occurrence carries the right one. A callback whose state arrived more than
// once is refused afterwards, so a duplicated state can never be accepted
// and can never smuggle a second value past the comparison.
func TestCallback_DuplicateStateIsRefused(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		query   string
		wantErr error
	}{
		{
			name:  "the right state and another one",
			query: "state=state-1&state=other&code=code-1",
		},
		{
			name:  "the right state and an empty one",
			query: "state=state-1&state=&code=code-1",
		},
		{
			name:    "a wrong state and the right one",
			query:   "state=wrong&state=state-1&code=code-1",
			wantErr: ErrStateMismatch,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			outcome, _ := deliverRawCallback(t, "state-1", testCase.query)
			if outcome.result != nil {
				t.Errorf("the callback produced a result: %+v", outcome.result)
			}
			if outcome.err == nil {
				t.Fatal("the callback produced no error")
			}
			if testCase.wantErr != nil {
				if !errors.Is(outcome.err, testCase.wantErr) {
					t.Errorf("the callback produced the error %v, want %v", outcome.err, testCase.wantErr)
				}
				return
			}
			if !errors.Is(outcome.err, ErrAmbiguousCallback) {
				t.Errorf("the callback produced the error %v, want the ambiguity refusal", outcome.err)
			}
			// The callback carried no error code, and the refusal must
			// not claim a provider error class: an absent code is not an
			// error code.
			if strings.Contains(outcome.err.Error(), "provider_error") {
				t.Errorf("the refusal names a provider error class for a callback that carried none: %q", outcome.err.Error())
			}
		})
	}
}

// TestCallback_DuplicateErrorFieldsAreRefused proves that a repeated error
// field is refused as well.
//
// The error field is a code and the description is the provider's text: a
// callback that repeats either of them cannot be attributed to a single
// value, and the second value may carry request material.
func TestCallback_DuplicateErrorFieldsAreRefused(t *testing.T) {
	const credential = "synthetic-duplicate-description-credential-0123456789"
	for _, testCase := range []struct {
		name  string
		query string
	}{
		{
			name:  "a duplicated error code",
			query: "state=state-1&error=access_denied&error=server_error",
		},
		{
			name: "a duplicated description",
			query: "state=state-1&error=access_denied&error_description=benign" +
				"&error_description=" + url.QueryEscape("rejected "+credential),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			outcome, _ := deliverRawCallback(t, "state-1", testCase.query)
			if outcome.result != nil {
				t.Errorf("the callback produced a result: %+v", outcome.result)
			}
			if outcome.err == nil {
				t.Fatal("the callback produced no error")
			}
			if !errors.Is(outcome.err, ErrAmbiguousCallback) {
				t.Errorf("the callback produced the error %v, want the ambiguity refusal", outcome.err)
			}
			if !strings.Contains(outcome.err.Error(), "access_denied") {
				t.Errorf("the error does not name the provider's class: %q", outcome.err.Error())
			}
			if strings.Contains(outcome.err.Error(), credential) {
				t.Errorf("the error discloses the credential: %q", outcome.err.Error())
			}
		})
	}
}

// TestCallback_DuplicateScopeIsCollected proves the rule of the scope.
//
// The scope is the ONE parameter whose value is a set: RFC 6749 section 3.3
// describes it as a space-delimited list, so a provider may split it over
// several occurrences of the parameter. The arrived values are therefore
// COLLECTED and joined, and the result is the same scope set that one
// occurrence carried.
func TestCallback_DuplicateScopeIsCollected(t *testing.T) {
	outcome, _ := deliverRawCallback(t, "state-1", "state=state-1&code=code-1&scope=openid&scope=email")
	if outcome.err != nil {
		t.Fatalf("the callback produced the error %v", outcome.err)
	}
	if outcome.result == nil {
		t.Fatal("the callback produced no result")
	}
	if outcome.result.Scope != "openid email" {
		t.Errorf("the callback carried the scope %q, want %q", outcome.result.Scope, "openid email")
	}
}

// TestCallback_NormalAccessDeniedIsUnchanged proves that a WELL-FORMED
// callback is still served the way it was.
//
// The refusal of an ambiguous callback must not cost the operator the
// provider's own reason: a conforming callback carries exactly one value
// per parameter, and its error code and its description stay visible.
func TestCallback_NormalAccessDeniedIsUnchanged(t *testing.T) {
	outcome, _ := deliverRawCallback(t, "state-1",
		"state=state-1&error=access_denied&error_description="+url.QueryEscape("the user declined the sign-in"))
	var callbackErr *CallbackError
	if !errors.As(outcome.err, &callbackErr) {
		t.Fatalf("the callback produced the error %v, want an OAuth error", outcome.err)
	}
	if callbackErr.Code != "access_denied" {
		t.Errorf("the callback error carried the code %q, want access_denied", callbackErr.Code)
	}
	if callbackErr.Description != "the user declined the sign-in" {
		t.Errorf("the callback error carried the description %q", callbackErr.Description)
	}
	if outcome.result != nil {
		t.Error("the denied callback produced a result")
	}
}

// TestCallbackError_CannotDiscloseItsOwnClientID proves that the pure-error
// branch redacts the callback's OWN client id.
//
// The branch renders the provider's description, and the description may
// echo the client id the callback carried. The client id is the callback's
// own value, so it is redacted from the provider's text before the text is
// bounded.
func TestCallbackError_CannotDiscloseItsOwnClientID(t *testing.T) {
	const clientID = "oaiapp_issued_synthetic_client_id"
	outcome, _ := deliverRawCallback(t, "state-1",
		"state=state-1&error=access_denied&client_id="+url.QueryEscape(clientID)+
			"&error_description="+url.QueryEscape("rejected for client "+clientID))
	var callbackErr *CallbackError
	if !errors.As(outcome.err, &callbackErr) {
		t.Fatalf("the callback produced the error %v, want an OAuth error", outcome.err)
	}
	if strings.Contains(callbackErr.Description, clientID) {
		t.Errorf("the description discloses the callback's own client id: %q", callbackErr.Description)
	}
	if callbackErr.Code != "access_denied" {
		t.Errorf("the callback error carried the code %q, want access_denied", callbackErr.Code)
	}
}

// TestCallbackError_CannotDiscloseThePendingIssuedClientID proves that the
// PENDING issued client id is redacted from the provider's text as well.
//
// The stated policy is one policy in one direction: the issued client id is
// redacted from provider-supplied text, and the callback's copy is only the
// copy that arrived with the callback. A returning authorization whose
// callback omits the client id must not be the gap that lets the value
// through.
func TestCallbackError_CannotDiscloseThePendingIssuedClientID(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
	registered, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	issued := registered.IssuedClientID
	if issued == "" {
		t.Fatal("the registration recorded no issued client id")
	}
	// The returning authorization's callback OMITS the client id, and the
	// provider echoes the issued client id in its description.
	h.authorizer.config.BrowserOpener = rawCallbackOpener(t, func(state string) string {
		return "state=" + url.QueryEscape(state) +
			"&error=access_denied" +
			"&error_description=" + url.QueryEscape("rejected for client "+issued)
	})

	_, err = h.authorizer.Reauthorize(context.Background(), ReauthorizeRequest{ProfileID: "acct-test"})
	if err == nil {
		t.Fatal("the returning authorization succeeded against a denied callback")
	}
	if strings.Contains(err.Error(), issued) {
		t.Errorf("the error discloses the issued client id: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("the error does not name the provider's class: %q", err.Error())
	}
}

// TestCallbackListener_ReleasesTheSecretSetAtClose proves that the attempt's
// pending values are released when the attempt is over.
//
// The values are credentials. The expiry timer holds a closure that holds
// the listener, and a timer that is only SUPPRESSED fires at the end of the
// attempt's lifetime: the listener - and the values it holds - stayed alive
// until then. The listener releases the set itself when it closes.
func TestCallbackListener_ReleasesTheSecretSetAtClose(t *testing.T) {
	listener, err := newCallbackListener("state-1", time.Minute, "synthetic-pending-secret")
	if err != nil {
		t.Fatalf("newCallbackListener: %v", err)
	}
	if len(listener.secrets) == 0 {
		t.Fatal("the listener holds no pending values")
	}
	if err := listener.close(); err != nil {
		t.Fatalf("close the listener: %v", err)
	}
	if len(listener.secrets) != 0 {
		t.Errorf("the closed listener still holds %d pending values", len(listener.secrets))
	}
}

// TestReauthorize_ReconciliationNoticeFollowsPersistence proves that the
// reconciliation is reported when the record carries it.
//
// The notice before the attempt reports the DECISION, and the notice after
// the save reports the PERSISTED FACT. A failed sign-in leaves the stored
// host id unchanged, so a notice that claims a reconciliation before the
// save would be a claim the store does not support.
func TestReauthorize_ReconciliationNoticeFollowsPersistence(t *testing.T) {
	source := newHarness(t)
	source.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
	registered, err := source.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	destination := newHarness(t)
	if err := destination.profiles.Save(registered); err != nil {
		t.Fatalf("Save the copied registration: %v", err)
	}

	// The sign-in FAILS: the provider denies it.
	original := destination.authorizer.config.BrowserOpener
	destination.authorizer.config.BrowserOpener = rawCallbackOpener(t, func(state string) string {
		return "state=" + url.QueryEscape(state) + "&error=access_denied"
	})
	if _, err := destination.authorizer.Reauthorize(
		context.Background(),
		ReauthorizeRequest{ProfileID: "acct-test"},
	); err == nil {
		t.Fatal("the returning authorization succeeded against a denied callback")
	}
	destination.authorizer.config.BrowserOpener = original

	if reportsReconciliation(destination.noticeMessages()) {
		t.Errorf("a failed sign-in reported a persisted reconciliation: %v", destination.noticeMessages())
	}
	stored, err := destination.profiles.Load("acct-test")
	if err != nil {
		t.Fatalf("Load the registration: %v", err)
	}
	if stored.HostID != registered.HostID {
		t.Errorf("the stored registration carries the host id %q, want the source host's %q", stored.HostID, registered.HostID)
	}

	// The successful sign-in reports the reconciliation: the record
	// carries this host's identity now.
	destination.setDelivery(callbackDelivery{code: "auth-code-2", omitClientID: true})
	if _, err := destination.authorizer.Reauthorize(
		context.Background(),
		ReauthorizeRequest{ProfileID: "acct-test"},
	); err != nil {
		t.Fatalf("Reauthorize: %v", err)
	}
	if !reportsReconciliation(destination.noticeMessages()) {
		t.Errorf("the reconciliation was not reported: %v", destination.noticeMessages())
	}
}
