package siwc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
)

// The loopback callback of one authorization attempt.
//
// The listener is the provider's return path: the browser is redirected to
// the URI the authorization request carried, and the provider delivers the
// authorization code, the issued client id and the state there.
//
// Three rules shape the implementation.
//
// The first is the ORDER of the checks. The state is validated FIRST, before
// the OAuth error is read and before the code is used. A callback that
// cannot prove it belongs to the attempt must not influence it at all.
//
// The second is the URI. The listener reports the exact URI it serves, and
// that string is what the authorization request carries and what the code
// exchange carries. The listener never rebuilds it: the port is the one the
// operating system assigned, the host is the literal loopback address, and
// the path is the fixed callback path.
//
// The third is the ANSWER. Every response the listener writes is
// length-delimited and complete before the attempt's result is delivered,
// so the browser never reads a truncated response when the attempt closes
// its listener the moment the result arrives.
//
// A fourth rule follows from the first three: the query is read as a WHOLE.
// A query that cannot be parsed is refused before any of its values is used,
// because a value the parser dropped is a value no check and no redaction
// would ever see.

// callbackShutdownTimeout bounds the graceful shutdown of the listener.
//
// The shutdown waits for the handler that is still writing its answer, and
// the bound keeps a stalled connection from holding the attempt open.
const callbackShutdownTimeout = 500 * time.Millisecond

// callbackOutcome is what the listener produced for one callback request.
type callbackOutcome struct {
	result *CallbackResult
	err    error
}

// CallbackResult is the validated outcome of the loopback callback.
//
// The code is a short-lived credential: it is exchanged once and never
// stored, logged or reported. The formatter below keeps it out of a
// formatted value; the documented bypasses are the ones ProfileRecord
// documents.
type CallbackResult struct {
	// Code is the authorization code.
	Code string
	// IssuedClientID is the client id the callback carried, if any. It is
	// empty on a returning authorization that omitted it.
	IssuedClientID string
	// Scope is the space-separated scope the callback carried, if any.
	Scope string
}

// String renders the result without its authorization code.
//
// The issued client id is NOT rendered either. The policy is ONE policy in
// ONE direction: the issued client id is a value this connector redacts
// from provider-supplied text, so it is redacted from the connector's own
// rendering as well. A rendering that showed it while the token-error path
// hid it would make the two paths disagree about the same value.
func (r CallbackResult) String() string {
	return fmt.Sprintf(
		"callback result (%d scopes)",
		len(strings.Fields(r.Scope)),
	)
}

// GoString renders the result without its authorization code.
func (r CallbackResult) GoString() string { return r.String() }

// Format implements fmt.Formatter, so that no formatting verb can reach the
// result's fields.
//
// The guarantee holds when fmt can dispatch to this result, so the routes
// where it cannot are the ones ProfileRecord documents.
func (r CallbackResult) Format(state fmt.State, verb rune) {
	safe := r.String()
	if verb == 'q' {
		_, _ = io.WriteString(state, strconv.Quote(safe))
		return
	}
	_, _ = io.WriteString(state, safe)
}

// CallbackError is an OAuth error the provider reported in the callback.
//
// The provider's error code is preserved: "access_denied" is a decision of
// the user, and it is reported as such rather than as a generic failure.
type CallbackError struct {
	// Code is the provider's OAuth error code.
	Code string
	// Description is the provider's bounded error description.
	Description string
}

// Error renders the failure without credential material.
func (e *CallbackError) Error() string {
	if e.Description == "" {
		return fmt.Sprintf("siwc: the authorization failed: %s", e.Code)
	}
	return fmt.Sprintf("siwc: the authorization failed: %s: %s", e.Code, e.Description)
}

