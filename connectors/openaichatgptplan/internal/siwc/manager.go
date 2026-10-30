package siwc

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// The SIWC profile manager.
//
// The manager is the surface the connector's own surfaces use: the operator
// commands and, later, the inference path. It owns the operations over the
// saved registrations - list, select, add, reauthorize, refresh and sign out
// - and it owns the POLICY that goes with them.
//
// # The selection is explicit
//
// The manager changes the selected registration in exactly two places:
//
//   - an explicit Select, and
//   - the successful completion of a sign-in the operator asked for.
//
// The documentation asks for exactly that: "Complete and validate the
// selected account's OAuth flow before making it active", and "Continue with
// a saved ChatGPT account ... Use a different ChatGPT account: Select its
// existing registration or register a new one".
//
// The selection NEVER changes because of a failure. A refresh that failed, a
// plan allowance that is exhausted, a provider error - none of them moves
// the operator to another registration. Requirement 2.13 forbids it, and the
// reason is a product one: a ChatGPT plan allowance is not a failover
// mechanism, and a proxy that quietly moves a request to another subscriber's
// account is exactly the pooling the requirement refuses.
//
// # The registration is identified by its local key
//
// Every NOTICE and every ERROR the manager produces names the registration
// through its local profile id and its label. The subject and the email
// hint of an account are never part of a notice or an error: a diagnostic
// identifies the account through the non-secret local key.
//
// The LISTING is the one report that carries the subject, and it carries it
// as the STORE's own summary: ProfileSummary names the subject of a
// registration, and the manager returns that summary unchanged. The
// subject is stored state - the account the registration belongs to - and
// the summary is the store's own non-secret view of it. The
// operator-facing rendering of a listing is the command's, and that
// rendering shows the local key.

// RegistrationAuthorizer performs the sign-in flows of the manager.
//
// The interface is consumed where the sign-in is needed, so that a caller
// that only lists the registrations never gains the ability to run one.
type RegistrationAuthorizer interface {
	// Register performs a first registration.
	Register(ctx context.Context, req RegisterRequest) (ProfileRecord, error)
	// Reauthorize performs a returning authorization.
	Reauthorize(ctx context.Context, req ReauthorizeRequest) (ProfileRecord, error)
}

// CredentialRefresher renews the credentials of one registration.
type CredentialRefresher interface {
	// Refresh renews the credentials of the named registration.
	Refresh(ctx context.Context, profileID string) (ProfileRecord, error)
}

// SessionRevoker ends the renewable session of one registration.
type SessionRevoker interface {
	// Logout ends the renewable session and clears the local tokens.
	Logout(ctx context.Context, profileID string) (LogoutResult, error)
}

// ManagerConfig is the configuration of the manager.
type ManagerConfig struct {
	// Profiles persists the registrations. It is required.
	Profiles *ProfileStore
	// Authorizer performs the sign-in flows. It is optional: a caller
	// that only lists and selects does not need one.
	Authorizer RegistrationAuthorizer
	// Refresher renews the credentials. It is optional.
	Refresher CredentialRefresher
	// Revoker ends the renewable sessions. It is optional.
	Revoker SessionRevoker
	// Notice reports non-fatal events to the operator. Empty means no
	// notice is reported.
	Notice Notice
}

// Manager is the SIWC profile manager.
//
// The manager is safe for concurrent use: the state it changes is the
// store's, and the store's operations are atomic.
type Manager struct {
	profiles   *ProfileStore
	authorizer RegistrationAuthorizer
	refresher  CredentialRefresher
	revoker    SessionRevoker
	notice     Notice
}

// NewManager returns the manager of the saved registrations.
//
// The profile store is required: a manager that cannot reach the
// registrations it manages is not a manager.
func NewManager(config ManagerConfig) (*Manager, error) {
	if config.Profiles == nil {
		return nil, errors.New("siwc: the manager needs a profile store")
	}
	return &Manager{
		profiles:   config.Profiles,
		authorizer: config.Authorizer,
		refresher:  config.Refresher,
		revoker:    config.Revoker,
		notice:     config.Notice,
	}, nil
}

// List returns the non-secret summary of every saved registration.
//
// The summaries hold no access token, no refresh token, no retained ID token
// and no email hint.
func (m *Manager) List(ctx context.Context) ([]ProfileSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.profiles.List()
}

// Selected returns the id of the explicitly selected registration.
//
// An empty id with no error means that no registration is selected yet.
func (m *Manager) Selected(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return m.profiles.Selected()
}

// Select records the explicitly selected registration.
//
// This is the operation that changes the selection. Nothing else does, and
// in particular no failure does: a refresh that failed or a plan allowance
// that is exhausted leaves the operator on the registration they chose.
func (m *Manager) Select(ctx context.Context, profileID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.profiles.Select(strings.TrimSpace(profileID))
}

