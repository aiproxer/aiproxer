package metering

import (
	"strings"
	"testing"
)

// FuzzSafeEvidenceValueTyping seeds the Finding 7 hostile lexemes and asserts a
// necessary invariant: any lexeme the public validator accepts at an
// allowlisted location must at least belong to that location's declared value
// alphabet. A regression to suffix guessing (accepting words/base64 at numeric
// locations) violates this invariant. The target is intentionally not
// registered for broad fuzzing; its seeds run under the normal test suite.

func fuzzEvidenceLexemeMatchesKind(kind safeEvidenceValueKind, lexeme string) bool {
	if lexeme == "" {
		return false
	}
	switch kind {
	case safeEvidenceValueCount:
		for i := 0; i < len(lexeme); i++ {
			if lexeme[i] < '0' || lexeme[i] > '9' {
				return false
			}
		}
		return true
	case safeEvidenceValueQuantity, safeEvidenceValueDuration, safeEvidenceValueAmount:
		for i := 0; i < len(lexeme); i++ {
			c := lexeme[i]
			if (c < '0' || c > '9') && c != '.' && c != 'e' && c != 'E' && c != '-' && c != '+' {
				return false
			}
		}
		return true
	case safeEvidenceValueCurrency:
		for i := 0; i < len(lexeme); i++ {
			if lexeme[i] < 'A' || lexeme[i] > 'Z' {
				return false
			}
		}
		return true
	case safeEvidenceValueIdentifier, safeEvidenceValueChargeKind:
		for _, r := range lexeme {
			if r <= 0x20 || r == 0x7f || r == '"' || r == '\'' || r == '\\' || r == ';' || r == '=' {
				return false
			}
		}
		return !strings.Contains(lexeme, "..")
	default:
		return false
	}
}

func FuzzSafeEvidenceValueTyping(f *testing.F) {
	seeds := []struct {
		location string
		lexeme   string
	}{
		{"$.usage.input_tokens", "100"},
		{"$.usage.input_tokens", "PrivateCustomerOutputWithoutSpaces"},
		{"$.usage.input_tokens", "c2VjcmV0LXZhbHVl"},
		{"$.usage.audio_seconds", "12.5"},
		{"$.usage.audio_seconds", "c2VjcmV0LXZhbHVl"},
		{"$.usage.web_search_requests", "0"},
		{"$.usage.web_search_requests", "OperationCompleted200"},
		{"content-length", "1024"},
		{"content-length", "abc"},
		{"$.billing.currency", "USD"},
		{"$.billing.currency", "usd"},
		{"$.billing.amount", "-2.50"},
		{"$.charge.id", "charge-abc-123"},
	}
	for _, seed := range seeds {
		f.Add(seed.location, seed.lexeme)
	}
	f.Fuzz(func(t *testing.T, location, lexeme string) {
		if lexeme == "" {
			return
		}
		kind, ok := safeEvidenceLocationKind(location)
		if !ok || !kind.valid() {
			return
		}
		field := SafeEvidenceField{
			Path: location, Lexeme: lexeme, Present: true,
			Acquisition: AcquisitionProviderResponse,
		}
		err := field.Validate()
		if err != nil {
			return
		}
		if !fuzzEvidenceLexemeMatchesKind(kind, lexeme) {
			t.Fatalf("accepted lexeme %q at %q does not match declared kind %v", lexeme, location, kind)
		}
	})
}
