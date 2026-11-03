package siwc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The refresh of a saved registration.
//
// The provider's refresh grant ROTATES: every successful refresh returns a
// replacement refresh token, and the token that was sent is consumed. Two
// refreshes of one token therefore invalidate each other, and a registration
// whose token was used twice needs a new authorization.
//
// The documentation requires the serialization in so many words: "Store and
// use the latest replacement, and serialize refreshes for the same session so
// two processes do not race a rotating token."
//
// The serialization has TWO halves, and both are needed:
//
//   - the IN-PROCESS half keeps one refresh at a time per registration
//     inside this process; and
//   - the CROSS-PROCESS half keeps one refresh at a time per registration
//     between two connector processes, which is what the lock file in the
//     protected store directory provides.
//
// The second half is not a refinement of the first. Two connector processes -
// the host's connector and an operator's login command, for example - share
// the store directory and not the process memory, and the token they would
// race is the same one.
//
// # The request
//
// The request is the documented one: a form-encoded POST to the token
// endpoint carrying the grant type, the ISSUED client id that was saved with
// this token set, the saved refresh token and the plan resource.
//
// The SCOPE is omitted, and the omission is the point: the documentation says
// to omit it so that the grant is retained. A request that carried the scope
// would ask the provider for a new grant instead of renewing the existing
// one.
//
// The issued client id comes from the REGISTRATION the refresh is for. It is
// never read out of a token response and never taken from another
// registration: the client id and the tokens of one registration belong
// together, and a refresh that carried another registration's client id would
// bind one account's credential to another account's client.

// The classes of a refresh outcome.
//
// The class names the KIND of the outcome, and the recovery differs per
// class:
//
//   - TERMINAL: the credential is unusable and the registration needs a new
//     authorization. The unusable tokens are cleared and the registration is
//     kept, so that a later sign-in can replace them. The clearing is a
//     required commit, and the outcome of THAT commit is reported
//     separately: a quarantine that could not be committed leaves the saved
//     credential in place, and the operator is told so.
//   - TRANSIENT: the provider could not be reached, or it failed. The saved
//     credentials are UNCHANGED, because a network failure says nothing about
//     the credential.
//   - INVALID_CLIENT: the provider refused the client configuration. It is
//     neither a credential fault nor a temporary one: the credentials stay,
//     and the CLIENT CONFIGURATION is what must be fixed.
//   - REJECTED: the provider refused the request for a reason that is
//     documented as neither of the above. The credentials stay, and the same
//     request must not be repeated unchanged.
//   - UNREADABLE: the provider's failure could not be read as a provider
//     error. It is NOT an absent error and NOT a temporary failure: the
//     connector does not know what the provider said, and it says so.
//   - UNCONFIRMED: the provider answered, the answer was published, and the
//     store could not confirm that the published record survives a crash.
//     The credential is NOT unusable - the record on the disk holds it - and
//     the state of the registration is INDETERMINATE.
//
// The class alone is therefore not the whole of the operator-facing
// diagnostic: two outcomes of the same class can differ in what happened to
// the LOCAL record of the provider's answer, and the commit outcome says
// which one it was.
const (
	// RefreshClassTerminal is the class of an unusable refresh token.
	RefreshClassTerminal = "terminal"

	// RefreshClassTransient is the class of a temporary failure.
	RefreshClassTransient = "transient"

	// RefreshClassInvalidClient is the class of a client-configuration
	// fault.
	RefreshClassInvalidClient = "invalid_client"

	// RefreshClassRejected is the class of a request the provider refused.
	RefreshClassRejected = "rejected"

	// RefreshClassUnreadable is the class of a failure the connector could
	// not read.
	RefreshClassUnreadable = "unreadable"

	// RefreshClassUnconfirmed is the class of a local record whose
	// DURABILITY was not confirmed.
	//
	// The provider answered and the connector published that answer, and
	// the store could not confirm that the published record survives a
	// crash. The credential is NOT unusable: the record on the disk holds
	// it, and a reader sees it.
	RefreshClassUnconfirmed = "unconfirmed"
)

// The classified failures of a refresh.
//
// They are sentinels so that a caller classifies an outcome without matching
// on message text. None of them carries credential material.
var (
	// ErrRefreshTerminal reports a refresh token the provider will not
	// accept again.
	ErrRefreshTerminal = errors.New(
		"siwc: the refresh token is unusable; the registration needs a new authorization",
	)

	// ErrRefreshTransient reports a temporary failure. The saved
	// credentials are unchanged.
	ErrRefreshTransient = errors.New(
		"siwc: the refresh failed temporarily; the saved credentials are unchanged",
	)

	// ErrRefreshInvalidClient reports a client-configuration fault.
	ErrRefreshInvalidClient = errors.New(
		"siwc: the provider refused the client configuration; the client configuration must be fixed",
	)

	// ErrRefreshRejected reports a request the provider refused.
	ErrRefreshRejected = errors.New(
		"siwc: the provider refused the refresh request",
	)

	// ErrRefreshUnreadable reports a failure the connector could not read.
	ErrRefreshUnreadable = errors.New(
		"siwc: the provider's refresh failure could not be read",
	)

	// ErrRefreshUnconfirmed reports that the local record of the provider's
	// answer was published and that its durability was not confirmed.
	ErrRefreshUnconfirmed = errors.New(
		"siwc: the local record was published but its durability was not confirmed",
	)
)