// Active returns the record of the selected registration.
//
// A registration that is not selected is not an error: the manager reports
// that no registration is active, and the caller decides what to do with it.
func (m *Manager) Active(ctx context.Context) (ProfileRecord, error) {
	id, err := m.Selected(ctx)
	if err != nil {
		return ProfileRecord{}, err
	}
	if id == "" {
		return ProfileRecord{}, errors.New("siwc: no registration is selected")
	}
	return m.profiles.Load(id)
}

// Add registers a new ChatGPT account.
//
// The sign-in is the documented one, and the registration becomes the active
// one when it completes: the operator chose this account, and the
// documentation asks for the flow to be completed and validated before the
// account is made active.
//
// A registration that already exists is refused: the documentation forbids
// overwriting one registration's credentials with another account's.
func (m *Manager) Add(ctx context.Context, req RegisterRequest) (ProfileRecord, error) {
	if m.authorizer == nil {
		return ProfileRecord{}, errors.New("siwc: the manager cannot sign in: no authorization service is configured")
	}
	record, err := m.authorizer.Register(ctx, req)
	if err != nil {
		return ProfileRecord{}, err
	}
	if err := m.profiles.Select(record.ID); err != nil {
		return record, err
	}
	return record, nil
}

// Reauthorize renews the credentials of a saved registration.
//
// The reauthorization confirms that the authorized account is the selected
// registration before it replaces the credentials, and it keeps the issued
// client id of that registration.
//
// A successful reauthorization makes the registration the active one, for
// the same reason a registration does: the operator asked for this account.
func (m *Manager) Reauthorize(
	ctx context.Context,
	req ReauthorizeRequest,
) (ProfileRecord, error) {
	if m.authorizer == nil {
		return ProfileRecord{}, errors.New("siwc: the manager cannot sign in: no authorization service is configured")
	}
	record, err := m.authorizer.Reauthorize(ctx, req)
	if err != nil {
		return ProfileRecord{}, err
	}
	if err := m.profiles.Select(record.ID); err != nil {
		return record, err
	}
	return record, nil
}

// Refresh renews the credentials of one registration.
//
// The refresh is serialized per registration and it never changes the
// selection.
func (m *Manager) Refresh(ctx context.Context, profileID string) (ProfileRecord, error) {
	if m.refresher == nil {
		return ProfileRecord{}, errors.New("siwc: the manager cannot refresh: no refresher is configured")
	}
	return m.refresher.Refresh(ctx, strings.TrimSpace(profileID))
}

// Token returns a usable access token of one registration.
//
// The token is renewed when it is near its expiry, and the renewal is
// serialized per registration. A registration whose credential is unusable
// reports the classified failure and keeps its place: the manager never
// moves the operator to another registration.
//
// The grant carries the one-way fingerprint of the credential pair the token
// was acquired from. It is taken from the SAME record that supplied the token,
// so a caller never has to infer the generation from a later read of the
// store - a later read may already describe a replacement.
func (m *Manager) Token(ctx context.Context, profileID string) (TokenGrant, error) {
	record, err := m.Refresh(ctx, profileID)
	if err != nil {
		return TokenGrant{}, err
	}
	if strings.TrimSpace(record.AccessToken) == "" {
		return TokenGrant{}, errors.New("siwc: the registration carries no access token")
	}
	generation, _ := record.CredentialGeneration()
	return TokenGrant{AccessToken: record.AccessToken, Generation: generation}, nil
}

// Logout ends the renewable session of one registration and clears its
// local tokens.
//
// The registration's account mapping and this host's id are retained, so
// that a later sign-in replaces the credentials of the same registration.
//
// The selection is NOT changed: the sign-out stops the requests of that
// registration, and it does not move the operator to another one.
//
// The unconfirmed revocation is reported through the notice, because the
// operator has to act on it: the session was not confirmed as ended at the
// provider, and the app can be disconnected in ChatGPT Settings.
func (m *Manager) Logout(ctx context.Context, profileID string) (LogoutResult, error) {
	if m.revoker == nil {
		return LogoutResult{}, errors.New("siwc: the manager cannot sign out: no revoker is configured")
	}
	result, err := m.revoker.Logout(ctx, strings.TrimSpace(profileID))
	if err != nil {
		return result, err
	}
	if result.Guidance != "" {
		m.notify(result.Guidance)
	}
	return result, nil
}

// notify reports a non-fatal event to the operator.
func (m *Manager) notify(message string) {
	if m.notice == nil {
		return
	}
	m.notice(message)
}

// DescribeRegistration renders the non-secret identity of a registration.
//
// The rendering names the local profile id and the label. It never names the
// subject, the email hint or a token: a diagnostic identifies the account
// through the non-secret local key.
func DescribeRegistration(record ProfileRecord) string {
	label := strings.TrimSpace(record.Label)
	if label == "" {
		return fmt.Sprintf("registration %s", record.ID)
	}
	return fmt.Sprintf("registration %s (%s)", record.ID, label)
}
