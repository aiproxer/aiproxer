package jsonshape_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
)

func TestScanner_BasicDocuments(t *testing.T) {
	t.Parallel()

	docs := []struct {
		name string
		json string
	}{
		{name: "empty object", json: `{}`},
		{name: "empty array", json: `[]`},
		{name: "simple object", json: `{"a": 1, "b": "hello"}`},
		{name: "nested object", json: `{"a": {"b": {"c": [1, 2, 3]}}}`},
		{name: "mixed array", json: `[1, "two", true, false, null]`},
		{name: "scalar integer", json: `42`},
		{name: "scalar negative float", json: `-3.1415`},
		{name: "scalar string", json: `"hello world"`},
		{name: "scalar true", json: `true`},
		{name: "scalar false", json: `false`},
		{name: "scalar null", json: `null`},
	}

	for _, tc := range docs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			expected, expErr := jsonshape.Preflight([]byte(tc.json), jsonshape.Limits{})
			if expErr != nil {
				t.Fatalf("Preflight failed unexpectedly for valid JSON: %v", expErr)
			}

			s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{})
			if err := s.Feed([]byte(tc.json)); err != nil {
				t.Fatalf("Feed failed: %v", err)
			}
			res, err := s.Finish()
			if err != nil {
				t.Fatalf("Finish failed: %v", err)
			}
			if res.Bytes != expected.Bytes {
				t.Errorf("Bytes mismatch: got %d, want %d", res.Bytes, expected.Bytes)
			}
			if res.Tokens != expected.Tokens {
				t.Errorf("Tokens mismatch: got %d, want %d", res.Tokens, expected.Tokens)
			}
			if res.MaxDepth != expected.MaxDepth {
				t.Errorf("MaxDepth mismatch: got %d, want %d", res.MaxDepth, expected.MaxDepth)
			}
		})
	}
}

func TestScanner_ChunkedFeeds(t *testing.T) {
	t.Parallel()

	doc := `{"model": "gpt-4o", "messages": [{"role": "user", "content": "Hello, world!"}], "stream": true, "max_tokens": 1024}`
	expected, expErr := jsonshape.Preflight([]byte(doc), jsonshape.Limits{})
	if expErr != nil {
		t.Fatalf("Preflight failed: %v", expErr)
	}

	chunkSizes := []int{1, 2, 3, 5, 7, 16, 32, 64, len(doc)}
	for _, sz := range chunkSizes {
		s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{})
		data := []byte(doc)
		for i := 0; i < len(data); i += sz {
			end := min(i+sz, len(data))
			if err := s.Feed(data[i:end]); err != nil {
				t.Fatalf("Feed sz=%d at offset %d: %v", sz, i, err)
			}
		}
		res, err := s.Finish()
		if err != nil {
			t.Fatalf("Finish sz=%d: %v", sz, err)
		}
		if res.Bytes != expected.Bytes || res.Tokens != expected.Tokens || res.MaxDepth != expected.MaxDepth {
			t.Errorf("Result mismatch for sz=%d: got %+v, want %+v", sz, res, expected)
		}
	}
}

func TestScanner_UTF8_Boundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		chunks   [][]byte
		wantKind jsonshape.Kind
	}{
		{
			name:   "2-byte utf-8 split across chunks",
			chunks: [][]byte{[]byte(`{"k": "`), {0xC2}, {0xA9}, []byte(`"}`)},
		},
		{
			name:   "3-byte utf-8 split 1 and 2",
			chunks: [][]byte{[]byte(`{"euro": "`), {0xE2}, {0x82, 0xAC}, []byte(`"}`)},
		},
		{
			name:   "3-byte utf-8 split 2 and 1",
			chunks: [][]byte{[]byte(`{"euro": "`), {0xE2, 0x82}, {0xAC}, []byte(`"}`)},
		},
		{
			name:   "4-byte utf-8 split 1, 1, 2",
			chunks: [][]byte{[]byte(`{"emoji": "`), {0xF0}, {0x9F}, {0x98, 0x80}, []byte(`"}`)},
		},
		{
			name:     "invalid raw byte 0xFF inside string",
			chunks:   [][]byte{[]byte(`{"k": "abc`), {0xFF}, []byte(`def"}`)},
			wantKind: jsonshape.KindInvalidUTF8,
		},
		{
			name:     "invalid raw byte 0xFF in whitespace",
			chunks:   [][]byte{[]byte(`{ `), {0xFF}, []byte(` "k": 1}`)},
			wantKind: jsonshape.KindInvalidUTF8,
		},
		{
			name:     "incomplete utf-8 at EOF",
			chunks:   [][]byte{[]byte(`{"k": "`), {0xC2}},
			wantKind: jsonshape.KindInvalidUTF8,
		},
		{
			name:     "overlong utf-8 0xC0",
			chunks:   [][]byte{[]byte(`{"k": "`), {0xC0, 0x80}, []byte(`"}`)},
			wantKind: jsonshape.KindInvalidUTF8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{})
			var feedErr error
			for _, c := range tt.chunks {
				if feedErr = s.Feed(c); feedErr != nil {
					break
				}
			}
			var finalErr error
			if feedErr != nil {
				finalErr = feedErr
			} else {
				_, finalErr = s.Finish()
			}

			if tt.wantKind == "" {
				if finalErr != nil {
					t.Fatalf("unexpected error: %v", finalErr)
				}
			} else {
				if got := jsonshape.Classify(finalErr); got != tt.wantKind {
					t.Fatalf("Classify(err) = %q, want %q (err=%v)", got, tt.wantKind, finalErr)
				}
			}
		})
	}
}