// The refresh vocabulary of the provider.
//
// The codes are the ones the documentation names for a refresh, plus the two
// OAuth codes that name a temporary condition. A code outside this set is
// not a code this connector knows, and it is never rendered: the provider's
// error field is a code, and a body that answers it with free text may carry
// request material that the provider echoed back.
var (
	// unusableRefreshCodes are the codes that report an UNUSABLE refresh
	// token.
	//
	// The documentation is explicit: "Unusable refresh token: invalid_grant
	// on refresh, invalid_refresh_token, token_expired,
	// refresh_token_expired, refresh_token_invalidated, or
	// refresh_token_reused. Clear unusable tokens and repeat OAuth with the
	// saved issued client ID."
	//
	// The set is used by the REFRESH path only. The same code on the
	// authorization-code exchange means something else - a rejected code
	// that is discarded and the sign-in is started again - and that path
	// classifies it on its own.
	unusableRefreshCodes = map[string]bool{
		"invalid_grant":             true,
		"invalid_refresh_token":     true,
		"token_expired":             true,
		"refresh_token_expired":     true,
		"refresh_token_invalidated": true,
		"refresh_token_reused":      true,
	}

	// temporaryRefreshCodes are the codes that report a temporary
	// condition.
	//
	// They are the OAuth spelling of what the documentation calls a
	// network or 5xx failure: the provider could not serve the request now,
	// and the same request may succeed later.
	temporaryRefreshCodes = map[string]bool{
		"temporarily_unavailable": true,
		"server_error":            true,
	}
)

// The commit outcomes of a failed refresh.
//
// The vocabulary is CLOSED: a commit outcome is one of these three, and the
// field that carries it never holds anything else. The outcome is a fact
// about the LOCAL state, and it is the part of the diagnostic the operator
// needs when the class alone cannot tell two outcomes apart - a provider
// that refused the token reads the same whether the refusal was recorded
// locally or not.
//
// The three outcomes keep the THREE facts of the store's publication
// boundary apart, because the boundary separates them:
//
//   - the PUBLICATION: the record was written, and a reader sees it;
//   - the DURABILITY: the write was confirmed, and a crash cannot roll it
//     back; and
//   - the SUPERSESSION: a different, newer credential is stored.
//
// A publication whose durability was not confirmed is NEITHER of the two
// states above: the record is in place, and only its persistence is
// unproven. A diagnostic that calls it a committed quarantine claims that
// the tokens are cleared, and one that calls it a failed quarantine claims
// that the registration is NOT marked. Neither claim was established, and
// the operator cannot act on either of them.
const (
	// RefreshCommitQuarantined reports that the unusable credentials were
	// cleared and the registration was marked as needing a new
	// authorization.
	//
	// The local state is the safe one: the consumed token is gone, and
	// the next refresh refuses to ask the provider with it.
	RefreshCommitQuarantined = "quarantined"

	// RefreshCommitQuarantineFailed reports that the quarantine could NOT
	// be committed.
	//
	// The local state is the UNSAFE one: the saved credential may be
	// stale, and the registration is NOT marked. The operator has to know
	// that before the registration is used again.
	RefreshCommitQuarantineFailed = "quarantine-failed"

	// RefreshCommitUnconfirmed reports that the local record was PUBLISHED
	// and that its DURABILITY was not confirmed.
	//
	// The local state is INDETERMINATE: the record is in place and holds
	// the outcome, and a crash may roll it back. The rendering therefore
	// asserts neither that the tokens are cleared nor that the
	// registration is NOT marked: neither was established.
	RefreshCommitUnconfirmed = "unconfirmed"
)

// RefreshError is the classified failure of a refresh.
//
// The class is preserved, because the recovery differs per class. The
// provider's own text is NOT rendered: the code is a code, and the
// description is redacted and bounded before it is kept.
//
// The cause is kept for classification only, and the formatter below keeps
// every formatting verb away from it.
type RefreshError struct {
	// Class names the class of the failure.
	Class string
	// StatusCode is the HTTP status, when the provider answered.
	StatusCode int
	// Code is the provider's machine-readable error code.
	//
	// The value is always one this build knows: a code outside the
	// vocabulary is reported as "provider_error" and never as the value
	// the provider sent.
	Code string
	// Description is the provider's bounded, redacted error description.
	Description string
	// Commit names the outcome of the LOCAL commit of the failure.
	//
	// The value is one of the closed commit vocabulary, and it is empty
	// when the failure did not reach a commit at all. The value is
	// constant text: it names a state of the local store and never a
	// value of it.
	Commit string
	// cause is the classified cause. It is never rendered.
	cause error
}

// NewRefreshError returns the classified failure of a refresh.
//
// The cause is preserved for classification, and it is never rendered: the
// class and the commit outcome are the whole of the operator-facing
// diagnostic.
func NewRefreshError(class string, status int, code, description string, cause error) *RefreshError {
	trimmed := strings.TrimSpace(class)
	if trimmed == "" {
		trimmed = RefreshClassUnreadable
	}
	return &RefreshError{
		Class:       trimmed,
		StatusCode:  status,
		Code:        code,
		Description: description,
		cause:       cause,
	}
}

// SetCommit records the outcome of the local commit of the failure.
//
// The value is checked against the CLOSED vocabulary, and a value outside
// it is IGNORED: the field can never carry a free-form string into a
// rendering, and a rendering never depends on a value the package does not
// know.
func (e *RefreshError) SetCommit(commit string) {
	switch commit {
	case RefreshCommitQuarantined, RefreshCommitQuarantineFailed, RefreshCommitUnconfirmed:
		e.Commit = commit
	}
}

// withRefreshCommit returns the failure with its commit outcome recorded.
//
// A failure that is not a RefreshError is returned unchanged: the commit
// outcome is a property of the classified failure, and a failure of
// another kind has no vocabulary to extend.
func withRefreshCommit(err error, commit string) error {
	var classified *RefreshError
	if errors.As(err, &classified) {
		classified.SetCommit(commit)
	}
	return err
}

