package metering

import (
	"strings"
	"testing"
)

// Finding 7 contract: the finite allowlist declares exactly one semantic type
// per location, and every type has a canonical positive fixture. This is the
// completeness guard: a new allowlisted location cannot be added without a
// declared, valid type, and no location silently falls back to a generic
// identifier/string type.

func TestSafeEvidenceAllowlistDeclaresExactlyOneValidTypePerLocation(t *testing.T) {
	t.Parallel()
	paths, headers := 0, 0
	for location, kind := range safeEvidencePathAllowlist {
		paths++
		if !strings.HasPrefix(location, "$.") {
			t.Errorf("path allowlist entry %q is not a canonical JSON object path", location)
		}
		if !kind.valid() {
			t.Errorf("allowlisted path %q has no declared value type", location)
		}
	}
	for location, kind := range safeEvidenceHeaderAllowlist {
		headers++
		if location != strings.ToLower(location) {
			t.Errorf("header allowlist entry %q must be lowercase", location)
		}
		if !kind.valid() {
			t.Errorf("allowlisted header %q has no declared value type", location)
		}
	}
	if paths == 0 || headers == 0 {
		t.Fatalf("allowlist inventory unexpectedly empty: paths=%d headers=%d", paths, headers)
	}
}

// TestSafeEvidenceCriticalLocationsHaveNumericTypes pins the families the
// Finding 7 bypass reached. A suffix guess (tokens/seconds/requests) would
// leave these as identifiers, which is exactly the defect.
func TestSafeEvidenceCriticalLocationsHaveNumericTypes(t *testing.T) {
	t.Parallel()
	want := map[string]safeEvidenceValueKind{
		"$.usage.input_tokens":                             safeEvidenceValueCount,
		"$.usage.prompt_tokens_details.cache_write_tokens": safeEvidenceValueCount,
		"$.usage.input_tokens_details.cache_write_tokens":  safeEvidenceValueCount,
		"$.usage.audio_seconds":                            safeEvidenceValueDuration,
		"$.usage.web_search_requests":                      safeEvidenceValueCount,
		"$.usage.web_fetch_requests":                       safeEvidenceValueCount,
		"$.usage.input_bytes":                              safeEvidenceValueCount,
		"$.usage.duration_seconds":                         safeEvidenceValueDuration,
		"$.provider_schema.custom_cost":                    safeEvidenceValueQuantity,
		"content-length":                                   safeEvidenceValueCount,
		"x-usage-credits":                                  safeEvidenceValueQuantity,
		"$.billing.amount":                                 safeEvidenceValueAmount,
		"$.billing.currency":                               safeEvidenceValueCurrency,
		"$.charge.kind":                                    safeEvidenceValueChargeKind,
		"$.charge.id":                                      safeEvidenceValueIdentifier,
	}
	for location, kind := range want {
		if got, ok := safeEvidenceLocationKind(location); !ok || got != kind {
			t.Errorf("declared type for %q = %v (present=%v), want %v", location, got, ok, kind)
		}
	}
}

// TestSafeEvidenceEveryLocationRejectsCrossTypeLexemes proves no location
// accepts a lexeme from another semantic family through a generic fallback.
func TestSafeEvidenceEveryLocationRejectsCrossTypeLexemes(t *testing.T) {
	t.Parallel()
	type probe struct {
		lexeme string
	}
	crossType := []probe{
		{lexeme: "USD"},
		{lexeme: "req-identifier-1"},
		{lexeme: "component"},
		{lexeme: "1.5"},
	}
	for location, kind := range safeEvidencePathAllowlist {
		for _, p := range crossType {
			probeLexeme := p.lexeme
			t.Run(location+"/"+probeLexeme, func(t *testing.T) {
				t.Parallel()
				field := sanitizeTestField(location, probeLexeme)
				err := field.Validate()
				switch kind {
				case safeEvidenceValueIdentifier, safeEvidenceValueChargeKind:
					// Identifiers and charge kinds legitimately accept
					// identifier-shaped lexemes; only numeric families must
					// reject every numeric-foreign probe.
					return
				}
				switch kind {
				case safeEvidenceValueCurrency:
					if probeLexeme == "USD" {
						return
					}
				case safeEvidenceValueAmount, safeEvidenceValueQuantity, safeEvidenceValueDuration:
					if probeLexeme == "1.5" {
						return
					}
				}
				if err == nil {
					t.Fatalf("cross-type lexeme %q accepted at %q (kind=%v)", probeLexeme, location, kind)
				}
			})
		}
	}
}

