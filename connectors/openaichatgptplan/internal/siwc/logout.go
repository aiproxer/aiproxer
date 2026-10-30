package siwc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The sign-out of a saved registration.
//
// A sign-out has TWO halves, and the order between them is the contract:
//
//  1. the renewable session is ended at the provider, through the
//     revocation endpoint the provider's OIDC configuration publishes; and
//  2. the local tokens are cleared.
//
// The revocation comes FIRST, because the revocation needs the refresh token
// that the clearing destroys. The tokens are cleared even when the
// revocation could not be confirmed: the sign-out completes locally, and
// the operator is told that the remote session was not confirmed.
//
// A CANCELLATION is not an unconfirmed revocation: it is the caller's
// withdrawal, and it aborts the sign-out with an error and NO local
// clearing. A caller that goes away did not sign out, and the credential
// stays until a sign-out that reaches the provider. The withdrawal is
// honoured wherever the revocation can fail - while the provider's
// configuration is read, while the request is made, while the answer is
// read, and while an answer that confirms nothing is classified - and a
// CONFIRMED revocation is never a withdrawal.
//
// The two outcomes are DISTINGUISHABLE. A confirmed revocation and a
// local-only completion are different facts about the world, and a caller
// that renders them the same way would tell the operator that the session
// was ended when it was not.
//
// The account mapping is RETAINED: the subject, the issued client id, the
// host id, the label and the granted scopes stay, so that a later sign-in
// can replace the credentials of the same registration.

// The outcomes of the revocation attempt.
//
// The outcome names WHAT HAPPENED, and the guidance names what the operator
// has to do about it. The two are separate on purpose: the outcome is a
// fact, and the guidance is an instruction.
const (
	// RevocationOutcomeConfirmed reports that the provider confirmed the
	// revocation: it answered the documented success response, which is an
	// EMPTY HTTP 200.
	RevocationOutcomeConfirmed = "confirmed"

	// RevocationOutcomeNoSession reports that there was no renewable
	// session to revoke, because the registration carried no refresh
	// token.
	RevocationOutcomeNoSession = "no-session"

	// RevocationOutcomeNoEndpoint reports that no usable revocation
	// endpoint was available: the provider published none, or the one it
	// published could not be used.
	RevocationOutcomeNoEndpoint = "no-endpoint"

	// RevocationOutcomeUnavailable reports that the provider's
	// configuration could not be read, so that the revocation endpoint
	// could not be resolved.
	RevocationOutcomeUnavailable = "unavailable"

	// RevocationOutcomeNetwork reports that the network failed.
	//
	// A CANCELLATION is not this outcome: it is the caller's withdrawal,
	// and the sign-out fails with the caller's own error. A network
	// failure is a failure of the path to the provider; a cancellation is
	// a decision of the caller, and the two are different facts.
	RevocationOutcomeNetwork = "network"

	// RevocationOutcomeServer reports that the provider failed.
	RevocationOutcomeServer = "server"

	// RevocationOutcomeRejected reports that the provider refused the
	// revocation.
	RevocationOutcomeRejected = "rejected"

	// RevocationOutcomeUnexpected reports a response that is not the
	// documented success response.
	RevocationOutcomeUnexpected = "unexpected"

	// RevocationOutcomeSuperseded reports that the session that is
	// stored now is not the session the revocation ended.
	//
	// A reauthorization that completed while the revocation was in
	// flight replaced the credential, and the revocation did not end the
	// replacement. The connector does not report a confirmation it did
	// not observe.
	RevocationOutcomeSuperseded = "superseded"
)

// RevocationGuidanceUnconfirmed is the operator-facing guidance for a
// sign-out that completed locally without a confirmed revocation.
//
// The guidance is the documented one: the user can disconnect the app in
// ChatGPT Settings, and that is the step that ends the session the provider
// did not confirm as ended.
const RevocationGuidanceUnconfirmed = "the remote session was not confirmed as revoked: " +
	"disconnect the app in ChatGPT Settings to end the session there"