// Error renders the classified failure.
//
// The rendering names the class, the provider's code and the provider's
// bounded, redacted description, and the outcome of the local commit when
// there was one. It never names a token: the description has been through
// the redaction, the code is a code, and the commit outcome is constant
// text about the local state.
func (e *RefreshError) Error() string {
	rendered := "siwc: the refresh failed: " + e.Class
	if e.Code != "" {
		rendered += " (provider code " + e.Code + ")"
	}
	if e.StatusCode > 0 {
		rendered += fmt.Sprintf(" (status %d)", e.StatusCode)
	}
	if e.Description != "" {
		rendered += ": " + e.Description
	}
	switch e.Commit {
	case RefreshCommitQuarantined:
		rendered += ": the quarantine was committed: the unusable tokens are cleared" +
			" and the registration needs a new authorization"
	case RefreshCommitQuarantineFailed:
		rendered += ": the quarantine could not be committed: the saved credential" +
			" may be stale and the registration is NOT marked"
	case RefreshCommitUnconfirmed:
		// The rendering asserts NEITHER that the tokens are cleared nor
		// that the registration is NOT marked: the record is in place and
		// its persistence is unproven, and neither of those two states was
		// established.
		rendered += ": the local record was published but its durability was not confirmed:" +
			" the state of the registration is indeterminate"
	}
	return rendered
}

// Unwrap returns the classified cause.
func (e *RefreshError) Unwrap() error { return e.cause }

// Is reports whether the failure is one of the classified classes.
//
// The comparison is by the sentinel, so a caller classifies the outcome
// without matching on message text.
func (e *RefreshError) Is(target error) bool {
	switch e.Class {
	case RefreshClassTerminal:
		return target == ErrRefreshTerminal
	case RefreshClassTransient:
		return target == ErrRefreshTransient
	case RefreshClassInvalidClient:
		return target == ErrRefreshInvalidClient
	case RefreshClassRejected:
		return target == ErrRefreshRejected
	case RefreshClassUnreadable:
		return target == ErrRefreshUnreadable
	case RefreshClassUnconfirmed:
		return target == ErrRefreshUnconfirmed
	default:
		return false
	}
}

// Format implements fmt.Formatter, so that no formatting verb can reach the
// cause.
//
// The guarantee holds when fmt can dispatch to this error, so the routes
// where it cannot are the ones ProfileRecord documents: "%T" is safe, "%w"
// and "%p" bypass every method on this type.
func (e *RefreshError) Format(state fmt.State, verb rune) {
	safe := e.Error()
	if verb == 'q' {
		_, _ = io.WriteString(state, strconv.Quote(safe))
		return
	}
	_, _ = io.WriteString(state, safe)
}

// The refresh tuning.
const (
	// DefaultRefreshSkew is the margin before the expiry at which a token
	// is refreshed.
	//
	// The margin is the connector's own: the provider's lifetime is one
	// hour, and a token that is used in its last minute is a token whose
	// request may arrive after it expired.
	DefaultRefreshSkew = 60 * time.Second

	// defaultRefreshLockTimeout bounds the wait for the rotation lock.
	//
	// The bound is a wall-clock bound, because it bounds a wait for
	// another process: a wait that never ends is a registration that can
	// never be refreshed again.
	defaultRefreshLockTimeout = defaultLockTimeout
)

// The failures of the refresh that are not the provider's.
var (
	// errRefreshNeedsReauth reports a registration that already requires
	// a new authorization.
	errRefreshNeedsReauth = errors.New(
		"siwc: the registration is marked as needing a new authorization",
	)

	// errRefreshNoToken reports a registration without a refresh token.
	errRefreshNoToken = errors.New(
		"siwc: the registration carries no refresh token",
	)

	// errRefreshSuperseded reports that the result of a refresh was not
	// published, because the registration's credential was replaced while
	// the refresh was in flight.
	//
	// The outcome is not a failure of the refresh: the registration holds
	// a NEWER credential, and the caller is handed that one.
	//
	// It is the outcome of the RECOVERY as well: a recovery that finds a
	// newer credential has nothing to quarantine, and a no-op is not a
	// committed quarantine.
	errRefreshSuperseded = errors.New(
		"siwc: the refresh result was superseded by a newer credential",
	)

	// errTokenResponseTooLarge reports a token response that exceeds the
	// bound.
	//
	// The failure is a CONSTANT: the response carries credential
	// material, and a diagnostic that repeated a part of it would put that
	// material into an operator- or support-facing error.
	errTokenResponseTooLarge = errors.New(
		"siwc: the token response exceeds its bound",
	)
)

// RefresherConfig is the configuration of the refresher.
type RefresherConfig struct {
	// Profiles persists the registrations. It is required.
	Profiles *ProfileStore
	// HTTPClient performs the refresh request. Empty means a client with a
	// bounded timeout.
	HTTPClient *http.Client
	// TokenEndpoint is the provider's token endpoint. Empty means
	// TokenEndpoint.
	TokenEndpoint string
	// Now is the clock the expiry is read with. Empty means time.Now.
	Now func() time.Time
	// Skew is the margin before the expiry at which a token is refreshed.
	// Zero means DefaultRefreshSkew.
	Skew time.Duration
	// LockTimeout bounds the wait for the cross-process refresh lock. Zero
	// means defaultRefreshLockTimeout.
	LockTimeout time.Duration
}

