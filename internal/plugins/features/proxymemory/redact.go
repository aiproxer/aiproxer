package proxymemory

import "regexp"

// redactedSecret is the Codex replacement marker for every redacted value.
const redactedSecret = "[REDACTED_SECRET]"

// The Codex redaction patterns (codex-rs/secrets/src/sanitizer.rs, design §4),
// compiled once into package-level immutable data.
var (
	bearerTokenPattern      = regexp.MustCompile(`(?i:\bBearer)[ \t]+[A-Za-z0-9._~+/-]{16,}=*`)
	openAIKeyPattern        = regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`)
	awsAccessKeyPattern     = regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)
	secretAssignmentPattern = regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password)\b(\s*[:=]\s*)(["']?)[^\s"']{8,}`)
)

// Redact replaces secret-like values in s with [REDACTED_SECRET] using the
// Codex redaction patterns applied in the Codex order (design §4,
// Requirement 3.3). A string with no match is returned unchanged.
func Redact(s string) string {
	s = bearerTokenPattern.ReplaceAllString(s, "Bearer "+redactedSecret)
	s = openAIKeyPattern.ReplaceAllString(s, redactedSecret)
	s = awsAccessKeyPattern.ReplaceAllString(s, redactedSecret)
	s = secretAssignmentPattern.ReplaceAllString(s, "${1}${2}${3}"+redactedSecret)
	return s
}
