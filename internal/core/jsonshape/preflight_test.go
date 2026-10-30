package jsonshape_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
)

func TestPreflightLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		data     string
		limits   jsonshape.Limits
		wantKind jsonshape.Kind
	}{
		{name: "bytes at limit", data: `{"x":1}`, limits: jsonshape.Limits{MaxBytes: int64(len(`{"x":1}`))}},
		{name: "bytes over limit", data: `{"x":1}`, limits: jsonshape.Limits{MaxBytes: int64(len(`{"x":1}`) - 1)}, wantKind: jsonshape.KindTooLarge},
		{name: "depth arrays at limit", data: `[[[1]]]`, limits: jsonshape.Limits{MaxDepth: 3}},
		{name: "depth arrays over limit", data: `[[[1]]]`, limits: jsonshape.Limits{MaxDepth: 2}, wantKind: jsonshape.KindTooDeep},
		{name: "depth objects at limit", data: `{"a":{"b":{"c":1}}}`, limits: jsonshape.Limits{MaxDepth: 3}},
		{name: "depth objects over limit", data: `{"a":{"b":{"c":1}}}`, limits: jsonshape.Limits{MaxDepth: 2}, wantKind: jsonshape.KindTooDeep},
		{name: "depth minus one of exact", data: `[[1]]`, limits: jsonshape.Limits{MaxDepth: 2}},
		{name: "depth exact", data: `[[[1]]]`, limits: jsonshape.Limits{MaxDepth: 3}},
		{name: "depth plus one over", data: `[[[[1]]]]`, limits: jsonshape.Limits{MaxDepth: 3}, wantKind: jsonshape.KindTooDeep},
		{name: "tokens at limit", data: `[1,2,3]`, limits: jsonshape.Limits{MaxTokens: 5}},
		{name: "tokens over limit", data: `[1,2,3]`, limits: jsonshape.Limits{MaxTokens: 4}, wantKind: jsonshape.KindTooManyTokens},
		{name: "array elements at limit", data: `[1,2,3]`, limits: jsonshape.Limits{MaxArrayElems: 3}},
		{name: "array elements over limit", data: `[1,2,3]`, limits: jsonshape.Limits{MaxArrayElems: 2}, wantKind: jsonshape.KindTooManyItems},
		{name: "object keys at limit", data: `{"a":1,"b":2}`, limits: jsonshape.Limits{MaxObjectKeys: 2}},
		{name: "object keys over limit", data: `{"a":1,"b":2}`, limits: jsonshape.Limits{MaxObjectKeys: 1}, wantKind: jsonshape.KindTooManyItems},
		{name: "string bytes at limit", data: `"abcd"`, limits: jsonshape.Limits{MaxStringBytes: 4}},
		{name: "string bytes over limit", data: `"abcd"`, limits: jsonshape.Limits{MaxStringBytes: 3}, wantKind: jsonshape.KindStringTooLong},
		{name: "key bytes at limit", data: `{"abcd":1}`, limits: jsonshape.Limits{MaxKeyBytes: 4}},
		{name: "key bytes over limit", data: `{"abcd":1}`, limits: jsonshape.Limits{MaxKeyBytes: 3}, wantKind: jsonshape.KindKeyTooLong},
		{name: "escaped string decoded bytes at limit", data: `"\u20ac"`, limits: jsonshape.Limits{MaxStringBytes: 3}},
		{name: "escaped string decoded bytes over limit", data: `"\u20ac"`, limits: jsonshape.Limits{MaxStringBytes: 2}, wantKind: jsonshape.KindStringTooLong},
		{name: "number literal at limit", data: `12345`, limits: jsonshape.Limits{MaxNumberBytes: 5}},
		{name: "number literal over limit", data: `123456`, limits: jsonshape.Limits{MaxNumberBytes: 5}, wantKind: jsonshape.KindNumberTooLong},
		{name: "wide array over", data: `[` + strings.TrimRight(strings.Repeat(`1,`, 8), ",") + `]`, limits: jsonshape.Limits{MaxArrayElems: 4}, wantKind: jsonshape.KindTooManyItems},
		{name: "wide object over", data: `{"a":1,"b":2,"c":3,"d":4,"e":5}`, limits: jsonshape.Limits{MaxObjectKeys: 3}, wantKind: jsonshape.KindTooManyItems},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := jsonshape.Preflight([]byte(tt.data), tt.limits)
			if tt.wantKind == "" {
				if err != nil {
					t.Fatalf("Preflight() error = %v", err)
				}
				if result.Bytes != len(tt.data) || result.Tokens <= 0 || result.MaxDepth < 0 {
					t.Fatalf("unexpected result: %+v", result)
				}
				return
			}
			if got := jsonshape.Classify(err); got != tt.wantKind {
				t.Fatalf("Classify(error) = %q, want %q (err=%v)", got, tt.wantKind, err)
			}
		})
	}
}