func TestScanner_EscapesAndSurrogates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		chunks   [][]byte
		wantKind jsonshape.Kind
	}{
		{
			name:   "simple escapes",
			chunks: [][]byte{[]byte(`{"k": "\" \\ \/ \b \f \n \r \t"}`)},
		},
		{
			name:   "unicode escape split across chunks",
			chunks: [][]byte{[]byte(`{"k": "\u`), []byte(`20`), []byte(`ac"}`)},
		},
		{
			name:   "surrogate pair split across chunks",
			chunks: [][]byte{[]byte(`{"k": "\uD83D`), []byte(`\uDE00"}`)},
		},
		{
			name:   "surrogate pair split between \\ and u",
			chunks: [][]byte{[]byte(`{"k": "\uD83D\`), []byte(`uDE00"}`)},
		},
		{
			name:     "invalid hex in unicode escape",
			chunks:   [][]byte{[]byte(`{"k": "\u123z"}`)},
			wantKind: jsonshape.KindMalformed,
		},
		{
			name:     "invalid escape character \\a",
			chunks:   [][]byte{[]byte(`{"k": "\a"}`)},
			wantKind: jsonshape.KindMalformed,
		},
		{
			name:     "unescaped control char newline in string",
			chunks:   [][]byte{[]byte("{\"k\": \"line1\nline2\"}")},
			wantKind: jsonshape.KindMalformed,
		},
		{
			name:     "unescaped null byte in string",
			chunks:   [][]byte{[]byte("{\"k\": \"null\x00byte\"}")},
			wantKind: jsonshape.KindMalformed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{})
			var feedErr error
			for _, c := range tt.chunks {
				if feedErr = s.Feed(c); feedErr != nil {
					break
				}
			}
			var finalErr error
			if feedErr != nil {
				finalErr = feedErr
			} else {
				_, finalErr = s.Finish()
			}

			if tt.wantKind == "" {
				if finalErr != nil {
					t.Fatalf("unexpected error: %v", finalErr)
				}
			} else {
				if got := jsonshape.Classify(finalErr); got != tt.wantKind {
					t.Fatalf("Classify(err) = %q, want %q (err=%v)", got, tt.wantKind, finalErr)
				}
			}
		})
	}
}

func TestScanner_NumberGrammar(t *testing.T) {
	t.Parallel()

	validNumbers := []string{
		`0`, `42`, `-42`, `3.14`, `-0.5`, `1e10`, `1E+10`, `1.5e-3`, `0.12345`,
	}
	for _, num := range validNumbers {
		t.Run("valid_"+num, func(t *testing.T) {
			s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{})
			if err := s.Feed([]byte(num)); err != nil {
				t.Fatalf("Feed failed: %v", err)
			}
			if _, err := s.Finish(); err != nil {
				t.Fatalf("Finish failed: %v", err)
			}
		})
	}

	invalidNumbers := []struct {
		name string
		raw  string
	}{
		{name: "leading zero", raw: `01`},
		{name: "plus sign", raw: `+1`},
		{name: "dangling minus", raw: `-`},
		{name: "dangling dot", raw: `1.`},
		{name: "dangling exponent", raw: `1e`},
		{name: "dangling exponent sign", raw: `1e+`},
		{name: "double dot", raw: `1..2`},
		{name: "hex number", raw: `0x1f`},
	}
	for _, tt := range invalidNumbers {
		t.Run("invalid_"+tt.name, func(t *testing.T) {
			t.Parallel()
			s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{})
			feedErr := s.Feed([]byte(tt.raw))
			var finalErr error
			if feedErr != nil {
				finalErr = feedErr
			} else {
				_, finalErr = s.Finish()
			}
			if got := jsonshape.Classify(finalErr); got != jsonshape.KindMalformed {
				t.Fatalf("Classify(err) = %q, want %q (err=%v)", got, jsonshape.KindMalformed, finalErr)
			}
		})
	}
}

