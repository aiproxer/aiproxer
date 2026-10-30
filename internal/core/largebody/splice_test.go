package largebody_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// scanModelSpan runs jsonshape.Scanner with TopLevelSpanTracker for "model"
// on data and returns the recorded model span.
func scanModelSpan(t *testing.T, data []byte) largebody.Span {
	t.Helper()
	tracker := jsonshape.NewTopLevelSpanTracker("model")
	sc := jsonshape.NewScanner(context.Background(), jsonshape.Limits{}, jsonshape.WithEventHandler(tracker))
	if err := sc.Feed(data); err != nil {
		t.Fatalf("scanner Feed failed: %v", err)
	}
	if _, err := sc.Finish(); err != nil {
		t.Fatalf("scanner Finish failed: %v", err)
	}
	s, ok := tracker.Span("model")
	if !ok {
		t.Fatalf("no top-level model span found in JSON: %s", string(data))
	}
	return largebody.Span{Offset: s.Offset, Length: s.Length}
}

// TestSpliceModelToken_SameShorterLonger verifies exact-span model token splicing
// when the replacement model is the same length, shorter, or longer than the original.
func TestSpliceModelToken_SameShorterLonger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		sourceJSON  string
		replacement string
	}{
		{
			name:        "same length model",
			sourceJSON:  `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hello"}]}`,
			replacement: "gpt-4b", // len("gpt-4b") == len("gpt-4o") == 6
		},
		{
			name:        "shorter model",
			sourceJSON:  `{"model": "gpt-4o-mini-2024-07-18", "messages": [{"role": "user", "content": "hi"}], "stream": true}`,
			replacement: "o1", // len 2 vs 22
		},
		{
			name:        "longer model",
			sourceJSON:  `{"model": "o1", "stream": true, "max_tokens": 100}`,
			replacement: "gpt-4o-2024-11-20-preview", // len 24 vs 2
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sourceBytes := []byte(tc.sourceJSON)
			span := scanModelSpan(t, sourceBytes)

			reader, err := largebody.SpliceModelToken(sourceBytes, span, tc.replacement)
			if err != nil {
				t.Fatalf("SpliceModelToken failed: %v", err)
			}
			defer func() { _ = reader.Close() }()

			gotBytes, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("reading spliced output failed: %v", err)
			}

			if int64(len(gotBytes)) != reader.RewrittenLength() {
				t.Fatalf("actual length %d != RewrittenLength() %d", len(gotBytes), reader.RewrittenLength())
			}

			// Parse resulting JSON and assert semantic equality.
			var parsed map[string]any
			if err := json.Unmarshal(gotBytes, &parsed); err != nil {
				t.Fatalf("spliced JSON is invalid (%v):\n%s", err, string(gotBytes))
			}

			if gotModel, ok := parsed["model"].(string); !ok || gotModel != tc.replacement {
				t.Fatalf("parsed model = %v, want %q", parsed["model"], tc.replacement)
			}

			// Verify source without model field parses with matching other fields.
			var origParsed map[string]any
			if err := json.Unmarshal(sourceBytes, &origParsed); err != nil {
				t.Fatalf("source JSON invalid: %v", err)
			}
			delete(origParsed, "model")
			delete(parsed, "model")
			origRemaining, _ := json.Marshal(origParsed)
			gotRemaining, _ := json.Marshal(parsed)
			if !bytes.Equal(origRemaining, gotRemaining) {
				t.Fatalf("non-model fields altered:\ngot:  %s\nwant: %s", string(gotRemaining), string(origRemaining))
			}
		})
	}
}

