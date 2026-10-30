package metering

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// safeEvidenceValueKind is the declared semantic value type of one allowlisted
// evidence location. Every allowlisted location maps to exactly one kind, and
// validation never infers a type from the spelling of the location. The zero
// value is intentionally invalid so a location cannot be added with no type.
type safeEvidenceValueKind uint8

const (
	safeEvidenceValueInvalid safeEvidenceValueKind = iota
	// safeEvidenceValueCount is a non-negative canonical base-10 integer
	// (tokens, requests, frames, pages, bytes, pixels, content length).
	safeEvidenceValueCount
	// safeEvidenceValueQuantity is a non-negative bounded exact decimal
	// (credits, pool limits/remaining, custom cost, rate counters).
	safeEvidenceValueQuantity
	// safeEvidenceValueDuration is a non-negative bounded exact decimal number
	// of seconds.
	safeEvidenceValueDuration
	// safeEvidenceValueAmount is a signed bounded exact decimal amount in major
	// currency units (billing, charge, cost and price).
	safeEvidenceValueAmount
	// safeEvidenceValueCurrency is an uppercase bounded currency code.
	safeEvidenceValueCurrency
	// safeEvidenceValueIdentifier is a bounded economic identifier/qualifier.
	safeEvidenceValueIdentifier
	// safeEvidenceValueChargeKind is one known charge-kind enum value.
	safeEvidenceValueChargeKind
)

func (k safeEvidenceValueKind) valid() bool {
	switch k {
	case safeEvidenceValueCount, safeEvidenceValueQuantity, safeEvidenceValueDuration,
		safeEvidenceValueAmount, safeEvidenceValueCurrency, safeEvidenceValueIdentifier,
		safeEvidenceValueChargeKind:
		return true
	default:
		return false
	}
}