func TestScanner_Limits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		data     string
		limits   jsonshape.Limits
		wantKind jsonshape.Kind
	}{
		{
			name:     "bytes over limit",
			data:     `{"x":1}`,
			limits:   jsonshape.Limits{MaxBytes: int64(len(`{"x":1}`) - 1)},
			wantKind: jsonshape.KindTooLarge,
		},
		{
			name:     "depth over limit",
			data:     `[[[1]]]`,
			limits:   jsonshape.Limits{MaxDepth: 2},
			wantKind: jsonshape.KindTooDeep,
		},
		{
			name:     "tokens over limit",
			data:     `[1,2,3]`,
			limits:   jsonshape.Limits{MaxTokens: 4},
			wantKind: jsonshape.KindTooManyTokens,
		},
		{
			name:     "array elements over limit",
			data:     `[1,2,3]`,
			limits:   jsonshape.Limits{MaxArrayElems: 2},
			wantKind: jsonshape.KindTooManyItems,
		},
		{
			name:     "object keys over limit",
			data:     `{"a":1,"b":2}`,
			limits:   jsonshape.Limits{MaxObjectKeys: 1},
			wantKind: jsonshape.KindTooManyItems,
		},
		{
			name:     "string bytes over limit",
			data:     `"abcd"`,
			limits:   jsonshape.Limits{MaxStringBytes: 3},
			wantKind: jsonshape.KindStringTooLong,
		},
		{
			name:     "escaped string bytes over limit",
			data:     `"\u20ac"`,
			limits:   jsonshape.Limits{MaxStringBytes: 2},
			wantKind: jsonshape.KindStringTooLong,
		},
		{
			name:     "key bytes over limit",
			data:     `{"abcd":1}`,
			limits:   jsonshape.Limits{MaxKeyBytes: 3},
			wantKind: jsonshape.KindKeyTooLong,
		},
		{
			name:     "number literal over limit",
			data:     `123456`,
			limits:   jsonshape.Limits{MaxNumberBytes: 5},
			wantKind: jsonshape.KindNumberTooLong,
		},
		{
			name:     "duplicate keys rejected",
			data:     `{"a":1,"a":2}`,
			limits:   jsonshape.Limits{RejectDuplicateNames: true},
			wantKind: jsonshape.KindDuplicateName,
		},
		{
			name:   "duplicate keys accepted by default",
			data:   `{"a":1,"a":2}`,
			limits: jsonshape.Limits{RejectDuplicateNames: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := jsonshape.NewScanner(context.Background(), tt.limits)
			feedErr := s.Feed([]byte(tt.data))
			var finalErr error
			if feedErr != nil {
				finalErr = feedErr
			} else {
				_, finalErr = s.Finish()
			}

			if tt.wantKind == "" {
				if finalErr != nil {
					t.Fatalf("unexpected error: %v", finalErr)
				}
			} else {
				if got := jsonshape.Classify(finalErr); got != tt.wantKind {
					t.Fatalf("Classify(err) = %q, want %q (err=%v)", got, tt.wantKind, finalErr)
				}
			}
		})
	}
}

func TestScanner_MalformedAndReasons(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		data       string
		wantReason jsonshape.MalformedReason
	}{
		{name: "empty", data: "", wantReason: jsonshape.MalformedEmpty},
		{name: "whitespace only", data: "   \t\n\r  ", wantReason: jsonshape.MalformedEmpty},
		{name: "non-json character syntax", data: "nope", wantReason: jsonshape.MalformedSyntax},
		{name: "non-json whitespace", data: "\u00a0", wantReason: jsonshape.MalformedSyntax},
		{name: "multiple values objects", data: "{}{}", wantReason: jsonshape.MalformedMultipleValues},
		{name: "multiple values scalars", data: "1 2", wantReason: jsonshape.MalformedMultipleValues},
		{name: "incomplete object", data: `{"a": 1`, wantReason: jsonshape.MalformedIncomplete},
		{name: "incomplete array", data: `[1, 2`, wantReason: jsonshape.MalformedIncomplete},
		{name: "incomplete colon", data: `{"a":`, wantReason: jsonshape.MalformedIncomplete},
		{name: "unexpected closing brace", data: `}`, wantReason: jsonshape.MalformedUnexpectedClosing},
		{name: "unexpected closing bracket", data: `]`, wantReason: jsonshape.MalformedUnexpectedClosing},
		{name: "mismatched delimiter", data: `{"a": [}`, wantReason: jsonshape.MalformedUnexpectedClosing},
		{name: "trailing data", data: `{"a": 1} trailing`, wantReason: jsonshape.MalformedTrailingData},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{})
			feedErr := s.Feed([]byte(tt.data))
			var finalErr error
			if feedErr != nil {
				finalErr = feedErr
			} else {
				_, finalErr = s.Finish()
			}

			var shapeErr *jsonshape.Error
			if !errors.As(finalErr, &shapeErr) || shapeErr.Kind != jsonshape.KindMalformed {
				t.Fatalf("error=%v, want KindMalformed", finalErr)
			}
			if shapeErr.Reason != tt.wantReason {
				t.Fatalf("Reason = %q, want %q", shapeErr.Reason, tt.wantReason)
			}
		})
	}
}