func TestPreflightMalformedAndRoots(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		data     string
		wantKind jsonshape.Kind
	}{
		{name: "empty", data: "", wantKind: jsonshape.KindMalformed},
		{name: "whitespace", data: " \n\t ", wantKind: jsonshape.KindMalformed},
		{name: "early malformed", data: `{`, wantKind: jsonshape.KindMalformed},
		{name: "late malformed", data: `{"a":[1,2,]}`, wantKind: jsonshape.KindMalformed},
		{name: "trailing token", data: `{} []`, wantKind: jsonshape.KindMalformed},
		{name: "trailing garbage", data: `{} nope`, wantKind: jsonshape.KindMalformed},
		{name: "multiple roots", data: `{}{}`, wantKind: jsonshape.KindMalformed},
		{name: "invalid utf8", data: "\"\xff\"", wantKind: jsonshape.KindInvalidUTF8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := jsonshape.Preflight([]byte(tt.data), jsonshape.Limits{})
			if got := jsonshape.Classify(err); got != tt.wantKind {
				t.Fatalf("Classify(error) = %q, want %q (err=%v)", got, tt.wantKind, err)
			}
		})
	}
}

func TestPreflightMalformedReasons(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		data       string
		wantReason jsonshape.MalformedReason
	}{
		{name: "empty", data: " \n\t ", wantReason: jsonshape.MalformedEmpty},
		{name: "non-JSON whitespace", data: "\u00a0", wantReason: jsonshape.MalformedSyntax},
		{name: "syntax", data: `nope`, wantReason: jsonshape.MalformedSyntax},
		{name: "multiple values", data: `{}{}`, wantReason: jsonshape.MalformedMultipleValues},
		{name: "incomplete", data: `{"a":1`, wantReason: jsonshape.MalformedIncomplete},
	}
	// MalformedUnexpectedClosing, MalformedTrailingData, and MalformedObjectValue
	// are defensive guards: encoding/json rejects the inputs that would reach
	// them (mismatched/trailing delimiters) before the token loop observes them.

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := jsonshape.Preflight([]byte(tt.data), jsonshape.Limits{})
			var shapeErr *jsonshape.Error
			if !errors.As(err, &shapeErr) || shapeErr.Kind != jsonshape.KindMalformed || shapeErr.Reason != tt.wantReason {
				t.Fatalf("error=%v, want KindMalformed reason %q", err, tt.wantReason)
			}
		})
	}
}

func TestPreflightDuplicateNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		data     string
		limits   jsonshape.Limits
		wantKind jsonshape.Kind
	}{
		{name: "sibling same names allowed across objects", data: `[{"a":1},{"a":2}]`, limits: jsonshape.Limits{RejectDuplicateNames: true}},
		{name: "nested same name different objects", data: `{"a":{"a":1}}`, limits: jsonshape.Limits{RejectDuplicateNames: true}},
		{name: "request envelope accepts duplicates", data: `{"a":1,"a":2}`, limits: jsonshape.RequestEnvelopeLimits()},
		{name: "custom false accepts duplicates", data: `{"a":1,"a":2}`, limits: jsonshape.Limits{RejectDuplicateNames: false}},
		{name: "custom true rejects duplicates", data: `{"a":1,"a":2}`, limits: jsonshape.Limits{RejectDuplicateNames: true}, wantKind: jsonshape.KindDuplicateName},
		{name: "tool schema rejects duplicates", data: `{"a":1,"a":2}`, limits: jsonshape.ToolSchemaLimits(), wantKind: jsonshape.KindDuplicateName},
		{name: "tool args rejects nested duplicates", data: `{"outer":{"x":1,"x":2}}`, limits: jsonshape.ToolArgumentsLimits(), wantKind: jsonshape.KindDuplicateName},
		{
			name: "reject true wins over MaxObjectKeys", data: `{"a":1,"a":2}`,
			limits: jsonshape.Limits{MaxObjectKeys: 1, RejectDuplicateNames: true}, wantKind: jsonshape.KindDuplicateName,
		},
		{
			name: "accept false counts keys for MaxObjectKeys", data: `{"a":1,"a":2}`,
			limits: jsonshape.Limits{MaxObjectKeys: 1, RejectDuplicateNames: false}, wantKind: jsonshape.KindTooManyItems,
		},
		{
			name: "distinct second key hits MaxObjectKeys", data: `{"a":1,"b":2}`,
			limits: jsonshape.Limits{MaxObjectKeys: 1}, wantKind: jsonshape.KindTooManyItems,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := jsonshape.Preflight([]byte(tt.data), tt.limits)
			if tt.wantKind == "" {
				if err != nil {
					t.Fatalf("Preflight() error = %v", err)
				}
				return
			}
			if got := jsonshape.Classify(err); got != tt.wantKind {
				t.Fatalf("Classify(error) = %q, want %q (err=%v)", got, tt.wantKind, err)
			}
		})
	}
}

func TestRequestEnvelopeDepthBoundaries(t *testing.T) {
	t.Parallel()
	depth := jsonshape.RequestEnvelopeLimits().MaxDepth
	at := []byte(strings.Repeat(`[`, depth) + `1` + strings.Repeat(`]`, depth))
	if _, err := jsonshape.Preflight(at, jsonshape.RequestEnvelopeLimits()); err != nil {
		t.Fatalf("exact depth %d: %v", depth, err)
	}
	over := []byte(strings.Repeat(`[`, depth+1) + `1` + strings.Repeat(`]`, depth+1))
	if got := jsonshape.Classify(mustPreflightErr(t, over, jsonshape.RequestEnvelopeLimits())); got != jsonshape.KindTooDeep {
		t.Fatalf("depth+1 Classify=%q want too_deep", got)
	}
}

func mustPreflightErr(t *testing.T, data []byte, limits jsonshape.Limits) error {
	t.Helper()
	_, err := jsonshape.Preflight(data, limits)
	if err == nil {
		t.Fatal("expected error")
	}
	return err
}

