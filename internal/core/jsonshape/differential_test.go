package jsonshape_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
)

// =============================================================================
// Task 5.3: Differential Corpus & Oracle Parity against Slice Preflight
//
// 5.1 Review Note Decisions:
//
// Decision 1 (Reason vs Stable Error Class):
// jsonshape.Kind (returned by jsonshape.Classify(err)) is the single authoritative
// stable error class contract across both incremental streaming (Scanner) and batch
// parsing (PreflightWithContext). MalformedReason provides internal diagnostic detail
// on KindMalformed (e.g. empty, syntax, incomplete), but the exact reason can
// legitimately differ between an incremental streaming chunk boundary (e.g. EOF inside
// a token mapped to MalformedIncomplete) and encoding/json's batch token boundary
// (mapped to MalformedSyntax). Therefore, differential testing compares Classify(err),
// not Reason or incidental error string text (err.Error() / Msg).
//
// Decision 2 (Key Length vs Duplicate Ordering):
// Resource bounds (KindKeyTooLong) take strict precedence over semantic validation
// (KindDuplicateName). When streaming JSON incrementally, the scanner must enforce
// memory and length limits as bytes arrive to prevent unbounded memory allocation
// in internal key buffers and duplicate-tracking sets before completing key decoding.
// =============================================================================

// diffRunner runs PreflightWithContext as the differential oracle and verifies that
// Scanner produces the exact same stable error Kind (via Classify) and, on success,
// the exact same aggregate counts (Bytes, Tokens, MaxDepth).
type diffRunner struct {
	limits jsonshape.Limits
}

func newDiffRunner(limits jsonshape.Limits) *diffRunner {
	return &diffRunner{limits: limits}
}

// verify compares Scanner behavior against PreflightWithContext across whole-body
// feed, 1-byte chunked feed, and pseudo-random chunked feeds.
func (d *diffRunner) verify(t *testing.T, label string, data []byte) {
	t.Helper()

	// 1. Oracle: PreflightWithContext
	expRes, expErr := jsonshape.PreflightWithContext(context.Background(), data, d.limits)
	expKind := jsonshape.Classify(expErr)

	// 2. Whole feed
	{
		sc := jsonshape.NewScanner(context.Background(), d.limits)
		feedErr := sc.Feed(data)
		var scRes jsonshape.Result
		var scErr error
		if feedErr != nil {
			scErr = feedErr
		} else {
			scRes, scErr = sc.Finish()
		}
		scKind := jsonshape.Classify(scErr)

		if scKind != expKind {
			t.Fatalf("[%s] whole feed Classify mismatch: got %q, want oracle %q (scErr=%v, expErr=%v)",
				label, scKind, expKind, scErr, expErr)
		}
		if expErr == nil {
			if scRes.Bytes != expRes.Bytes || scRes.Tokens != expRes.Tokens || scRes.MaxDepth != expRes.MaxDepth {
				t.Fatalf("[%s] whole feed Result mismatch: got %+v, want %+v", label, scRes, expRes)
			}
		}
	}

	// 3. 1-byte chunked feed
	{
		sc := jsonshape.NewScanner(context.Background(), d.limits)
		var byteErr error
		var byteRes jsonshape.Result
		for i := range data {
			if byteErr = sc.Feed(data[i : i+1]); byteErr != nil {
				break
			}
		}
		if byteErr == nil {
			byteRes, byteErr = sc.Finish()
		}
		byteKind := jsonshape.Classify(byteErr)

		if byteKind != expKind {
			t.Fatalf("[%s] 1-byte feed Classify mismatch: got %q, want oracle %q (byteErr=%v, expErr=%v)",
				label, byteKind, expKind, byteErr, expErr)
		}
		if expErr == nil {
			if byteRes.Bytes != expRes.Bytes || byteRes.Tokens != expRes.Tokens || byteRes.MaxDepth != expRes.MaxDepth {
				t.Fatalf("[%s] 1-byte feed Result mismatch: got %+v, want %+v", label, byteRes, expRes)
			}
		}
	}

	// 4. Deterministic pseudo-random chunked feeds
	if len(data) > 1 {
		seeds := []uint64{42, 1337, 99999, 314159, 271828}
		for sIdx, seed := range seeds {
			rng := rand.New(rand.NewPCG(seed, seed^0x55aa55aa))
			sc := jsonshape.NewScanner(context.Background(), d.limits)
			var randErr error
			var randRes jsonshape.Result

			offset := 0
			for offset < len(data) {
				rem := len(data) - offset
				chunkSize := rng.IntN(rem) + 1
				chunk := data[offset : offset+chunkSize]
				offset += chunkSize

				if randErr = sc.Feed(chunk); randErr != nil {
					break
				}
			}
			if randErr == nil {
				randRes, randErr = sc.Finish()
			}
			randKind := jsonshape.Classify(randErr)

			if randKind != expKind {
				t.Fatalf("[%s] random split (seed %d, iter %d) Classify mismatch: got %q, want oracle %q (randErr=%v, expErr=%v)",
					label, seed, sIdx, randKind, expKind, randErr, expErr)
			}
			if expErr == nil {
				if randRes.Bytes != expRes.Bytes || randRes.Tokens != expRes.Tokens || randRes.MaxDepth != expRes.MaxDepth {
					t.Fatalf("[%s] random split (seed %d) Result mismatch: got %+v, want %+v", label, seed, randRes, expRes)
				}
			}
		}
	}
}

