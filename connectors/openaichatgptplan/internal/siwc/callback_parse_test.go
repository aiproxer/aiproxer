package siwc

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

// The WHOLE query of a callback.
//
// A callback is a query the provider controls, and the query is read as a
// WHOLE: a query the parser cannot read is refused before any of its values
// is used.
//
// The defect was a LOSSY parse. r.URL.Query() calls url.ParseQuery and
// DISCARDS the error, and a discarded parse error is a LOST VALUE: a
// parameter with an invalid percent escape or an unescaped semicolon is
// silently omitted from the parsed values. The collector therefore collected
// every value that SURVIVED the parser, and both the refusal of a repeated
// parameter and the redaction set were downstream of that loss. A callback
// that carried a credential in a dropped parameter looked like a PURE error,
// the mixed-callback refusal was skipped, and the credential the provider
// echoed in its description reached the error text and the command's stderr.
//
// The regressions below drive the real listener over a real HTTP round trip,
// and the flow regressions drive the real authorization flow against the
// fake provider.

// malformedCodeQuery is a raw code parameter that the query parser cannot
// read.
type malformedCodeQuery struct {
	// name is the name of the shape.
	name string
	// query is the raw text of the code parameters.
	query string
}

// malformedCodeQueries returns the raw code parameters of a callback whose
// query the parser cannot read.
//
// The two malformations are the ones a provider's redirect can carry: an
// invalid percent escape and an unescaped semicolon. The semicolon is the
// harder one, because the parser rejects the WHOLE parameter that carries
// it, and the credential in that parameter therefore disappears from the
// parsed values instead of arriving malformed.
func malformedCodeQueries(credential string) []malformedCodeQuery {
	return []malformedCodeQuery{
		{
			name:  "an invalid percent escape in the code",
			query: "&code=" + credential + "%ZZ",
		},
		{
			name:  "an unescaped semicolon in the code",
			query: "&code=" + credential + ";ignored",
		},
		{
			name:  "an empty first code and a credential hidden in the second",
			query: "&code=&code=" + credential + ";ignored",
		},
		{
			name:  "an empty first code and a credential hidden in the second by an escape",
			query: "&code=&code=" + credential + "%ZZ",
		},
	}
}

// TestCallback_MalformedQueryCannotDiscloseTheCredential proves that a
// callback whose query the parser cannot read cannot put a credential into
// an operator- or support-facing error.
//
// The refusal is the connector's own, it carries no provider text, and the
// credential is not disclosed - not whole, and not as a prefix. Nothing is
// exchanged and nothing is persisted.
func TestCallback_MalformedQueryCannotDiscloseTheCredential(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		credential string
	}{
		{
			name:       "a full credential",
			credential: "synthetic-malformed-callback-code-0123456789",
		},
		{
			name:       "a short credential",
			credential: "synthetic-short-code-abc",
		},
		{
			// The description is longer than the bound, so a credential
			// that is not redacted leaves a LONG PREFIX of itself behind.
			name:       "a credential that crosses the description bound",
			credential: strings.Repeat("synthetic-bound-crossing-code-", 12),
		},
	} {
		for _, malformed := range malformedCodeQueries(testCase.credential) {
			t.Run(testCase.name+": "+malformed.name, func(t *testing.T) {
				h := newHarness(t)
				h.authorizer.config.BrowserOpener = rawCallbackOpener(t, func(state string) string {
					return "state=" + url.QueryEscape(state) +
						malformed.query +
						"&error=access_denied" +
						"&error_description=" + url.QueryEscape("rejected: "+testCase.credential)
				})

				_, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
				if err == nil {
					t.Fatal("the registration succeeded against a malformed callback")
				}
				// The credential is not disclosed, not even as a prefix.
				if strings.Contains(err.Error(), testCase.credential) {
					t.Errorf("the error discloses the credential: %q", err.Error())
				}
				if len(testCase.credential) >= 26 && strings.Contains(err.Error(), testCase.credential[:26]) {
					t.Errorf("the error discloses a prefix of the credential: %q", err.Error())
				}
				// The provider's own text does not reach the error: the
				// refusal is connector-owned.
				if strings.Contains(err.Error(), "rejected") {
					t.Errorf("the error carries the provider's description: %q", err.Error())
				}
				// The refusal names the malformation. The wording is the
				// connector's own, so the assertion is on the property and
				// not on an exact string.
				if !strings.Contains(strings.ToLower(err.Error()), "malformed") {
					t.Errorf("the error does not name the malformation: %q", err.Error())
				}
				// Nothing was exchanged.
				if exchanges := h.fake.exchangeRequests(); len(exchanges) != 0 {
					t.Errorf("the token endpoint received %d exchanges, want 0", len(exchanges))
				}
				// Nothing was persisted.
				profiles, listErr := h.profiles.List()
				if listErr != nil {
					t.Fatalf("list the profiles: %v", listErr)
				}
				if len(profiles) != 0 {
					t.Errorf("the malformed callback persisted %d registrations, want 0", len(profiles))
				}
			})
		}
	}
}