// The tuning of the revocation attempt.
const (
	// defaultRevocationAttempts is how often the revocation is attempted
	// before the sign-out completes locally.
	//
	// The bound is what makes the retry BOUNDED: the attempt stops, and
	// the sign-out completes locally with an unconfirmed revocation.
	defaultRevocationAttempts = 3

	// defaultRevocationBackoff is the first delay between two attempts.
	//
	// The delay grows with every attempt and is capped, so the retry does
	// not hammer a provider that is failing.
	defaultRevocationBackoff = 250 * time.Millisecond

	// maxRevocationBackoff caps the delay between two attempts.
	maxRevocationBackoff = 2 * time.Second

	// maxRevocationResponseBytes bounds the revocation response body.
	//
	// The documented success response is EMPTY, and the body is read only
	// to tell an empty success from a response that carries something.
	maxRevocationResponseBytes = 64 << 10
)

// LogoutResult is the outcome of a sign-out.
//
// The result is the whole of the report: a caller renders the outcome and
// the guidance, and it never has to guess whether the remote session was
// ended.
type LogoutResult struct {
	// ProfileID is the local id of the registration that was signed out.
	//
	// It is the non-secret local key the operator recognizes. The
	// registration's subject and its email hint are never part of the
	// report: a diagnostic identifies the account through its local key.
	ProfileID string
	// RemoteRevocationConfirmed reports that the provider confirmed the
	// revocation of the renewable session.
	RemoteRevocationConfirmed bool
	// LocalTokensCleared reports that the local tokens are gone. It is false
	// when a concurrent reauthorization superseded the revoked session.
	LocalTokensCleared bool
	// RevocationOutcome names what the revocation attempt produced.
	RevocationOutcome string
	// Guidance is the operator-facing instruction for an outcome that
	// needs a manual step. It is empty when there is nothing to do.
	Guidance string
}

// RevokerConfig is the configuration of the sign-out.
type RevokerConfig struct {
	// Profiles persists the registrations. It is required.
	Profiles *ProfileStore
	// OIDC publishes the provider's OIDC configuration, which names the
	// revocation endpoint. It is required: the connector never guesses an
	// endpoint.
	OIDC *OIDCClient
	// HTTPClient performs the revocation request. Empty means a client
	// with a bounded timeout.
	HTTPClient *http.Client
	// Sleep waits between two revocation attempts. Empty means a timer.
	Sleep func(context.Context, time.Duration) error
	// Attempts is how often the revocation is attempted. Zero means
	// defaultRevocationAttempts.
	Attempts int
	// Backoff is the first delay between two attempts. Zero means
	// defaultRevocationBackoff.
	Backoff time.Duration
	// LockTimeout bounds the wait for the registration lock. Zero means
	// defaultRefreshLockTimeout.
	LockTimeout time.Duration
}

// Revoker ends the renewable session of a saved registration and signs the
// registration out locally.
//
// The revoker is safe for concurrent use.
type Revoker struct {
	profiles    *ProfileStore
	oidc        *OIDCClient
	client      *http.Client
	sleep       func(context.Context, time.Duration) error
	attempts    int
	backoff     time.Duration
	lockTimeout time.Duration
}