// -----------------------------------------------------------------------------
// 1. UTF-8 Boundaries & Splits
// -----------------------------------------------------------------------------

func TestScanner_Differential_UTF8(t *testing.T) {
	t.Parallel()

	limits := jsonshape.RequestEnvelopeLimits()
	runner := newDiffRunner(limits)

	cases := []struct {
		name string
		json string
	}{
		// Valid multibyte sequences inside strings & keys
		{name: "ascii_only", json: `{"key": "value"}`},
		{name: "2_byte_cyrillic", json: `{"ключ": "значение"}`},
		{name: "2_byte_greek", json: `{"κλειδί": "τιμή"}`},
		{name: "3_byte_cjk", json: `{"键": "值", "日本語": "テスト"}`},
		{name: "3_byte_euro", json: `{"price": "100 €", "symbol": "€"}`},
		{name: "4_byte_emoji_rocket", json: `{"launch": "🚀", "status": "active"}`},
		{name: "4_byte_emoji_poop", json: `{"mood": "💩"}`},
		{name: "mixed_multibyte", json: `{"msg": "Hello 世界! 🚀 100€"}`},

		// Invalid UTF-8 cases
		{name: "invalid_utf8_c0_overlong", json: "{\"a\": \"\xc0\xaf\"}"},
		{name: "invalid_utf8_c1_overlong", json: "{\"a\": \"\xc1\x80\"}"},
		{name: "invalid_utf8_e0_overlong", json: "{\"a\": \"\xe0\x80\xaf\"}"},
		{name: "invalid_utf8_f0_overlong", json: "{\"a\": \"\xf0\x80\x80\xaf\"}"},
		{name: "invalid_utf8_standalone_cont", json: "{\"a\": \"\x80\"}"},
		{name: "invalid_utf8_ff_byte", json: "{\"a\": \"\xff\"}"},
		{name: "invalid_utf8_fe_byte", json: "{\"a\": \"\xfe\"}"},
		{name: "invalid_utf8_truncated_2byte", json: "{\"a\": \"\xc2\"}"},
		{name: "invalid_utf8_truncated_3byte", json: "{\"a\": \"\xe2\x82\"}"},
		{name: "invalid_utf8_truncated_4byte", json: "{\"a\": \"\xf0\x9f\x9a\"}"},
		{name: "invalid_utf8_surrogate_half", json: "{\"a\": \"\xed\xa0\x80\"}"}, // U+D800 encoded as UTF-8
		{name: "invalid_utf8_in_key", json: "{\"\xff\": 1}"},
		{name: "invalid_utf8_in_whitespace", json: " { \"a\" : 1 } \xff "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner.verify(t, tc.name, []byte(tc.json))
		})
	}
}