// TestCallback_MalformedDuplicateStateIsRefused proves that a callback whose
// state arrived more than once is refused even when one of the occurrences
// could not be parsed.
//
// The state is the attempt's ownership proof, and a repetition of it cannot
// be attributed to a single value. A malformed occurrence is dropped by the
// parser, so a callback that carried the right state and a malformed second
// state used to look like a callback with ONE state: the attempt accepted it
// and exchanged its code.
//
// The wrong-state precedence and the ambiguity refusal of a WELL-FORMED
// callback are unchanged, and the table states both next to the malformed
// cases.
func TestCallback_MalformedDuplicateStateIsRefused(t *testing.T) {
	for _, testCase := range []struct {
		name string
		// query is the raw query of the callback.
		query string
		// wantErr is the refusal a WELL-FORMED query of this shape
		// produces. It is empty for a malformed query, whose refusal is
		// the connector's own malformation refusal.
		wantErr error
	}{
		{
			name:  "a malformed second state",
			query: "state=state-1&state=wrong%ZZ&code=code-1",
		},
		{
			name:  "a malformed first state",
			query: "state=wrong%ZZ&state=state-1&code=code-1",
		},
		{
			name:  "a malformed second state with a semicolon",
			query: "state=state-1&state=wrong;ignored&code=code-1",
		},
		{
			// The well-formed shapes keep their own refusals: the wrong
			// state first is a state mismatch, and the repeated state is
			// an ambiguous parameter.
			name:    "a well-formed wrong state keeps the state precedence",
			query:   "state=wrong&state=state-1&code=code-1",
			wantErr: ErrStateMismatch,
		},
		{
			name:    "a well-formed repeated state keeps the ambiguity refusal",
			query:   "state=state-1&state=other&code=code-1",
			wantErr: ErrAmbiguousCallback,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			outcome, _ := deliverRawCallback(t, "state-1", testCase.query)
			// The callback must never become a successful result.
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
			// The refusal of a malformed query is the connector's own:
			// it names the malformation, and it is neither a state
			// mismatch nor an ambiguity refusal.
			if !strings.Contains(strings.ToLower(outcome.err.Error()), "malformed") {
				t.Errorf("the error does not name the malformation: %q", outcome.err.Error())
			}
			if errors.Is(outcome.err, ErrStateMismatch) {
				t.Errorf("the malformed query was reported as a state mismatch: %v", outcome.err)
			}
			if errors.Is(outcome.err, ErrAmbiguousCallback) {
				t.Errorf("the malformed query was reported as an ambiguous parameter: %v", outcome.err)
			}
		})
	}
}

// TestCallback_MalformedRefusalKeepsTheWellFormedObservability proves that
// the refusal of a malformed query does not swallow the observability of a
// WELL-FORMED one.
//
// The provider's error code is a class of failure and its description is the
// provider's own reason: a conforming callback carries exactly one value
// per parameter, and both stay visible. The refusal of a malformed query
// must not cost the operator the reason of a refusal the provider sent.
func TestCallback_MalformedRefusalKeepsTheWellFormedObservability(t *testing.T) {
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