// callbackListener serves the loopback callback of one attempt.
//
// The listener is bound to the literal loopback address on an ephemeral
// port, and it serves exactly one path.
type callbackListener struct {
	server   *http.Server
	listener net.Listener
	state    string
	// secrets are the pending values of the attempt. The listener renders
	// provider-supplied text in its own responses, so the values are
	// removed from that text before it is shown.
	//
	// The set is passed at construction, so the listener never learns a
	// value after it started serving. A value that arrived later would be
	// a value the serving goroutine reads while another goroutine writes
	// it.
	//
	// The set is RELEASED when the listener closes: the values are
	// credentials, and an attempt that is over must not keep them alive.
	// The mutex makes the release safe against the handler goroutine,
	// which reads the set while it renders provider-supplied text.
	secretsMu sync.RWMutex
	secrets   []string

	// outcomes carries the single result of the attempt.
	outcomes chan callbackOutcome
	// expired is closed when the attempt outlived its lifetime.
	expired chan struct{}
	// closed is closed when the listener stopped serving.
	closed chan struct{}

	closeOnce  sync.Once
	expireOnce sync.Once
	// stopExpiry tells the expiry timer that the attempt is over.
	//
	// The channel covers the window in which the timer was ALREADY
	// firing when the attempt closed: a timer that fires after the close
	// must not run its action. The timer itself is stopped through
	// expiryTimer, and the channel alone would only SUPPRESS the action
	// of a timer that still fires at the end of the attempt's lifetime.
	stopExpiry chan struct{}
	// expiryTimer is the attempt's expiry timer.
	//
	// The timer is stored so that the close can STOP it, and not only
	// suppress its action. A suppressed timer still fires at the end of
	// the attempt's lifetime, and the closure it holds keeps this
	// listener - and with it the attempt's pending values - alive until
	// then.
	//
	// The pointer is ATOMIC because the timer's own goroutine may run
	// before the constructor stores the timer: a plain field would be a
	// field that one goroutine writes while another reads it.
	expiryTimer atomic.Pointer[time.Timer]
}

// newCallbackListener starts the loopback listener of one attempt.
//
// The listener binds an ephemeral port on the literal loopback address, so
// two concurrent attempts never share a port. The URI it reports is the
// exact string the authorization request and the code exchange must carry.
//
// The listener starts serving before it is returned, so the caller can open
// the browser immediately afterwards: the provider may redirect to the
// callback before the browser is even visible.
//
// The secrets are the attempt's pending values, and they are passed HERE,
// at construction. The listener renders provider-supplied text in its own
// responses, and it therefore has to know the attempt's values BEFORE it
// serves the first request: a value that is assigned after the server
// started serving is a value the serving goroutine reads while another
// goroutine writes it.
func newCallbackListener(state string, ttl time.Duration, secrets ...string) (*callbackListener, error) {
	if strings.TrimSpace(state) == "" {
		return nil, errors.New("siwc: the callback listener needs the attempt's state")
	}
	if ttl <= 0 {
		ttl = DefaultAttemptTTL
	}
	address := net.JoinHostPort(LoopbackHost, "0")
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("siwc: start the loopback callback listener: %w", err)
	}
	loopback := &callbackListener{
		listener:   listener,
		state:      state,
		secrets:    append([]string(nil), secrets...),
		outcomes:   make(chan callbackOutcome, 1),
		expired:    make(chan struct{}),
		closed:     make(chan struct{}),
		stopExpiry: make(chan struct{}),
	}
	loopback.server = &http.Server{
		Handler:           loopback,
		ReadHeaderTimeout: 10 * time.Second,
		// The callback is one request; a long-lived connection has no
		// purpose and would keep the attempt's port open.
		IdleTimeout: 30 * time.Second,
		// The server's own diagnostics are NOT rendered. net/http reports a
		// malformed request with the raw request line, and the request
		// line of a callback can carry the authorization code. The
		// listener therefore replaces the server's text with a fixed,
		// secret-free message.
		ErrorLog: log.New(safeListenerLog{out: log.Default().Writer()}, "", 0),
	}
	// The attempt serves ONE request and is closed right after it. The
	// connection is therefore not kept alive: the response carries
	// "Connection: close" and the server closes the connection itself once
	// the response is written. A closed attempt can then never truncate
	// the answer the browser is reading.
	loopback.server.SetKeepAlivesEnabled(false)
	go func() {
		_ = loopback.server.Serve(listener)
	}()
	// The attempt is bounded in time: an abandoned attempt must not keep
	// its port open.
	//
	// The timer is STOPPED by the close of the listener, and the timer
	// value is stored in an ATOMIC pointer: the timer's own goroutine
	// starts before the constructor can store the result, and a plain
	// field would be a field that one goroutine writes while another
	// reads it.
	loopback.expiryTimer.Store(time.AfterFunc(ttl, func() {
		// An attempt that already finished is not expired: the timer is
		// stopped by the close, and this check covers the window in
		// which the timer was already firing.
		select {
		case <-loopback.stopExpiry:
			return
		default:
		}
		loopback.expireOnce.Do(func() { close(loopback.expired) })
		_ = loopback.close()
	}))
	return loopback, nil
}