// NewRevoker returns the sign-out of the saved registrations.
func NewRevoker(config RevokerConfig) (*Revoker, error) {
	if config.Profiles == nil {
		return nil, errors.New("siwc: the sign-out needs a profile store")
	}
	if config.OIDC == nil {
		return nil, errors.New("siwc: the sign-out needs the provider's OIDC configuration")
	}
	if config.Sleep == nil {
		config.Sleep = sleepFor
	}
	if config.Attempts <= 0 {
		config.Attempts = defaultRevocationAttempts
	}
	if config.Backoff <= 0 {
		config.Backoff = defaultRevocationBackoff
	}
	if config.LockTimeout <= 0 {
		config.LockTimeout = defaultRefreshLockTimeout
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Revoker{
		profiles:    config.Profiles,
		oidc:        config.OIDC,
		client:      refreshClient(client),
		sleep:       config.Sleep,
		attempts:    config.Attempts,
		backoff:     config.Backoff,
		lockTimeout: config.LockTimeout,
	}, nil
}

// Logout ends the renewable session of one registration and clears its
// local tokens.
//
// The revocation is attempted BEFORE the tokens are cleared, and the refresh
// token stays available while it is attempted: a network failure or a 5xx is
// retried with a bounded backoff, and only the exhausted attempt ends the
// local sign-out.
//
// The tokens are cleared in BOTH outcomes of a revocation that REACHED the
// provider. A registration that kept its tokens after a sign-out would keep
// using a session the operator believes to be closed.
//
// The account mapping and this host's id are RETAINED, so that a later
// sign-in can replace the credentials of the same registration.
//
// The error is reserved for a local failure and for the caller's
// withdrawal: a sign-out that could not completed locally did not happen,
// and the caller has to know. The unconfirmed revocation is NOT an error -
// the sign-out completed locally, and the result says what could not be
// confirmed.
//
// # The caller-reporting policy
//
// The REVOCATION phase runs under the caller's context, and a cancellation
// there ABORTS the sign-out with an error and no local clearing. That is the
// caller's withdrawal, and it is accepted: nothing was confirmed, so
// nothing is recorded, and the local tokens stay until a sign-out that
// reaches the provider.
//
// The withdrawal is recognised at the three points where the revocation
// can fail: while the provider's configuration is read, while the request
// is made, and while the answer is read. A failure at any of them is the
// caller's cancellation, and the sign-out reports it as such - it is not
// reported as a network failure, and it never reaches the local clearing.
//
// It is recognised at ONE more point, and it is the point the LAST attempt
// reaches: an answer that was read WHOLE and confirms NOTHING - a 5xx, a
// refusal, or a 200 that carries something - is classified as a
// nonconfirming outcome, and a caller that went away while the answer was
// read withdrew. The withdrawal is therefore honoured there too, and the
// saved credential stays: nothing was confirmed, so nothing is cleared.
//
// The withdrawal is NOT a blanket check after every response. A CONFIRMED
// revocation is the provider's commitment, and the local record of it is
// the connector's obligation: a cancellation that arrives after it does not
// withdraw the sign-out.
//
// The LOCAL CLEARING is a different matter: it is the record of an outcome
// the provider has already committed, so it is a REQUIRED COMMIT and it is
// not contingent on the caller's context. A cancellation that arrives AFTER
// the revocation was confirmed therefore yields a completed sign-out -
// RemoteRevocationConfirmed and LocalTokensCleared, and no error - because
// the obligation to record the confirmed revocation is the connector's and
// not the caller's.
func (r *Revoker) Logout(ctx context.Context, profileID string) (LogoutResult, error) {
	id := strings.TrimSpace(profileID)
	if err := validateProfileID(id); err != nil {
		return LogoutResult{}, fmt.Errorf("siwc: the profile id is unusable: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// The ROTATION lock keeps a refresh from rotating the token while
	// this sign-out is revoking it: the sign-out ends the session that is
	// there now, and a rotation in flight would leave a session the
	// sign-out never revoked.
	//
	// The store directory is resolved ONCE for this call, and the lock,
	// the load and the publication all work on that one location: a flow
	// that locked one directory and wrote another would break the
	// path-identity contract of the package.
	dir, err := r.profiles.resolve()
	if err != nil {
		return LogoutResult{}, err
	}
	if err := r.profiles.prepare(dir); err != nil {
		return LogoutResult{}, err
	}
	rotation, err := r.profiles.acquireProfileLock(
		ctx, dir, id, rotationLockKind, r.lockTimeout,
	)
	if err != nil {
		return LogoutResult{}, err
	}
	defer rotation()
	// The record is read under the lock: a refresh that held the lock
	// before may have replaced the token set this sign-out is about to
	// revoke.
	rec, err := r.profiles.loadFromDir(dir, id)
	if err != nil {
		return LogoutResult{}, err
	}
	result := LogoutResult{ProfileID: id}
	if strings.TrimSpace(rec.RefreshToken) == "" {
		// There is no renewable session to end. The connector does not
		// claim a confirmation it did not observe.
		result.RevocationOutcome = RevocationOutcomeNoSession
	} else {
		outcome, err := r.revoke(ctx, rec)
		if err != nil {
			return result, err
		}
		result.RevocationOutcome = outcome
		result.RemoteRevocationConfirmed = outcome == RevocationOutcomeConfirmed
	}
	// The local sign-out. The mapping is kept; the tokens are not.
	//
	// The clearing is applied to the credential that is stored NOW, and
	// not to the record this sign-out read: a reauthorization that
	// completed while the revocation was in flight must not be undone by
	// a stale whole-record write.
	//
	// The comparison happens under the publication lock. A newer credential
	// was never revoked by this operation and must remain unchanged.
	superseded := false
	if err := r.clearLocalTokens(dir, id, rec.RefreshToken, &superseded); err != nil {
		result.LocalTokensCleared = false
		result.Guidance = RevocationGuidanceUnconfirmed
		return result, err
	}
	result.LocalTokensCleared = !superseded
	if superseded {
		// The session that is stored now is not the session the
		// revocation ended. It remains usable: neither local clearing nor
		// remote revocation of the current session is confirmed.
		result.RemoteRevocationConfirmed = false
		result.RevocationOutcome = RevocationOutcomeSuperseded
	}
	if !result.RemoteRevocationConfirmed && result.RevocationOutcome != RevocationOutcomeNoSession {
		result.Guidance = RevocationGuidanceUnconfirmed
	}
	return result, nil
}

// revoke attempts the revocation of the renewable session.
//
// The attempt is repeated while the failure is one the documentation names
// as retryable - a network failure or a 5xx - and the number of attempts is
// bounded. The refresh token is still available while the attempts run: it
// is cleared only after the attempt is over.
func (r *Revoker) revoke(ctx context.Context, rec ProfileRecord) (string, error) {
	outcome := RevocationOutcomeUnavailable
	for attempt := 1; attempt <= r.attempts; attempt++ {
		result, retryable, err := r.revokeOnce(ctx, rec)
		if err != nil {
			return "", err
		}
		outcome = result
		if !retryable {
			return outcome, nil
		}
		if attempt == r.attempts {
			break
		}
		if err := r.sleep(ctx, r.backoffFor(attempt)); err != nil {
			return "", err
		}
	}
	return outcome, nil
}

// revokeOnce performs one revocation attempt.
//
// The three outcomes are distinct: a CONFIRMED revocation is the documented
// empty HTTP 200, a response that is not that is NOT a confirmation, and a
// network failure or a 5xx is RETRYABLE.
//
// The CLASSIFICATION of an answer that confirms nothing consults the
// caller's withdrawal: a caller that went away while the answer was read
// withdrew, and the sign-out is aborted with the caller's own error. A
// confirmed revocation does not consult it, because the confirmation is the
// provider's commitment and not the caller's.
//
// The request is form-encoded and carries the saved refresh token, the
// token type hint and the issued client id of the REGISTRATION.
func (r *Revoker) revokeOnce(
	ctx context.Context,
	rec ProfileRecord,
) (string, bool, error) {
	endpoint, err := r.revocationEndpoint(ctx)
	if err != nil {
		// The provider's configuration could not be read. The provider
		// could not be reached, and the attempt is worth repeating.
		//
		// A cancellation is NOT a network failure: it is the caller's
		// withdrawal, and the sign-out did not happen. The failure is
		// therefore the caller's own, and the local tokens stay.
		if cancelled := revocationWithdrawal(ctx); cancelled != nil {
			return "", false, cancelled
		}
		return RevocationOutcomeUnavailable, true, nil
	}
	if endpoint == "" {
		return RevocationOutcomeNoEndpoint, false, nil
	}
	form := url.Values{}
	form.Set("token", rec.RefreshToken)
	form.Set("token_type_hint", "refresh_token")
	form.Set("client_id", rec.IssuedClientID)
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()),
	)
	if err != nil {
		return "", false, fmt.Errorf("siwc: build the revocation request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		// The network failed. The refresh token is still available, and
		// the attempt is worth repeating.
		//
		// A cancellation is NOT a network failure: it is the caller's
		// withdrawal, and nothing was confirmed. The failure is the
		// caller's own, and the local tokens stay.
		if cancelled := revocationWithdrawal(ctx); cancelled != nil {
			return "", false, cancelled
		}
		return RevocationOutcomeNetwork, true, nil
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxRevocationResponseBytes))
		_ = response.Body.Close()
	}()
	// The body is read one byte past the bound, so that a response that
	// EXCEEDS the bound is DETECTED instead of silently truncated.
	//
	// The confirmation is a statement about the WHOLE body: the
	// documented success response is an EMPTY HTTP 200, and a bounded
	// PREFIX of a longer body is not that response. A body of whitespace
	// is tolerated, and the tolerance is proved over the whole body: the
	// read is complete, so "every byte is whitespace" is a fact about the
	// response and not about a prefix of it.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRevocationResponseBytes+1))
	if err != nil {
		// The answer could not be read, and the revocation is therefore
		// not confirmed. A cancellation is the caller's withdrawal and
		// not a network failure.
		if cancelled := revocationWithdrawal(ctx); cancelled != nil {
			return "", false, cancelled
		}
		return RevocationOutcomeNetwork, true, nil
	}
	switch {
	case response.StatusCode == http.StatusOK &&
		len(body) <= maxRevocationResponseBytes &&
		len(bytes.TrimSpace(body)) == 0:
		// The documented success response: an EMPTY HTTP 200. It is the
		// success response for an already-invalid token as well.
		//
		// A cancellation that arrives here is NOT a withdrawal: the
		// revocation was confirmed, and the local record of that
		// confirmation is the connector's obligation.
		return RevocationOutcomeConfirmed, false, nil
	case response.StatusCode == http.StatusOK:
		// A 200 that CARRIES something is not the documented success
		// response, and neither is a 200 whose body was longer than the
		// bound. The revocation is not confirmed, and the connector does
		// not pretend otherwise.
		//
		// The answer was read WHOLE, and a caller that went away while
		// it was read withdrew: nothing was confirmed, so the credential
		// stays.
		if cancelled := revocationWithdrawal(ctx); cancelled != nil {
			return "", false, cancelled
		}
		return RevocationOutcomeUnexpected, false, nil
	case response.StatusCode >= 500:
		// The provider failed. The attempt is worth repeating, and the
		// refresh token is still available.
		//
		// The answer was read WHOLE, and a caller that went away while
		// it was read withdrew: nothing was confirmed, so the credential
		// stays. The withdrawal is recognised HERE, at the
		// classification of the answer, and not in a blanket check after
		// every response - a confirmed revocation is not a withdrawal.
		if cancelled := revocationWithdrawal(ctx); cancelled != nil {
			return "", false, cancelled
		}
		return RevocationOutcomeServer, true, nil
	default:
		// A refusal, and a redirect is a refusal too: the refresh token
		// must not be sent to a destination the provider chose.
		//
		// The answer was read WHOLE, and a caller that went away while
		// it was read withdrew: nothing was confirmed, so the credential
		// stays.
		if cancelled := revocationWithdrawal(ctx); cancelled != nil {
			return "", false, cancelled
		}
		return RevocationOutcomeRejected, false, nil
	}
}

