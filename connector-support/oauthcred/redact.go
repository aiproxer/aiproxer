package oauthcred

import (
	"errors"
	"strings"
)

const redactedPlaceholder = "[REDACTED]"

// Redact replaces any occurrences of secret strings with [REDACTED].
func Redact(s string, secrets ...string) string {
	for _, sec := range secrets {
		trimmed := strings.TrimSpace(sec)
		if trimmed != "" {
			s = strings.ReplaceAll(s, trimmed, redactedPlaceholder)
		}
	}
	return s
}

// RedactError returns an error whose message has secrets redacted.
func RedactError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	return errors.New(Redact(err.Error(), secrets...))
}