// redirectURI returns the exact callback URI this listener serves.
//
// The URI is built from the address the operating system assigned and from
// the fixed callback path. The host is the literal loopback address and
// never the name "localhost", and the port is the one this listener really
// holds.
func (l *callbackListener) redirectURI() string {
	port := 0
	if address, ok := l.listener.Addr().(*net.TCPAddr); ok {
		port = address.Port
	}
	if port == 0 {
		// The address is not a TCP address, so the port is read from the
		// textual form. The value is still the one the listener holds.
		if _, rawPort, err := net.SplitHostPort(l.listener.Addr().String()); err == nil {
			port, _ = strconv.Atoi(rawPort)
		}
	}
	return "http://" + net.JoinHostPort(LoopbackHost, strconv.Itoa(port)) + CallbackPath
}

// callbackParameters is every value that arrived for the protocol
// parameters of one callback.
//
// The values are COLLECTED and not SAMPLED. url.Values.Get answers the
// first value only, so a callback that repeats a parameter would be read as
// if it carried one value: the values behind the first one - which may be a
// credential the provider echoed - would be invisible to the refusal that
// follows and absent from the redaction set.
//
// The collection is therefore the source of BOTH: the ambiguity check reads
// it, and the redaction set is built from it.
//
// The collection is COMPLETE: it is built from a query that was parsed in
// full. A query the parser could not read is refused before the collection
// is built, so every value in the collection is a value that ARRIVED.
type callbackParameters struct {
	state            []string
	code             []string
	clientID         []string
	errorCode        []string
	errorDescription []string
	scope            []string
}

// collectCallbackParameters collects every arrived value of the protocol
// parameters a callback may carry.
//
// The scope is collected as well, and it is NOT a singleton: RFC 6749
// section 3.3 describes the scope as a space-delimited LIST, so a provider
// may legitimately split it over several occurrences of the parameter. The
// scope values are therefore JOINED rather than refused.
func collectCallbackParameters(query url.Values) callbackParameters {
	return callbackParameters{
		state:            query["state"],
		code:             query["code"],
		clientID:         query["client_id"],
		errorCode:        query["error"],
		errorDescription: query["error_description"],
		scope:            query["scope"],
	}
}

// ambiguousParameter returns the name of the protocol parameter that
// arrived more than once, or an empty string.
//
// RFC 6749 section 3.1 forbids a repeated parameter, so a conforming
// callback carries exactly one value per parameter. A callback that repeats
// one cannot be attributed to a single value - which of the arrived values
// the provider meant is not established - and the repetition is refused
// even when the values are equal: comparing them would mean trusting the
// provider's repetition.
//
// The scope is not part of the check: its value is a set.
func (p callbackParameters) ambiguousParameter() string {
	for _, candidate := range []struct {
		name   string
		values []string
	}{
		{name: "state", values: p.state},
		{name: "code", values: p.code},
		{name: "client_id", values: p.clientID},
		{name: "error", values: p.errorCode},
		{name: "error_description", values: p.errorDescription},
	} {
		if len(candidate.values) > 1 {
			return candidate.name
		}
	}
	return ""
}

