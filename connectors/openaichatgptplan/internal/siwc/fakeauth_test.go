package siwc

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The fake authorization server of this package's tests.
//
// It stands in for OpenAI's authorization and token endpoints. It records
// the EXACT authorization request it received and the EXACT form-encoded
// exchange it received, and it refuses an exchange whose redirect_uri
// differs from the one the authorization request carried.
//
// The exchange therefore travels a real HTTP round trip. A stubbed
// function could not prove the two central properties of this task: that
// the exchange carries the issued client id, and that it carries the very
// callback URI the authorization request carried.
//
// ACCEPTED RESIDUAL - this server is a TEST DOUBLE and nothing else. It
// is not a provider, it is not a component of the connector, and it is not
// reachable outside the test binary: the connector's production code never
// talks to it, and the endpoints it answers are the ones the tests
// override. The rule is stated here so that the next reader does not
// re-open it as an unknown: the fake's own answers are synthetic, and the
// properties this task proves are properties of the connector's requests,
// not of the server that receives them.
type fakeAuthServer struct {
	server *httptest.Server

	mu             sync.Mutex
	authorizations []url.Values
	exchanges      []url.Values
	issuedClientID string
	invalidGrants  int
	rejectMismatch bool
	tokenCounter   int
	// errorBody is the raw body the token endpoint answers a failed
	// exchange with. It is empty while the endpoint answers the documented
	// error shape.
	errorBody string
	// errorStatus is the HTTP status of a failed exchange.
	errorStatus int
	// scope overrides the granted scope set of a successful exchange. An
	// empty value means the documented scope set.
	scope string
	// tokenBody overrides the raw body the token endpoint answers a
	// successful exchange with. An empty value means the documented body.
	tokenBody string
}

// newFakeAuthServer starts the fake server.
func newFakeAuthServer(t *testing.T) *fakeAuthServer {
	t.Helper()
	fake := &fakeAuthServer{issuedClientID: "oaiapp_issued_1"}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.route))
	t.Cleanup(fake.server.Close)
	return fake
}

// route dispatches the two documented endpoints.
func (f *fakeAuthServer) route(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/authorize"):
		f.handleAuthorize(w, r)
	case strings.HasSuffix(r.URL.Path, "/oauth/token"):
		f.handleToken(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleAuthorize records the authorization request.
func (f *fakeAuthServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.authorizations = append(f.authorizations, r.URL.Query())
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("<html><body>authorize</body></html>"))
}

// handleToken records the exchange and answers it.
//
// The exchange is refused when the redirect_uri differs from the one the
// authorization request carried: that is the provider's own rule, and it
// is what makes a reconstructed or mutated callback URI detectable.
func (f *fakeAuthServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the exchange carried an unreadable form", http.StatusBadRequest)
		return
	}
	form := r.PostForm

	f.mu.Lock()
	f.exchanges = append(f.exchanges, form)
	grants := f.invalidGrants
	if grants > 0 {
		f.invalidGrants--
	}
	reject := f.rejectMismatch
	expected := ""
	if len(f.authorizations) > 0 {
		expected = f.authorizations[len(f.authorizations)-1].Get("redirect_uri")
	}
	f.tokenCounter++
	counter := f.tokenCounter
	errorBody := f.errorBody
	errorStatus := f.errorStatus
	scope := f.scope
	override := f.tokenBody
	f.mu.Unlock()

	if errorBody != "" {
		status := errorStatus
		if status == 0 {
			status = http.StatusBadRequest
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(errorBody))
		return
	}
	if grants > 0 {
		writeTokenError(w, http.StatusBadRequest, "invalid_grant", "the authorization code is not valid")
		return
	}
	if reject && expected != "" && form.Get("redirect_uri") != expected {
		writeTokenError(w, http.StatusBadRequest, "invalid_grant", "the redirect_uri does not match the authorization request")
		return
	}
	granted := scope
	if granted == "" {
		granted = "chatgpt.tokens.use.direct email offline_access openid profile resource.invoke"
	}
	payload := map[string]any{
		"access_token":  "access-token-" + strconv.Itoa(counter),
		"refresh_token": "refresh-token-" + strconv.Itoa(counter),
		"id_token":      "id-token-" + strconv.Itoa(counter),
		"token_type":    "Bearer",
		"expires_in":    3600,
		"scope":         granted,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "the exchange could not be answered", http.StatusInternalServerError)
		return
	}
	if override != "" {
		body = []byte(override)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// writeTokenError answers with the provider's OAuth error shape.
func writeTokenError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             code,
		"error_description": description,
	})
}