func TestScanner_Cancellation(t *testing.T) {
	t.Parallel()

	t.Run("canceled before feed", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s := jsonshape.NewScanner(ctx, jsonshape.Limits{})
		err := s.Feed([]byte(`{"a": 1}`))
		if got := jsonshape.Classify(err); got != jsonshape.KindCanceled {
			t.Fatalf("Classify(err) = %q, want %q (err=%v)", got, jsonshape.KindCanceled, err)
		}
	})

	t.Run("canceled between feeds", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		s := jsonshape.NewScanner(ctx, jsonshape.Limits{})
		if err := s.Feed([]byte(`{"a": `)); err != nil {
			t.Fatalf("Feed 1 failed: %v", err)
		}
		cancel()
		err := s.Feed([]byte(`1}`))
		if got := jsonshape.Classify(err); got != jsonshape.KindCanceled {
			t.Fatalf("Classify(err) = %q, want %q (err=%v)", got, jsonshape.KindCanceled, err)
		}
	})

	t.Run("deadline exceeded before finish", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
		defer cancel()
		s := jsonshape.NewScanner(ctx, jsonshape.Limits{})
		_, err := s.Finish()
		if got := jsonshape.Classify(err); got != jsonshape.KindCanceled {
			t.Fatalf("Classify(err) = %q, want %q (err=%v)", got, jsonshape.KindCanceled, err)
		}
	})
}

func TestScanner_GiantStringNoRetention(t *testing.T) {
	t.Parallel()

	// 512 KiB string fed in 4 KiB chunks
	chunkSize := 4096
	totalSize := 512 * 1024
	limits := jsonshape.Limits{
		MaxBytes:       int64(totalSize + 1024),
		MaxStringBytes: totalSize,
	}

	s := jsonshape.NewScanner(context.Background(), limits)
	if err := s.Feed([]byte(`{"data":"`)); err != nil {
		t.Fatalf("Feed header: %v", err)
	}

	chunk := strings.Repeat("a", chunkSize)
	for sent := 0; sent < totalSize; sent += chunkSize {
		if err := s.Feed([]byte(chunk)); err != nil {
			t.Fatalf("Feed chunk at %d: %v", sent, err)
		}
	}

	if err := s.Feed([]byte(`"}`)); err != nil {
		t.Fatalf("Feed footer: %v", err)
	}

	res, err := s.Finish()
	if err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	if res.Tokens != 4 { // {, data, giant_str, }
		t.Errorf("Tokens = %d, want 4", res.Tokens)
	}
}

func TestScanner_Events(t *testing.T) {
	t.Parallel()

	jsonStr := `{"model":"gpt-4","items":[1,true,null]}`
	var events []jsonshape.Event

	handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
		events = append(events, e)
		return nil
	})

	s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{}, jsonshape.WithEventHandler(handler))
	if err := s.Feed([]byte(jsonStr)); err != nil {
		t.Fatalf("Feed failed: %v", err)
	}
	if _, err := s.Finish(); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	expectedTypes := []jsonshape.EventType{
		jsonshape.EventObjectStart,
		jsonshape.EventKey,
		jsonshape.EventString,
		jsonshape.EventKey,
		jsonshape.EventArrayStart,
		jsonshape.EventNumber,
		jsonshape.EventTrue,
		jsonshape.EventNull,
		jsonshape.EventArrayEnd,
		jsonshape.EventObjectEnd,
	}

	if len(events) != len(expectedTypes) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(expectedTypes), events)
	}

	for i, exp := range expectedTypes {
		if events[i].Type != exp {
			t.Errorf("event %d: type = %v, want %v", i, events[i].Type, exp)
		}
	}

	// Verify key was captured
	if events[1].Key != "model" {
		t.Errorf("event 1 Key = %q, want 'model'", events[1].Key)
	}
	if events[3].Key != "items" {
		t.Errorf("event 3 Key = %q, want 'items'", events[3].Key)
	}
}