// single returns the value that arrived for a parameter, or an empty
// string.
//
// The caller has already refused an ambiguous parameter, so the slice holds
// at most one value and the first one is THE value.
func (p callbackParameters) single(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// errorCodeValue returns the provider's error code, trimmed.
func (p callbackParameters) errorCodeValue() string {
	return strings.TrimSpace(p.single(p.errorCode))
}

// codeValue returns the authorization code, trimmed.
func (p callbackParameters) codeValue() string {
	return strings.TrimSpace(p.single(p.code))
}

// clientIDValue returns the issued client id, trimmed.
func (p callbackParameters) clientIDValue() string {
	return strings.TrimSpace(p.single(p.clientID))
}

// descriptionValue returns the provider's error description.
func (p callbackParameters) descriptionValue() string {
	return p.single(p.errorDescription)
}

// classifiedErrorCode classifies the provider's error code of the callback.
//
// An ABSENT error code yields an EMPTY code. classifyProviderErrorCode
// answers "provider_error" for an empty value, and that class describes an
// UNKNOWN error, not the absence of one.
//
// The FIRST arrived value is classified when the parameter was ambiguous:
// the value is a CODE, it is classified against the documented set, and the
// refusal of the ambiguity must not hide the class of the failure.
func (p callbackParameters) classifiedErrorCode() string {
	if len(p.errorCode) == 0 {
		return ""
	}
	return classifyProviderErrorCode(p.errorCode[0])
}

// scopeValue returns the scope set the callback carried.
//
// The scope is a SET: RFC 6749 section 3.3 describes it as a space-delimited
// list, and a provider may split it over several occurrences of the
// parameter. The arrived values are therefore JOINED with a space, and the
// result is the same scope set as the one occurrence carried.
func (p callbackParameters) scopeValue() string {
	parts := make([]string, 0, len(p.scope))
	for _, value := range p.scope {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " ")
}

// credentialValues returns every arrived value that may carry a credential.
//
// The set is the COMPLETE set of arrived values: the codes and the client
// ids, in EVERY occurrence. A value that arrived after the attempt was
// built is not in the attempt's set, and an enumeration that is built once
// can never be complete for the values that arrive after it.
//
// The values are passed as they arrived. The redaction trims a secret
// itself, and the raw value is the one the provider echoed into its own
// text.
func (p callbackParameters) credentialValues() []string {
	values := make([]string, 0, len(p.code)+len(p.clientID))
	values = append(values, p.code...)
	values = append(values, p.clientID...)
	return values
}

// errMalformedCallback is the refusal of a callback whose query could not
// be parsed.
//
// The text is FIXED and CONNECTOR-OWNED: it names the malformation, and it
// carries no value, no parameter name and no provider text.
//
// The parser's own error is deliberately NEITHER rendered NOR wrapped: the
// parser quotes the input it rejected, and the rejected input of a callback
// can be a credential.
var errMalformedCallback = errors.New("siwc: the callback carried a malformed query")

// ServeHTTP handles one callback request.
//
// The path is checked first: the listener serves one path, and a request
// for another path is not a callback at all. The state is checked next,
// before anything in the query is used. A query the parser could not read
// is refused right after it, before the query's values are collected and
// before any provider text is rendered.
func (l *callbackListener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != CallbackPath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET")
		http.Error(w, "the callback accepts GET", http.StatusMethodNotAllowed)
		return
	}
	// The query is parsed EXPLICITLY, and the parser's error is KEPT.
	//
	// r.URL.Query() calls url.ParseQuery and DISCARDS the error, and a
	// discarded parse error is a LOST VALUE: a parameter with an invalid
	// percent escape or an unescaped semicolon is silently omitted from
	// the parsed values. The collection below would then hold only the
	// values that SURVIVED the parser, and a credential the provider
	// echoed in a dropped parameter would be absent from BOTH the refusal
	// of a repeated parameter and the redaction set.
	query, queryErr := url.ParseQuery(r.URL.RawQuery)

	// The state is validated BEFORE the result is read. A callback that
	// does not belong to this attempt is refused, and the attempt ends:
	// the attempt is bound to a fresh ephemeral port and a fresh state, and
	// a request that cannot prove it belongs to the attempt is a security
	// event rather than a hint that the user should wait longer.
	if err := oauthcred.ValidateState(l.state, query.Get("state")); err != nil {
		writeCallbackResponse(
			w,
			http.StatusBadRequest,
			"text/plain; charset=utf-8",
			"the callback does not belong to this authorization attempt",
		)
		l.deliver(callbackOutcome{err: fmt.Errorf("%w: %v", ErrStateMismatch, err)})
		return
	}

	// A query that could not be parsed is REFUSED before any of its values
	// is used.
	//
	// The parser reports the FIRST problem it found and then continues, so
	// the parsed values are a PARTIAL view of the query: every parameter
	// that failed to parse is missing from them. A partial view is not the
	// query the provider sent, and the values it holds are not established
	// either - a parameter that arrived twice may have had one of its
	// occurrences dropped, and the value that is left would then be read
	// as THE value.
	//
	// The refusal therefore precedes the collection, the ambiguity
	// refusal and the branch: no partially parsed value is ever used for
	// the attempt's outcome, and no provider text is ever rendered.
	//
	// The refusal is FIXED and CONNECTOR-OWNED: it names the malformation
	// and it carries no value, no parameter and no provider text. The
	// parser's own error is deliberately NOT rendered and NOT wrapped: it
	// quotes the input it rejected, and the rejected input of a callback
	// can be a credential.
	if queryErr != nil {
		writeCallbackResponse(
			w,
			http.StatusBadRequest,
			"text/plain; charset=utf-8",
			"the callback carried a malformed query",
		)
		l.deliver(callbackOutcome{err: errMalformedCallback})
		return
	}

	// The callback's values are COLLECTED, not sampled.
	//
	// url.Values.Get answers the FIRST value only, so a callback that
	// carries the same parameter twice is read as if it carried one
	// value: the values behind the first one - which may be a credential
	// the provider echoed - are invisible to the refusal below and absent
	// from the redaction set. The collection is the source of BOTH: the
	// ambiguity check reads it, and the redaction set is built from it.
	//
	// The collection is COMPLETE: the query above was parsed in full, and a
	// query that could not be parsed was refused before this line. Every
	// value in the collection is therefore a value that ARRIVED.
	arrived := collectCallbackParameters(query)

	// A repeated parameter is REFUSED before any provider text is
	// rendered.
	//
	// RFC 6749 section 3.1 forbids a repeated parameter: a conforming
	// callback carries exactly one value per parameter. A callback that
	// repeats one cannot be attributed to a single value - which of the
	// arrived values the provider meant is not established - so its
	// authority cannot be established either, and the attempt is refused.
	//
	// The refusal is fixed and secret-free: it names the protocol
	// parameter and the classified provider code, and it carries no
	// provider text at all. No code is exchanged.
	if parameter := arrived.ambiguousParameter(); parameter != "" {
		writeCallbackResponse(
			w,
			http.StatusBadRequest,
			"text/plain; charset=utf-8",
			"the callback carried an ambiguous parameter",
		)
		l.deliver(callbackOutcome{err: &AmbiguousCallbackError{
			Parameter:    parameter,
			ProviderCode: arrived.classifiedErrorCode(),
		}})
		return
	}

	// The callback's credential-bearing values are read ONCE, BEFORE the
	// branch. They ARRIVE from the provider, so they are untrusted until
	// they are classified, and every rendering path below has to be able
	// to remove them: a value that is only read in the success branch is
	// a value the error branch cannot redact.
	errorCode := arrived.errorCodeValue()
	authorizationCode := arrived.codeValue()
	clientID := arrived.clientIDValue()

	// An OAuth error stops the attempt. No code is exchanged, and the
	// provider's own reason is reported.
	if errorCode != "" {
		// The browser's answer is the same in both cases: the sign-in was
		// not completed, and the answer carries no provider text.
		writeCallbackResponse(
			w,
			http.StatusOK,
			"text/html; charset=utf-8",
			callbackPage("The sign-in was not completed. You can close this window."),
		)
		// A callback that carries BOTH an error and a code is REFUSED with
		// a fixed, classified message. A provider that answers an error
		// and echoes the authorization code in its description would
		// otherwise put the credential into an operator- or support-facing
		// error, and the bound on the description would not save it: a
		// code that crosses the bound leaves a prefix behind.
		//
		// The classified code is preserved, because the recovery differs
		// per code, and no provider text reaches the error at all.
		if authorizationCode != "" {
			l.deliver(callbackOutcome{err: &CallbackError{
				Code:        classifyProviderErrorCode(errorCode),
				Description: "",
			}})
			return
		}
		// Defence in depth: EVERY arrived code value and EVERY arrived
		// client id value are redacted from the description, even in the
		// pure error branch. The redaction set is derived from the data
		// actually in hand, so an arrived credential is in the set that
		// redacts it - even if the refusal above were relaxed later.
		description := l.safeText(arrived.descriptionValue(), arrived.credentialValues()...)
		l.deliver(callbackOutcome{err: &CallbackError{
			Code:        classifyProviderErrorCode(errorCode),
			Description: description,
		}})
		return
	}

	if authorizationCode == "" {
		writeCallbackResponse(
			w,
			http.StatusBadRequest,
			"text/plain; charset=utf-8",
			"the callback carried no authorization code",
		)
		l.deliver(callbackOutcome{err: errors.New("siwc: the callback carried no authorization code")})
		return
	}

	writeCallbackResponse(
		w,
		http.StatusOK,
		"text/html; charset=utf-8",
		callbackPage("Sign-in completed. You can close this window."),
	)
	l.deliver(callbackOutcome{result: &CallbackResult{
		Code:           authorizationCode,
		IssuedClientID: clientID,
		Scope:          arrived.scopeValue(),
	}})
}

