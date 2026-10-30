package largebody_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// =============================================================================
// Remediation Plan Phase 1: Streaming Proof Core Differential Tests
//
// Requirements:
// - One-pass replay stream through jsonshape.Scanner + SHA-256 + CallIdentityWriter.
// - Fixed buffers, zero io.ReadAll.
// - Streaming digest byte-identical to CanonicalCallIdentity and diag.StableCallSum
//   across 15.1/16.1 corpora, 1 MiB large strings, reasoning items, and late models.
// - Whole-message-marshal mutation test catches regression to whole-message json.Marshal.
// =============================================================================

//go:fix inline
func float64Ptr(f float64) *float64 { return new(f) }

func TestStreamingProofDifferential_OpenAIResponsesCorpus(t *testing.T) {
	testCases := []struct {
		name           string
		body           string
		headers        http.Header
		isSingleString bool
	}{
		{
			name:           "simple string input",
			body:           `{"model":"gpt-4o","input":"Hello, how are you today?"}`,
			isSingleString: true,
		},
		{
			name: "message array with system instruction",
			body: `{
				"model": "gpt-4o",
				"instructions": "You are a specialized code translation assistant.",
				"input": [
					{"type": "message", "role": "user", "content": "Translate this to Go"}
				]
			}`,
		},
		{
			name: "multi-turn conversation with options",
			body: `{
				"model": "gpt-4o",
				"temperature": 0.3,
				"max_output_tokens": 1024,
				"input": [
					{"type": "message", "role": "user", "content": "Query 1"},
					{"type": "message", "role": "assistant", "content": "Answer 1"},
					{"type": "message", "role": "user", "content": "Query 2"}
				]
			}`,
		},
		{
			name: "html safety and escapes in input",
			body: `{
				"model": "gpt-4o",
				"input": "<div>Escaped & <script>\"test\"</script></div> \u2028\u2029 \n\t\r"
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Decode canonically via frontend decoder oracle
			opts := openairesponses.DecodeOptions{
				RouteSelector: "stub:gpt-4o",
				Headers:       tc.headers,
			}
			decoded, err := openairesponses.DecodeCreateRequest([]byte(tc.body), opts)
			if err != nil {
				t.Fatalf("canonical decode failed: %v", err)
			}
			wantDigest := largebody.CanonicalCallIdentity(decoded.Call)
			wantSum := diag.StableCallSum(decoded.Call)
			if wantDigest.Sum() != wantSum {
				t.Fatalf("oracle mismatch: CanonicalCallIdentity %x != diag.StableCallSum %x", wantDigest.Sum(), wantSum)
			}

			// 1. Streaming message path via AddMessage (chunked string parts, no whole-message marshal)
			w, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
				ExplicitID:         decoded.Call.ID,
				Session:            decoded.Call.Session,
				Route:              decoded.Call.Route,
				Instructions:       decoded.Call.Instructions,
				PreviousResponseID: decoded.Call.PreviousResponseID,
				PromptCacheKey:     decoded.Call.PromptCacheKey,
				SemanticExtensions: decoded.Call.SemanticExtensions,
				Tools:              decoded.Call.Tools,
				ToolChoice:         decoded.Call.ToolChoice,
				Options:            decoded.Call.Options,
				Extensions:         decoded.Call.Extensions,
			})
			if err != nil {
				t.Fatalf("NewCallIdentityWriter: %v", err)
			}
			if err := w.StartMessages(); err != nil {
				t.Fatalf("StartMessages: %v", err)
			}
			for _, msg := range decoded.Call.Messages {
				if err := w.AddMessage(msg); err != nil {
					t.Fatalf("AddMessage: %v", err)
				}
			}
			gotDigest, err := w.Digest()
			if err != nil {
				t.Fatalf("Digest: %v", err)
			}
			if gotDigest.Sum() != wantDigest.Sum() {
				t.Fatalf("[%s] streaming message digest mismatch:\ngot:  %x\nwant: %x", tc.name, gotDigest.Sum(), wantDigest.Sum())
			}

			// 2. For single-string input payloads, also verify CompileStreamingProof helper.
			// The protocol edge supplies the wire-field interpretation; core applies none.
			if tc.isSingleString {
				cfg := largebody.StreamingProofConfig{
					Reader:               strings.NewReader(tc.body),
					MaxBytes:             int64(len(tc.body) + 1024),
					ChunkSize:            32, // stress small chunk boundaries
					StringWriterResolver: openairesponses.InputStringResolver,
					CallIdentityConfig: largebody.CallIdentityConfig{
						ExplicitID:         decoded.Call.ID,
						Session:            decoded.Call.Session,
						Route:              decoded.Call.Route,
						Instructions:       decoded.Call.Instructions,
						PreviousResponseID: decoded.Call.PreviousResponseID,
						PromptCacheKey:     decoded.Call.PromptCacheKey,
						SemanticExtensions: decoded.Call.SemanticExtensions,
						Tools:              decoded.Call.Tools,
						ToolChoice:         decoded.Call.ToolChoice,
						Options:            decoded.Call.Options,
						Extensions:         decoded.Call.Extensions,
					},
				}
				res, err := largebody.CompileStreamingProof(context.Background(), cfg)
				if err != nil {
					t.Fatalf("CompileStreamingProof failed: %v", err)
				}

				// Verify body hash matches exact SHA-256 of raw body
				expectedBodyHash := sha256.Sum256([]byte(tc.body))
				if res.BodyHash != expectedBodyHash {
					t.Fatalf("BodyHash mismatch:\ngot:  %x\nwant: %x", res.BodyHash, expectedBodyHash)
				}

				if res.Digest.Sum() != wantDigest.Sum() {
					t.Fatalf("[%s] CompileStreamingProof digest mismatch:\ngot:  %x\nwant: %x", tc.name, res.Digest.Sum(), wantDigest.Sum())
				}
			}
		})
	}
}

func TestStreamingProofDifferential_OpenAIChatCorpus(t *testing.T) {
	testCases := []struct {
		name    string
		body    string
		headers http.Header
	}{
		{
			name: "simple user message",
			body: `{"model":"gpt-4o","messages":[{"role":"user","content":"Hello chat world"}]}`,
		},
		{
			name: "system and user messages with options",
			body: `{
				"model": "gpt-4o",
				"messages": [
					{"role": "system", "content": "You are a helpful assistant."},
					{"role": "user", "content": "Tell me a joke"}
				],
				"temperature": 0.7,
				"top_p": 0.95
			}`,
		},
		{
			name: "escapes and unicode in chat",
			body: `{
				"model": "gpt-4o",
				"messages": [
					{"role": "user", "content": "Quotes: \"hello\", HTML: <b>&amp;</b>, Emoji: 🚀"}
				]
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			opts := openailegacy.DecodeOptions{
				RouteSelector: "stub:gpt-4o",
				Headers:       tc.headers,
			}
			decoded, err := openailegacy.DecodeChatRequest([]byte(tc.body), opts)
			if err != nil {
				t.Fatalf("canonical decode failed: %v", err)
			}
			wantDigest := largebody.CanonicalCallIdentity(decoded.Call)

			w, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
				ExplicitID:         decoded.Call.ID,
				Session:            decoded.Call.Session,
				Route:              decoded.Call.Route,
				Instructions:       decoded.Call.Instructions,
				PreviousResponseID: decoded.Call.PreviousResponseID,
				PromptCacheKey:     decoded.Call.PromptCacheKey,
				SemanticExtensions: decoded.Call.SemanticExtensions,
				Tools:              decoded.Call.Tools,
				ToolChoice:         decoded.Call.ToolChoice,
				Options:            decoded.Call.Options,
				Extensions:         decoded.Call.Extensions,
			})
			if err != nil {
				t.Fatalf("NewCallIdentityWriter: %v", err)
			}
			if err := w.StartMessages(); err != nil {
				t.Fatalf("StartMessages: %v", err)
			}
			for _, msg := range decoded.Call.Messages {
				if err := w.AddMessage(msg); err != nil {
					t.Fatalf("AddMessage: %v", err)
				}
			}
			gotDigest, err := w.Digest()
			if err != nil {
				t.Fatalf("Digest: %v", err)
			}

			if gotDigest.Sum() != wantDigest.Sum() {
				t.Fatalf("[%s] chat digest mismatch:\ngot:  %x\nwant: %x", tc.name, gotDigest.Sum(), wantDigest.Sum())
			}
		})
	}
}