// -----------------------------------------------------------------------------
// 2. Escapes and Surrogates
// -----------------------------------------------------------------------------

func TestScanner_Differential_Escapes(t *testing.T) {
	t.Parallel()

	limits := jsonshape.RequestEnvelopeLimits()
	runner := newDiffRunner(limits)

	cases := []struct {
		name string
		json string
	}{
		// Standard escapes
		{name: "quote_escape", json: `{"quote": "\""}`},
		{name: "backslash_escape", json: `{"path": "C:\\Users\\Mateusz\\file.txt"}`},
		{name: "slash_escape", json: `{"url": "https:\/\/example.com\/"}`},
		{name: "control_escapes", json: `{"escapes": "\b\f\n\r\t"}`},
		{name: "escapes_in_key", json: `{"line\nbreak": true, "tab\tkey": 1, "quote\"key": 2}`},

		// Unicode 4-hex escapes
		{name: "unicode_ascii", json: `{"char": "\u0041"}`},
		{name: "unicode_euro", json: `{"symbol": "\u20ac"}`},
		{name: "unicode_null", json: `{"null_char": "\u0000"}`},
		{name: "unicode_cjk", json: `{"chinese": "\u4e16\u754c"}`},

		// UTF-16 surrogate pairs
		{name: "surrogate_rocket", json: `{"emoji": "\uD83D\uDE80"}`},
		{name: "surrogate_poop", json: `{"emoji": "\uD83D\uDCA9"}`},
		{name: "surrogate_musical_symbol", json: `{"clef": "\uD834\uDD1E"}`},
		{name: "multiple_surrogates", json: `{"emojis": "\uD83D\uDE80\uD83D\uDCA9"}`},

		// Malformed escapes
		{name: "invalid_escape_x", json: `{"a": "\x"}`},
		{name: "invalid_escape_a", json: `{"a": "\a"}`},
		{name: "invalid_escape_0", json: `{"a": "\0"}`},
		{name: "incomplete_unicode_hex", json: `{"a": "\u12"}`},
		{name: "invalid_unicode_hex_char", json: `{"a": "\u123z"}`},
		{name: "lone_high_surrogate", json: `{"a": "\uD800"}`},
		{name: "lone_low_surrogate", json: `{"a": "\uDC00"}`},
		{name: "high_surrogate_followed_by_ascii", json: `{"a": "\uD800A"}`},
		{name: "high_surrogate_followed_by_non_surrogate", json: `{"a": "\uD800\u0041"}`},
		{name: "high_surrogate_followed_by_another_high", json: `{"a": "\uD800\uD800"}`},
		{name: "low_surrogate_first", json: `{"a": "\uDC00\uD800"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner.verify(t, tc.name, []byte(tc.json))
		})
	}
}

// -----------------------------------------------------------------------------
// 3. Numbers
// -----------------------------------------------------------------------------

func TestScanner_Differential_Numbers(t *testing.T) {
	t.Parallel()

	limits := jsonshape.RequestEnvelopeLimits()
	runner := newDiffRunner(limits)

	cases := []struct {
		name string
		json string
	}{
		// Valid numbers
		{name: "zero", json: `{"v": 0}`},
		{name: "negative_zero", json: `{"v": -0}`},
		{name: "positive_int", json: `{"v": 42}`},
		{name: "negative_int", json: `{"v": -42}`},
		{name: "large_int", json: `{"v": 9007199254740991}`},
		{name: "zero_fraction", json: `{"v": 0.0}`},
		{name: "small_fraction", json: `{"v": 0.000001}`},
		{name: "standard_fraction", json: `{"v": 3.1415926535}`},
		{name: "negative_fraction", json: `{"v": -0.5}`},
		{name: "exponent_lower", json: `{"v": 1e10}`},
		{name: "exponent_upper", json: `{"v": 1E10}`},
		{name: "exponent_plus", json: `{"v": 1.5e+10}`},
		{name: "exponent_minus", json: `{"v": 2.5E-10}`},
		{name: "exponent_zero", json: `{"v": 0e0}`},
		{name: "array_of_numbers", json: `[0, -0, 1, -1, 0.5, -0.5, 1e5, -1e-5]`},
		{name: "scalar_number_int", json: `12345`},
		{name: "scalar_number_float", json: `-987.654e+2`},

		// Malformed numbers
		{name: "leading_zero_integer", json: `{"v": 01}`},
		{name: "leading_zero_negative", json: `{"v": -01}`},
		{name: "plus_sign_start", json: `{"v": +1}`},
		{name: "dot_start", json: `{"v": .5}`},
		{name: "trailing_dot", json: `{"v": 1.}`},
		{name: "trailing_exp", json: `{"v": 1e}`},
		{name: "trailing_exp_sign", json: `{"v": 1e+}`},
		{name: "standalone_minus", json: `{"v": -}`},
		{name: "double_minus", json: `{"v": --1}`},
		{name: "double_dot", json: `{"v": 1.2.3}`},
		{name: "hex_number", json: `{"v": 0x1A}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner.verify(t, tc.name, []byte(tc.json))
		})
	}
}