// Safe evidence locations are a finite, provider-neutral contract. Provider
// normalizers own mapping from wire fields into these exact locations. A new
// provider field must use a versioned normalizer; arbitrary paths and header
// prefixes are not a retention mechanism. Each location's map value is the one
// semantic type its lexeme must satisfy.
var safeEvidencePathAllowlist = map[string]safeEvidenceValueKind{
	"$.usage.input_tokens":                   safeEvidenceValueCount,
	"$.usage.output_tokens":                  safeEvidenceValueCount,
	"$.usage.total_tokens":                   safeEvidenceValueCount,
	"$.usage.cache_read_input_tokens":        safeEvidenceValueCount,
	"$.usage.cache_write_input_tokens":       safeEvidenceValueCount,
	"$.usage.reasoning_output_tokens":        safeEvidenceValueCount,
	"$.usage.tool_use_prompt_tokens":         safeEvidenceValueCount,
	"$.usage.audio_seconds":                  safeEvidenceValueDuration,
	"$.usage.video_seconds":                  safeEvidenceValueDuration,
	"$.usage.image_count":                    safeEvidenceValueCount,
	"$.usage.input_image_count":              safeEvidenceValueCount,
	"$.usage.output_image_count":             safeEvidenceValueCount,
	"$.usage.input_image_tokens":             safeEvidenceValueCount,
	"$.usage.output_image_tokens":            safeEvidenceValueCount,
	"$.usage.input_text_tokens":              safeEvidenceValueCount,
	"$.usage.output_text_tokens":             safeEvidenceValueCount,
	"$.usage.input_audio_tokens":             safeEvidenceValueCount,
	"$.usage.output_audio_tokens":            safeEvidenceValueCount,
	"$.usage.input_audio_seconds":            safeEvidenceValueDuration,
	"$.usage.output_audio_seconds":           safeEvidenceValueDuration,
	"$.usage.input_video_tokens":             safeEvidenceValueCount,
	"$.usage.output_video_tokens":            safeEvidenceValueCount,
	"$.usage.input_video_seconds":            safeEvidenceValueDuration,
	"$.usage.output_video_seconds":           safeEvidenceValueDuration,
	"$.usage.input_video_frames":             safeEvidenceValueCount,
	"$.usage.output_video_frames":            safeEvidenceValueCount,
	"$.usage.input_document_tokens":          safeEvidenceValueCount,
	"$.usage.output_document_tokens":         safeEvidenceValueCount,
	"$.usage.input_document_pages":           safeEvidenceValueCount,
	"$.usage.output_document_pages":          safeEvidenceValueCount,
	"$.usage.cache_creation_5m_input_tokens": safeEvidenceValueCount,
	"$.usage.cache_creation_1h_input_tokens": safeEvidenceValueCount,
	"$.usage.web_fetch_requests":             safeEvidenceValueCount,
	"$.usage.web_search_requests":            safeEvidenceValueCount,
	"$.usage.input_file_pages":               safeEvidenceValueCount,
	"$.usage.output_file_pages":              safeEvidenceValueCount,
	"$.usage.input_bytes":                    safeEvidenceValueCount,
	"$.usage.output_bytes":                   safeEvidenceValueCount,
	"$.usage.image_pixels":                   safeEvidenceValueCount,
	"$.usage.video_frames":                   safeEvidenceValueCount,
	"$.usage.file_pages":                     safeEvidenceValueCount,
	"$.usage.bytes":                          safeEvidenceValueCount,
	"$.usage.queries":                        safeEvidenceValueCount,
	"$.usage.credits":                        safeEvidenceValueQuantity,
	"$.usage.duration_seconds":               safeEvidenceValueDuration,
	"$.usage.frame_count":                    safeEvidenceValueCount,
	"$.usage.page_count":                     safeEvidenceValueCount,
	"$.billing.amount":                       safeEvidenceValueAmount,
	"$.billing.currency":                     safeEvidenceValueCurrency,
	"$.charge.amount":                        safeEvidenceValueAmount,
	"$.charge.currency":                      safeEvidenceValueCurrency,
	"$.charge.id":                            safeEvidenceValueIdentifier,
	"$.charge.kind":                          safeEvidenceValueChargeKind,
	"$.cost.amount":                          safeEvidenceValueAmount,
	"$.cost.currency":                        safeEvidenceValueCurrency,
	"$.price.amount":                         safeEvidenceValueAmount,
	"$.price.currency":                       safeEvidenceValueCurrency,
	"$.quota.limit":                          safeEvidenceValueQuantity,
	"$.quota.remaining":                      safeEvidenceValueQuantity,
	"$.quota.reset":                          safeEvidenceValueIdentifier,
	"$.allowance.limit":                      safeEvidenceValueQuantity,
	"$.allowance.remaining":                  safeEvidenceValueQuantity,
	"$.allowance.reset":                      safeEvidenceValueIdentifier,
	"$.rate_limit.limit":                     safeEvidenceValueQuantity,
	"$.rate_limit.remaining":                 safeEvidenceValueQuantity,
	"$.rate_limit.reset":                     safeEvidenceValueIdentifier,
	"$.cache.read_tokens":                    safeEvidenceValueCount,
	"$.cache.write_tokens":                   safeEvidenceValueCount,
	"$.cache.lifetime":                       safeEvidenceValueIdentifier,
	"$.provider_schema.custom_cost":          safeEvidenceValueQuantity,
	"$.provider_schema.service_context":      safeEvidenceValueIdentifier,
	"$.provider_schema.service_tier":         safeEvidenceValueIdentifier,
}

// Provider-family nested paths and compatible aliases are separately bounded
// so evidence can retain the exact wire location without widening the generic
// top-level economic path contract.
var safeEvidenceProviderNestedPathAllowlist = map[string]safeEvidenceValueKind{
	"$.usage.audio_input_tokens":                       safeEvidenceValueCount,
	"$.usage.prompt_audio_tokens":                      safeEvidenceValueCount,
	"$.usage.audio_output_tokens":                      safeEvidenceValueCount,
	"$.usage.completion_audio_tokens":                  safeEvidenceValueCount,
	"$.usage.prompt_tokens_details.text_tokens":        safeEvidenceValueCount,
	"$.usage.prompt_tokens_details.audio_tokens":       safeEvidenceValueCount,
	"$.usage.prompt_tokens_details.image_tokens":       safeEvidenceValueCount,
	"$.usage.prompt_tokens_details.cache_write_tokens": safeEvidenceValueCount,
	"$.usage.completion_tokens_details.text_tokens":    safeEvidenceValueCount,
	"$.usage.completion_tokens_details.audio_tokens":   safeEvidenceValueCount,
	"$.usage.input_tokens_details.text_tokens":         safeEvidenceValueCount,
	"$.usage.input_tokens_details.audio_tokens":        safeEvidenceValueCount,
	"$.usage.input_tokens_details.cache_write_tokens":  safeEvidenceValueCount,
	"$.usage.output_tokens_details.text_tokens":        safeEvidenceValueCount,
	"$.usage.output_tokens_details.audio_tokens":       safeEvidenceValueCount,
	"$.usage.input_tokens_details.images":              safeEvidenceValueCount,
	"$.usage.output_tokens_details.images":             safeEvidenceValueCount,
}

