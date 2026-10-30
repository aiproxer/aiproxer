package acp

import (
	"strings"
	"unicode/utf8"
)

// MaxBoundStderrBytes caps stderr folded into inventory command errors.
const MaxBoundStderrBytes = 8192

// SanitizeBoundStderr sanitizes subprocess stderr for error wrapping: truncates
// to MaxBoundStderrBytes, strips invalid UTF-8 and non-printable runes
// (keeps tab/newline/carriage return), then trims space. Operates on a view of
// b without requiring a defensive clone when the caller already owns a capped buffer.
func SanitizeBoundStderr(b []byte) string {
	if len(b) > MaxBoundStderrBytes {
		b = b[:MaxBoundStderrBytes]
	}
	var out []byte
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		switch {
		case r == '\t', r == '\n', r == '\r':
			out = utf8.AppendRune(out, r)
		case r >= 32 && r != 127:
			out = utf8.AppendRune(out, r)
		}
		i += size
	}
	return strings.TrimSpace(string(out))
}