// safeListenerLog replaces the listener's own diagnostics with a fixed
// message.
//
// net/http reports a malformed request with the raw request line, and the
// request line of a callback can carry the authorization code. The server's
// text is therefore never rendered: the operator learns that a request was
// not served, and nothing else.
//
// The fixed message names what it can know: the listener could not serve a
// request. A handler panic is reported through the same channel, and a
// message that called every failure a malformed request would misdescribe
// it.
type safeListenerLog struct {
	out io.Writer
}

// Write replaces the server's message with a fixed one.
func (l safeListenerLog) Write(p []byte) (int, error) {
	const message = "siwc: the callback listener could not serve a request\n"
	if _, err := io.WriteString(l.out, message); err != nil {
		return 0, err
	}
	return len(p), nil
}

// safeText prepares provider-supplied text for an operator-facing error.
//
// The attempt's pending values are removed FIRST and the result is bounded
// SECOND: a bound applied first would shorten a secret beyond recognition,
// and the prefix that survives the bound would no longer match the complete
// secret.
//
// The set is the attempt's COMPLETE pending values - the state, the nonce,
// the verifier, the retained identity hints and the issued client id - PLUS
// the values the callback itself carried. The extra values are EVERY
// arrived code value and EVERY arrived client id value: they ARRIVE from
// the provider, and a value that arrives after the attempt was built is not
// in the attempt's set. The set is therefore derived at the point of use
// from the data actually in hand, instead of being a pre-computed
// enumeration that a later value can escape.
func (l *callbackListener) safeText(text string, extra ...string) string {
	pending := l.secretSet()
	secrets := make([]string, 0, len(pending)+len(extra)+1)
	secrets = append(secrets, l.state)
	secrets = append(secrets, pending...)
	secrets = append(secrets, extra...)
	return safeProviderText(text, maxErrorDescriptionLength, secrets...)
}