// TestSpliceModelToken_EscapedModel tests both:
// 1. Replacement model requiring JSON escaping (quotes, backslashes, newlines, tabs, unicode, emoji).
// 2. Source JSON having escaped characters in the original model token.
func TestSpliceModelToken_EscapedModel(t *testing.T) {
	t.Parallel()

	t.Run("escaped replacement model", func(t *testing.T) {
		t.Parallel()

		replacements := []string{
			`model"with"quotes`,
			`model\with\backslashes`,
			"model\nwith\nnewlines",
			"model\twith\ttabs",
			`model/with/slashes`,
			`modèle_français_🚀`,
			`"already-quoted-model"`,
		}

		sourceJSON := `{"model": "base-model", "temperature": 0.5}`
		sourceBytes := []byte(sourceJSON)
		span := scanModelSpan(t, sourceBytes)

		for _, repl := range replacements {
			t.Run(repl, func(t *testing.T) {
				t.Parallel()
				reader, err := largebody.SpliceModelToken(sourceBytes, span, repl)
				if err != nil {
					t.Fatalf("SpliceModelToken failed for replacement %q: %v", repl, err)
				}
				defer func() { _ = reader.Close() }()

				spliced, err := io.ReadAll(reader)
				if err != nil {
					t.Fatalf("ReadAll failed: %v", err)
				}

				if int64(len(spliced)) != reader.RewrittenLength() {
					t.Fatalf("length mismatch: got %d, RewrittenLength = %d", len(spliced), reader.RewrittenLength())
				}

				var parsed map[string]any
				if err := json.Unmarshal(spliced, &parsed); err != nil {
					t.Fatalf("spliced JSON invalid (%v):\n%s", err, string(spliced))
				}

				// If replacement was `"already-quoted-model"`, unmarshal decodes to already-quoted-model.
				wantModel := repl
				if len(repl) >= 2 && repl[0] == '"' && repl[len(repl)-1] == '"' {
					wantModel = repl[1 : len(repl)-1]
				}
				if gotModel, ok := parsed["model"].(string); !ok || gotModel != wantModel {
					t.Fatalf("parsed model = %q, want %q", parsed["model"], wantModel)
				}
			})
		}
	})

	t.Run("escaped original model in source JSON", func(t *testing.T) {
		t.Parallel()

		sourceJSON := `{"model": "gpt-\u0034o", "stream": false}`
		sourceBytes := []byte(sourceJSON)
		span := scanModelSpan(t, sourceBytes)

		// Verify scanner span covers the full escaped token `"gpt-\u0034o"`.
		rawSpan := string(sourceBytes[span.Offset : span.Offset+span.Length])
		if rawSpan != `"gpt-\u0034o"` {
			t.Fatalf("expected raw span %q, got %q", `"gpt-\u0034o"`, rawSpan)
		}

		reader, err := largebody.SpliceModelToken(sourceBytes, span, "claude-3-7-sonnet")
		if err != nil {
			t.Fatalf("SpliceModelToken failed: %v", err)
		}
		defer func() { _ = reader.Close() }()

		spliced, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}

		var parsed map[string]any
		if err := json.Unmarshal(spliced, &parsed); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, string(spliced))
		}
		if parsed["model"] != "claude-3-7-sonnet" {
			t.Fatalf("parsed model = %v, want claude-3-7-sonnet", parsed["model"])
		}
	})
}

// TestSpliceModelToken_LateModel verifies splicing when "model" is the last field
// after a large payload (e.g. >100KB prefix).
func TestSpliceModelToken_LateModel(t *testing.T) {
	t.Parallel()

	// Construct a large payload where "model" is at the very end.
	var sb strings.Builder
	sb.WriteString(`{"messages": [`)
	for i := range 200 {
		if i > 0 {
			sb.WriteString(`,`)
		}
		fmt.Fprintf(&sb, `{"role": "user", "content": "%s"}`, strings.Repeat("a", 500))
	}
	sb.WriteString(`], "temperature": 0.7, "model": "gpt-4o"}`)

	sourceBytes := []byte(sb.String())
	if len(sourceBytes) < 100_000 {
		t.Fatalf("expected source payload >= 100KB, got %d", len(sourceBytes))
	}

	span := scanModelSpan(t, sourceBytes)
	if span.Offset < 100_000 {
		t.Fatalf("expected late model span offset >= 100000, got %d", span.Offset)
	}

	reader, err := largebody.SpliceModelToken(sourceBytes, span, "o3-mini")
	if err != nil {
		t.Fatalf("SpliceModelToken failed: %v", err)
	}
	defer func() { _ = reader.Close() }()

	spliced, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if int64(len(spliced)) != reader.RewrittenLength() {
		t.Fatalf("length mismatch: got %d, RewrittenLength = %d", len(spliced), reader.RewrittenLength())
	}

	var parsed map[string]any
	if err := json.Unmarshal(spliced, &parsed); err != nil {
		t.Fatalf("spliced JSON is invalid: %v", err)
	}
	if parsed["model"] != "o3-mini" {
		t.Fatalf("parsed model = %v, want o3-mini", parsed["model"])
	}
}