func TestPreflightObjectValueTracking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		data      string
		limits    jsonshape.Limits
		wantKind  jsonshape.Kind
		wantValue int
	}{
		{name: "string value counts one key", data: `{"a":"b"}`, limits: jsonshape.Limits{MaxObjectKeys: 1}},
		{
			name: "second key rejects after string value", data: `{"a":"b","c":"d"}`,
			limits: jsonshape.Limits{MaxObjectKeys: 1}, wantKind: jsonshape.KindTooManyItems, wantValue: 2,
		},
		{name: "nested object value preserves parent key count", data: `{"a":{"b":"c"}}`, limits: jsonshape.Limits{MaxObjectKeys: 1}},
		{name: "array value strings count as array elements", data: `{"a":["b","c"]}`, limits: jsonshape.Limits{MaxObjectKeys: 1, MaxArrayElems: 2}},
		{name: "root array string elements are not object keys", data: `["a","b"]`, limits: jsonshape.Limits{MaxObjectKeys: 1, MaxArrayElems: 2}},
		{
			name: "array string elements reject on array limit", data: `{"a":["b","c"]}`,
			limits: jsonshape.Limits{MaxObjectKeys: 1, MaxArrayElems: 1}, wantKind: jsonshape.KindTooManyItems, wantValue: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := jsonshape.Preflight([]byte(tt.data), tt.limits)
			if tt.wantKind == "" {
				if err != nil {
					t.Fatalf("Preflight() error = %v", err)
				}
				return
			}
			if got := jsonshape.Classify(err); got != tt.wantKind {
				t.Fatalf("Classify(error) = %q, want %q (err=%v)", got, tt.wantKind, err)
			}
			var guardErr *jsonshape.Error
			if !errors.As(err, &guardErr) {
				t.Fatalf("error %T does not unwrap to *jsonshape.Error", err)
			}
			if guardErr.Value != tt.wantValue {
				t.Fatalf("Error.Value = %d, want %d", guardErr.Value, tt.wantValue)
			}
		})
	}
}

func TestPreflightErrorsArePayloadFree(t *testing.T) {
	t.Parallel()

	secret := "SECRET_PAYLOAD_VALUE_xyz"
	cases := [][]byte{
		[]byte(`{"` + secret + `":1,"` + secret + `":2}`),
		[]byte(`{"k":"` + secret + strings.Repeat("a", 100) + `"}`),
		[]byte(`{"k":` + strings.Repeat("9", 80) + `}`),
		[]byte(`{"` + secret + `":`),
	}
	for i, data := range cases {
		_, err := jsonshape.Preflight(data, jsonshape.Limits{
			MaxStringBytes: 16, MaxNumberBytes: 16, MaxObjectKeys: 8, RejectDuplicateNames: true,
		})
		if err == nil {
			t.Fatalf("case %d: expected error", i)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("case %d: error leaked payload: %v", i, err)
		}
		if strings.Contains(err.Error(), strings.Repeat("9", 20)) {
			t.Fatalf("case %d: error leaked number literal: %v", i, err)
		}
	}
}

func TestPreflightWithContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := jsonshape.PreflightWithContext(ctx, []byte(`{"a":1}`), jsonshape.Limits{})
	if got := jsonshape.Classify(err); got != jsonshape.KindCanceled {
		t.Fatalf("Classify(error) = %q, want %q (err=%v)", got, jsonshape.KindCanceled, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(Canceled)=false err=%v", err)
	}
	if got := err.Error(); got != "jsonshape: context canceled" {
		t.Fatalf("Error() = %q, want stable payload-free text", got)
	}
}