func TestScanner_TopLevelSpansAndNestedKeyDiscrimination(t *testing.T) {
	t.Parallel()

	jsonStr := `{
		"messages": [
			{"role": "user", "content": "{\"model\": \"fake-content-string\"}", "model": "nested-model-val"}
		],
		"model": "gpt-4o",
		"stream": true,
		"max_tokens": 1024,
		"stop": null,
		"stream_options": {"include_usage": true}
	}`
	data := []byte(jsonStr)

	tracker := jsonshape.NewTopLevelSpanTracker("model", "stream", "max_tokens", "stop", "stream_options", "messages")
	var events []jsonshape.Event
	handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
		events = append(events, e)
		return tracker.OnEvent(e)
	})
	s := jsonshape.NewScanner(
		context.Background(), jsonshape.Limits{},
		jsonshape.WithEventHandler(handler),
		jsonshape.WithTrackedTopLevelSpans("model", "stream", "max_tokens", "stop", "stream_options", "messages"),
	)

	if err := s.Feed(data); err != nil {
		t.Fatalf("Feed failed: %v", err)
	}
	if _, err := s.Finish(); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	// 1. Verify TopLevel and Path discrimination across events
	var foundNestedModelKey, foundNestedModelVal bool
	var foundTopLevelModelKey, foundTopLevelModelVal bool

	for _, e := range events {
		if e.Key == "model" {
			if e.TopLevel {
				switch e.Type {
				case jsonshape.EventKey:
					foundTopLevelModelKey = true
					if !slices.Equal(e.Path, []string{"model"}) {
						t.Errorf("top-level model key path = %v, want ['model']", e.Path)
					}
				case jsonshape.EventString:
					foundTopLevelModelVal = true
					if !slices.Equal(e.Path, []string{"model"}) {
						t.Errorf("top-level model value path = %v, want ['model']", e.Path)
					}
					raw := string(data[e.Span.Offset : e.Span.Offset+e.Span.Length])
					if raw != `"gpt-4o"` {
						t.Errorf("top-level model value raw span = %q, want %q", raw, `"gpt-4o"`)
					}
				}
			} else {
				switch e.Type {
				case jsonshape.EventKey:
					foundNestedModelKey = true
					if !slices.Equal(e.Path, []string{"messages", "model"}) {
						t.Errorf("nested model key path = %v, want ['messages', 'model']", e.Path)
					}
				case jsonshape.EventString:
					foundNestedModelVal = true
					if !slices.Equal(e.Path, []string{"messages", "model"}) {
						t.Errorf("nested model val path = %v, want ['messages', 'model']", e.Path)
					}
					raw := string(data[e.Span.Offset : e.Span.Offset+e.Span.Length])
					if raw != `"nested-model-val"` {
						t.Errorf("nested model value raw span = %q, want %q", raw, `"nested-model-val"`)
					}
				}
			}
		}
	}

	if !foundNestedModelKey || !foundNestedModelVal {
		t.Fatalf("nested model key/val not found: key=%v val=%v", foundNestedModelKey, foundNestedModelVal)
	}
	if !foundTopLevelModelKey || !foundTopLevelModelVal {
		t.Fatalf("top-level model key/val not found: key=%v val=%v", foundTopLevelModelKey, foundTopLevelModelVal)
	}

	// 2. Verify tracker recorded ONLY top-level spans and exact raw content
	modelSpan, ok := s.TopLevelSpan("model")
	if !ok {
		t.Fatal("missing top-level model span from scanner")
	}
	if raw := string(data[modelSpan.Offset : modelSpan.Offset+modelSpan.Length]); raw != `"gpt-4o"` {
		t.Errorf("scanner model span = %q, want '\"gpt-4o\"'", raw)
	}

	streamSpan, ok := s.TopLevelSpan("stream")
	if !ok || string(data[streamSpan.Offset:streamSpan.Offset+streamSpan.Length]) != "true" {
		t.Errorf("stream span mismatch: ok=%v, span=%+v", ok, streamSpan)
	}

	maxTokensSpan, ok := s.TopLevelSpan("max_tokens")
	if !ok || string(data[maxTokensSpan.Offset:maxTokensSpan.Offset+maxTokensSpan.Length]) != "1024" {
		t.Errorf("max_tokens span mismatch: ok=%v, span=%+v", ok, maxTokensSpan)
	}

	stopSpan, ok := s.TopLevelSpan("stop")
	if !ok || string(data[stopSpan.Offset:stopSpan.Offset+stopSpan.Length]) != "null" {
		t.Errorf("stop span mismatch: ok=%v, span=%+v", ok, stopSpan)
	}

	streamOptsSpan, ok := s.TopLevelSpan("stream_options")
	if !ok || string(data[streamOptsSpan.Offset:streamOptsSpan.Offset+streamOptsSpan.Length]) != `{"include_usage": true}` {
		t.Errorf("stream_options span mismatch: ok=%v, raw=%q", ok, string(data[streamOptsSpan.Offset:streamOptsSpan.Offset+streamOptsSpan.Length]))
	}

	messagesSpan, ok := s.TopLevelSpan("messages")
	if !ok {
		t.Fatal("missing messages span from scanner")
	}
	if rawMessages := string(data[messagesSpan.Offset : messagesSpan.Offset+messagesSpan.Length]); !strings.HasPrefix(rawMessages, "[") || !strings.HasSuffix(rawMessages, "]") {
		t.Errorf("messages raw span not array: %q", rawMessages)
	}

	// 3. Test standalone tracker directly
	trackerSpans := tracker.Spans()
	if len(trackerSpans) != 6 {
		t.Errorf("tracker recorded %d spans, want 6", len(trackerSpans))
	}
	if trModel, ok := tracker.Span("model"); !ok || trModel != modelSpan {
		t.Errorf("tracker model span mismatch: got %+v, want %+v", trModel, modelSpan)
	}
}