// TestSpliceModelToken_NestedMisleadingText tests that nested "model" occurrences
// (in message content, nested objects, tool parameters) are ignored by the top-level
// scanner span and preserved completely unchanged after splice.
func TestSpliceModelToken_NestedMisleadingText(t *testing.T) {
	t.Parallel()

	sourceJSON := `{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": "Please do not use model: \"gpt-3.5\" or {\"model\": \"claude\"}."},
			{"role": "assistant", "model": "nested-model-attribute", "content": "Nested model key here."}
		],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "select_model",
					"parameters": {
						"type": "object",
						"properties": {
							"model": {"type": "string", "description": "The target model"}
						}
					}
				}
			}
		]
	}`

	sourceBytes := []byte(sourceJSON)
	span := scanModelSpan(t, sourceBytes)

	// Top-level span must be `"gpt-4o"`.
	rawSpan := string(sourceBytes[span.Offset : span.Offset+span.Length])
	if rawSpan != `"gpt-4o"` {
		t.Fatalf("expected top-level raw span %q, got %q", `"gpt-4o"`, rawSpan)
	}

	replacement := "gemini-2.5-flash"
	reader, err := largebody.SpliceModelToken(sourceBytes, span, replacement)
	if err != nil {
		t.Fatalf("SpliceModelToken failed: %v", err)
	}
	defer func() { _ = reader.Close() }()

	spliced, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(spliced, &parsed); err != nil {
		t.Fatalf("spliced JSON invalid: %v", err)
	}

	if parsed["model"] != replacement {
		t.Fatalf("top-level model = %v, want %q", parsed["model"], replacement)
	}

	// Verify nested model occurrences were preserved intact.
	msgs, ok := parsed["messages"].([]any)
	if !ok {
		t.Fatalf("parsed[\"messages\"] is %T, want []any", parsed["messages"])
	}
	m1, ok := msgs[1].(map[string]any)
	if !ok {
		t.Fatalf("msgs[1] is %T, want map[string]any", msgs[1])
	}
	if m1["model"] != "nested-model-attribute" {
		t.Fatalf("nested message model altered: %v", m1["model"])
	}

	tools, ok := parsed["tools"].([]any)
	if !ok {
		t.Fatalf("parsed[\"tools\"] is %T, want []any", parsed["tools"])
	}
	tool0, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tools[0] is %T, want map[string]any", tools[0])
	}
	fn, ok := tool0["function"].(map[string]any)
	if !ok {
		t.Fatalf("tool0[\"function\"] is %T, want map[string]any", tool0["function"])
	}
	params, ok := fn["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("fn[\"parameters\"] is %T, want map[string]any", fn["parameters"])
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("params[\"properties\"] is %T, want map[string]any", params["properties"])
	}
	modelProp, ok := props["model"].(map[string]any)
	if !ok {
		t.Fatalf("props[\"model\"] is %T, want map[string]any", props["model"])
	}
	if modelProp["type"] != "string" {
		t.Fatalf("tool parameter model altered: %v", modelProp)
	}
}

// TestSpliceModelToken_DuplicateSpansAndKeys tests:
// 1. Detection/handling of duplicate top-level "model" keys in source JSON (Requirement 9.5).
// 2. Rejection of duplicate/overlapping spans passed to ValidateModelSpans.
func TestSpliceModelToken_DuplicateSpansAndKeys(t *testing.T) {
	t.Parallel()

	t.Run("duplicate top-level model keys detected", func(t *testing.T) {
		t.Parallel()

		dupJSON := `{"model": "first-model", "temperature": 0.5, "model": "second-model"}`
		tracker := jsonshape.NewTopLevelSpanTracker("model")
		sc := jsonshape.NewScanner(context.Background(), jsonshape.Limits{}, jsonshape.WithEventHandler(tracker))
		if err := sc.Feed([]byte(dupJSON)); err != nil {
			t.Fatalf("Feed failed: %v", err)
		}
		if _, err := sc.Finish(); err != nil {
			t.Fatalf("Finish failed: %v", err)
		}

		if tracker.Count("model") != 2 {
			t.Fatalf("tracker.Count('model') = %d, want 2", tracker.Count("model"))
		}
		if !tracker.HasDuplicate("model") {
			t.Fatal("expected tracker.HasDuplicate('model') == true")
		}
	})

	t.Run("ValidateModelSpans rejects duplicate and overlapping spans", func(t *testing.T) {
		t.Parallel()

		sourceSize := int64(1000)
		s1 := largebody.Span{Offset: 10, Length: 8}
		s2 := largebody.Span{Offset: 10, Length: 8} // exact duplicate
		s3 := largebody.Span{Offset: 15, Length: 8} // overlapping

		if err := largebody.ValidateModelSpans(sourceSize, s1, s2); err == nil {
			t.Fatal("expected error on duplicate spans, got nil")
		}
		if err := largebody.ValidateModelSpans(sourceSize, s1, s3); err == nil {
			t.Fatal("expected error on overlapping spans, got nil")
		}

		// Non-overlapping valid spans
		s4 := largebody.Span{Offset: 50, Length: 10}
		if err := largebody.ValidateModelSpans(sourceSize, s1, s4); err != nil {
			t.Fatalf("unexpected error on valid distinct spans: %v", err)
		}
	})
}