// Refresher refreshes the credentials of the saved registrations.
//
// The refresher is safe for concurrent use: the registration's locks are
// the store's, and the records it publishes are the store's atomic
// replacements.
type Refresher struct {
	profiles      *ProfileStore
	client        *http.Client
	tokenEndpoint string
	now           func() time.Time
	skew          time.Duration
	lockTimeout   time.Duration
}

// NewRefresher returns the refresher of the saved registrations.
//
// The profile store is required: a refresh that cannot persist the
// replacement refresh token would consume the only copy of a renewable
// credential.
func NewRefresher(config RefresherConfig) (*Refresher, error) {
	if config.Profiles == nil {
		return nil, errors.New("siwc: the refresher needs a profile store")
	}
	if strings.TrimSpace(config.TokenEndpoint) == "" {
		config.TokenEndpoint = TokenEndpoint
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Skew <= 0 {
		config.Skew = DefaultRefreshSkew
	}
	if config.LockTimeout <= 0 {
		config.LockTimeout = defaultRefreshLockTimeout
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Refresher{
		profiles:      config.Profiles,
		client:        refreshClient(client),
		tokenEndpoint: config.TokenEndpoint,
		now:           config.Now,
		skew:          config.Skew,
		lockTimeout:   config.LockTimeout,
	}, nil
}

// refreshClient returns the client the refresh uses.
//
// The refresh MUST NOT follow a redirect: the refresh token is in the
// request, and a redirect would let the provider send it to a destination of
// its choosing. The caller's transport, timeout and cookie jar are the
// caller's; only the redirect policy is this connector's.
func refreshClient(caller *http.Client) *http.Client {
	return &http.Client{
		Transport: caller.Transport,
		Timeout:   caller.Timeout,
		Jar:       caller.Jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Refresh renews the credentials of one saved registration.
//
// The refresh is serialized per registration, and it issues AT MOST ONE
// request to the provider: a rotating refresh token is single-use, so a
// second request with the same token would invalidate the credential. A
// caller that wants to try again calls Refresh again, and the call re-reads
// the saved record - so a second attempt uses the token that the first
// attempt stored, and never the one that was consumed.
//
// The registration is named explicitly. The refresher never chooses a
// registration on the caller's behalf, and it never switches to another one:
// a plan allowance is not a failover mechanism, and requirement 2.13 forbids
// rotating to another subscriber.
//
// The outcome is classified. A terminal failure clears the unusable tokens
// and marks the registration as needing a new authorization, and it KEEPS
// the account mapping and the issued client id so that the reauthorization
// can replace the credentials. A transient failure changes nothing: the
// saved credentials are still there, and the registration still works until
// the access token expires.
//
// # The caller-reporting policy
//
// The ROTATION ADMISSION and the PROVIDER I/O run under the caller's
// context: a caller that goes away before the provider answered withdraws,
// and the failure is the caller's cancellation. The provider was not asked,
// nothing was committed, and the saved credentials are untouched.
//
// The PUBLICATION of the outcome runs under the store's own bounded
// budget: the provider has already answered, the token it consumed is
// consumed, and the replacement is the only copy of the renewable
// credential. A cancellation that arrives after the answer therefore does
// NOT withdraw the refresh: the replacement is committed and the refresh
// reports SUCCESS. The caller's own cancellation is the caller's knowledge,
// and the connector does not let it undo a rotation the provider has
// already made.
//
// The bound is the TOTAL of ONE reconciliation. The commit and the one
// generation-checked fallback that may follow it share that budget: the
// fallback has what is left of the commit's budget, and a failed commit
// does not buy a second full budget. A caller therefore waits at most the
// store's budget for the whole outcome, and not twice that.
//
// The rotation ownership is HELD across that publication: the rotation lock
// is released only after the outcome is committed, so that no second
// refresh of the same registration can present the token the first one
// consumed.
//
// # The commit states
//
// The local record of the outcome has THREE states, and the diagnostic says
// which one was established:
//
//   - COMMITTED: the record was published and its durability was confirmed;
//   - NOT COMMITTED: nothing was published, and the saved credential may be
//     stale; and
//   - PUBLISHED BUT UNCONFIRMED: the record is in place and only its
//     persistence is unproven.
//
// The third state is the one that a reader of the store can observe and a
// reader of the diagnostic cannot: the record on the disk holds the
// outcome, and a crash may roll it back. The diagnostic therefore asserts
// neither that the tokens are cleared nor that the registration is NOT
// marked - neither was established.
func (r *Refresher) Refresh(ctx context.Context, profileID string) (ProfileRecord, error) {
	id := strings.TrimSpace(profileID)
	if err := validateProfileID(id); err != nil {
		return ProfileRecord{}, fmt.Errorf("siwc: the profile id is unusable: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// The in-process half of the serialization is the store's admission,
	// which the operating system's lock acquisition below takes together
	// with the cross-process half. The two share one deadline.
	//
	// The cross-process half. The lock file lives in the resolved store
	// directory, which is the protected state directory of the connector.
	//
	// The store directory is resolved ONCE for this call, and the lock,
	// the load and the publication all work on that one location: a flow
	// that locked one directory and wrote another would break the
	// path-identity contract of the package.
	dir, err := r.profiles.resolve()
	if err != nil {
		return ProfileRecord{}, err
	}
	if err := r.profiles.prepare(dir); err != nil {
		return ProfileRecord{}, err
	}
	// The ROTATION lock is held for the whole operation, across the
	// provider request. A rotating token is single-use, so two refreshes
	// of one token must not run at the same time.
	//
	// The acquisition takes the in-process admission and the operating
	// system's lock under ONE deadline, so the total wait is bounded by
	// the caller's budget and a cancelled caller does not wait for the
	// live request.
	release, err := r.profiles.acquireProfileLock(
		ctx, dir, id, rotationLockKind, r.lockTimeout,
	)
	if err != nil {
		return ProfileRecord{}, err
	}
	defer release()
	// The record is read AFTER the lock. A refresh that waited for another
	// one must see the token that the other one stored: the replacement
	// refresh token is the only token that may be used next.
	rec, err := r.profiles.loadFromDir(dir, id)
	if err != nil {
		return ProfileRecord{}, err
	}
	if rec.NeedsReauth {
		// The credential is already known to be unusable. The provider is
		// not asked again: a refresh of a token the provider refused would
		// be a second use of a single-use token.
		return ProfileRecord{}, NewRefreshError(
			RefreshClassTerminal, 0, "", "", errRefreshNeedsReauth,
		)
	}
	if strings.TrimSpace(rec.RefreshToken) == "" {
		return ProfileRecord{}, NewRefreshError(
			RefreshClassTerminal, 0, "", "", errRefreshNoToken,
		)
	}
	if r.fresh(rec) {
		// The access token is still inside its lifetime. There is nothing
		// to renew, and the provider is not asked: a rotation that was not
		// needed would consume a token for nothing.
		return rec, nil
	}
	tokens, err := r.requestRefresh(ctx, rec)
	// The RECONCILIATION is started here: the provider has answered, and
	// the commit and its one generation-checked fallback share ONE budget.
	// A commit that fails therefore does not buy a second full budget for
	// the fallback - the fallback runs on what is left of the first one.
	reconciliation := r.profiles.BeginReconciliation()
	defer reconciliation.Close()
	if err != nil {
		if isTerminalRefresh(err) {
			// The token is unusable. It is cleared, so that a later
			// sign-in is not attempted with it, and the registration is
			// marked as needing a new authorization.
			//
			// The clearing is published through the SAME rule as every
			// other publication: it is applied to the credential that is
			// stored NOW, and a terminal result about a credential that
			// is no longer there clears nothing.
			cleared, publishErr := r.publishTerminal(reconciliation, dir, rec)
			switch classifyPublication(publishErr) {
			case publishSuperseded:
				// The registration holds a newer credential. The old
				// failure says nothing about it, and the caller is
				// handed the credential that is there now.
				return cleared, nil
			case publishUnconfirmed:
				// The quarantine WAS published - the rename cleared the
				// tokens and marked the record - and only its DURABILITY
				// was not confirmed. The state of the registration is
				// therefore INDETERMINATE, and the diagnostic says so:
				// it asserts neither that the tokens are cleared nor that
				// the registration is NOT marked.
				failure := NewRefreshError(
					RefreshClassTerminal, 0, "", "",
					fmt.Errorf(
						"%w (the quarantine was published, but its durability was not confirmed)",
						publishErr,
					),
				)
				failure.SetCommit(RefreshCommitUnconfirmed)
				return ProfileRecord{}, failure
			case publishFailed:
				// The quarantine could not be committed. The provider
				// has already refused the token, and the saved
				// credential may therefore be stale: the message says
				// so, and it says that the registration is NOT marked.
				failure := NewRefreshError(
					RefreshClassTerminal, 0, "", "",
					fmt.Errorf(
						"%w (the quarantine could not be committed, so the saved credential may be stale)",
						publishErr,
					),
				)
				failure.SetCommit(RefreshCommitQuarantineFailed)
				return ProfileRecord{}, failure
			default:
				// The quarantine WAS committed: the unusable tokens are
				// cleared and the registration is marked. The operator is
				// told so, because the class alone cannot tell this outcome
				// from one whose local record was never written.
				return ProfileRecord{}, withRefreshCommit(err, RefreshCommitQuarantined)
			}
		}
		return ProfileRecord{}, err
	}
	updated, err := r.publishRenewed(reconciliation, dir, rec, tokens)
	if err != nil {
		switch classifyPublication(err) {
		case publishSuperseded:
			// The registration holds a newer credential. The rotation
			// that this refresh performed is not published, because
			// publishing it would replace the newer session with a
			// result that belongs to the older one.
			return updated, nil
		case publishUnconfirmed:
			// The replacement IS in place: the rename published it, and
			// only the confirmation of the directory entry failed. The
			// stored credential is the connector's OWN replacement - it is
			// neither stale nor superseded - and there is NOTHING to
			// quarantine.
			//
			// The quarantine must therefore not run: it would reload
			// that very replacement and mistake it for a superseding
			// credential. The diagnostic says that the durability of the
			// record is unproven, and it does not ask for a new
			// authorization: the record on the disk holds the
			// replacement.
			failure := NewRefreshError(
				RefreshClassUnconfirmed, 0, "", "",
				fmt.Errorf(
					"%w (the replacement was published, but its durability was not confirmed)",
					err,
				),
			)
			failure.SetCommit(RefreshCommitUnconfirmed)
			return ProfileRecord{}, failure
		}
		// The provider consumed the token that was sent, and the
		// replacement could not be committed. The saved credential is
		// therefore STALE: it is the token the provider has consumed,
		// and presenting it again would be a replay. The registration
		// must not be used until it is authorized again.
		//
		// The quarantine is attempted once, under the SAME
		// reconciliation and therefore under the SAME budget: it has
		// what is left of the commit's budget, and it cannot restart
		// the clock. If the quarantine fails too, the returned error
		// says so: the credential is then known to be stale AND the
		// registration is not marked. If the quarantine is committed,
		// the returned error says that the unusable credential was
		// cleared and that the registration needs a new authorization.
		quarantined, outcome, quarantineErr := r.quarantine(reconciliation, dir, rec)
		switch outcome {
		case publishSuperseded:
			// The recovery found a NEWER credential: the registration is
			// not stale, and the quarantine is a NO-OP. A no-op is not a
			// committed quarantine - the old failure says nothing about
			// the stored credential, and the caller is handed the
			// credential that is there now.
			return quarantined, nil
		case publishUnconfirmed:
			// The quarantine WAS published and only its durability was
			// not confirmed: the state of the registration is
			// indeterminate.
			failure := NewRefreshError(
				RefreshClassTerminal, 0, "", "",
				fmt.Errorf(
					"%w (the replacement could not be committed, and the quarantine was published, but its durability was not confirmed)",
					err,
				),
			)
			failure.SetCommit(RefreshCommitUnconfirmed)
			return ProfileRecord{}, failure
		case publishFailed:
			failure := NewRefreshError(
				RefreshClassTerminal, 0, "", "",
				fmt.Errorf(
					"%w (the replacement could not be committed, and the quarantine could not be committed either: %v)",
					err, quarantineErr,
				),
			)
			failure.SetCommit(RefreshCommitQuarantineFailed)
			return ProfileRecord{}, failure
		default:
			failure := NewRefreshError(
				RefreshClassTerminal, 0, "", "",
				fmt.Errorf(
					"%w (the replacement could not be committed, and the quarantine cleared the unusable credential)",
					err,
				),
			)
			failure.SetCommit(RefreshCommitQuarantined)
			return ProfileRecord{}, failure
		}
	}
	return updated, nil
}

// publishOutcome is what one publication of the refresh left behind.
//
// The store's publication boundary separates three facts, and this type
// keeps them apart: the PUBLICATION (the record was written), the DURABILITY
// (the write was confirmed) and the SUPERSESSION (a different, newer
// credential is stored).
type publishOutcome string

const (
	// publishCommitted: the record was published and its durability was
	// confirmed.
	publishCommitted publishOutcome = "committed"

	// publishFailed: nothing was published, and the stored record is
	// unchanged.
	publishFailed publishOutcome = "failed"

	// publishUnconfirmed: the record was published, and its durability was
	// NOT confirmed.
	publishUnconfirmed publishOutcome = "unconfirmed"

	// publishSuperseded: a different, newer credential is stored, and
	// nothing was published.
	publishSuperseded publishOutcome = "superseded"
)

// classifyPublication maps the failure of a publication to the state it left
// the store in.
//
// The three failures are the three facts of the boundary, and they are NOT
// the same thing: a publication that never happened left the record alone, a
// publication whose durability is unproven left the record in place, and a
// publication that was refused left a newer credential alone.
func classifyPublication(err error) publishOutcome {
	switch {
	case err == nil:
		return publishCommitted
	case errors.Is(err, errRefreshSuperseded):
		return publishSuperseded
	case errors.Is(err, errDurabilityUnconfirmed):
		return publishUnconfirmed
	default:
		return publishFailed
	}
}

// publishRenewed publishes the token set a successful refresh produced.
//
// The publication is GENERATION-CHECKED: the change is applied to the
// credential that is stored NOW, and it is applied only when that
// credential is still the one this refresh rotated. A registration that was
// reauthorized or signed out while the refresh waited for the provider
// holds a different credential, and the rotation of the older one must not
// be published over it.
//
// The publication is a REQUIRED COMMIT and it does not take a context: the
// provider has already consumed the token, and the local record of the
// replacement is the only copy of it. It takes the RECONCILIATION instead,
// and the reconciliation owns the budget: the commit and the fallback that
// may follow it are bounded together.
func (r *Refresher) publishRenewed(
	reconciliation *Reconciliation,
	dir string,
	rec ProfileRecord,
	tokens TokenResponse,
) (ProfileRecord, error) {
	return reconciliation.Publish(dir, rec.ID, func(current ProfileRecord) (ProfileRecord, error) {
		if current.RefreshToken != rec.RefreshToken {
			return current, errRefreshSuperseded
		}
		return r.renewed(current, tokens), nil
	})
}

// publishTerminal clears the unusable tokens of a registration and marks it
// as needing a new authorization.
//
// The clearing is GENERATION-CHECKED for the same reason the rotation is:
// a terminal result about a credential that is no longer stored must not
// clear the credential that is.
//
// The publication is a REQUIRED COMMIT and it does not take a context: the
// provider has already refused the token, and the local record of the
// refusal is what keeps the next refresh from presenting it again. It
// takes the RECONCILIATION instead, and the reconciliation owns the
// budget.
func (r *Refresher) publishTerminal(
	reconciliation *Reconciliation,
	dir string,
	rec ProfileRecord,
) (ProfileRecord, error) {
	return reconciliation.Publish(dir, rec.ID, func(current ProfileRecord) (ProfileRecord, error) {
		if current.RefreshToken != rec.RefreshToken {
			return current, errRefreshSuperseded
		}
		return r.needsReauth(current), nil
	})
}

// quarantine marks a registration as needing a new authorization.
//
// It is the recovery of a FAILED required commit: the provider has consumed
// the token and the replacement could not be committed, so the saved
// credential is stale. The quarantine clears it and marks the registration,
// and the next refresh then refuses to ask the provider with it.
//
// The quarantine is generation-checked like every other publication: it
// clears the credential it is about, and not a newer one. A newer
// credential is NOT stale, and the recovery is then a NO-OP - the outcome
// says so, and the caller is handed the credential that is there now.
//
// The quarantine runs under the SAME reconciliation as the commit it
// recovers from, and therefore under the SAME budget: it has what is left
// of that budget, and it cannot restart the clock. A failed commit
// therefore costs the store ONE budget and not two.
//
// The outcome is the STATE the recovery left the store in, and the three
// states are kept apart: a committed quarantine, a quarantine that was
// published without a confirmed durability, and a quarantine that could not
// be published at all.
func (r *Refresher) quarantine(
	reconciliation *Reconciliation,
	dir string,
	rec ProfileRecord,
) (ProfileRecord, publishOutcome, error) {
	updated, err := r.publishTerminal(reconciliation, dir, rec)
	return updated, classifyPublication(err), err
}

// fresh reports whether the record's access token is still inside its
// lifetime.
func (r *Refresher) fresh(rec ProfileRecord) bool {
	if strings.TrimSpace(rec.AccessToken) == "" {
		return false
	}
	if rec.Expiry.IsZero() {
		return false
	}
	return r.now().Add(r.skew).Before(rec.Expiry)
}

// renewed returns the record that the refreshed token set produces.
//
// The replacement refresh token is stored when the provider sends one. A
// response that carries NO replacement keeps the saved token: the connector
// never discards the only copy of a renewable credential because a member
// is absent, and the documented provider always sends a replacement.
//
// The granted scopes are replaced only when the response carries a scope
// set. The request omits the scope so that the grant is retained, and an
// absent scope in the answer is not a statement about the grant.
func (r *Refresher) renewed(rec ProfileRecord, tokens TokenResponse) ProfileRecord {
	updated := rec
	updated.AccessToken = tokens.AccessToken
	if replacement := strings.TrimSpace(tokens.RefreshToken); replacement != "" {
		updated.RefreshToken = replacement
	}
	if idToken := strings.TrimSpace(tokens.IDToken); idToken != "" {
		updated.IDToken = idToken
	}
	if scopes := parseScopeSet(tokens.Scope); len(scopes) > 0 {
		updated.GrantedScopes = scopes
	}
	updated.Expiry = tokenExpiry(r.now(), tokens.ExpiresIn)
	updated.NeedsReauth = false
	return updated
}

// needsReauth clears the unusable tokens of a registration and marks it as
// needing a new authorization.
//
// The ACCOUNT MAPPING is kept: the subject, the issued client id, the host
// id, the label and the granted scopes stay, because the reauthorization
// needs the issued client id and confirms the account through the subject.
//
// The retained ID token is kept as well. It is the identity hint a later
// reauthorization carries, it identifies the account and it is not a session:
// the documentation uses a retained ID token as the id_token_hint for exactly
// this purpose.
func (r *Refresher) needsReauth(rec ProfileRecord) ProfileRecord {
	updated := rec
	updated.AccessToken = ""
	updated.RefreshToken = ""
	updated.Expiry = time.Time{}
	updated.NeedsReauth = true
	return updated
}

// requestRefresh performs the one refresh request of one registration.
//
// The request carries the issued client id of the REGISTRATION, the saved
// refresh token and the plan resource. It does NOT carry a scope: the
// documentation requires the omission so that the grant is retained.
//
// # ACCEPTED RESIDUAL - an interrupted answer is indeterminate
//
// The provider I/O runs under the CALLER's context, so a caller that goes
// away can interrupt the request. An interrupted answer is NOT treated as
// a complete outcome: a read that fails is a temporary failure, and a
// partial body is never decoded.
//
// The state of the credential is nevertheless INDETERMINATE in that case:
// the provider may have consumed the token before the answer was
// interrupted, and the connector cannot tell. Requirement 2.10 forbids
// erasing the credentials on a temporary failure, so the connector does not
// erase them.
//
// The mitigations are the two rules that are already in force: the refresh
// issues AT MOST ONE request per call, and the next refresh RE-READS the
// saved record before it presents the token again. A credential that the
// provider consumed is therefore presented at most once more, and the
// provider's own answer then decides.
//
// This is an accepted residual and NOT a defect to be repaired: the
// alternative - erasing the credentials on an interrupted read - is
// forbidden by requirement 2.10.
func (r *Refresher) requestRefresh(
	ctx context.Context,
	rec ProfileRecord,
) (TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", rec.IssuedClientID)
	form.Set("refresh_token", rec.RefreshToken)
	form.Set("resource", PlanResource)
	// The scope is deliberately NOT set. A request that carried it would
	// ask the provider for a new grant instead of renewing this one, and
	// the documentation says to omit it.

	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, r.tokenEndpoint, strings.NewReader(form.Encode()),
	)
	if err != nil {
		return TokenResponse{}, NewRefreshError(
			RefreshClassTransient, 0, "", "", classifyExchangeCause(err),
		)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return TokenResponse{}, NewRefreshError(
			RefreshClassTransient, 0, "", "", classifyExchangeCause(err),
		)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxTokenResponseBytes))
		_ = response.Body.Close()
	}()
	// The body is read one byte past the bound, so that a response that
	// EXCEEDS the bound is DETECTED instead of silently truncated.
	//
	// A truncated prefix is not a smaller document: a valid JSON object
	// followed by more content is truncated into a COMPLETE and VALID
	// prefix, and decoding that prefix would accept a response the
	// connector never read whole. The oversize response is therefore
	// refused BEFORE anything is decoded, with a constant failure that
	// names no part of it.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTokenResponseBytes+1))
	if err != nil {
		return TokenResponse{}, NewRefreshError(
			RefreshClassTransient, response.StatusCode, "", "", classifyExchangeCause(err),
		)
	}
	if len(body) > maxTokenResponseBytes {
		return TokenResponse{}, NewRefreshError(
			RefreshClassUnreadable, response.StatusCode, "", "", errTokenResponseTooLarge,
		)
	}
	if response.StatusCode != http.StatusOK {
		return TokenResponse{}, r.refreshFailure(response.StatusCode, body, rec)
	}
	// The response is read by EXACT member name, with the same reader the
	// code exchange uses. A member whose name differs only in case is a
	// different member, and a member that ARRIVED with a shape the
	// connector cannot read makes the response unreadable - it is never
	// read as though the response had not carried it.
	tokens, err := decodeTokenResponse(body)
	if err != nil {
		return TokenResponse{}, NewRefreshError(
			RefreshClassUnreadable, response.StatusCode, "", "", err,
		)
	}
	if strings.TrimSpace(tokens.AccessToken) == "" {
		return TokenResponse{}, NewRefreshError(
			RefreshClassUnreadable, response.StatusCode, "", "",
			errors.New("siwc: the token endpoint returned no access token"),
		)
	}
	return tokens, nil
}

// refreshFailure classifies a refused refresh.
//
// The provider's machine-readable code is the authority for the credential
// state, and the status is the authority for the provider's condition. The
// two are read in that order, and a failure that carries NEITHER a readable
// code nor a 5xx status is reported as unreadable: an error body that
// arrived and could not be read is not an absent error, and it is not a
// temporary failure either.
//
// The provider's description is REDACTED before it is bounded. The order
// matters: a description that is truncated first can no longer be matched
// against the complete secret it carries.
func (r *Refresher) refreshFailure(
	status int,
	body []byte,
	rec ProfileRecord,
) *RefreshError {
	if status >= 300 && status < 400 {
		// A redirect is refused rather than followed: the refresh token is
		// in the request, and the provider must not choose the destination
		// of a request that carries it. The Location is never rendered.
		return NewRefreshError(
			RefreshClassRejected, status, "redirect_refused", "", nil,
		)
	}
	code, description, readable := readProviderError(body)
	safe := safeProviderText(
		description, maxErrorDescriptionLength,
		rec.AccessToken, rec.RefreshToken, rec.IDToken,
	)
	switch {
	case unusableRefreshCodes[code]:
		return NewRefreshError(RefreshClassTerminal, status, code, safe, nil)
	case code == "invalid_client":
		return NewRefreshError(RefreshClassInvalidClient, status, code, safe, nil)
	case temporaryRefreshCodes[code]:
		return NewRefreshError(RefreshClassTransient, status, code, safe, nil)
	case status >= 500:
		// A 5xx is a temporary condition whatever the body carries.
		return NewRefreshError(RefreshClassTransient, status, code, safe, nil)
	case readable:
		return NewRefreshError(RefreshClassRejected, status, code, safe, nil)
	default:
		return NewRefreshError(RefreshClassUnreadable, status, code, safe, nil)
	}
}

// readProviderError reads the provider's error code and description.
//
// The body is read as a JSON OBJECT whose "error" member is a string. A body
// that is not an object, and a body whose "error" member ARRIVED with
// another shape, are UNREADABLE: the provider refused, and the connector
// cannot tell why.
//
// The read is EXACT, like every other document this package reads: a member
// whose name differs only in case is a different member.
//
// A document that REPEATS a key is refused here, and that is stricter than
// the rule the code exchange applies to its own error body. The exchange's
// error body is a diagnostic of a failed sign-in, and the recovery is to
// start the sign-in again. This body decides whether a credential is ERASED,
// and a document that repeats the member that decides it is a document the
// connector does not trust: the outcome is the unreadable one, and the
// saved credentials survive.
func readProviderError(body []byte) (code string, description string, readable bool) {
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return "", "", false
	}
	members, err := jsonMembers(body)
	if err != nil {
		return "", "", false
	}
	// The error member is read by its EXACT name, and its presence and
	// shape are kept apart: a member that ARRIVED with a shape the
	// connector cannot read makes the answer unreadable, and a member
	// that is absent says nothing at all.
	value := memberString(members, "error")
	if value.State != memberUsable {
		return "", "", false
	}
	trimmed := strings.TrimSpace(value.Value)
	if trimmed == "" {
		return "", "", false
	}
	code = classifyProviderErrorCode(trimmed)
	text, err := documentString(members, "error_description")
	if err != nil {
		return code, "", true
	}
	return code, text, true
}

// isTerminalRefresh reports whether the failure is the terminal class.
func isTerminalRefresh(err error) bool {
	return errors.Is(err, ErrRefreshTerminal)
}

// tokenExpiry returns the expiry of a token set.
//
// A response that carries no lifetime yields the zero expiry: the connector
// does not invent a lifetime the provider did not state, and a token with an
// unknown expiry is treated as one that has to be renewed.
func tokenExpiry(now time.Time, expiresIn int64) time.Time {
	if expiresIn <= 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(expiresIn) * time.Second)
}

// parseScopeSet parses a scope string into the SET of granted scopes.
//
// The scope string is a SET: a scope that appears twice is one scope, and
// the set holds it once. The order of the first appearance is kept, so that
// two equal sets render the same way.
//
// The set is never SAMPLED: every scope the string carries is collected, and
// a scope this build does not know is kept as well. The plan-usage check
// looks for one exact name, and a scope set that dropped the unknown names
// would be a set the provider did not grant.
func parseScopeSet(raw string) []string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(fields))
	scopes := make([]string, 0, len(fields))
	for _, field := range fields {
		if _, duplicate := seen[field]; duplicate {
			continue
		}
		seen[field] = struct{}{}
		scopes = append(scopes, field)
	}
	return scopes
}