func TestPreflightWithContextDeadlineExceeded(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()
	_, err := jsonshape.PreflightWithContext(ctx, []byte(`{"a":1}`), jsonshape.Limits{})
	if got := jsonshape.Classify(err); got != jsonshape.KindCanceled {
		t.Fatalf("Classify(error) = %q, want %q (err=%v)", got, jsonshape.KindCanceled, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(DeadlineExceeded)=false err=%v", err)
	}
	if got := err.Error(); got != "jsonshape: context deadline exceeded" {
		t.Fatalf("Error() = %q, want stable payload-free text", got)
	}
}

func TestPreflightWithContextLargeTokenDeadlineSupplemental(t *testing.T) {
	t.Parallel()

	// Deterministic: cancel after a fixed number of Err() polls during a wide scan.
	body := `[` + strings.TrimRight(strings.Repeat(`1,`, 10_000), ",") + `]`
	ctx := &cancelAfterN{Context: context.Background(), after: 3}
	_, err := jsonshape.PreflightWithContext(ctx, []byte(body), jsonshape.Limits{MaxArrayElems: 100_000, MaxTokens: 1_000_000})
	if got := jsonshape.Classify(err); got != jsonshape.KindCanceled {
		t.Fatalf("Classify(error) = %q, want %q (err=%v)", got, jsonshape.KindCanceled, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(Canceled)=false err=%v", err)
	}
}

// cancelAfterN returns context.Canceled after after successful Err() polls.
type cancelAfterN struct {
	context.Context
	after int
	n     int
}

func (c *cancelAfterN) Err() error {
	c.n++
	if c.n > c.after {
		return context.Canceled
	}
	if c.Context != nil {
		return c.Context.Err()
	}
	return nil
}

func TestPreflightLargeTextWhenConfigured(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("a", 256<<10)
	data := `"` + large + `"`
	if _, err := jsonshape.Preflight([]byte(data), jsonshape.Limits{
		MaxBytes:       int64(len(data)),
		MaxStringBytes: len(large),
		MaxTokens:      8,
	}); err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
}

func TestPreflightDifferentialValid(t *testing.T) {
	t.Parallel()

	corpus := []string{
		`null`, `true`, `false`, `0`, `-1.5e+10`, `""`, `"hi"`, `[]`, `{}`,
		`[1,2,3]`, `{"a":1,"b":[true,null]}`, `{"a":{"b":{"c":[]}}}`,
		`{"euro":"\u20ac"}`,
		`{`, `}`, `[`, `]`, `{"a":}`, `{"a":1,}`, `[1,]`, `{}{}`, `1 2`,
		"\"\xff\"", ``, `   `, `{"a":1} trailing`,
	}
	for _, s := range corpus {
		data := []byte(s)
		_, err := jsonshape.Preflight(data, jsonshape.Limits{
			MaxBytes: 1 << 20, MaxDepth: 64, MaxTokens: 1_000_000,
			MaxArrayElems: 100_000, MaxObjectKeys: 100_000,
			MaxStringBytes: 1 << 20, MaxKeyBytes: 1 << 20, MaxNumberBytes: 1 << 20,
		})
		valid := json.Valid(data)
		if err == nil && !valid {
			t.Fatalf("Preflight accepted invalid JSON %q", s)
		}
		if err != nil && valid {
			kind := jsonshape.Classify(err)
			if kind != jsonshape.KindInvalidUTF8 {
				t.Fatalf("Preflight rejected valid JSON %q with %q (%v)", s, kind, err)
			}
		}
	}
}

func TestNormalizeAndProfiles(t *testing.T) {
	t.Parallel()

	env := jsonshape.RequestEnvelopeLimits()
	if env.MaxBytes != 8<<20 {
		t.Fatalf("RequestEnvelopeLimits.MaxBytes = %d, want 8MiB", env.MaxBytes)
	}
	if env.RejectDuplicateNames {
		t.Fatal("RequestEnvelopeLimits must accept duplicate names")
	}
	got := jsonshape.NormalizeLimits(jsonshape.Limits{MaxDepth: 2, MaxBytes: -1})
	if got.MaxDepth != 2 {
		t.Fatalf("MaxDepth = %d, want 2", got.MaxDepth)
	}
	if got.MaxBytes != env.MaxBytes {
		t.Fatalf("MaxBytes = %d, want %d", got.MaxBytes, env.MaxBytes)
	}
	if got.MaxNumberBytes != env.MaxNumberBytes || got.MaxNumberBytes <= 0 {
		t.Fatalf("MaxNumberBytes not normalized: %+v", got)
	}
	if got.RejectDuplicateNames {
		t.Fatal("NormalizeLimits must not invent RejectDuplicateNames=true")
	}

	schema := jsonshape.ToolSchemaLimits()
	if schema.MaxBytes != 256<<10 || schema.MaxDepth != 32 || schema.MaxObjectKeys != 1024 || !schema.RejectDuplicateNames {
		t.Fatalf("ToolSchemaLimits unexpected: %+v", schema)
	}
	if schema.MaxTokens < 8*4096 {
		t.Fatalf("ToolSchemaLimits.MaxTokens = %d, want >= 8*MaxNodes", schema.MaxTokens)
	}
	args := jsonshape.ToolArgumentsLimits()
	if args.MaxBytes != 64<<10 || args.MaxDepth != 64 || args.MaxNumberBytes <= 0 || !args.RejectDuplicateNames {
		t.Fatalf("ToolArgumentsLimits unexpected: %+v", args)
	}

	partial := jsonshape.Limits{MaxBytes: 256 << 10}
	withSchema := jsonshape.NormalizeWithDefaults(partial, jsonshape.ToolSchemaLimits())
	if withSchema.MaxDepth != schema.MaxDepth || withSchema.MaxTokens != schema.MaxTokens || withSchema.MaxObjectKeys != schema.MaxObjectKeys {
		t.Fatalf("NormalizeWithDefaults(schema) = %+v", withSchema)
	}
	if withSchema.RejectDuplicateNames {
		t.Fatal("NormalizeWithDefaults must not copy RejectDuplicateNames from defaults")
	}
	keepFalse := jsonshape.NormalizeWithDefaults(jsonshape.Limits{
		MaxDepth: 2, RejectDuplicateNames: false,
	}, jsonshape.ToolSchemaLimits())
	if keepFalse.RejectDuplicateNames {
		t.Fatal("explicit RejectDuplicateNames=false must survive schema defaults")
	}
	keepTrue := jsonshape.NormalizeWithDefaults(jsonshape.Limits{
		MaxDepth: 2, RejectDuplicateNames: true,
	}, jsonshape.RequestEnvelopeLimits())
	if !keepTrue.RejectDuplicateNames {
		t.Fatal("explicit RejectDuplicateNames=true must survive envelope defaults")
	}
	withEnv := partial.Normalized(jsonshape.RequestEnvelopeLimits())
	if withEnv.MaxTokens != env.MaxTokens {
		t.Fatalf("Normalized(envelope).MaxTokens = %d, want %d", withEnv.MaxTokens, env.MaxTokens)
	}
}

func TestToolSchemaProfileNearWideProperties(t *testing.T) {
	t.Parallel()

	build := func(n int) []byte {
		var b strings.Builder
		b.WriteString(`{"type":"object","properties":{`)
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `"p%d":{"type":"string"}`, i)
		}
		b.WriteString(`}}`)
		return []byte(b.String())
	}

	limits := jsonshape.ToolSchemaLimits()
	atCap := build(limits.MaxObjectKeys)
	if int64(len(atCap)) > limits.MaxBytes {
		t.Fatalf("fixture exceeds MaxBytes: %d > %d", len(atCap), limits.MaxBytes)
	}
	if _, err := jsonshape.Preflight(atCap, limits); err != nil {
		t.Fatalf("near-wide schema Preflight error = %v", err)
	}
	over := build(limits.MaxObjectKeys + 1)
	if _, err := jsonshape.Preflight(over, limits); jsonshape.Classify(err) != jsonshape.KindTooManyItems {
		t.Fatalf("Classify = %q, want too_many_items (err=%v)", jsonshape.Classify(err), err)
	}
}

func TestPreflightDelimiterMismatchMalformed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
	}{
		{name: "object closed by bracket", data: `{]`},
		{name: "array closed by brace", data: `[}`},
		{name: "nested object closed by bracket", data: `{"a":[}`},
		{name: "nested array closed by brace", data: `[{"a":1]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := jsonshape.Preflight([]byte(tt.data), jsonshape.Limits{})
			if got := jsonshape.Classify(err); got != jsonshape.KindMalformed {
				t.Fatalf("Classify = %q, want malformed (err=%v)", got, err)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()
	if got := jsonshape.Classify(nil); got != "" {
		t.Fatalf("Classify(nil) = %q", got)
	}
	if got := jsonshape.Classify(errors.New("x")); got != "" {
		t.Fatalf("Classify(other) = %q", got)
	}
}