// revocationWithdrawal returns the caller's withdrawal, when the caller's
// context is done.
//
// The helper is used at the failure sites of ONE revocation attempt and at
// the CLASSIFICATION of an answer that confirms nothing, and only there: a
// cancellation that arrives while the answer is being read is a withdrawal,
// a cancellation that arrives after a completely-read answer that confirms
// nothing is a withdrawal as well, and a cancellation that arrives AFTER a
// confirmed revocation is not - the revocation was confirmed, and the local
// record of it is the connector's obligation. A blanket check after every
// response would undo that.
//
// The failure is the caller's own, and it names no token and no value.
func revocationWithdrawal(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	cancelled := ctx.Err()
	if cancelled == nil {
		return nil
	}
	return fmt.Errorf("siwc: the revocation was cancelled: %w", cancelled)
}

// revocationEndpoint resolves the endpoint the renewable session is ended
// at.
//
// The endpoint comes from the provider's OIDC configuration, which is the
// authority for it. The connector never guesses an endpoint, and it never
// uses an endpoint that is not authenticated: the request carries the
// refresh token, and a plaintext destination would hand it to anyone on the
// path.
func (r *Revoker) revocationEndpoint(ctx context.Context) (string, error) {
	discovery, err := r.oidc.Discovery(ctx)
	if err != nil {
		return "", err
	}
	endpoint := strings.TrimSpace(discovery.RevocationEndpoint)
	if endpoint == "" {
		return "", nil
	}
	if err := requireSecureDocumentURL(endpoint); err != nil {
		return "", nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		// The published endpoint is not a location a request can be sent
		// to. The connector does not guess another one: the sign-out
		// completes locally with an unconfirmed revocation.
		return "", nil
	}
	return endpoint, nil
}