func TestSafeEvidenceCanonicalFixturesPerKind(t *testing.T) {
	t.Parallel()
	fixtures := []struct {
		name     string
		location string
		lexeme   string
	}{
		{name: "count zero", location: "$.usage.input_tokens", lexeme: "0"},
		{name: "count integer", location: "$.usage.output_tokens", lexeme: "100"},
		{name: "count large", location: "content-length", lexeme: "9007199254740993"},
		{name: "quantity decimal", location: "$.usage.credits", lexeme: "0.25"},
		{name: "quantity integer", location: "$.quota.remaining", lexeme: "5"},
		{name: "custom cost", location: "$.provider_schema.custom_cost", lexeme: "0.25"},
		{name: "duration fractional", location: "$.usage.audio_seconds", lexeme: "12.5"},
		{name: "duration integer", location: "$.usage.video_seconds", lexeme: "3"},
		{name: "amount decimal", location: "$.billing.amount", lexeme: "1.32"},
		{name: "amount negative correction", location: "$.charge.amount", lexeme: "-2.50"},
		{name: "amount scientific", location: "$.cost.amount", lexeme: "1.25e3"},
		{name: "amount zero", location: "$.price.amount", lexeme: "0"},
		{name: "currency", location: "$.billing.currency", lexeme: "USD"},
		{name: "currency header", location: "x-provider-charge-currency", lexeme: "EUR"},
		{name: "charge id", location: "$.charge.id", lexeme: "charge-abc-123"},
		{name: "reset timestamp", location: "$.quota.reset", lexeme: "2026-09-01T00:00:00Z"},
		{name: "lifetime qualifier", location: "$.cache.lifetime", lexeme: "5m"},
		{name: "service context", location: "$.provider_schema.service_context", lexeme: "standard"},
		{name: "request id header", location: "x-request-id", lexeme: "req_01J9Z8X7Y6"},
		{name: "charge kind", location: "$.charge.kind", lexeme: "aggregate"},
		{name: "charge kind header", location: "x-charge-kind", lexeme: "credit"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			if err := sanitizeTestField(fixture.location, fixture.lexeme).Validate(); err != nil {
				t.Fatalf("canonical fixture %q at %q rejected: %v", fixture.lexeme, fixture.location, err)
			}
		})
	}
}

func TestSafeEvidenceTypeGrammarBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		location string
		lexeme   string
	}{
		{name: "count rejects negative", location: "$.usage.input_tokens", lexeme: "-1"},
		{name: "count rejects explicit plus", location: "$.usage.input_tokens", lexeme: "+1"},
		{name: "count rejects fraction", location: "$.usage.input_tokens", lexeme: "1.5"},
		{name: "count rejects trailing zero fraction", location: "$.usage.input_tokens", lexeme: "1.0"},
		{name: "count rejects exponent", location: "$.usage.input_tokens", lexeme: "1e3"},
		{name: "count rejects leading zero", location: "$.usage.input_tokens", lexeme: "00100"},
		{name: "count rejects overflow", location: "$.usage.input_tokens", lexeme: strings.Repeat("9", 20)},
		{name: "count rejects internal space", location: "$.usage.input_tokens", lexeme: "1 00"},
		{name: "count rejects surrounding space", location: "$.usage.input_tokens", lexeme: " 100"},
		{name: "count rejects currency", location: "$.usage.input_tokens", lexeme: "USD"},
		{name: "count rejects identifier", location: "$.usage.input_tokens", lexeme: "charge-abc"},
		{name: "duration rejects negative", location: "$.usage.audio_seconds", lexeme: "-1"},
		{name: "duration rejects negative fraction", location: "$.usage.audio_seconds", lexeme: "-0.5"},
		{name: "duration rejects word", location: "$.usage.audio_seconds", lexeme: "soon"},
		{name: "duration rejects NaN", location: "$.usage.audio_seconds", lexeme: "NaN"},
		{name: "quantity rejects negative", location: "$.usage.credits", lexeme: "-0.25"},
		{name: "quantity rejects word", location: "$.usage.credits", lexeme: "many"},
		{name: "amount rejects word", location: "$.billing.amount", lexeme: "about twelve dollars"},
		{name: "amount rejects NaN", location: "$.billing.amount", lexeme: "NaN"},
		{name: "amount rejects infinity", location: "$.billing.amount", lexeme: "Inf"},
		{name: "amount rejects multiple points", location: "$.billing.amount", lexeme: "1.2.3"},
		{name: "currency rejects lowercase", location: "$.billing.currency", lexeme: "usd"},
		{name: "currency rejects digit", location: "$.billing.currency", lexeme: "US1"},
		{name: "currency rejects space", location: "$.billing.currency", lexeme: "US D"},
		{name: "currency rejects oversize", location: "$.billing.currency", lexeme: strings.Repeat("A", 17)},
		{name: "identifier rejects space", location: "$.charge.id", lexeme: "charge 123"},
		{name: "identifier rejects semicolon", location: "x-request-id", lexeme: "req; DROP TABLE"},
		{name: "identifier rejects equals", location: "x-request-id", lexeme: "req=1"},
		{name: "identifier rejects quote", location: "$.charge.id", lexeme: `charge"1`},
		{name: "identifier rejects traversal", location: "$.charge.id", lexeme: "charge..1"},
		{name: "kind rejects unknown", location: "$.charge.kind", lexeme: "unknown-kind"},
		{name: "kind rejects casing", location: "$.charge.kind", lexeme: "COMPONENT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := sanitizeTestField(tc.location, tc.lexeme).Validate()
			if err == nil {
				t.Fatalf("boundary lexeme %q accepted at %q", tc.lexeme, tc.location)
			}
			if strings.Contains(err.Error(), tc.lexeme) {
				t.Fatalf("boundary error echoed hostile lexeme %q: %v", tc.lexeme, err)
			}
		})
	}
}

func TestSafeEvidenceUnknownAndAmbiguousLocationsReject(t *testing.T) {
	t.Parallel()
	for _, location := range []string{
		"$.usage.input_token",
		"$.usage.inputtokens",
		"$.usage.input-tokens",
		"$.usage.input_tokens2",
		"$.usage.input_tokens.extra",
		"$.usage.anything_seconds",
		"$.usage.secret_values",
		"$.provider_schema.custom_cost_extra",
		"x-usage-input-tokens-extra",
		"x-usage-anything-bytes",
		"x-provider-custom-field",
	} {
		t.Run(location, func(t *testing.T) {
			t.Parallel()
			field := sanitizeTestField(location, "100")
			if err := field.Validate(); err == nil {
				t.Fatalf("unknown/ambiguous location %q accepted", location)
			}
		})
	}
}