// recordAuthorization records the authorization request the browser would
// have sent for the given URL.
func (f *fakeAuthServer) recordAuthorization(rawURL string) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authorizations = append(f.authorizations, parsed.Query())
}

// authorizationRequests returns the recorded authorization requests.
func (f *fakeAuthServer) authorizationRequests() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.authorizations...)
}

// exchangeRequests returns the recorded exchanges.
func (f *fakeAuthServer) exchangeRequests() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.exchanges...)
}

// setScope replaces the granted scope set a successful exchange answers
// with.
//
// An empty value restores the documented scope set.
func (f *fakeAuthServer) setScope(scope string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scope = scope
}

// setTokenBody replaces the raw body a successful exchange answers with.
//
// An empty value restores the documented body.
func (f *fakeAuthServer) setTokenBody(body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenBody = body
}

// callbackDelivery is what the simulated browser delivers to the callback.
type callbackDelivery struct {
	// code is the authorization code the provider hands over.
	code string
	// clientID is the issued client id the callback carries. An empty value
	// with omitClientID unset is delivered as an empty parameter, which the
	// provider would not do; use omitClientID to leave it out.
	clientID string
	// omitClientID leaves the client id out of the callback, which a
	// returning authorization may do.
	omitClientID bool
	// scope is the scope the callback carries.
	scope string
	// state overrides the state the callback carries, which is how a
	// mismatching callback is produced.
	state string
	// errorCode is the OAuth error the callback carries.
	errorCode string
	// errorDescription is the description the callback carries with the
	// error. A provider may describe its own rejection, and the
	// description may echo request material back.
	errorDescription string
}

// deliverCallback performs the callback the provider would perform.
//
// The simulated browser first requests the authorization URL, which is what
// the provider records, and then it follows the redirect to the callback
// URI and delivers the query the provider would send.
//
// The delivery error is returned as well as reported: a truncated answer is
// an observation of the test, not only a test failure.
func (f *fakeAuthServer) deliverCallback(t *testing.T, authorizationURL string, delivery callbackDelivery) error {
	t.Helper()
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Errorf("parse the authorization URL: %v", err)
		return err
	}
	authorization, err := http.Get(authorizationURL)
	if err != nil {
		t.Errorf("request the authorization URL: %v", err)
		return err
	}
	_ = authorization.Body.Close()
	query := parsed.Query()
	redirectURI := query.Get("redirect_uri")
	if redirectURI == "" {
		t.Error("the authorization request carried no redirect_uri")
		return errors.New("the authorization request carried no redirect_uri")
	}
	state := delivery.state
	if state == "" {
		state = query.Get("state")
	}
	values := url.Values{}
	values.Set("state", state)
	if delivery.errorCode != "" {
		values.Set("error", delivery.errorCode)
		if delivery.errorDescription != "" {
			values.Set("error_description", delivery.errorDescription)
		}
	}
	if delivery.code != "" {
		values.Set("code", delivery.code)
	}
	if !delivery.omitClientID {
		values.Set("client_id", delivery.clientID)
	}
	if delivery.scope != "" {
		values.Set("scope", delivery.scope)
	}
	target := redirectURI + "?" + values.Encode()
	response, err := http.Get(target)
	if err != nil {
		t.Errorf("deliver the callback: %v", err)
		return err
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		// A truncated answer is the failure this delivery reports: the
		// attempt closed the listener before the response was framed.
		t.Errorf("read the callback answer: %v", readErr)
		return readErr
	}
	if len(body) == 0 {
		t.Error("the callback answer was empty")
		return errors.New("the callback answer was empty")
	}
	return nil
}