// clearLocalTokens clears the tokens of a signed-out registration.
//
// The ACCOUNT MAPPING is kept: the subject, the issued client id, the host
// id, the label and the granted scopes stay, so that a later sign-in
// replaces the credentials of the same registration instead of creating a
// new one.
//
// The retained ID token is cleared as well: the documentation requires the
// access, refresh and ID tokens of the signed-out account to be cleared.
//
// The clearing is applied to the record that is CURRENTLY stored, under the
// registration's publication lock. A stale whole-record write would put
// back the credentials that were just cleared, and would undo a
// reauthorization that completed while the revocation was in flight.
//
// The clearing is a REQUIRED COMMIT: the provider has already confirmed
// the revocation, and the local record of that is what stops the connector
// from using the tokens. It is therefore not contingent on the caller's
// context, and the publication does not take one.
//
// The revoked token is compared with the stored one, and the comparison
// reports whether the session that is stored now is the session the
// revocation ended.
func (r *Revoker) clearLocalTokens(
	dir string,
	id string,
	revokedRefreshToken string,
	superseded *bool,
) error {
	_, err := r.profiles.publishInDir(dir, id, func(current ProfileRecord) (ProfileRecord, error) {
		if current.RefreshToken != revokedRefreshToken {
			if superseded != nil {
				*superseded = true
			}
			return current, nil
		}
		cleared := current
		cleared.AccessToken = ""
		cleared.RefreshToken = ""
		cleared.IDToken = ""
		cleared.Expiry = time.Time{}
		cleared.NeedsReauth = true
		return cleared, nil
	})
	return err
}

// backoffFor returns the delay before the attempt that follows the given
// one.
//
// The delay grows with every attempt and is capped, so a provider that
// keeps failing is not hammered.
func (r *Revoker) backoffFor(attempt int) time.Duration {
	delay := r.backoff
	for index := 1; index < attempt; index++ {
		delay *= 2
		if delay >= maxRevocationBackoff {
			return maxRevocationBackoff
		}
	}
	if delay > maxRevocationBackoff {
		return maxRevocationBackoff
	}
	return delay
}

// sleepFor waits for the given duration.
//
// The wait is interrupted by the context: a sign-out that is cancelled does
// not wait for the next attempt.
func sleepFor(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