// secretSet returns the pending values the listener holds.
//
// The set is read under the lock, so the release at the close cannot tear
// it. The slice itself is never mutated: the release REPLACES it, so a
// reader that already took the slice keeps a complete set.
func (l *callbackListener) secretSet() []string {
	l.secretsMu.RLock()
	defer l.secretsMu.RUnlock()
	return l.secrets
}

// releaseSecrets releases the pending values the listener holds.
//
// The values are credentials, and the attempt is over: the listener must
// not keep them alive. The release happens after the graceful shutdown,
// when no handler is running any more, so a handler can never build its
// redaction set from an emptied one.
func (l *callbackListener) releaseSecrets() {
	l.secretsMu.Lock()
	defer l.secretsMu.Unlock()
	l.secrets = nil
}

// deliver hands one outcome to the attempt.
//
// The delivery never blocks: the browser is waiting for the response, and
// the attempt's channel holds one outcome. A second callback - a reload, a
// prefetch - is dropped.
func (l *callbackListener) deliver(outcome callbackOutcome) {
	select {
	case l.outcomes <- outcome:
	default:
	}
}

// close stops the listener.
//
// The shutdown is GRACEFUL first: a handler that is still writing its
// answer is allowed to finish, so the browser reads a complete response.
// The bound keeps a stalled connection from holding the attempt open, and
// the hard close that follows releases the remaining connections.
//
// The expiry timer is STOPPED. An attempt that already delivered its
// outcome must not be overtaken by the expiry of its own timer: the
// outcome and the expiry race in the attempt's select, and the expiry
// would report a failure for an attempt that succeeded.
//
// Stopping the timer also ends the RETENTION it caused: the timer's
// closure holds this listener, and a timer that is only SUPPRESSED would
// keep that closure - and with it the attempt's pending values - alive
// until the attempt's lifetime passes.
//
// The pending values are RELEASED once the shutdown is complete. They are
// credentials, and an attempt that is over must not keep them alive.
//
// It is safe to call more than once.
func (l *callbackListener) close() error {
	var err error
	l.closeOnce.Do(func() {
		// The timer is stopped through the stored timer value, and the
		// close of the channel is the signal for the window in which the
		// timer was already firing.
		if timer := l.expiryTimer.Load(); timer != nil {
			timer.Stop()
		}
		close(l.stopExpiry)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), callbackShutdownTimeout)
		defer cancel()
		if shutdownErr := l.server.Shutdown(shutdownCtx); shutdownErr != nil {
			// The shutdown did not complete: a handler may still be
			// running, and a handler that renders provider-supplied text
			// must keep its redaction set. The values are released with
			// the listener itself.
			err = l.server.Close()
		} else {
			// The shutdown is complete: no handler is running any more.
			l.releaseSecrets()
		}
		close(l.closed)
	})
	return err
}

// writeCallbackResponse answers the browser with a COMPLETE response.
//
// The response is LENGTH-DELIMITED: the Content-Length header states the
// exact size of the body. A response without that header is framed as a
// chunked stream, and the final chunk is written by the server after the
// handler returns. The attempt may close the listener the moment its
// result is delivered, and a chunked response would then be truncated
// before the browser read it: the browser would see a failed load.
//
// With the length stated, the body is complete on the wire as soon as it is
// flushed, and the answer survives the close of the attempt.
//
// The body carries no credential material.
func writeCallbackResponse(w http.ResponseWriter, status int, contentType, body string) {
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
	flushResponse(w)
}

// callbackPage renders the page the browser shows.
//
// The page carries no credential material: it tells the operator that the
// sign-in finished, and nothing else.
func callbackPage(message string) string {
	return "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>Sign in with ChatGPT</title></head>" +
		"<body><p>" + message + "</p></body></html>"
}

// flushResponse writes the response to the browser before the attempt
// delivers its outcome.
//
// The delivery ends the attempt, and the attempt closes its listener. The
// answer therefore has to be on the wire before the delivery, or the
// browser would read a truncated response.
func flushResponse(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
