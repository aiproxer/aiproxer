package diagredact

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Marker replaces recognized secret material in diagnostics.
const Marker = "[redacted]"

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----[\s\S]*?-----END (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`(?i)ghp_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`(?i)sk-or-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)sk-ant-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)sk-(?:live|proj|test)-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)\bsk-[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\-._~+/]+=*`),
	regexp.MustCompile(`(?i)(?:api[_-]?key|client[_-]?secret|access[_-]?key|secret[_-]?access[_-]?key|password|token|authorization)\s*[=:]\s*\S+`),
}

// Sanitize redacts recognized credential material then bounds the result.
// Redaction runs before truncation so secrets past maxBytes still cannot leak.
func Sanitize(s string, maxBytes int) string {
	if maxBytes < 0 {
		maxBytes = 0
	}
	s = stripUnsafeControls(s)
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, Marker)
	}
	return truncateBytes(s, maxBytes)
}

func stripUnsafeControls(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			// drop C0 controls and DEL (log/ANSI injection)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func truncateBytes(s string, maxBytes int) string {
	if maxBytes == 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	for i := maxBytes; i > 0; i-- {
		if utf8.RuneStart(s[i]) {
			return s[:i]
		}
	}
	return ""
}