// -----------------------------------------------------------------------------
// 4. Deep and Wide JSON
// -----------------------------------------------------------------------------

func TestScanner_Differential_DeepAndWide(t *testing.T) {
	t.Parallel()

	// Use custom limits to test boundaries precisely
	limits := jsonshape.Limits{
		MaxBytes:             1 << 20,
		MaxDepth:             20,
		MaxTokens:            10_000,
		MaxArrayElems:        50,
		MaxObjectKeys:        50,
		MaxStringBytes:       10_000,
		MaxKeyBytes:          500,
		MaxNumberBytes:       500,
		RejectDuplicateNames: false,
	}
	runner := newDiffRunner(limits)

	cases := []struct {
		name string
		json string
	}{
		// Deep structures within limit (MaxDepth: 20)
		{name: "deep_array_depth_5", json: `[[[[[1]]]]]`},
		{name: "deep_array_depth_10", json: strings.Repeat(`[`, 10) + `0` + strings.Repeat(`]`, 10)},
		{name: "deep_array_depth_20_exact", json: strings.Repeat(`[`, 20) + `0` + strings.Repeat(`]`, 20)},
		{name: "deep_array_depth_21_exceeded", json: strings.Repeat(`[`, 21) + `0` + strings.Repeat(`]`, 21)},
		{name: "deep_object_depth_5", json: `{"a":{"b":{"c":{"d":{"e":1}}}}}`},
		{
			name: "deep_object_depth_20_exact",
			json: func() string {
				var sb strings.Builder
				for i := 1; i <= 20; i++ {
					fmt.Fprintf(&sb, `{"k%d":`, i)
				}
				sb.WriteString(`1`)
				sb.WriteString(strings.Repeat(`}`, 20))
				return sb.String()
			}(),
		},
		{
			name: "deep_object_depth_21_exceeded",
			json: func() string {
				var sb strings.Builder
				for i := 1; i <= 21; i++ {
					fmt.Fprintf(&sb, `{"k%d":`, i)
				}
				sb.WriteString(`1`)
				sb.WriteString(strings.Repeat(`}`, 21))
				return sb.String()
			}(),
		},

		// Wide structures within/exceeding limit (MaxArrayElems: 50, MaxObjectKeys: 50)
		{
			name: "wide_array_50_exact",
			json: func() string {
				elems := make([]string, 50)
				for i := range elems {
					elems[i] = "1"
				}
				return `[` + strings.Join(elems, ",") + `]`
			}(),
		},
		{
			name: "wide_array_51_exceeded",
			json: func() string {
				elems := make([]string, 51)
				for i := range elems {
					elems[i] = "1"
				}
				return `[` + strings.Join(elems, ",") + `]`
			}(),
		},
		{
			name: "wide_object_50_exact",
			json: func() string {
				pairs := make([]string, 50)
				for i := range pairs {
					pairs[i] = fmt.Sprintf(`"k%d": %d`, i, i)
				}
				return `{` + strings.Join(pairs, ",") + `}`
			}(),
		},
		{
			name: "wide_object_51_exceeded",
			json: func() string {
				pairs := make([]string, 51)
				for i := range pairs {
					pairs[i] = fmt.Sprintf(`"k%d": %d`, i, i)
				}
				return `{` + strings.Join(pairs, ",") + `}`
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner.verify(t, tc.name, []byte(tc.json))
		})
	}
}