// TestSpliceModelToken_InvalidSpans covers invalid span offsets, lengths, bounds,
// checked int64 overflow, negative/invalid source sizes, and empty replacement model.
func TestSpliceModelToken_InvalidSpans(t *testing.T) {
	t.Parallel()

	validSource := []byte(`{"model": "gpt-4o", "stream": true}`)
	sourceSize := int64(len(validSource))

	tests := []struct {
		name        string
		source      any
		span        largebody.Span
		replacement string
		wantErrSub  string
	}{
		{
			name:        "negative span offset",
			source:      validSource,
			span:        largebody.Span{Offset: -1, Length: 8},
			replacement: "o1",
			wantErrSub:  "span offset must be >= 0",
		},
		{
			name:        "negative span length",
			source:      validSource,
			span:        largebody.Span{Offset: 10, Length: -5},
			replacement: "o1",
			wantErrSub:  "span length must be >= 0",
		},
		{
			name:        "zero span length",
			source:      validSource,
			span:        largebody.Span{Offset: 10, Length: 0},
			replacement: "o1",
			wantErrSub:  "span length must be > 0",
		},
		{
			name:        "span length 1 cannot be quoted JSON string",
			source:      validSource,
			span:        largebody.Span{Offset: 10, Length: 1},
			replacement: "o1",
			wantErrSub:  "span length must be >= 2",
		},
		{
			name:        "span offset exceeds source size",
			source:      validSource,
			span:        largebody.Span{Offset: sourceSize + 10, Length: 5},
			replacement: "o1",
			wantErrSub:  "exceeds source size",
		},
		{
			name:        "span end exceeds source size",
			source:      validSource,
			span:        largebody.Span{Offset: sourceSize - 2, Length: 10},
			replacement: "o1",
			wantErrSub:  "exceeds source size",
		},
		{
			name:        "span end checked int64 overflow",
			source:      validSource,
			span:        largebody.Span{Offset: math.MaxInt64 - 5, Length: 10},
			replacement: "o1",
			wantErrSub:  "overflows int64",
		},
		{
			name:        "nil source",
			source:      nil,
			span:        largebody.Span{Offset: 10, Length: 8},
			replacement: "o1",
			wantErrSub:  "source must not be nil",
		},
		{
			name:        "empty replacement model",
			source:      validSource,
			span:        largebody.Span{Offset: 10, Length: 8},
			replacement: "",
			wantErrSub:  "replacement model must not be empty",
		},
		{
			name:        "whitespace-only replacement model",
			source:      validSource,
			span:        largebody.Span{Offset: 10, Length: 8},
			replacement: "   ",
			wantErrSub:  "replacement model must not be empty",
		},
		{
			name:        "span does not point to quoted string in source",
			source:      validSource,
			span:        largebody.Span{Offset: 0, Length: 7}, // points to `{"model`
			replacement: "o1",
			wantErrSub:  "quoted JSON string",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := largebody.SpliceModelToken(tc.source, tc.span, tc.replacement)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErrSub)
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("error %q does not contain expected substring %q", err.Error(), tc.wantErrSub)
			}
		})
	}
}

