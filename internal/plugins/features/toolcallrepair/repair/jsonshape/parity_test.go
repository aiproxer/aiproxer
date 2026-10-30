package jsonshape_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corejsonshape "github.com/aiproxer/aiproxer/internal/core/jsonshape"
	localjsonshape "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair/jsonshape"
)

// TestPreflight_DifferentialParityAgainstCoreOracle executes differential
// verification between the feature-local preflight implementation and the
// internal/core/jsonshape oracle during Phase 3 migration.
func TestPreflight_DifferentialParityAgainstCoreOracle(t *testing.T) {
	t.Parallel()

	testInputs := []struct {
		name string
		data []byte
	}{
		{"empty", []byte("")},
		{"whitespace_only", []byte("   \r\n\t  ")},
		{"empty_object", []byte("{}")},
		{"empty_array", []byte("[]")},
		{"simple_scalar_num", []byte("12345")},
		{"simple_scalar_str", []byte(`"hello world"`)},
		{"simple_scalar_bool", []byte("true")},
		{"simple_scalar_null", []byte("null")},
		{"basic_obj", []byte(`{"a":1,"b":"val","c":[1,2,3],"d":{"nested":true}}`)},
		{"duplicate_key", []byte(`{"a":1,"a":2}`)},
		{"nested_duplicate_key", []byte(`{"outer":{"inner":{"x":1,"x":2}}}`)},
		{"syntax_error_unclosed_obj", []byte(`{"a":1`)},
		{"syntax_error_unclosed_arr", []byte(`[1,2,3`)},
		{"syntax_error_extra_comma", []byte(`{"a":1,}`)},
		{"syntax_error_trailing_data", []byte(`{"a":1} trailing`)},
		{"syntax_error_multiple_roots", []byte(`{"a":1}{"b":2}`)},
		{"syntax_error_unexpected_close_obj", []byte(`}`)},
		{"syntax_error_unexpected_close_arr", []byte(`]`)},
		{"syntax_error_mismatched_close", []byte(`{"a":[1,2]}}`)},
		{"invalid_utf8_1", []byte("\"\xff\"")},
		{"invalid_utf8_2", []byte("{\"key\": \"invalid \xc0\xaf sequence\"}")},
		{"escaped_quotes_and_slashes", []byte(`{"msg":"hello \"world\" / \\ \n \t \r"}`)},
		{"unicode_escapes", []byte(`{"msg":"\u0048\u0065\u006c\u006c\u006f"}`)},
		{"numbers_exponent", []byte(`{"n":1.23e4,"m":-5.67E-8}`)},
		{"numbers_long", []byte(`{"n":123456789012345678901234567890}`)},
		{"depth_10", []byte(strings.Repeat(`[`, 10) + `1` + strings.Repeat(`]`, 10))},
		{"depth_32", []byte(strings.Repeat(`[`, 32) + `1` + strings.Repeat(`]`, 32))},
		{"depth_33", []byte(strings.Repeat(`[`, 33) + `1` + strings.Repeat(`]`, 33))},
		{"depth_64", []byte(strings.Repeat(`[`, 64) + `1` + strings.Repeat(`]`, 64))},
		{"depth_65", []byte(strings.Repeat(`[`, 65) + `1` + strings.Repeat(`]`, 65))},
		{"wide_object_50", []byte(generateWideObj(50))},
		{"wide_object_1024", []byte(generateWideObj(1024))},
		{"wide_object_1025", []byte(generateWideObj(1025))},
		{"wide_array_50", []byte(generateWideArr(50))},
		{"wide_array_4096", []byte(generateWideArr(4096))},
		{"wide_array_4097", []byte(generateWideArr(4097))},
		{"long_string_1000", []byte(`{"k":"` + strings.Repeat("x", 1000) + `"}`)},
		{"long_key_1000", []byte(`{"` + strings.Repeat("k", 1000) + `":1}`)},
	}

	profiles := []struct {
		name        string
		coreLimits  func() corejsonshape.Limits
		localLimits func() localjsonshape.Limits
	}{
		{
			name: "ToolSchemaLimits",
			coreLimits: func() corejsonshape.Limits {
				return corejsonshape.ToolSchemaLimits()
			},
			localLimits: func() localjsonshape.Limits {
				return localjsonshape.ToolSchemaLimits()
			},
		},
		{
			name: "ToolArgumentsLimits",
			coreLimits: func() corejsonshape.Limits {
				return corejsonshape.ToolArgumentsLimits()
			},
			localLimits: func() localjsonshape.Limits {
				return localjsonshape.ToolArgumentsLimits()
			},
		},
	}

	for _, prof := range profiles {
		t.Run(prof.name, func(t *testing.T) {
			t.Parallel()
			for _, tc := range testInputs {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					cRes, cErr := corejsonshape.PreflightWithContext(context.Background(), tc.data, prof.coreLimits())
					lRes, lErr := localjsonshape.PreflightWithContext(context.Background(), tc.data, prof.localLimits())

					// Compare error presence and Kind
					if (cErr == nil) != (lErr == nil) {
						t.Fatalf("error presence mismatch on %s: core=%v, local=%v", tc.name, cErr, lErr)
					}
					if cErr != nil {
						cKind := corejsonshape.Classify(cErr)
						lKind := localjsonshape.Classify(lErr)
						if string(cKind) != string(lKind) {
							t.Fatalf("kind mismatch on %s: core=%s, local=%s", tc.name, cKind, lKind)
						}
					} else {
						// On success, compare Result metrics
						if cRes.Bytes != lRes.Bytes {
							t.Fatalf("bytes mismatch on %s: core=%d, local=%d", tc.name, cRes.Bytes, lRes.Bytes)
						}
						if cRes.Tokens != lRes.Tokens {
							t.Fatalf("tokens mismatch on %s: core=%d, local=%d", tc.name, cRes.Tokens, lRes.Tokens)
						}
						if cRes.MaxDepth != lRes.MaxDepth {
							t.Fatalf("depth mismatch on %s: core=%d, local=%d", tc.name, cRes.MaxDepth, lRes.MaxDepth)
						}
					}
				})
			}
		})
	}
}