func TestStreamingProofDifferential_OpenResponsesCorpus(t *testing.T) {
	testCases := []struct {
		name string
		body string
	}{
		{
			name: "single string input mapped to message item",
			body: `{"model":"gpt-4o","input":"Hello OpenResponses world"}`,
		},
		{
			name: "explicit item with content parts",
			body: `{
				"model": "gpt-4o",
				"input": [
					{
						"type": "message",
						"role": "user",
						"content": [
							{"type": "text", "text": "First content part."},
							{"type": "text", "text": "Second content part with <div>&\"'</div> \u2028\u2029 🧪"}
						]
					}
				]
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			opts := openresponses.DecodeCreateOptions{
				RouteSelector: "stub:gpt-4o",
			}
			decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), []byte(tc.body), opts)
			if err != nil {
				t.Fatalf("canonical decode failed: %v", err)
			}
			wantDigest := largebody.CanonicalCallIdentity(decoded.Call)

			w, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
				Route:      decoded.Call.Route,
				Options:    decoded.Call.Options,
				Extensions: decoded.Call.Extensions,
			})
			if err != nil {
				t.Fatalf("NewCallIdentityWriter: %v", err)
			}
			if err := w.StartItems(); err != nil {
				t.Fatalf("StartItems: %v", err)
			}
			for _, item := range decoded.Call.Items {
				if err := w.AddItem(item); err != nil {
					t.Fatalf("AddItem: %v", err)
				}
			}
			gotDigest, err := w.Digest()
			if err != nil {
				t.Fatalf("Digest: %v", err)
			}

			if gotDigest.Sum() != wantDigest.Sum() {
				t.Fatalf("[%s] openresponses digest mismatch:\ngot:  %x\nwant: %x", tc.name, gotDigest.Sum(), wantDigest.Sum())
			}
		})
	}
}

func TestStreamingProofDifferential_LateModelAndEscapes(t *testing.T) {
	// JSON payload where "model" appears AFTER "input" and "messages", with unicode/surrogate escapes
	body := `{"input":"Prefix \u003cdiv\u003e & \uD83D\uDE80 \\n\\t string","temperature":0.7,"model":"gpt-4o-late"}`

	// Canonical decode
	opts := openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-late",
	}
	decoded, err := openairesponses.DecodeCreateRequest([]byte(body), opts)
	if err != nil {
		t.Fatalf("canonical decode failed: %v", err)
	}
	wantDigest := largebody.CanonicalCallIdentity(decoded.Call)

	// Extensions known prior to late model discovery (e.g. flavor)
	initialExt := make(map[string]json.RawMessage)
	for k, v := range decoded.Call.Extensions {
		if k != "openairesponses.model" {
			initialExt[k] = v
		}
	}

	// Stream through ProcessReplayChunks with jsonshape.Scanner tracking model and streaming input
	idWriter, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
		ExplicitID:         decoded.Call.ID,
		Session:            decoded.Call.Session,
		Route:              decoded.Call.Route,
		Instructions:       decoded.Call.Instructions,
		PreviousResponseID: decoded.Call.PreviousResponseID,
		PromptCacheKey:     decoded.Call.PromptCacheKey,
		SemanticExtensions: decoded.Call.SemanticExtensions,
		Tools:              decoded.Call.Tools,
		ToolChoice:         decoded.Call.ToolChoice,
		Options:            decoded.Call.Options,
		Extensions:         initialExt,
	})
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}

	resolver := jsonshape.StringWriterResolver(func(sctx jsonshape.StringContext) (io.Writer, error) {
		if sctx.TopLevel && sctx.Key == "input" {
			mw, err := idWriter.BeginMessage(aipapi.RoleUser)
			if err != nil {
				return nil, err
			}
			pw, err := mw.BeginTextPart()
			if err != nil {
				return nil, err
			}
			return pw, nil
		}
		return nil, nil
	})

	tracker := jsonshape.NewTopLevelSpanTracker("model")
	scanner := jsonshape.NewScanner(
		context.Background(),
		jsonshape.Limits{RejectDuplicateNames: true, MaxBytes: int64(len(body) + 512)},
		jsonshape.WithEventHandler(tracker),
		jsonshape.WithStringWriterResolver(resolver),
	)

	replayCfg := largebody.ReplayChunkReaderConfig{
		Reader:    strings.NewReader(body),
		MaxBytes:  int64(len(body) + 512),
		ChunkSize: 16,
		Scanner:   scanner,
		Hasher:    sha256.New(),
	}
	res, err := largebody.ProcessReplayChunks(replayCfg)
	if err != nil {
		t.Fatalf("ProcessReplayChunks failed: %v", err)
	}

	expectedBodyHash := sha256.Sum256([]byte(body))
	if res.BodyHash != expectedBodyHash {
		t.Fatalf("BodyHash mismatch:\ngot:  %x\nwant: %x", res.BodyHash, expectedBodyHash)
	}

	// Verify model span recorded late
	span, found := tracker.Span("model")
	if !found {
		t.Fatal("model span not found")
	}
	extractedModel := body[span.Offset : span.Offset+span.Length]
	if extractedModel != `"gpt-4o-late"` {
		t.Fatalf("extracted model mismatch: got %q, want %q", extractedModel, `"gpt-4o-late"`)
	}

	// Apply late model to identity writer
	if err := idWriter.SetClientModel("gpt-4o-late", "openairesponses.model"); err != nil {
		t.Fatalf("SetClientModel: %v", err)
	}

	gotDigest, err := idWriter.Digest()
	if err != nil {
		t.Fatalf("idWriter.Digest: %v", err)
	}

	if gotDigest.Sum() != wantDigest.Sum() {
		t.Fatalf("late model digest mismatch:\ngot:  %x\nwant: %x", gotDigest.Sum(), wantDigest.Sum())
	}
}

func TestStreamingProofDifferential_LargeStringPayload(t *testing.T) {
	// Build 1 MiB large string with varied unicode and JSON escape characters
	pattern := "Lorem ipsum dolor sit amet, <div>&\"'</div> \u2028\u2029 🧪 café \\u0041 \n\t\r 🚀🌍 "
	repeats := (1024 * 1024) / len(pattern)
	var sb strings.Builder
	for range repeats {
		sb.WriteString(pattern)
	}
	largeText := sb.String()

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:large-string"},
		Messages: []aipapi.Message{
			{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart(largeText)},
			},
		},
		Options: aipapi.GenerationOptions{
			ReasoningEffort: "low",
		},
	}

	wantDigest := largebody.CanonicalCallIdentity(call)

	// Stream via CallIdentityWriter with AddMessage (which must stream internally without whole-message marshal)
	w, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
		Route:   call.Route,
		Options: call.Options,
	})
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}

	if err := w.StartMessages(); err != nil {
		t.Fatalf("StartMessages: %v", err)
	}
	if err := w.AddMessage(call.Messages[0]); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	gotDigest, err := w.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	if gotDigest.Sum() != wantDigest.Sum() {
		t.Fatalf("1 MiB large string digest mismatch:\ngot:  %x\nwant: %x", gotDigest.Sum(), wantDigest.Sum())
	}
}

func TestStreamingProofDifferential_ReasoningItems(t *testing.T) {
	// Items with reasoning parts and dialects
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:reasoning-route"},
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindMessage,
				ID:     "item_reasoning_1",
				Status: aipapi.ItemStatusCompleted,
				Role:   aipapi.RoleAssistant,
				Phase:  aipapi.AssistantPhaseFinalAnswer,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "Final response text."},
				},
				Reasoning: &aipapi.ReasoningItem{
					Reasoning: &aipapi.ReasoningPart{
						Dialect:   aipapi.ReasoningDialectOpenAIResponsesItemV1,
						Text:      "Internal chain of thought reasoning",
						Signature: "sig_abc123",
					},
				},
			},
			{
				Kind:   aipapi.ItemKindMessage,
				ID:     "item_user_2",
				Status: aipapi.ItemStatusCompleted,
				Role:   aipapi.RoleUser,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "Follow up question with & < > \" ' escapes"},
				},
			},
		},
	}

	wantDigest := largebody.CanonicalCallIdentity(call)

	w, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
		Route: call.Route,
	})
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}

	if err := w.StartItems(); err != nil {
		t.Fatalf("StartItems: %v", err)
	}
	for _, item := range call.Items {
		if err := w.AddItem(item); err != nil {
			t.Fatalf("AddItem(%s): %v", item.ID, err)
		}
	}
	gotDigest, err := w.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	if gotDigest.Sum() != wantDigest.Sum() {
		t.Fatalf("reasoning item digest mismatch:\ngot:  %x\nwant: %x", gotDigest.Sum(), wantDigest.Sum())
	}
}

func TestWholeMessageMarshalMutation(t *testing.T) {
	// Mutation test:
	// Verify that AddMessage streams in bounded buffers (< 64 KiB allocated during AddMessage)
	// and does NOT perform whole-message json.Marshal (which allocates > 1 MiB for a 1 MiB message).
	// When the whole-message path is forced on a writer instance, it MUST allocate > 1 MiB.

	// Construct 1 MiB text message
	hugeText := strings.Repeat("0123456789abcdef", 64*1024) // 1 MiB
	msg := aipapi.Message{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{aipapi.TextPart(hugeText)},
	}

	// 1. Streaming behavior (production path): allocated heap during AddMessage must be bounded
	runtime.GC()
	var m1, m2 runtime.MemStats
	runtime.ReadMemStats(&m1)

	w, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
		Route: aipapi.RouteIntent{Selector: "stub:route"},
	})
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}
	if err := w.StartMessages(); err != nil {
		t.Fatalf("StartMessages: %v", err)
	}
	if err := w.AddMessage(msg); err != nil {
		t.Fatalf("AddMessage streaming: %v", err)
	}
	runtime.ReadMemStats(&m2)
	streamingAllocs := m2.TotalAlloc - m1.TotalAlloc

	// 2. Mutated behavior (old whole-message marshal path), forced per instance
	runtime.GC()
	var m3, m4 runtime.MemStats
	runtime.ReadMemStats(&m3)

	wMutated, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
		Route: aipapi.RouteIntent{Selector: "stub:route"},
	})
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}
	largebody.ForceWholeMessageMarshalForTest(wMutated)
	if err := wMutated.StartMessages(); err != nil {
		t.Fatalf("StartMessages: %v", err)
	}
	if err := wMutated.AddMessage(msg); err != nil {
		t.Fatalf("AddMessage mutated: %v", err)
	}
	runtime.ReadMemStats(&m4)
	mutatedAllocs := m4.TotalAlloc - m3.TotalAlloc

	t.Logf("Memory allocations during AddMessage(1 MiB): streaming=%d bytes, whole-message-marshal=%d bytes",
		streamingAllocs, mutatedAllocs)

	// Whole-message marshal allocates at least the 1 MiB JSON representation
	if mutatedAllocs < 1024*1024 {
		t.Fatalf("expected mutated whole-message marshal to allocate at least 1 MiB, got %d bytes", mutatedAllocs)
	}

	// Streaming AddMessage must not allocate proportional to the 1 MiB text (must be well under 128 KiB)
	if streamingAllocs > 128*1024 {
		t.Fatalf("streaming AddMessage allocated %d bytes (exceeded 128 KiB bound), whole-message marshal regression detected", streamingAllocs)
	}
}

func TestTrimSpaceWriter_NormalTrimming(t *testing.T) {
	var buf bytes.Buffer
	w := largebody.NewTrimSpaceWriter(&buf)

	chunks := [][]byte{
		[]byte("   \n\t  "),
		[]byte("hello "),
		[]byte("world"),
		[]byte("   \r\n  "),
	}
	for _, c := range chunks {
		if _, err := w.Write(c); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := buf.String()
	want := "hello world"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if w.TrimmedBytes() != int64(len(want)) {
		t.Fatalf("TrimmedBytes: got %d, want %d", w.TrimmedBytes(), len(want))
	}
}

func TestTrimSpaceWriter_TrailingWhitespaceBounded(t *testing.T) {
	var buf bytes.Buffer
	w := largebody.NewTrimSpaceWriter(&buf)

	// Write non-whitespace start
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("Write hello: %v", err)
	}

	// Write whitespace within budget (e.g. 32 KiB)
	withinBudget := bytes.Repeat([]byte(" "), 32*1024)
	if _, err := w.Write(withinBudget); err != nil {
		t.Fatalf("Write within budget: %v", err)
	}

	// Write additional whitespace exceeding the 64 KiB default budget
	exceedingBudget := bytes.Repeat([]byte(" "), 40*1024)
	_, err := w.Write(exceedingBudget)
	if err == nil {
		t.Fatal("expected error for trailing whitespace exceeding budget, got nil")
	}
	if !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("expected ErrSemanticFactBudgetExceeded, got: %v", err)
	}
}