// TestSpliceModelToken_SourceTypes verifies that SpliceModelToken seamlessly supports
// various source abstractions: []byte, string, Source (memory and spill), io.ReaderAt,
// io.Reader, and RewritePlan.
func TestSpliceModelToken_SourceTypes(t *testing.T) {
	t.Parallel()

	sourceJSON := `{"model": "gpt-4o", "messages": [{"role": "user", "content": "ping"}]}`
	sourceBytes := []byte(sourceJSON)
	span := scanModelSpan(t, sourceBytes)
	replacement := "claude-3-5-sonnet"

	t.Run("source as []byte", func(t *testing.T) {
		t.Parallel()
		res, err := largebody.SpliceModelTokenBytes(sourceBytes, span, replacement)
		if err != nil {
			t.Fatalf("SpliceModelTokenBytes failed: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(res, &parsed); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if parsed["model"] != replacement {
			t.Fatalf("model = %v, want %q", parsed["model"], replacement)
		}
	})

	t.Run("source as string", func(t *testing.T) {
		t.Parallel()
		reader, err := largebody.SpliceModelToken(sourceJSON, span, replacement)
		if err != nil {
			t.Fatalf("SpliceModelToken failed: %v", err)
		}
		defer func() { _ = reader.Close() }()
		res, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(res, &parsed); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if parsed["model"] != replacement {
			t.Fatalf("model = %v, want %q", parsed["model"], replacement)
		}
	})

	t.Run("source as NewMemorySource", func(t *testing.T) {
		t.Parallel()
		src := largebody.NewMemorySource(sourceBytes)
		reader, err := largebody.SpliceModelToken(src, span, replacement)
		if err != nil {
			t.Fatalf("SpliceModelToken failed: %v", err)
		}
		defer func() { _ = reader.Close() }()
		res, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(res, &parsed); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if parsed["model"] != replacement {
			t.Fatalf("model = %v, want %q", parsed["model"], replacement)
		}
	})

	t.Run("source as SpillBuffer with file backing", func(t *testing.T) {
		t.Parallel()
		spoolDir := t.TempDir()
		// 0 memory spool forces file spill
		spillBuf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
			MemorySpoolBytes: 0,
			SpoolDir:         spoolDir,
		})
		if err != nil {
			t.Fatalf("NewSpillBuffer failed: %v", err)
		}
		defer func() { _ = spillBuf.Close() }()

		if _, err := spillBuf.Write(sourceBytes); err != nil {
			t.Fatalf("Write to spillBuf failed: %v", err)
		}
		compSource, err := spillBuf.Complete()
		if err != nil {
			t.Fatalf("Complete failed: %v", err)
		}
		defer func() { _ = compSource.Close() }()

		reader, err := largebody.SpliceModelToken(compSource, span, replacement)
		if err != nil {
			t.Fatalf("SpliceModelToken on CompletedSource failed: %v", err)
		}
		defer func() { _ = reader.Close() }()

		res, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(res, &parsed); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if parsed["model"] != replacement {
			t.Fatalf("model = %v, want %q", parsed["model"], replacement)
		}
	})

	t.Run("source as io.ReaderAt via SpliceModelTokenAt", func(t *testing.T) {
		t.Parallel()
		rAt := bytes.NewReader(sourceBytes)
		reader, err := largebody.SpliceModelTokenAt(rAt, int64(len(sourceBytes)), span, replacement)
		if err != nil {
			t.Fatalf("SpliceModelTokenAt failed: %v", err)
		}
		defer func() { _ = reader.Close() }()
		res, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(res, &parsed); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if parsed["model"] != replacement {
			t.Fatalf("model = %v, want %q", parsed["model"], replacement)
		}
	})

	t.Run("source as io.Reader via SpliceModelTokenReader", func(t *testing.T) {
		t.Parallel()
		rStream := bytes.NewReader(sourceBytes)
		reader, err := largebody.SpliceModelTokenReader(rStream, int64(len(sourceBytes)), span, replacement)
		if err != nil {
			t.Fatalf("SpliceModelTokenReader failed: %v", err)
		}
		defer func() { _ = reader.Close() }()
		res, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(res, &parsed); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if parsed["model"] != replacement {
			t.Fatalf("model = %v, want %q", parsed["model"], replacement)
		}
	})

	t.Run("source with RewritePlan via SpliceModelTokenPlan", func(t *testing.T) {
		t.Parallel()

		encodedToken, err := largebody.EncodeModelToken(replacement)
		if err != nil {
			t.Fatalf("EncodeModelToken failed: %v", err)
		}
		prefixLen := span.Offset
		suffixLen := int64(len(sourceBytes)) - (span.Offset + span.Length)
		expectedRewrittenLen, err := largebody.CheckedSpliceLength(prefixLen, int64(len(encodedToken)), suffixLen)
		if err != nil {
			t.Fatalf("CheckedSpliceLength failed: %v", err)
		}

		rewriteSem, err := largebody.NewModelTokenRewrite(span)
		if err != nil {
			t.Fatalf("NewModelTokenRewrite failed: %v", err)
		}

		plan := largebody.RewritePlan{
			Rewrite:          rewriteSem,
			ReplacementModel: replacement,
			RewrittenLength:  expectedRewrittenLen,
		}

		reader, err := largebody.SpliceModelTokenPlan(sourceBytes, plan, 1<<20)
		if err != nil {
			t.Fatalf("SpliceModelTokenPlan failed: %v", err)
		}
		defer func() { _ = reader.Close() }()

		res, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		if int64(len(res)) != expectedRewrittenLen {
			t.Fatalf("expected len %d, got %d", expectedRewrittenLen, len(res))
		}

		// Test length mismatch in plan is rejected
		badPlan := plan
		badPlan.RewrittenLength = expectedRewrittenLen + 100
		if _, err := largebody.SpliceModelTokenPlan(sourceBytes, badPlan, 1<<20); err == nil {
			t.Fatal("expected error on rewritten length mismatch in plan, got nil")
		}
	})
}

