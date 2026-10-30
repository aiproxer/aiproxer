package oauthcred

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	// ErrTerminalRefresh indicates an unrecoverable refresh failure (e.g. invalid_grant, 401/403).
	ErrTerminalRefresh = errors.New("oauthcred: terminal refresh failure")

	// ErrQuarantined indicates the credentials are quarantined and require explicit re-login.
	ErrQuarantined = errors.New("oauthcred: credentials are quarantined; explicit re-login required")

	// ErrLoggedOut indicates no credentials exist or the session was logged out.
	ErrLoggedOut = errors.New("oauthcred: not logged in or credentials deleted")
)

// DefaultSkew is the default margin before expiry to trigger a token refresh (60s).
const DefaultSkew = 60 * time.Second

// Refresher defines the interface for refreshing OAuth tokens.
type Refresher interface {
	Refresh(ctx context.Context, refreshToken string) (access, refresh string, expiry time.Time, err error)
}

// RefresherFunc is an adapter allowing a bare function to be used as a Refresher.
type RefresherFunc func(ctx context.Context, refreshToken string) (access, refresh string, expiry time.Time, err error)

func (f RefresherFunc) Refresh(ctx context.Context, refreshToken string) (string, string, time.Time, error) {
	return f(ctx, refreshToken)
}

// HTTPError wraps an HTTP error status and body.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("oauthcred: refresh HTTP %d: %s", e.StatusCode, Redact(e.Body))
}

func (e *HTTPError) HTTPStatusCode() int {
	return e.StatusCode
}

// IsTerminalError determines if a refresh error is unrecoverable (warrants quarantining credentials).
func IsTerminalError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrTerminalRefresh) {
		return true
	}

	type statusCoder interface {
		StatusCode() int
	}
	type httpStatusCoder interface {
		HTTPStatusCode() int
	}

	// Refresher is an interface external connectors implement and decorate, so a
	// status-coded error can arrive wrapped. Classify by identity: a plain type
	// assertion misses the wrapped cause and silently downgrades a terminal
	// refresh failure to the message-text fallback below (which only covers
	// 400/401/403).
	var sc int
	var hsc httpStatusCoder
	if errors.As(err, &hsc) {
		sc = hsc.HTTPStatusCode()
	} else {
		var scErr statusCoder
		if errors.As(err, &scErr) {
			sc = scErr.StatusCode()
		}
	}

	if sc >= 400 && sc <= 499 {
		// 408 Request Timeout and 429 Too Many Requests are transient, not terminal
		if sc != 408 && sc != 429 {
			return true
		}
	}

	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "invalid_grant") ||
		strings.Contains(msg, "invalid_client") ||
		strings.Contains(msg, "unauthorized_client") ||
		strings.Contains(msg, "unsupported_grant_type") ||
		strings.Contains(msg, "http 401") ||
		strings.Contains(msg, "status 401") ||
		strings.Contains(msg, "http 403") ||
		strings.Contains(msg, "status 403") ||
		strings.Contains(msg, "http 400") ||
		strings.Contains(msg, "status 400") {
		return true
	}

	return false
}

// SessionOption configures a Session.
type SessionOption func(*Session)

// WithSkew overrides the default 60s expiration skew.
func WithSkew(skew time.Duration) SessionOption {
	return func(s *Session) {
		s.skew = skew
	}
}

// Session manages credential lifecycle, proactive refresh, and terminal quarantine.
type Session struct {
	store     Store
	refresher Refresher
	skew      time.Duration
	mu        sync.Mutex
	// quarantined latches a terminal refresh outcome in-process so a failed
	// quarantine persistence cannot cause a terminal credential replay.
	// Guarded by mu; cleared only by explicit Save/Delete.
	quarantined      bool
	quarantineReason string
}