func TestPreflight_CancellationParity(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	payload := []byte(`{"a":1,"b":[2,3]}`)

	_, cErr := corejsonshape.PreflightWithContext(ctx, payload, corejsonshape.ToolArgumentsLimits())
	_, lErr := localjsonshape.PreflightWithContext(ctx, payload, localjsonshape.ToolArgumentsLimits())

	if cErr == nil || lErr == nil {
		t.Fatalf("expected error on canceled context: core=%v, local=%v", cErr, lErr)
	}
	if corejsonshape.Classify(cErr) != corejsonshape.KindCanceled {
		t.Fatalf("core did not return KindCanceled: %v", cErr)
	}
	if !strings.Contains(lErr.Error(), string(localjsonshape.KindCanceled)) {
		t.Fatalf("local did not return KindCanceled: %v", lErr)
	}

	deadCtx, deadCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer deadCancel()
	_, cErr2 := corejsonshape.PreflightWithContext(deadCtx, payload, corejsonshape.ToolArgumentsLimits())
	_, lErr2 := localjsonshape.PreflightWithContext(deadCtx, payload, localjsonshape.ToolArgumentsLimits())
	if cErr2 == nil || lErr2 == nil {
		t.Fatalf("expected error on deadline context: core=%v, local=%v", cErr2, lErr2)
	}
}

func generateWideObj(n int) string {
	var b strings.Builder
	b.WriteByte('{')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%d":%d`, i, i)
	}
	b.WriteByte('}')
	return b.String()
}

func generateWideArr(n int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `%d`, i)
	}
	b.WriteByte(']')
	return b.String()
}
