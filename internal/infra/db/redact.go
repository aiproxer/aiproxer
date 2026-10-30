package db

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// RedactDSN returns a string safe to show in operator-facing messages: passwords
// and similar secrets are elided. Non-URL DSNs are heuristically scrubbed.
func RedactDSN(dsn string) string {
	if dsn == "" {
		return dsn
	}
	if out := redactURLDSN(dsn); out != dsn {
		return out
	}
	return redactKeyValPassword(dsn)
}

func redactURLDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	if u.User == nil {
		return dsn
	}
	user := u.User.Username()
	_, hasPass := u.User.Password()
	if !hasPass {
		return dsn
	}
	// Unchanged user, redacted password; avoid net/url re-encoding path/query differences.
	redu := *u
	redu.User = url.UserPassword(user, "[REDACTED]")
	return redu.String()
}

var libpqPasswordRe = regexp.MustCompile(`(?i)\bpassword=([^\s]+)`)

func redactKeyValPassword(s string) string {
	return libpqPasswordRe.ReplaceAllString(s, "password=[REDACTED]")
}

func redactErrorString(errText, dsn string) string {
	out := errText
	trimmed := strings.TrimSpace(dsn)
	if trimmed != "" {
		out = strings.ReplaceAll(out, dsn, RedactDSN(dsn))
		if dsn != trimmed {
			out = strings.ReplaceAll(out, trimmed, RedactDSN(trimmed))
		}
	}
	if p := passwordFromDSN(dsn); p != "" {
		out = strings.ReplaceAll(out, p, "[REDACTED]")
	}
	return out
}

func passwordFromDSN(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && u.User != nil {
		if p, ok := u.User.Password(); ok {
			return p
		}
	}
	m := libpqPasswordRe.FindStringSubmatch(dsn)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

// redactedOpenError surfaces only redacted text in Error() while preserving the chain for
// errors.Is / errors.As. Using fmt.Errorf("... %w", err) would append err.Error() again and
// could re-expose DSN secrets if a driver echoes them.
type redactedOpenError struct {
	op      string
	visible string
	err     error
}

func (e *redactedOpenError) Error() string {
	return fmt.Sprintf("db: %s: %s", e.op, e.visible)
}

func (e *redactedOpenError) Unwrap() error {
	return e.err
}

// redactOpenError wraps a database open or ping failure without raw DSN or password text.
// The returned error wraps err so errors.Is and errors.As still observe driver or context errors.
func redactOpenError(dsn, op string, err error) error {
	if err == nil {
		return nil
	}
	visible := redactErrorString(err.Error(), dsn)
	return &redactedOpenError{op: op, visible: visible, err: err}
}