// NewSession creates a new Session.
func NewSession(store Store, refresher Refresher, opts ...SessionOption) *Session {
	s := &Session{
		store:     store,
		refresher: refresher,
		skew:      DefaultSkew,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Token returns a valid access token. If quarantined, it returns ErrQuarantined without calling Refresher.
// If the access token is fresh (expires > skew), it returns the cached token.
// Otherwise, it calls Refresher once to refresh tokens.
// If refresh fails terminally, it latches quarantine in-process, persists to
// store, and returns error. A quarantine persistence failure is surfaced to
// the caller but the in-process latch still prevents refresher replay; only
// an explicit Save/Delete clears the latch.
func (s *Session) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.quarantined {
		return "", fmt.Errorf("%w: %s", ErrQuarantined, s.quarantineReason)
	}

	rec, err := s.store.Load()
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrLoggedOut
		}
		return "", fmt.Errorf("oauthcred: load token: %w", err)
	}

	if rec.Quarantined {
		s.quarantined = true
		s.quarantineReason = rec.QuarantineReason
		return "", fmt.Errorf("%w: %s", ErrQuarantined, rec.QuarantineReason)
	}

	skew := s.skew
	if skew <= 0 {
		skew = DefaultSkew
	}

	isFresh := rec.AccessToken != "" && !rec.Expiry.IsZero() && time.Until(rec.Expiry) > skew
	if isFresh {
		return rec.AccessToken, nil
	}

	if strings.TrimSpace(rec.RefreshToken) == "" {
		return "", fmt.Errorf("oauthcred: refresh token is empty")
	}
	if s.refresher == nil {
		return "", fmt.Errorf("oauthcred: refresher is required")
	}

	newAccess, newRefresh, newExpiry, err := s.refresher.Refresh(ctx, rec.RefreshToken)
	if err != nil {
		if IsTerminalError(err) {
			cleanErr := Redact(err.Error(), rec.AccessToken, rec.RefreshToken)
			rec.Quarantined = true
			rec.QuarantineReason = cleanErr
			s.quarantined = true
			s.quarantineReason = cleanErr
			if saveErr := s.store.Save(rec); saveErr != nil {
				return "", fmt.Errorf("%w: %s: quarantine persist failed: %w", ErrTerminalRefresh, cleanErr, saveErr)
			}
			return "", fmt.Errorf("%w: %s", ErrTerminalRefresh, cleanErr)
		}
		return "", fmt.Errorf("oauthcred: refresh failed: %w", RedactError(err, rec.AccessToken, rec.RefreshToken))
	}

	if strings.TrimSpace(newAccess) == "" {
		rec.Quarantined = true
		rec.QuarantineReason = "refresher returned empty access token"
		s.quarantined = true
		s.quarantineReason = rec.QuarantineReason
		if saveErr := s.store.Save(rec); saveErr != nil {
			return "", fmt.Errorf("%w: %s: quarantine persist failed: %w", ErrTerminalRefresh, rec.QuarantineReason, saveErr)
		}
		return "", fmt.Errorf("%w: %s", ErrTerminalRefresh, rec.QuarantineReason)
	}

	rec.AccessToken = newAccess
	if strings.TrimSpace(newRefresh) != "" {
		rec.RefreshToken = newRefresh
	}
	rec.Expiry = newExpiry
	rec.Quarantined = false
	rec.QuarantineReason = ""

	if err := s.store.Save(rec); err != nil {
		return "", fmt.Errorf("oauthcred: save refreshed token: %w", err)
	}

	return rec.AccessToken, nil
}

// Record returns the currently stored TokenRecord.
func (s *Session) Record() (TokenRecord, error) {
	return s.store.Load()
}

// Save saves a TokenRecord to the store. Saving a record with Quarantined=false clears quarantine.
func (s *Session) Save(rec TokenRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.Save(rec); err != nil {
		return err
	}
	s.quarantined = rec.Quarantined
	s.quarantineReason = rec.QuarantineReason
	return nil
}

// Delete removes the credentials from storage.
func (s *Session) Delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.Delete(); err != nil {
		return err
	}
	s.quarantined = false
	s.quarantineReason = ""
	return nil
}

// Logout removes the credentials from storage.
func (s *Session) Logout() error {
	return s.Delete()
}