var safeEvidenceHeaderAllowlist = map[string]safeEvidenceValueKind{
	"content-length":                   safeEvidenceValueCount,
	"x-request-id":                     safeEvidenceValueIdentifier,
	"x-correlation-id":                 safeEvidenceValueIdentifier,
	"x-provider-request-id":            safeEvidenceValueIdentifier,
	"x-provider-charge-id":             safeEvidenceValueIdentifier,
	"x-provider-account-id":            safeEvidenceValueIdentifier,
	"x-provider-account-key":           safeEvidenceValueIdentifier,
	"x-usage-input-tokens":             safeEvidenceValueCount,
	"x-usage-output-tokens":            safeEvidenceValueCount,
	"x-usage-total-tokens":             safeEvidenceValueCount,
	"x-usage-cache-read-input-tokens":  safeEvidenceValueCount,
	"x-usage-cache-write-input-tokens": safeEvidenceValueCount,
	"x-usage-reasoning-output-tokens":  safeEvidenceValueCount,
	"x-usage-audio-seconds":            safeEvidenceValueDuration,
	"x-usage-video-seconds":            safeEvidenceValueDuration,
	"x-usage-image-count":              safeEvidenceValueCount,
	"x-usage-file-pages":               safeEvidenceValueCount,
	"x-usage-queries":                  safeEvidenceValueCount,
	"x-usage-credits":                  safeEvidenceValueQuantity,
	"x-usage-bytes":                    safeEvidenceValueCount,
	"x-usage-duration-seconds":         safeEvidenceValueDuration,
	"x-usage-frame-count":              safeEvidenceValueCount,
	"x-billing-amount":                 safeEvidenceValueAmount,
	"x-billing-currency":               safeEvidenceValueCurrency,
	"x-charge-amount":                  safeEvidenceValueAmount,
	"x-charge-currency":                safeEvidenceValueCurrency,
	"x-charge-item-id":                 safeEvidenceValueIdentifier,
	"x-charge-kind":                    safeEvidenceValueChargeKind,
	"x-cost-amount":                    safeEvidenceValueAmount,
	"x-cost-currency":                  safeEvidenceValueCurrency,
	"x-price-amount":                   safeEvidenceValueAmount,
	"x-price-currency":                 safeEvidenceValueCurrency,
	"x-quota-limit":                    safeEvidenceValueQuantity,
	"x-quota-remaining":                safeEvidenceValueQuantity,
	"x-quota-reset":                    safeEvidenceValueIdentifier,
	"x-allowance-limit":                safeEvidenceValueQuantity,
	"x-allowance-remaining":            safeEvidenceValueQuantity,
	"x-allowance-reset":                safeEvidenceValueIdentifier,
	"x-rate-limit-limit":               safeEvidenceValueQuantity,
	"x-rate-limit-remaining":           safeEvidenceValueQuantity,
	"x-rate-limit-reset":               safeEvidenceValueIdentifier,
	"x-ratelimit-limit":                safeEvidenceValueQuantity,
	"x-ratelimit-remaining":            safeEvidenceValueQuantity,
	"x-ratelimit-reset":                safeEvidenceValueIdentifier,
	"x-cache-read-tokens":              safeEvidenceValueCount,
	"x-cache-write-tokens":             safeEvidenceValueCount,
	"x-cache-lifetime":                 safeEvidenceValueIdentifier,
	"x-provider-usage-input-tokens":    safeEvidenceValueCount,
	"x-provider-usage-output-tokens":   safeEvidenceValueCount,
	"x-provider-usage-total-tokens":    safeEvidenceValueCount,
	"x-provider-billing-amount":        safeEvidenceValueAmount,
	"x-provider-billing-currency":      safeEvidenceValueCurrency,
	"x-provider-charge-amount":         safeEvidenceValueAmount,
	"x-provider-charge-currency":       safeEvidenceValueCurrency,
}

