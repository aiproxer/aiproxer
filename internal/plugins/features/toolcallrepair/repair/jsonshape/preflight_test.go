package jsonshape_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair/jsonshape"
)

func TestPreflight_ValidJSON(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		json string
	}{
		{"empty_object", `{}`},
		{"empty_array", `[]`},
		{"simple_object", `{"name":"test","value":42,"ok":true,"nil":null}`},
		{"nested_array", `[1,[2,[3,[4]]]]`},
		{"nested_object", `{"a":{"b":{"c":{"d":1}}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := jsonshape.Preflight([]byte(tc.json), jsonshape.ToolArgumentsLimits())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Bytes != len(tc.json) {
				t.Fatalf("bytes: got %d, want %d", res.Bytes, len(tc.json))
			}
			if res.Tokens <= 0 {
				t.Fatalf("tokens must be > 0, got %d", res.Tokens)
			}
		})
	}
}

func TestPreflight_LimitsEnforcement(t *testing.T) {
	t.Parallel()

	t.Run("max_bytes", func(t *testing.T) {
		t.Parallel()
		limits := jsonshape.Limits{MaxBytes: 10}
		_, err := jsonshape.Preflight([]byte(`{"toolong":1}`), limits)
		assertKind(t, err, jsonshape.KindTooLarge)
	})

	t.Run("max_depth", func(t *testing.T) {
		t.Parallel()
		limits := jsonshape.ToolArgumentsLimits()
		limits.MaxDepth = 3
		// depth 4 should fail
		_, err := jsonshape.Preflight([]byte(`{"a":{"b":{"c":{"d":1}}}}`), limits)
		assertKind(t, err, jsonshape.KindTooDeep)
	})

	t.Run("max_tokens", func(t *testing.T) {
		t.Parallel()
		limits := jsonshape.ToolArgumentsLimits()
		limits.MaxTokens = 5
		_, err := jsonshape.Preflight([]byte(`[1,2,3,4,5,6,7,8]`), limits)
		assertKind(t, err, jsonshape.KindTooManyTokens)
	})

	t.Run("max_array_elems", func(t *testing.T) {
		t.Parallel()
		limits := jsonshape.ToolArgumentsLimits()
		limits.MaxArrayElems = 3
		_, err := jsonshape.Preflight([]byte(`[1,2,3,4]`), limits)
		assertKind(t, err, jsonshape.KindTooManyItems)
	})

	t.Run("max_object_keys", func(t *testing.T) {
		t.Parallel()
		limits := jsonshape.ToolArgumentsLimits()
		limits.MaxObjectKeys = 2
		_, err := jsonshape.Preflight([]byte(`{"a":1,"b":2,"c":3}`), limits)
		assertKind(t, err, jsonshape.KindTooManyItems)
	})

	t.Run("max_key_bytes", func(t *testing.T) {
		t.Parallel()
		limits := jsonshape.ToolArgumentsLimits()
		limits.MaxKeyBytes = 5
		_, err := jsonshape.Preflight([]byte(`{"longkeyname":1}`), limits)
		assertKind(t, err, jsonshape.KindKeyTooLong)
	})

	t.Run("max_string_bytes", func(t *testing.T) {
		t.Parallel()
		limits := jsonshape.ToolArgumentsLimits()
		limits.MaxStringBytes = 5
		_, err := jsonshape.Preflight([]byte(`{"k":"toolongstring"}`), limits)
		assertKind(t, err, jsonshape.KindStringTooLong)
	})

	t.Run("max_number_bytes", func(t *testing.T) {
		t.Parallel()
		limits := jsonshape.ToolArgumentsLimits()
		limits.MaxNumberBytes = 5
		_, err := jsonshape.Preflight([]byte(`{"k":123456789}`), limits)
		assertKind(t, err, jsonshape.KindNumberTooLong)
	})

	t.Run("duplicate_names", func(t *testing.T) {
		t.Parallel()
		limits := jsonshape.ToolArgumentsLimits()
		limits.RejectDuplicateNames = true
		_, err := jsonshape.Preflight([]byte(`{"dup":1,"dup":2}`), limits)
		assertKind(t, err, jsonshape.KindDuplicateName)
	})

	t.Run("invalid_utf8", func(t *testing.T) {
		t.Parallel()
		_, err := jsonshape.Preflight([]byte("{\"key\": \"\xff\"}"), jsonshape.ToolArgumentsLimits())
		assertKind(t, err, jsonshape.KindInvalidUTF8)
	})

	t.Run("empty_body", func(t *testing.T) {
		t.Parallel()
		_, err := jsonshape.Preflight([]byte("   \n\t  "), jsonshape.ToolArgumentsLimits())
		assertKind(t, err, jsonshape.KindMalformed)
	})

	t.Run("trailing_data", func(t *testing.T) {
		t.Parallel()
		_, err := jsonshape.Preflight([]byte(`{"a":1} trailing`), jsonshape.ToolArgumentsLimits())
		assertKind(t, err, jsonshape.KindMalformed)
	})

	t.Run("incomplete_body", func(t *testing.T) {
		t.Parallel()
		_, err := jsonshape.Preflight([]byte(`{"a":`), jsonshape.ToolArgumentsLimits())
		assertKind(t, err, jsonshape.KindMalformed)
	})

	t.Run("unexpected_closing", func(t *testing.T) {
		t.Parallel()
		_, err := jsonshape.Preflight([]byte(`}`), jsonshape.ToolArgumentsLimits())
		assertKind(t, err, jsonshape.KindMalformed)
	})

	t.Run("object_value_without_key", func(t *testing.T) {
		t.Parallel()
		_, err := jsonshape.Preflight([]byte(`{1}`), jsonshape.ToolArgumentsLimits())
		assertKind(t, err, jsonshape.KindMalformed)
	})

	t.Run("canceled_context", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := jsonshape.PreflightWithContext(ctx, []byte(`{"a":1}`), jsonshape.ToolArgumentsLimits())
		assertKind(t, err, jsonshape.KindCanceled)
	})

	t.Run("deadline_exceeded", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, err := jsonshape.PreflightWithContext(ctx, []byte(`{"a":1}`), jsonshape.ToolArgumentsLimits())
		assertKind(t, err, jsonshape.KindCanceled)
	})
}

func TestToolProfiles(t *testing.T) {
	t.Parallel()
	schema := jsonshape.ToolSchemaLimits()
	if schema.MaxBytes != 256<<10 || schema.MaxDepth != 32 || !schema.RejectDuplicateNames {
		t.Fatalf("unexpected schema limits: %+v", schema)
	}

	args := jsonshape.ToolArgumentsLimits()
	if args.MaxBytes != 64<<10 || args.MaxDepth != 64 || !args.RejectDuplicateNames {
		t.Fatalf("unexpected args limits: %+v", args)
	}

	norm := jsonshape.NormalizeWithDefaults(jsonshape.Limits{MaxBytes: 100}, args)
	if norm.MaxBytes != 100 || norm.MaxDepth != args.MaxDepth {
		t.Fatalf("unexpected normalized limits: %+v", norm)
	}
}

func assertKind(t *testing.T, err error, want jsonshape.Kind) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error of kind %q, got nil", want)
	}
	got := jsonshape.Classify(err)
	if got != want {
		t.Fatalf("expected error kind %q, got %q (err: %v)", want, got, err)
	}
}

func BenchmarkPreflight(b *testing.B) {
	payload := fmt.Appendf(nil, `{"items":[%s],"meta":{"count":100}}`,
		strings.Repeat(`{"id":1,"name":"benchmark"},`, 99)+`{"id":100,"name":"benchmark"}`)
	limits := jsonshape.ToolArgumentsLimits()
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, err := jsonshape.Preflight(payload, limits)
		if err != nil {
			b.Fatal(err)
		}
	}
}
