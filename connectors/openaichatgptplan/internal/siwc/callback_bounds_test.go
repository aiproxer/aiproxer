package siwc

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The callback handler renders provider-supplied text in one place: the
// description of a CallbackError. These tests pin the boundary of that
// rendering with deterministic inputs, instead of searching arbitrary
// answers for arbitrary values:
//
//   - the bound is measured in CHARACTERS, so valid multibyte text survives
//     and a longer text is shortened without splitting a character;
//   - the truncation suffix is connector-owned, so it is not an echo of an
//     arrived value;
//   - the browser's answer is fixed, so a credential that happens to equal
//     fixed page text cannot be echoed by the answer;
//   - the attempt's pending values are removed from the provider's text.
//
// Parser and listener tests cover deterministic malformed-input and boundary
// cases; the untrusted document and token readers have separate fuzz targets.

// TestCallbackDescriptionIsBoundedByRunes proves that the provider's
// description is bounded by characters, not bytes.
//
// A description of exactly maxErrorDescriptionLength emoji is valid text and
// is preserved unchanged. A longer one loses whole characters and gains the
// connector's own suffix.
func TestCallbackDescriptionIsBoundedByRunes(t *testing.T) {
	atBound := strings.Repeat("\U0001F600", maxErrorDescriptionLength)
	if got := deliverDeniedCallback(t, atBound, "").Description; got != atBound {
		t.Errorf("the description at the bound changed: %d characters", utf8.RuneCountInString(got))
	}

	overBound := atBound + "\U0001F600"
	want := atBound + "..."
	if got := deliverDeniedCallback(t, overBound, "").Description; got != want {
		t.Errorf("the over-bound description = %q, want %q", got, want)
	}
}

// TestCallbackTruncationSuffixIsConnectorOwned proves that the "..." suffix
// is generated text, not an echo of an arrived value.
//
// The provider's text carries no dots, so the suffix cannot come from it.
// An arrived client id of "..." and an unrelated client id produce the same
// bounded description.
func TestCallbackTruncationSuffixIsConnectorOwned(t *testing.T) {
	raw := strings.Repeat("a", 1024)
	if strings.Contains(raw, ".") {
		t.Fatal("the fixture gained dots")
	}
	want := strings.Repeat("a", maxErrorDescriptionLength) + "..."
	for _, clientID := range []string{"...", "oaiapp_issued_synthetic"} {
		if got := deliverDeniedCallback(t, raw, clientID).Description; got != want {
			t.Errorf("the description with the client id %q = %q, want %q", clientID, got, want)
		}
	}
}

// TestCallbackPageDoesNotEchoAPageShapedCredential proves that a credential
// equal to the fixed page text is not echoed by the browser's answer.
//
// The page is fixed: a page-shaped authorization code and an ordinary one
// produce the same answer. The code itself is delivered unchanged.
func TestCallbackPageDoesNotEchoAPageShapedCredential(t *testing.T) {
	const pageShaped = "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>Sign in with ChatGPT</title></head>"

	pageShapedOutcome, pageShapedBody := serveCallback(t, "state-1", nil,
		"state=state-1&code="+url.QueryEscape(pageShaped))
	ordinaryOutcome, ordinaryBody := serveCallback(t, "state-1", nil,
		"state=state-1&code="+url.QueryEscape("synthetic-authorization-code"))

	if pageShapedBody != ordinaryBody {
		t.Errorf("the page-shaped code changed the answer: %q and %q", pageShapedBody, ordinaryBody)
	}
	if !strings.Contains(ordinaryBody, "Sign-in completed") {
		t.Errorf("the answer is %q, want the fixed success page", ordinaryBody)
	}
	if pageShapedOutcome.result == nil || pageShapedOutcome.result.Code != pageShaped {
		t.Errorf("the callback delivered %q, want the page-shaped code unchanged", pageShapedOutcome.result)
	}
	if ordinaryOutcome.result == nil || ordinaryOutcome.result.Code != "synthetic-authorization-code" {
		t.Errorf("the callback delivered %q, want the ordinary code unchanged", ordinaryOutcome.result)
	}
}

// TestCallbackDescriptionCannotDiscloseThePendingValues proves that a
// provider that echoes the attempt's pending nonce and PKCE verifier cannot
// put them into an operator-facing error.
func TestCallbackDescriptionCannotDiscloseThePendingValues(t *testing.T) {
	const (
		nonce    = "synthetic-pending-nonce"
		verifier = "synthetic-pending-verifier"
	)
	callbackErr := deliverDeniedCallback(t,
		"rejected for "+nonce+" and "+verifier, "", nonce, verifier)
	for _, pending := range []string{nonce, verifier} {
		if strings.Contains(callbackErr.Description, pending) {
			t.Errorf("the description discloses the pending value %q: %q", pending, callbackErr.Description)
		}
		if strings.Contains(callbackErr.Error(), pending) {
			t.Errorf("the error discloses the pending value %q: %q", pending, callbackErr.Error())
		}
	}
}

// deliverDeniedCallback serves one pure-error callback through the real
// handler and returns the operator-facing error.
//
// The listener holds the given pending values, so a description that echoes
// one of them has to be redacted by the production path.
func deliverDeniedCallback(t *testing.T, description, clientID string, pending ...string) *CallbackError {
	t.Helper()
	values := url.Values{
		"state":             {"state-1"},
		"error":             {"access_denied"},
		"error_description": {description},
	}
	if clientID != "" {
		values.Set("client_id", clientID)
	}
	outcome, _ := serveCallback(t, "state-1", pending, values.Encode())

	var callbackErr *CallbackError
	if !errors.As(outcome.err, &callbackErr) {
		t.Fatalf("the callback produced %v, want an OAuth error", outcome.err)
	}
	if callbackErr.Code != "access_denied" {
		t.Errorf("the callback error carried the code %q, want access_denied", callbackErr.Code)
	}
	return callbackErr
}

// serveCallback serves one raw query through the real listener and returns
// the outcome and the browser's answer.
func serveCallback(t *testing.T, state string, pending []string, rawQuery string) (callbackOutcome, string) {
	t.Helper()
	listener, err := newCallbackListener(state, time.Minute, pending...)
	if err != nil {
		t.Fatalf("newCallbackListener: %v", err)
	}
	t.Cleanup(func() { _ = listener.close() })

	response, err := http.Get(listener.redirectURI() + "?" + rawQuery)
	if err != nil {
		t.Fatalf("GET the callback: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}

	select {
	case outcome := <-listener.outcomes:
		return outcome, string(body)
	case <-time.After(10 * time.Second):
		t.Fatal("the callback produced no outcome")
		return callbackOutcome{}, ""
	}
}
