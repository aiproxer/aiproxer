package reasoningpreservation

import (
	"context"
	"errors"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// SecretguardTrustedSanitizer adapts the existing pkg/aipsdk/secretguard.Matcher
// contract to the feature's narrow TrustedTextSanitizer seam. This reuses the
// repository's authoritative exact-match secret redaction authority and avoids a
// second heuristic detector. No new detection logic is introduced here.
//
// Import direction: feature depends on pkg/aipsdk contract only, never on
// concrete secretguard feature/engine implementation. Composition (runtimebundle, later
// task) will inject an instance produced from MatcherResolver.
type SecretguardTrustedSanitizer struct {
	Matcher secretguard.Matcher
}

// SanitizeText redacts exact catalog matches via the underlying Matcher.
// Findings are discarded at this seam; only the sanitized text is returned.
// A nil Matcher fails closed rather than returning unredacted input.
func (s SecretguardTrustedSanitizer) SanitizeText(ctx context.Context, text string) (string, error) {
	if s.Matcher == nil {
		return "", errors.New("reasoning-output-preservation: trusted secret matcher is required")
	}
	out, _, err := s.Matcher.RedactString(ctx, text)
	return out, err
}

// NewTrustedSanitizerFromMatcher returns a TrustedTextSanitizer backed by m.
// A nil Matcher yields nil so CompressionServices validation can fail closed
// when a redact-then-allow policy is configured but no sanitizer is wired.
func NewTrustedSanitizerFromMatcher(m secretguard.Matcher) TrustedTextSanitizer {
	if m == nil {
		return nil
	}
	return SecretguardTrustedSanitizer{Matcher: m}
}

var _ TrustedTextSanitizer = SecretguardTrustedSanitizer{}