func TestScanner_TopLevelSpans_ChunkedFeeds(t *testing.T) {
	t.Parallel()

	jsonStr := `{
		"model": "claude-3-5-sonnet-20241022",
		"stream": false,
		"temperature": 0.5,
		"messages": [{"role": "user", "content": "hi"}],
		"metadata": {"session_id": "sess-123"}
	}`
	data := []byte(jsonStr)

	scBase := jsonshape.NewScanner(
		context.Background(), jsonshape.Limits{},
		jsonshape.WithTrackedTopLevelSpans("model", "stream", "temperature", "messages", "metadata"),
	)
	if err := scBase.Feed(data); err != nil {
		t.Fatalf("base Feed failed: %v", err)
	}
	if _, err := scBase.Finish(); err != nil {
		t.Fatalf("base Finish failed: %v", err)
	}
	baseSpans := scBase.TopLevelSpans()

	chunkSizes := []int{1, 2, 3, 5, 7, 13, 27, len(data)}
	for _, sz := range chunkSizes {
		sc := jsonshape.NewScanner(
			context.Background(), jsonshape.Limits{},
			jsonshape.WithTrackedTopLevelSpans("model", "stream", "temperature", "messages", "metadata"),
		)
		for i := 0; i < len(data); i += sz {
			end := min(i+sz, len(data))
			if err := sc.Feed(data[i:end]); err != nil {
				t.Fatalf("chunked feed sz=%d at %d: %v", sz, i, err)
			}
		}
		if _, err := sc.Finish(); err != nil {
			t.Fatalf("chunked finish sz=%d: %v", sz, err)
		}
		spans := sc.TopLevelSpans()
		for k, wantSpan := range baseSpans {
			gotSpan, ok := spans[k]
			if !ok || gotSpan != wantSpan {
				t.Errorf("sz=%d key=%q gotSpan=%+v wantSpan=%+v", sz, k, gotSpan, wantSpan)
			}
		}
	}
}

func TestScanner_SpanValidation(t *testing.T) {
	t.Parallel()

	valid := jsonshape.Span{Offset: 10, Length: 20}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid span failed: %v", err)
	}
	end, err := valid.End()
	if err != nil || end != 30 {
		t.Errorf("valid span End() = %d, %v; want 30, nil", end, err)
	}

	negOffset := jsonshape.Span{Offset: -1, Length: 10}
	if err := negOffset.Validate(); err == nil {
		t.Error("expected error for negative offset")
	}

	negLength := jsonshape.Span{Offset: 10, Length: -1}
	if err := negLength.Validate(); err == nil {
		t.Error("expected error for negative length")
	}

	overflow := jsonshape.Span{Offset: math.MaxInt64 - 5, Length: 10}
	if err := overflow.Validate(); err == nil {
		t.Error("expected error for overflow span")
	}
	if _, err := overflow.End(); err == nil {
		t.Error("expected End() overflow error")
	}
}