// TestSpliceModelToken_StreamingChunkVariations tests reading with varied chunk sizes
// (including 1 byte, prime sizes, large buffers), WriteTo, and unexpected EOF.
func TestSpliceModelToken_StreamingChunkVariations(t *testing.T) {
	t.Parallel()

	sourceJSON := `{"model": "gpt-4o", "temperature": 0.2, "messages": [{"role": "user", "content": "chunk test"}]}`
	sourceBytes := []byte(sourceJSON)
	span := scanModelSpan(t, sourceBytes)
	replacement := "claude-3-opus"

	// Baseline full read
	expectedBytes, err := largebody.SpliceModelTokenBytes(sourceBytes, span, replacement)
	if err != nil {
		t.Fatalf("SpliceModelTokenBytes failed: %v", err)
	}

	chunkSizes := []int{1, 2, 3, 7, 13, 31, 64, 128, 1024, 4096}
	for _, chunk := range chunkSizes {
		t.Run(fmt.Sprintf("buffer_size_%d", chunk), func(t *testing.T) {
			t.Parallel()
			reader, err := largebody.SpliceModelToken(sourceBytes, span, replacement)
			if err != nil {
				t.Fatalf("SpliceModelToken failed: %v", err)
			}
			defer func() { _ = reader.Close() }()

			var buf bytes.Buffer
			p := make([]byte, chunk)
			for {
				n, err := reader.Read(p)
				if n > 0 {
					buf.Write(p[:n])
				}
				if err != nil {
					if errors.Is(err, io.EOF) {
						break
					}
					t.Fatalf("read error with chunk size %d: %v", chunk, err)
				}
			}

			if !bytes.Equal(buf.Bytes(), expectedBytes) {
				t.Fatalf("content mismatch for chunk size %d:\ngot:  %s\nwant: %s",
					chunk, buf.String(), string(expectedBytes))
			}
		})
	}

	t.Run("WriteTo implementation", func(t *testing.T) {
		t.Parallel()
		reader, err := largebody.SpliceModelToken(sourceBytes, span, replacement)
		if err != nil {
			t.Fatalf("SpliceModelToken failed: %v", err)
		}
		defer func() { _ = reader.Close() }()

		var buf bytes.Buffer
		n, err := reader.WriteTo(&buf)
		if err != nil {
			t.Fatalf("WriteTo failed: %v", err)
		}
		if n != reader.RewrittenLength() {
			t.Fatalf("WriteTo returned %d, want %d", n, reader.RewrittenLength())
		}
		if !bytes.Equal(buf.Bytes(), expectedBytes) {
			t.Fatalf("WriteTo content mismatch")
		}
	})

	t.Run("unexpected EOF on truncated source", func(t *testing.T) {
		t.Parallel()
		// Source claims length of full JSON, but reader only has prefix
		truncatedReader := bytes.NewReader(sourceBytes[:span.Offset+2])
		reader, err := largebody.SpliceModelTokenReader(truncatedReader, int64(len(sourceBytes)), span, replacement)
		if err != nil {
			t.Fatalf("SpliceModelTokenReader failed: %v", err)
		}
		defer func() { _ = reader.Close() }()

		_, err = io.ReadAll(reader)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("expected io.ErrUnexpectedEOF on truncated source, got: %v", err)
		}
	})
}