// -----------------------------------------------------------------------------
// 5. Giant Strings and Memory Limits
// -----------------------------------------------------------------------------

func TestScanner_Differential_GiantStrings(t *testing.T) {
	t.Parallel()

	limits := jsonshape.Limits{
		MaxBytes:             2 << 20,
		MaxDepth:             10,
		MaxTokens:            10_000,
		MaxArrayElems:        100,
		MaxObjectKeys:        100,
		MaxStringBytes:       1000,
		MaxKeyBytes:          50,
		MaxNumberBytes:       100,
		RejectDuplicateNames: false,
	}
	runner := newDiffRunner(limits)

	cases := []struct {
		name string
		json string
	}{
		// String value limits (MaxStringBytes: 1000)
		{name: "string_value_exact_limit", json: `{"text":"` + strings.Repeat("a", 1000) + `"}`},
		{name: "string_value_exceeded_by_1", json: `{"text":"` + strings.Repeat("a", 1001) + `"}`},
		{name: "string_value_escaped_exact", json: `{"text":"` + strings.Repeat(`\n`, 500) + `"}`},    // 500 decoded bytes
		{name: "string_value_escaped_exceeded", json: `{"text":"` + strings.Repeat(`\n`, 501) + `"}`}, // 501 decoded bytes

		// String key limits (MaxKeyBytes: 50)
		{name: "key_exact_limit", json: `{"` + strings.Repeat("k", 50) + `": 1}`},
		{name: "key_exceeded_by_1", json: `{"` + strings.Repeat("k", 51) + `": 1}`},
		{name: "key_escaped_exact", json: `{"` + strings.Repeat(`\t`, 50) + `": 1}`},
		{name: "key_escaped_exceeded", json: `{"` + strings.Repeat(`\t`, 51) + `": 1}`},

		// Number length limits (MaxNumberBytes: 100)
		{name: "number_exact_limit", json: `{"num":` + strings.Repeat("9", 100) + `}`},
		{name: "number_exceeded_by_1", json: `{"num":` + strings.Repeat("9", 101) + `}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner.verify(t, tc.name, []byte(tc.json))
		})
	}
}

// -----------------------------------------------------------------------------
// 6. Duplicates and Key-vs-Duplicate Ordering
// -----------------------------------------------------------------------------

func TestScanner_Differential_Duplicates(t *testing.T) {
	t.Parallel()

	// Profile with RejectDuplicateNames: true
	rejectLimits := jsonshape.Limits{
		MaxBytes:             1 << 20,
		MaxDepth:             10,
		MaxTokens:            1000,
		MaxArrayElems:        100,
		MaxObjectKeys:        100,
		MaxStringBytes:       1000,
		MaxKeyBytes:          10, // Short key limit to test key-vs-dup precedence
		MaxNumberBytes:       100,
		RejectDuplicateNames: true,
	}
	rejectRunner := newDiffRunner(rejectLimits)

	// Profile with RejectDuplicateNames: false
	acceptLimits := rejectLimits
	acceptLimits.RejectDuplicateNames = false
	acceptRunner := newDiffRunner(acceptLimits)

	cases := []struct {
		name         string
		json         string
		rejectRunner bool
	}{
		// Duplicate rejection enabled
		{name: "duplicate_rejected_top_level", json: `{"a": 1, "a": 2}`, rejectRunner: true},
		{name: "duplicate_rejected_nested", json: `{"outer": {"dup": 1, "dup": 2}}`, rejectRunner: true},
		{name: "duplicate_rejected_with_escapes", json: `{"a\nb": 1, "a\nb": 2}`, rejectRunner: true},
		{name: "sibling_objects_same_key_allowed", json: `[{"a": 1}, {"a": 2}]`, rejectRunner: true},
		{name: "nested_distinct_keys_allowed", json: `{"a": {"k": 1}, "b": {"k": 2}}`, rejectRunner: true},

		// Duplicate rejection disabled (RequestEnvelope semantics)
		{name: "duplicate_accepted_top_level", json: `{"a": 1, "a": 2}`, rejectRunner: false},
		{name: "duplicate_accepted_nested", json: `{"outer": {"dup": 1, "dup": 2}}`, rejectRunner: false},

		// 5.1 Review Note 2: Key length vs Duplicate ordering
		// If a key exceeds MaxKeyBytes (10 bytes), KindKeyTooLong must be returned.
		// Even if the overlong key is repeated, KindKeyTooLong must fire on the first key.
		{name: "overlong_key_repeated", json: `{"0123456789X": 1, "0123456789X": 2}`, rejectRunner: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.rejectRunner {
				rejectRunner.verify(t, tc.name, []byte(tc.json))
			} else {
				acceptRunner.verify(t, tc.name, []byte(tc.json))
			}
		})
	}
}

// -----------------------------------------------------------------------------
// 7. Malformed and Trailing Data
// -----------------------------------------------------------------------------

func TestScanner_Differential_MalformedAndTrailing(t *testing.T) {
	t.Parallel()

	limits := jsonshape.RequestEnvelopeLimits()
	runner := newDiffRunner(limits)

	cases := []struct {
		name string
		json string
	}{
		// Empty inputs
		{name: "empty_string", json: ``},
		{name: "whitespace_spaces", json: `   `},
		{name: "whitespace_all_types", json: " \t\r\n \n\t "},

		// Truncated / unclosed delimiters
		{name: "unclosed_object", json: `{"a": 1`},
		{name: "unclosed_array", json: `[1, 2`},
		{name: "unclosed_string_value", json: `{"a": "hello`},
		{name: "unclosed_string_key", json: `{"hello`},
		{name: "unclosed_nested", json: `{"a": [1, {"b": 2`},

		// Unexpected closing delimiters
		{name: "unexpected_close_brace", json: `}`},
		{name: "unexpected_close_bracket", json: `]`},
		{name: "extra_close_brace", json: `{"a": 1}}`},
		{name: "extra_close_bracket", json: `[1]]`},
		{name: "mismatched_brace_for_bracket", json: `[1, 2}`},
		{name: "mismatched_bracket_for_brace", json: `{"a": 1]`},

		// Colons and commas
		{name: "missing_colon", json: `{"a" 1}`},
		{name: "missing_value_after_colon", json: `{"a":}`},
		{name: "double_colon", json: `{"a":: 1}`},
		{name: "leading_comma_array", json: `[, 1]`},
		{name: "leading_comma_object", json: `{,"a": 1}`},
		{name: "trailing_comma_array", json: `[1, 2,]`},
		{name: "trailing_comma_object", json: `{"a": 1,}`},
		{name: "double_comma", json: `[1,, 2]`},

		// Non-string object keys
		{name: "number_as_key", json: `{1: "val"}`},
		{name: "boolean_as_key", json: `{true: "val"}`},
		{name: "null_as_key", json: `{null: "val"}`},

		// Multiple root values
		{name: "two_integers", json: `1 2`},
		{name: "two_objects", json: `{} {}`},
		{name: "two_arrays", json: `[] []`},
		{name: "two_booleans", json: `true false`},
		{name: "two_nulls", json: `null null`},
		{name: "object_and_array", json: `{} []`},

		// Invalid literals
		{name: "truncated_true", json: `tru`},
		{name: "truncated_false", json: `fals`},
		{name: "truncated_null", json: `nul`},
		{name: "misspelled_true", json: `truth`},
		{name: "misspelled_null", json: `nil`},
		{name: "unquoted_identifier", json: `{key: "val"}`},

		// Trailing garbage after root
		{name: "trailing_text_after_obj", json: `{"a": 1} trailing`},
		{name: "trailing_text_after_array", json: `[1, 2] extra`},
		{name: "trailing_text_after_scalar", json: `42 extra`},
		{name: "trailing_null_byte", json: "{\"a\": 1} \x00"},

		// Valid trailing whitespace (must pass)
		{name: "valid_trailing_whitespace", json: "{\"a\": 1} \n\t\r "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner.verify(t, tc.name, []byte(tc.json))
		})
	}
}

// -----------------------------------------------------------------------------
// 8. Exact Limits (At Limit vs Exceeded by 1)
// -----------------------------------------------------------------------------

func TestScanner_Differential_ExactLimits(t *testing.T) {
	t.Parallel()

	// A. MaxBytes
	t.Run("MaxBytes", func(t *testing.T) {
		t.Parallel()
		dataExact := []byte(`{"a": 12}`) // exactly 9 bytes
		limits := jsonshape.Limits{
			MaxBytes: 9, MaxDepth: 10, MaxTokens: 100, MaxArrayElems: 10, MaxObjectKeys: 10,
			MaxStringBytes: 100, MaxKeyBytes: 100, MaxNumberBytes: 100,
		}
		r := newDiffRunner(limits)
		r.verify(t, "max_bytes_exact", dataExact)

		dataOver := []byte(`{"a": 123}`) // 10 bytes -> exceeds 9
		r.verify(t, "max_bytes_over", dataOver)
	})

	// B. MaxTokens
	t.Run("MaxTokens", func(t *testing.T) {
		t.Parallel()
		// `{"a": 1}` has 4 tokens: { (1), "a" (2), 1 (3), } (4)
		limits := jsonshape.Limits{
			MaxBytes: 1000, MaxDepth: 10, MaxTokens: 4, MaxArrayElems: 10, MaxObjectKeys: 10,
			MaxStringBytes: 100, MaxKeyBytes: 100, MaxNumberBytes: 100,
		}
		r := newDiffRunner(limits)
		r.verify(t, "max_tokens_exact", []byte(`{"a": 1}`))

		// `{"a": 1, "b": 2}` has 6 tokens -> exceeds 4
		r.verify(t, "max_tokens_over", []byte(`{"a": 1, "b": 2}`))
	})

	// C. MaxDepth
	t.Run("MaxDepth", func(t *testing.T) {
		t.Parallel()
		limits := jsonshape.Limits{
			MaxBytes: 1000, MaxDepth: 3, MaxTokens: 100, MaxArrayElems: 10, MaxObjectKeys: 10,
			MaxStringBytes: 100, MaxKeyBytes: 100, MaxNumberBytes: 100,
		}
		r := newDiffRunner(limits)
		r.verify(t, "max_depth_exact", []byte(`{"a": {"b": [1]}}`))  // depth 3
		r.verify(t, "max_depth_over", []byte(`{"a": {"b": [[1]]}}`)) // depth 4
	})

	// D. MaxArrayElems
	t.Run("MaxArrayElems", func(t *testing.T) {
		t.Parallel()
		limits := jsonshape.Limits{
			MaxBytes: 1000, MaxDepth: 10, MaxTokens: 100, MaxArrayElems: 3, MaxObjectKeys: 10,
			MaxStringBytes: 100, MaxKeyBytes: 100, MaxNumberBytes: 100,
		}
		r := newDiffRunner(limits)
		r.verify(t, "max_array_elems_exact", []byte(`[1, 2, 3]`))
		r.verify(t, "max_array_elems_over", []byte(`[1, 2, 3, 4]`))
	})

	// E. MaxObjectKeys
	t.Run("MaxObjectKeys", func(t *testing.T) {
		t.Parallel()
		limits := jsonshape.Limits{
			MaxBytes: 1000, MaxDepth: 10, MaxTokens: 100, MaxArrayElems: 10, MaxObjectKeys: 2,
			MaxStringBytes: 100, MaxKeyBytes: 100, MaxNumberBytes: 100,
		}
		r := newDiffRunner(limits)
		r.verify(t, "max_object_keys_exact", []byte(`{"a": 1, "b": 2}`))
		r.verify(t, "max_object_keys_over", []byte(`{"a": 1, "b": 2, "c": 3}`))
	})

	// F. MaxStringBytes
	t.Run("MaxStringBytes", func(t *testing.T) {
		t.Parallel()
		limits := jsonshape.Limits{
			MaxBytes: 1000, MaxDepth: 10, MaxTokens: 100, MaxArrayElems: 10, MaxObjectKeys: 10,
			MaxStringBytes: 5, MaxKeyBytes: 100, MaxNumberBytes: 100,
		}
		r := newDiffRunner(limits)
		r.verify(t, "max_string_bytes_exact", []byte(`{"s": "12345"}`))
		r.verify(t, "max_string_bytes_over", []byte(`{"s": "123456"}`))
	})

	// G. MaxKeyBytes
	t.Run("MaxKeyBytes", func(t *testing.T) {
		t.Parallel()
		limits := jsonshape.Limits{
			MaxBytes: 1000, MaxDepth: 10, MaxTokens: 100, MaxArrayElems: 10, MaxObjectKeys: 10,
			MaxStringBytes: 100, MaxKeyBytes: 4, MaxNumberBytes: 100,
		}
		r := newDiffRunner(limits)
		r.verify(t, "max_key_bytes_exact", []byte(`{"1234": 1}`))
		r.verify(t, "max_key_bytes_over", []byte(`{"12345": 1}`))
	})

	// H. MaxNumberBytes
	t.Run("MaxNumberBytes", func(t *testing.T) {
		t.Parallel()
		limits := jsonshape.Limits{
			MaxBytes: 1000, MaxDepth: 10, MaxTokens: 100, MaxArrayElems: 10, MaxObjectKeys: 10,
			MaxStringBytes: 100, MaxKeyBytes: 100, MaxNumberBytes: 4,
		}
		r := newDiffRunner(limits)
		r.verify(t, "max_number_bytes_exact", []byte(`{"n": 1234}`))
		r.verify(t, "max_number_bytes_over", []byte(`{"n": 12345}`))
	})
}

// -----------------------------------------------------------------------------
// 9. Cancellation
// -----------------------------------------------------------------------------

func TestScanner_Differential_Cancellation(t *testing.T) {
	t.Parallel()

	data := []byte(`{"model": "gpt-4o", "messages": [{"role": "user", "content": "Hello!"}]}`)
	limits := jsonshape.RequestEnvelopeLimits()

	// 1. Pre-canceled context
	t.Run("pre_canceled_context", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, expErr := jsonshape.PreflightWithContext(ctx, data, limits)
		if got, want := jsonshape.Classify(expErr), jsonshape.KindCanceled; got != want {
			t.Fatalf("PreflightWithContext pre-canceled Kind mismatch: got %q, want %q", got, want)
		}

		sc := jsonshape.NewScanner(ctx, limits)
		feedErr := sc.Feed(data)
		var scErr error
		if feedErr != nil {
			scErr = feedErr
		} else {
			_, scErr = sc.Finish()
		}
		if got, want := jsonshape.Classify(scErr), jsonshape.KindCanceled; got != want {
			t.Fatalf("Scanner pre-canceled Kind mismatch: got %q, want %q", got, want)
		}
	})

	// 2. Cancellation mid-stream (between chunks)
	t.Run("mid_stream_cancellation", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		sc := jsonshape.NewScanner(ctx, limits)

		// Feed half of the data
		half := len(data) / 2
		if err := sc.Feed(data[:half]); err != nil {
			t.Fatalf("Feed first half failed: %v", err)
		}

		// Cancel context mid-scan
		cancel()

		// Feed second half should detect canceled context
		err := sc.Feed(data[half:])
		if got, want := jsonshape.Classify(err), jsonshape.KindCanceled; got != want {
			t.Fatalf("Scanner mid-stream cancel Kind mismatch: got %q, want %q (err=%v)", got, want, err)
		}
	})

	// 3. Cancellation before Finish()
	t.Run("cancel_before_finish", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		sc := jsonshape.NewScanner(ctx, limits)

		if err := sc.Feed(data); err != nil {
			t.Fatalf("Feed failed: %v", err)
		}

		cancel()

		_, err := sc.Finish()
		if got, want := jsonshape.Classify(err), jsonshape.KindCanceled; got != want {
			t.Fatalf("Scanner finish cancel Kind mismatch: got %q, want %q (err=%v)", got, want, err)
		}
	})
}