func TestScanner_ProviderNeutral(t *testing.T) {
	t.Parallel()

	jsonDoc := `{"x_custom_field": "val1", "payload_data": [10, 20], "flag": true}`
	data := []byte(jsonDoc)

	sc := jsonshape.NewScanner(
		context.Background(), jsonshape.Limits{},
		jsonshape.WithTrackedTopLevelSpans("x_custom_field", "payload_data", "flag"),
	)
	if err := sc.Feed(data); err != nil {
		t.Fatalf("Feed failed: %v", err)
	}
	if _, err := sc.Finish(); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	sp1, ok1 := sc.TopLevelSpan("x_custom_field")
	if !ok1 || string(data[sp1.Offset:sp1.Offset+sp1.Length]) != `"val1"` {
		t.Errorf("custom field span mismatch: %v, %q", ok1, string(data[sp1.Offset:sp1.Offset+sp1.Length]))
	}

	sp2, ok2 := sc.TopLevelSpan("payload_data")
	if !ok2 || string(data[sp2.Offset:sp2.Offset+sp2.Length]) != `[10, 20]` {
		t.Errorf("payload_data span mismatch: %v, %q", ok2, string(data[sp2.Offset:sp2.Offset+sp2.Length]))
	}
}

func TestScanner_TopLevelSpans_EdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("arbitrary field positions and whitespace", func(t *testing.T) {
		t.Parallel()

		// model placed late, with irregular whitespace around tokens
		jsonDoc := `{
			"headers"   :   { "auth": "bearer token" }   ,
			"stream"    :   false   ,
			"messages"  :   [ { "content": "hello" } ] ,
			"model"     :   "gpt-4-turbo-preview\n\t\"quoted\""
		}`
		data := []byte(jsonDoc)

		sc := jsonshape.NewScanner(
			context.Background(), jsonshape.Limits{},
			jsonshape.WithTrackedTopLevelSpans("model", "stream", "headers", "messages"),
		)
		if err := sc.Feed(data); err != nil {
			t.Fatalf("Feed failed: %v", err)
		}
		if _, err := sc.Finish(); err != nil {
			t.Fatalf("Finish failed: %v", err)
		}

		mSpan, ok := sc.TopLevelSpan("model")
		if !ok {
			t.Fatal("missing late model span")
		}
		expectedModelRaw := `"gpt-4-turbo-preview\n\t\"quoted\""`
		if got := string(data[mSpan.Offset : mSpan.Offset+mSpan.Length]); got != expectedModelRaw {
			t.Errorf("late model raw span mismatch: got %q, want %q", got, expectedModelRaw)
		}

		sSpan, ok := sc.TopLevelSpan("stream")
		if !ok {
			t.Fatal("missing stream span")
		}
		if got := string(data[sSpan.Offset : sSpan.Offset+sSpan.Length]); got != "false" {
			t.Errorf("stream raw span mismatch: got %q, want 'false'", got)
		}

		hSpan, ok := sc.TopLevelSpan("headers")
		if !ok {
			t.Fatal("missing headers span")
		}
		if got := string(data[hSpan.Offset : hSpan.Offset+hSpan.Length]); got != `{ "auth": "bearer token" }` {
			t.Errorf("headers raw span mismatch: got %q", got)
		}
	})

	t.Run("handler error stops scanner", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("stop scanner")
		h := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
			if e.Key == "trigger" {
				return sentinel
			}
			return nil
		})

		sc := jsonshape.NewScanner(context.Background(), jsonshape.Limits{}, jsonshape.WithEventHandler(h))
		err := sc.Feed([]byte(`{"a": 1, "trigger": "boom", "c": 3}`))
		if !errors.Is(err, sentinel) {
			t.Errorf("expected sentinel error, got: %v", err)
		}
	})

	t.Run("empty object yields no top level value spans", func(t *testing.T) {
		t.Parallel()

		tracker := jsonshape.NewTopLevelSpanTracker("model")
		sc := jsonshape.NewScanner(context.Background(), jsonshape.Limits{}, jsonshape.WithEventHandler(tracker))
		if err := sc.Feed([]byte(`{}`)); err != nil {
			t.Fatalf("Feed failed: %v", err)
		}
		if _, err := sc.Finish(); err != nil {
			t.Fatalf("Finish failed: %v", err)
		}

		if _, ok := tracker.Span("model"); ok {
			t.Error("unexpected span recorded for empty object")
		}
	})
}