func validateSafeEvidenceLocation(location string) error {
	if !utf8.ValidString(location) {
		return fmt.Errorf("safe evidence location must be valid UTF-8")
	}
	if strings.Contains(location, "..") {
		return fmt.Errorf("safe evidence location %q traverses outside the allowlist", location)
	}
	if len(location) > MaxSafeEvidenceFieldBytes {
		return fmt.Errorf("safe evidence location exceeds %d bytes", MaxSafeEvidenceFieldBytes)
	}
	if len(location) > 0 && location[0] == '$' {
		if _, ok := safeEvidenceLocationKind(location); !ok {
			return fmt.Errorf("safe evidence path is not an allowlisted economic location")
		}
		return nil
	}
	if _, ok := safeEvidenceHeaderAllowlist[strings.ToLower(location)]; !ok {
		return fmt.Errorf("safe evidence header is not an allowlisted economic location")
	}
	return nil
}

func validateSafeEvidenceLexeme(field, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if err := validateBoundedText(field, value, MaxSafeEvidenceFieldBytes); err != nil {
		return err
	}
	return rejectSecretShapedLexeme(field, value)
}

// Secret-shaped lexemes can never be economic evidence. Prompts, tool
// arguments, headers, cookies, credentials, key material and ciphertext
// are rejected at the public/normalization boundary and never persist,
// never enter diagnostics or errors, and never cross the operator surface.
var safeEvidenceSecretPrefixes = []string{
	"sk-", "sk_ant_", "xoxa-", "xoxb-", "xoxp-", "xoxr-", "xoxs-",
	"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "aiza", "akia", "gsk_",
	"bearer ", "basic ",
}

var safeEvidenceSecretMarkers = []string{
	"-----begin", "-----end",
}

var safeEvidenceSecretSubstrings = []string{
	"password", "passwd", "secret", "api_key", "apikey",
	"auth_token", "access_token", "refresh_token", "client_secret", "session_token",
}

func rejectSecretShapedLexeme(field, value string) error {
	lower := strings.ToLower(value)
	for _, prefix := range safeEvidenceSecretPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return fmt.Errorf("%s carries secret-shaped material", field)
		}
	}
	for _, marker := range safeEvidenceSecretMarkers {
		if strings.Contains(lower, marker) {
			return fmt.Errorf("%s carries secret-shaped material", field)
		}
	}
	for _, fragment := range safeEvidenceSecretSubstrings {
		if strings.Contains(lower, fragment) {
			return fmt.Errorf("%s carries secret-shaped material", field)
		}
	}
	if strings.Contains(value, "=") && strings.Contains(value, ";") {
		return fmt.Errorf("%s carries cookie-shaped material", field)
	}
	return nil
}

// validateSafeEvidenceLexemeForLocation enforces the declared type of the
// exact allowlisted location. Free-form provider text, prompts and tool
// arguments have no authorized location and are rejected before this point.
func validateSafeEvidenceLexemeForLocation(location, field, value string) error {
	if err := validateSafeEvidenceLexeme(field, value); err != nil {
		return err
	}
	kind, ok := safeEvidenceLocationKind(location)
	if !ok || !kind.valid() {
		return fmt.Errorf("%s is not an allowlisted economic location", field)
	}
	switch kind {
	case safeEvidenceValueCount:
		return validateSafeEvidenceCount(field, value)
	case safeEvidenceValueQuantity, safeEvidenceValueDuration:
		return validateSafeEvidenceNonNegativeDecimal(field, value)
	case safeEvidenceValueAmount:
		return validateSafeEvidenceAmount(field, value)
	case safeEvidenceValueCurrency:
		// The canonical currency validator owns the exact bounded grammar.
		if err := validateCurrencyCode(value); err != nil {
			return fmt.Errorf("%s must be a bounded uppercase currency code", field)
		}
		return nil
	case safeEvidenceValueChargeKind:
		return validateSafeEvidenceChargeKind(field, value)
	case safeEvidenceValueIdentifier:
		return validateSafeEvidenceIdentifier(field, value)
	default:
		return fmt.Errorf("%s has no declared economic value type", field)
	}
}

func safeEvidenceLocationKind(location string) (safeEvidenceValueKind, bool) {
	if len(location) > 0 && location[0] == '$' {
		kind, ok := safeEvidencePathAllowlist[location]
		if !ok {
			kind, ok = safeEvidenceProviderNestedPathAllowlist[location]
		}
		return kind, ok
	}
	kind, ok := safeEvidenceHeaderAllowlist[strings.ToLower(location)]
	return kind, ok
}

