package metering

import (
	"strings"
	"testing"
)

// Finding 7 RED contract: every allowlisted evidence location declares exactly
// one semantic value type. Numeric-bearing locations accept only their exact
// canonical numeric grammar; single-token words and base64-like strings are
// rejected there and never echoed back in the validation error.

// hostileNonNumericLexemes must never be accepted at any numeric-bearing
// location. They are not secret-shaped, so the pre-existing secret screen does
// not mask the value-type bypass this contract targets.
var hostileNonNumericLexemes = []struct {
	name   string
	lexeme string
}{
	{name: "single requested word", lexeme: "PrivateCustomerOutputWithoutSpaces"},
	{name: "base64-like", lexeme: "c2VjcmV0LXZhbHVl"},
	{name: "base64-like padded", lexeme: "c2VjcmV0LXZhbHVlPQ"},
	{name: "alpha numeric word", lexeme: "OperationCompleted200"},
	{name: "hex word", lexeme: "0xdeadbeef"},
	{name: "exponent overflow", lexeme: "1e309"},
	{name: "not a number", lexeme: "NaN"},
	{name: "positive infinity", lexeme: "Inf"},
	{name: "negative infinity", lexeme: "-Inf"},
	{name: "multiple decimal points", lexeme: "1.2.3"},
	{name: "grouped digits", lexeme: "1,000"},
	{name: "underscored digits", lexeme: "1_000"},
}

// numericEvidencePaths is the numeric-bearing half of the finite allowlist.
var numericEvidencePaths = []string{
	"$.usage.input_tokens",
	"$.usage.output_tokens",
	"$.usage.total_tokens",
	"$.usage.cache_read_input_tokens",
	"$.usage.cache_write_input_tokens",
	"$.usage.reasoning_output_tokens",
	"$.usage.tool_use_prompt_tokens",
	"$.usage.audio_seconds",
	"$.usage.video_seconds",
	"$.usage.image_count",
	"$.usage.input_image_count",
	"$.usage.output_image_count",
	"$.usage.input_image_tokens",
	"$.usage.output_image_tokens",
	"$.usage.input_text_tokens",
	"$.usage.output_text_tokens",
	"$.usage.input_audio_tokens",
	"$.usage.output_audio_tokens",
	"$.usage.input_audio_seconds",
	"$.usage.output_audio_seconds",
	"$.usage.input_video_tokens",
	"$.usage.output_video_tokens",
	"$.usage.input_video_seconds",
	"$.usage.output_video_seconds",
	"$.usage.input_video_frames",
	"$.usage.output_video_frames",
	"$.usage.input_document_tokens",
	"$.usage.output_document_tokens",
	"$.usage.input_document_pages",
	"$.usage.output_document_pages",
	"$.usage.cache_creation_5m_input_tokens",
	"$.usage.cache_creation_1h_input_tokens",
	"$.usage.web_fetch_requests",
	"$.usage.web_search_requests",
	"$.usage.input_file_pages",
	"$.usage.output_file_pages",
	"$.usage.input_bytes",
	"$.usage.output_bytes",
	"$.usage.image_pixels",
	"$.usage.video_frames",
	"$.usage.file_pages",
	"$.usage.bytes",
	"$.usage.queries",
	"$.usage.credits",
	"$.usage.duration_seconds",
	"$.usage.frame_count",
	"$.usage.page_count",
	"$.billing.amount",
	"$.charge.amount",
	"$.cost.amount",
	"$.price.amount",
	"$.quota.limit",
	"$.quota.remaining",
	"$.allowance.limit",
	"$.allowance.remaining",
	"$.rate_limit.limit",
	"$.rate_limit.remaining",
	"$.cache.read_tokens",
	"$.cache.write_tokens",
	"$.provider_schema.custom_cost",
}

var numericEvidenceHeaders = []string{
	"content-length",
	"x-usage-input-tokens",
	"x-usage-output-tokens",
	"x-usage-total-tokens",
	"x-usage-cache-read-input-tokens",
	"x-usage-cache-write-input-tokens",
	"x-usage-reasoning-output-tokens",
	"x-usage-audio-seconds",
	"x-usage-video-seconds",
	"x-usage-image-count",
	"x-usage-file-pages",
	"x-usage-queries",
	"x-usage-credits",
	"x-usage-bytes",
	"x-usage-duration-seconds",
	"x-usage-frame-count",
	"x-billing-amount",
	"x-charge-amount",
	"x-cost-amount",
	"x-price-amount",
	"x-quota-limit",
	"x-quota-remaining",
	"x-allowance-limit",
	"x-allowance-remaining",
	"x-rate-limit-limit",
	"x-rate-limit-remaining",
	"x-ratelimit-limit",
	"x-ratelimit-remaining",
	"x-cache-read-tokens",
	"x-cache-write-tokens",
	"x-provider-usage-input-tokens",
	"x-provider-usage-output-tokens",
	"x-provider-usage-total-tokens",
	"x-provider-billing-amount",
	"x-provider-charge-amount",
}