func TestScanner_DifferentialPreflight(t *testing.T) {
	t.Parallel()

	corpus := []string{
		`null`, `true`, `false`, `0`, `-1.5e+10`, `""`, `"hi"`, `[]`, `{}`,
		`[1,2,3]`, `{"a":1,"b":[true,null]}`, `{"a":{"b":{"c":[]}}}`,
		`{"euro":"\u20ac"}`,
		`{`, `}`, `[`, `]`, `{"a":}`, `{"a":1,}`, `[1,]`, `{}{}`, `1 2`,
		"\"\xff\"", ``, `   `, `{"a":1} trailing`,
	}

	limits := jsonshape.Limits{
		MaxBytes: 1 << 20, MaxDepth: 64, MaxTokens: 1_000_000,
		MaxArrayElems: 100_000, MaxObjectKeys: 100_000,
		MaxStringBytes: 1 << 20, MaxKeyBytes: 1 << 20, MaxNumberBytes: 1 << 20,
	}

	for _, s := range corpus {
		t.Run(s, func(t *testing.T) {
			t.Parallel()
			data := []byte(s)

			// 1. Run Preflight
			expectedRes, preErr := jsonshape.Preflight(data, limits)

			// 2. Run Scanner on whole data
			scWhole := jsonshape.NewScanner(context.Background(), limits)
			feedErr := scWhole.Feed(data)
			var scErr error
			var scRes jsonshape.Result
			if feedErr != nil {
				scErr = feedErr
			} else {
				scRes, scErr = scWhole.Finish()
			}

			if (preErr == nil) != (scErr == nil) {
				t.Fatalf("Preflight error = %v, Scanner error = %v for input %q", preErr, scErr, s)
			}
			if preErr != nil {
				if got, want := jsonshape.Classify(scErr), jsonshape.Classify(preErr); got != want {
					t.Fatalf("Classify mismatch for %q: got %q, want %q (scErr=%v, preErr=%v)", s, got, want, scErr, preErr)
				}
			} else {
				if scRes.Bytes != expectedRes.Bytes || scRes.Tokens != expectedRes.Tokens || scRes.MaxDepth != expectedRes.MaxDepth {
					t.Fatalf("Result mismatch for %q: got %+v, want %+v", s, scRes, expectedRes)
				}
			}

			// 3. Run Scanner 1-byte chunked
			scByte := jsonshape.NewScanner(context.Background(), limits)
			var byteErr error
			var byteRes jsonshape.Result
			for i := range data {
				if byteErr = scByte.Feed(data[i : i+1]); byteErr != nil {
					break
				}
			}
			if byteErr == nil {
				byteRes, byteErr = scByte.Finish()
			}

			if (preErr == nil) != (byteErr == nil) {
				t.Fatalf("Preflight error = %v, Byte-chunked Scanner error = %v for input %q", preErr, byteErr, s)
			}
			if preErr != nil {
				if got, want := jsonshape.Classify(byteErr), jsonshape.Classify(preErr); got != want {
					t.Fatalf("Byte-chunked Classify mismatch for %q: got %q, want %q", s, got, want)
				}
			} else {
				if byteRes.Bytes != expectedRes.Bytes || byteRes.Tokens != expectedRes.Tokens || byteRes.MaxDepth != expectedRes.MaxDepth {
					t.Fatalf("Byte-chunked Result mismatch for %q: got %+v, want %+v", s, byteRes, expectedRes)
				}
			}
		})
	}
}

type trackingBufferCloser struct {
	bytes.Buffer
	closed bool
}

func (t *trackingBufferCloser) Close() error {
	t.closed = true
	return nil
}

func TestScanner_StringWriterResolver(t *testing.T) {
	t.Parallel()

	jsonInput := `{"model":"gpt-4o","input":"Hello \n\t\u003cworld\u003e \uD83D\uDE80","other":"ignored"}`
	var capturedInput trackingBufferCloser

	resolver := jsonshape.StringWriterResolver(func(ctx jsonshape.StringContext) (io.Writer, error) {
		if ctx.TopLevel && ctx.Key == "input" {
			return &capturedInput, nil
		}
		return nil, nil
	})

	s := jsonshape.NewScanner(
		context.Background(), jsonshape.Limits{},
		jsonshape.WithStringWriterResolver(resolver),
	)

	// Feed in 3-byte chunks to stress boundaries across escapes and UTF-8
	data := []byte(jsonInput)
	chunkSize := 3
	for offset := 0; offset < len(data); offset += chunkSize {
		end := min(offset+chunkSize, len(data))
		if err := s.Feed(data[offset:end]); err != nil {
			t.Fatalf("Feed failed at offset %d: %v", offset, err)
		}
	}
	if _, err := s.Finish(); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	if !capturedInput.closed {
		t.Error("expected captured writer to be closed")
	}

	expectedString := "Hello \n\t<world> 🚀"
	if capturedInput.String() != expectedString {
		t.Errorf("captured string mismatch:\ngot:  %q\nwant: %q", capturedInput.String(), expectedString)
	}
}