// maxSafeEvidenceCountDigits bounds non-negative integer evidence so a count
// cannot exceed an unsigned 64-bit quantity.
const maxSafeEvidenceCountDigits = 19

// validateSafeEvidenceCount accepts only a canonical non-negative base-10
// integer. Signs, fractions, exponents, grouping separators, underscores,
// leading zeroes, whitespace and overflow are rejected.
func validateSafeEvidenceCount(field, value string) error {
	if value == "" || len(value) > maxSafeEvidenceCountDigits {
		return fmt.Errorf("%s must be a bounded non-negative integer count", field)
	}
	if len(value) > 1 && value[0] == '0' {
		return fmt.Errorf("%s must be a canonical non-negative integer count", field)
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return fmt.Errorf("%s must be a canonical non-negative integer count", field)
		}
	}
	if _, err := strconv.ParseUint(value, 10, 64); err != nil {
		return fmt.Errorf("%s count exceeds the supported bound", field)
	}
	return nil
}

// validateSafeEvidenceNonNegativeDecimal accepts a canonical bounded exact
// decimal (optionally fractional) that is not negative.
func validateSafeEvidenceNonNegativeDecimal(field, value string) error {
	parsed, err := ParseDecimal(value)
	if err != nil || strings.HasPrefix(parsed.Coefficient, "-") {
		return fmt.Errorf("%s must be a bounded non-negative decimal", field)
	}
	return nil
}

// validateSafeEvidenceAmount accepts a signed canonical bounded exact decimal
// monetary amount in major currency units.
func validateSafeEvidenceAmount(field, value string) error {
	if _, err := ParseDecimal(value); err != nil {
		return fmt.Errorf("%s must be a bounded decimal amount", field)
	}
	return nil
}

func validateSafeEvidenceChargeKind(field, value string) error {
	if !ChargeKind(value).IsKnown() {
		return fmt.Errorf("%s is not a known charge kind", field)
	}
	return nil
}

// validateSafeEvidenceIdentifier bounds identifier lexemes (request, charge
// and account identities, qualifiers, timestamps, lifetimes). Identifiers
// carry no whitespace, no cookie/query structure and no quoting; anything
// else is not an economic identity.
func validateSafeEvidenceIdentifier(field, value string) error {
	for _, r := range value {
		switch {
		case r <= 0x20 || r == 0x7f || !unicode.IsPrint(r):
			return fmt.Errorf("%s contains unsafe characters", field)
		case r == '"' || r == '\'' || r == '\\' || r == ';' || r == '=':
			return fmt.Errorf("%s is not a bounded economic identifier", field)
		}
	}
	if strings.Contains(value, "..") {
		return fmt.Errorf("%s is not a bounded economic identifier", field)
	}
	if len(strings.Fields(value)) > 1 {
		return fmt.Errorf("%s is not a bounded economic identifier", field)
	}
	return nil
}

// MaxSanitizerMarkerBytes bounds the sanitizer identity/version marker.
const MaxSanitizerMarkerBytes = 128

// validateSanitizerMarker checks the optional sanitizer identity/version
// marker stamped by the normalizer that produced sanitized evidence. The
// marker is name/version with bounded lowercase identities; it participates
// in the deterministic content hash when present and is absent otherwise.
func validateSanitizerMarker(field, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > MaxSanitizerMarkerBytes {
		return fmt.Errorf("%s exceeds %d bytes", field, MaxSanitizerMarkerBytes)
	}
	name, version, ok := strings.Cut(value, "/")
	if !ok {
		return fmt.Errorf("%s must be name/version", field)
	}
	for _, part := range []string{name, version} {
		if part == "" || len(part) > 64 {
			return fmt.Errorf("%s must be name/version with bounded identities", field)
		}
		for _, r := range part {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' {
				return fmt.Errorf("%s must use lowercase identities", field)
			}
		}
	}
	return nil
}

func isKnownSafeEvidenceAcquisition(value string) bool {
	_, ok := knownAcquisitionOrigin[value]
	return ok
}

func safeEvidenceLocationKey(field SafeEvidenceField) string {
	location := field.Path
	if location == "" {
		location = field.Name
	}
	if location == "" || location[0] != '$' {
		location = strings.ToLower(location)
	}
	return location
}