func TestSafeEvidenceNumericLocationsRejectNonNumericLexemes(t *testing.T) {
	t.Parallel()
	locations := append(append([]string{}, numericEvidencePaths...), numericEvidenceHeaders...)
	for _, location := range locations {
		for _, probe := range hostileNonNumericLexemes {
			t.Run(location+"/"+probe.name, func(t *testing.T) {
				t.Parallel()
				field := sanitizeTestField(location, probe.lexeme)
				err := field.Validate()
				if err == nil {
					t.Fatalf("hostile lexeme %q accepted at numeric location %q", probe.lexeme, location)
				}
				if strings.Contains(err.Error(), probe.lexeme) {
					t.Fatalf("validation error for %q echoed the hostile lexeme: %v", location, err)
				}
			})
		}
	}
}

// TestSafeEvidenceReproducedBypassPaths pins the three independently reproduced
// bypasses so they can never silently regress.
func TestSafeEvidenceReproducedBypassPaths(t *testing.T) {
	t.Parallel()
	for _, location := range []string{
		"$.usage.input_tokens",
		"$.usage.audio_seconds",
		"$.usage.web_search_requests",
	} {
		t.Run(location, func(t *testing.T) {
			t.Parallel()
			field := sanitizeTestField(location, "PrivateCustomerOutputWithoutSpaces")
			if err := field.Validate(); err == nil {
				t.Fatalf("reproduced Finding 7 bypass still accepted at %q", location)
			}
		})
	}
}

// TestSafeEvidenceProviderNestedImageTokenPath is the R7-C1B contract for the
// true OpenAI Chat provider-family location
// $.usage.prompt_tokens_details.image_tokens. The exact nested wire path is a
// bounded non-negative integer count and must be allowlisted with the generic
// count grammar, so a real prompt image token quantity can be retained at its
// provider identity instead of being rewritten onto a substitute canonical
// location.
func TestSafeEvidenceProviderNestedImageTokenPath(t *testing.T) {
	t.Parallel()
	const location = "$.usage.prompt_tokens_details.image_tokens"
	for _, valid := range []string{"0", "2", "1000"} {
		t.Run("valid/"+valid, func(t *testing.T) {
			t.Parallel()
			if err := sanitizeTestField(location, valid).Validate(); err != nil {
				t.Fatalf("valid count %q rejected at %q: %v", valid, location, err)
			}
		})
	}
	for _, probe := range []struct {
		name   string
		lexeme string
	}{
		{name: "string", lexeme: "oops"},
		{name: "negative", lexeme: "-1"},
		{name: "fractional", lexeme: "2.5"},
		{name: "exponent", lexeme: "1e3"},
		{name: "overflow", lexeme: strings.Repeat("9", 20)},
		{name: "leading zero", lexeme: "007"},
	} {
		t.Run("invalid/"+probe.name, func(t *testing.T) {
			t.Parallel()
			if err := sanitizeTestField(location, probe.lexeme).Validate(); err == nil {
				t.Fatalf("non-canonical lexeme %q accepted at %q", probe.lexeme, location)
			}
		})
	}
}

func TestSafeEvidenceIntegerLocationsRejectSignsFractionsAndOverflow(t *testing.T) {
	t.Parallel()
	invalid := []struct {
		name   string
		lexeme string
	}{
		{name: "negative", lexeme: "-1"},
		{name: "explicit plus", lexeme: "+1"},
		{name: "fraction", lexeme: "1.5"},
		{name: "trailing zero fraction", lexeme: "1.0"},
		{name: "exponent", lexeme: "1e3"},
		{name: "leading zero", lexeme: "00100"},
		{name: "overflow", lexeme: strings.Repeat("9", 20)},
		{name: "surrounding space", lexeme: " 100"},
		{name: "internal space", lexeme: "1 00"},
	}
	for _, probe := range invalid {
		for _, location := range []string{"$.usage.input_tokens", "content-length", "$.usage.web_search_requests"} {
			t.Run(location+"/"+probe.name, func(t *testing.T) {
				t.Parallel()
				if err := sanitizeTestField(location, probe.lexeme).Validate(); err == nil {
					t.Fatalf("non-canonical integer lexeme %q accepted at %q", probe.lexeme, location)
				}
			})
		}
	}
}
