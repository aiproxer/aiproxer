package largebody_test

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func freezeBaseCall() *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("freeze hello")},
		}},
	}
}

//go:fix inline
func freezeIntPtr(v int) *int { return new(v) }

//go:fix inline
func freezeFloatPtr(v float64) *float64 { return new(v) }

func freezeModelExt(model string) map[string]json.RawMessage {
	return map[string]json.RawMessage{"openai.model": json.RawMessage(`"` + model + `"`)}
}

// TestIdentityWriter_StreamingStringEscapeParity tests that streaming string escaping
// produces byte-for-byte identical output to json.Marshal across all character ranges,
// HTML-sensitive characters, control chars, and split UTF-8 runes across chunk boundaries.
func TestIdentityWriter_StreamingStringEscapeParity(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		in   string
	}{
		{"simple ascii", "hello world 123"},
		{"quotes and slashes", `"hello \ "world" \\`},
		{"control chars", "\x00\x01\x02\x1f\n\r\t\b\f"},
		{"html sensitive", "<div>&\"'\\</div>"},
		{"unicode line separators", "\u2028 and \u2029"},
		{"emoji and accents", "🧪 café \\u0041 \n\t\r 🚀🌍"},
		{"tricky full fixture", "<div>&\"'\\</div>\u2028\u2029🧪 café \\u0041 \n\t\r"},
		{"huge string", strings.Repeat("abc <div>&\"'</div> \u2028\u2029 ", 1000)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			wantBytes, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("json.Marshal failed: %v", err)
			}
			// wantBytes has leading and trailing quotes: `"..."`
			wantEscaped := string(wantBytes[1 : len(wantBytes)-1])

			// Test streaming all at once
			var buf bytes.Buffer
			sw := largebody.NewStreamingEscapeWriter(&buf)
			if _, err := io.WriteString(sw, tc.in); err != nil {
				t.Fatalf("WriteString: %v", err)
			}
			if err := sw.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if got := buf.String(); got != wantEscaped {
				t.Fatalf("all-at-once mismatch:\ngot:  %q\nwant: %q", got, wantEscaped)
			}

			// Test chunked byte-by-byte (tests split UTF-8 rune handling)
			buf.Reset()
			sw = largebody.NewStreamingEscapeWriter(&buf)
			for i := 0; i < len(tc.in); i++ {
				if _, err := sw.Write([]byte{tc.in[i]}); err != nil {
					t.Fatalf("Write single byte %d: %v", i, err)
				}
			}
			if err := sw.Close(); err != nil {
				t.Fatalf("Close byte-by-byte: %v", err)
			}
			if got := buf.String(); got != wantEscaped {
				t.Fatalf("byte-by-byte mismatch:\ngot:  %q\nwant: %q", got, wantEscaped)
			}

			// Test small chunk sizes: 2, 3, 5, 7 bytes
			for _, chunkSize := range []int{2, 3, 5, 7} {
				buf.Reset()
				sw = largebody.NewStreamingEscapeWriter(&buf)
				inBytes := []byte(tc.in)
				for offset := 0; offset < len(inBytes); offset += chunkSize {
					end := min(offset+chunkSize, len(inBytes))
					if _, err := sw.Write(inBytes[offset:end]); err != nil {
						t.Fatalf("Write chunk [%d:%d]: %v", offset, end, err)
					}
				}
				if err := sw.Close(); err != nil {
					t.Fatalf("Close chunk size %d: %v", chunkSize, err)
				}
				if got := buf.String(); got != wantEscaped {
					t.Fatalf("chunk size %d mismatch:\ngot:  %q\nwant: %q", chunkSize, got, wantEscaped)
				}
			}
		})
	}
}

// TestIdentityWriter_ParityWithDiagFixtures tests that largebody.CanonicalCallIdentity
// matches diag.StableCallSum byte-for-byte across all frozen canonical fixtures.
func TestIdentityWriter_ParityWithDiagFixtures(t *testing.T) {
	t.Parallel()

	assertParity := func(t *testing.T, name string, call *aipapi.Call) {
		t.Helper()
		wantSum := diag.StableCallSum(call)
		digest := largebody.CanonicalCallIdentity(call)
		if digest.Sum() != wantSum {
			t.Fatalf("[%s] digest mismatch:\ngot:  %x\nwant: %x", name, digest.Sum(), wantSum)
		}

		wantToken := diag.StableCallToken(call)
		if gotToken := digest.Token(); gotToken != wantToken {
			t.Fatalf("[%s] token mismatch: got %q, want %q", name, gotToken, wantToken)
		}

		var explicitID string
		if call != nil {
			explicitID = call.ID
		}
		wantCallID := diag.StableCallID(call)
		if gotCallID := digest.CallID(explicitID); gotCallID != wantCallID {
			t.Fatalf("[%s] callID mismatch: got %q, want %q", name, gotCallID, wantCallID)
		}
	}

	t.Run("nil call", func(t *testing.T) {
		t.Parallel()
		assertParity(t, "nil call", nil)
	})

	t.Run("empty call", func(t *testing.T) {
		t.Parallel()
		assertParity(t, "empty call", &aipapi.Call{})
	})

	t.Run("base call", func(t *testing.T) {
		t.Parallel()
		assertParity(t, "base call", freezeBaseCall())
	})

	t.Run("call with explicit ID", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.ID = "call-explicit-1"
		assertParity(t, "call with explicit ID", c)
	})

	t.Run("call with whitespace ID", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.ID = "  call-whitespace-2  "
		assertParity(t, "call with whitespace ID", c)
	})

	t.Run("call with blank ID", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.ID = "   "
		assertParity(t, "call with blank ID", c)
	})

	t.Run("huge string fixture", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.Messages[0].Parts[0] = aipapi.TextPart(strings.Repeat("a", 1<<20))
		assertParity(t, "huge string fixture", c)
	})

	t.Run("tricky unicode and html escapes", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.Messages[0].Parts[0] = aipapi.TextPart("<div>&\"'\\</div>\u2028\u2029🧪 café \\u0041 \n\t\r")
		assertParity(t, "tricky unicode and html escapes", c)
	})

	t.Run("tools and tool choice", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.Tools = []aipapi.ToolDef{{Name: "get_weather", Description: "lookup"}}
		c.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}
		assertParity(t, "tools and tool choice", c)
	})

	t.Run("items shape", func(t *testing.T) {
		t.Parallel()
		c := &aipapi.Call{
			Route: aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
			Items: []aipapi.Item{{
				Kind:    aipapi.ItemKindMessage,
				ID:      "m1",
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "freeze hello"}},
			}},
		}
		assertParity(t, "items shape", c)
	})

	t.Run("model and route extensions", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.Route.Selector = "stub:gpt-4o"
		c.Extensions = freezeModelExt("gpt-4o-mini")
		assertParity(t, "model and route extensions", c)
	})

	t.Run("session fields", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.Session.AuthoritativeSessionID = "sess-1"
		c.Session.ClientSessionID = "client-1"
		c.Session.ALegID = "a-1"
		c.Session.ResumeToken = "tok-secret"
		c.Session.ContinuityKey = "ck-1"
		assertParity(t, "session fields", c)
	})

	t.Run("optional generation options", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.Options = aipapi.GenerationOptions{
			MaxOutputTokens: new(1024),
			Temperature:     new(0.7),
			TopP:            new(0.9),
			ReasoningEffort: "high",
			Verbosity:       aipapi.VerbosityLow,
		}
		c.PreviousResponseID = "resp_prev"
		c.PromptCacheKey = "cache_key_1"
		assertParity(t, "optional generation options", c)
	})
}

// TestIdentityWriter_StreamingMessages tests constructing the identity hash
// by streaming large message text parts in chunks without retaining the full string.
func TestIdentityWriter_StreamingMessages(t *testing.T) {
	t.Parallel()

	hugeText := strings.Repeat("Lorem ipsum dolor sit amet <div>&\"'</div> \u2028\u2029 🧪 ", 5000)

	call := &aipapi.Call{
		Route:      aipapi.RouteIntent{Selector: "stub:gpt-4o"},
		Extensions: freezeModelExt("gpt-4o"),
		Session: aipapi.SessionRef{
			ClientSessionID:        "client-sess-1",
			AuthoritativeSessionID: "auth-sess-1",
			ALegID:                 "aleg-1",
			ResumeToken:            "secret-token",
		},
		Options: aipapi.GenerationOptions{
			MaxOutputTokens: new(2048),
			Temperature:     new(0.7),
		},
		Messages: []aipapi.Message{
			{
				Role:  aipapi.RoleSystem,
				Parts: []aipapi.Part{aipapi.TextPart("You are a helpful assistant.")},
			},
			{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart(hugeText)},
			},
		},
	}

	wantSum := diag.StableCallSum(call)

	// Construct via CallIdentityWriter with streaming text chunks
	cfg := largebody.CallIdentityConfig{
		Session:    call.Session,
		Route:      call.Route,
		Extensions: call.Extensions,
		Options:    call.Options,
	}

	w, err := largebody.NewCallIdentityWriter(cfg)
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}

	// Add system message
	if err := w.AddMessage(call.Messages[0]); err != nil {
		t.Fatalf("AddMessage(system): %v", err)
	}

	// Stream user message with huge text
	msgWriter, err := w.BeginMessage(aipapi.RoleUser)
	if err != nil {
		t.Fatalf("BeginMessage: %v", err)
	}

	textWriter, err := msgWriter.BeginTextPart()
	if err != nil {
		t.Fatalf("BeginTextPart: %v", err)
	}

	// Write in 1024-byte chunks
	chunkSize := 1024
	inBytes := []byte(hugeText)
	for i := 0; i < len(inBytes); i += chunkSize {
		end := min(i+chunkSize, len(inBytes))
		if _, err := textWriter.Write(inBytes[i:end]); err != nil {
			t.Fatalf("Write chunk: %v", err)
		}
	}
	if err := textWriter.Close(); err != nil {
		t.Fatalf("textWriter.Close: %v", err)
	}
	if err := msgWriter.EndMessage(); err != nil {
		t.Fatalf("msgWriter.EndMessage: %v", err)
	}

	digest, err := w.Digest()
	if err != nil {
		t.Fatalf("w.Digest: %v", err)
	}

	if digest.Sum() != wantSum {
		t.Fatalf("streaming messages sum mismatch:\ngot:  %x\nwant: %x", digest.Sum(), wantSum)
	}
}

// TestIdentityWriter_StreamingItems tests constructing the identity hash
// by streaming large item content parts in chunks without retaining the full string.
func TestIdentityWriter_StreamingItems(t *testing.T) {
	t.Parallel()

	hugeText := strings.Repeat("item text payload <div>&\"'</div> \u2028\u2029 🧪 ", 5000)

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Items: []aipapi.Item{{
			Kind:    aipapi.ItemKindMessage,
			ID:      "m1",
			Status:  aipapi.ItemStatusCompleted,
			Role:    aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: hugeText}},
		}},
	}

	wantSum := diag.StableCallSum(call)

	cfg := largebody.CallIdentityConfig{
		Route: call.Route,
	}

	w, err := largebody.NewCallIdentityWriter(cfg)
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}

	itemWriter, err := w.BeginMessageItem("m1", aipapi.ItemStatusCompleted, aipapi.RoleUser, "")
	if err != nil {
		t.Fatalf("BeginMessageItem: %v", err)
	}

	textWriter, err := itemWriter.BeginTextContentPart()
	if err != nil {
		t.Fatalf("BeginTextContentPart: %v", err)
	}

	chunkSize := 512
	inBytes := []byte(hugeText)
	for i := 0; i < len(inBytes); i += chunkSize {
		end := min(i+chunkSize, len(inBytes))
		if _, err := textWriter.Write(inBytes[i:end]); err != nil {
			t.Fatalf("Write chunk: %v", err)
		}
	}
	if err := textWriter.Close(); err != nil {
		t.Fatalf("textWriter.Close: %v", err)
	}
	if err := itemWriter.EndItem(); err != nil {
		t.Fatalf("itemWriter.EndItem: %v", err)
	}

	digest, err := w.Digest()
	if err != nil {
		t.Fatalf("w.Digest: %v", err)
	}

	if digest.Sum() != wantSum {
		t.Fatalf("streaming items sum mismatch:\ngot:  %x\nwant: %x", digest.Sum(), wantSum)
	}
}

func TestIdentityWriter_BeginTextContentPart_EmptyText(t *testing.T) {
	t.Parallel()

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Items: []aipapi.Item{{
			Kind:    aipapi.ItemKindMessage,
			ID:      "m1",
			Status:  aipapi.ItemStatusCompleted,
			Role:    aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: ""}},
		}},
	}

	wantSum := diag.StableCallSum(call)

	w, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
		Route: call.Route,
	})
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}

	itemWriter, err := w.BeginMessageItem("m1", aipapi.ItemStatusCompleted, aipapi.RoleUser, "")
	if err != nil {
		t.Fatalf("BeginMessageItem: %v", err)
	}

	textWriter, err := itemWriter.BeginTextContentPart()
	if err != nil {
		t.Fatalf("BeginTextContentPart: %v", err)
	}
	if err := textWriter.Close(); err != nil {
		t.Fatalf("textWriter.Close: %v", err)
	}
	if err := itemWriter.EndItem(); err != nil {
		t.Fatalf("itemWriter.EndItem: %v", err)
	}

	digest, err := w.Digest()
	if err != nil {
		t.Fatalf("w.Digest: %v", err)
	}

	if digest.Sum() != wantSum {
		t.Fatalf("streaming items empty text sum mismatch:\ngot:  %x\nwant: %x", digest.Sum(), wantSum)
	}
}

// TestIdentityWriter_WriteCall_AllFixtures tests that CallIdentityWriter.WriteCall
// matches diag.StableCallSum on all fixtures.
func TestIdentityWriter_WriteCall_AllFixtures(t *testing.T) {
	t.Parallel()

	fixtures := []*aipapi.Call{
		nil,
		{},
		freezeBaseCall(),
		func() *aipapi.Call {
			c := freezeBaseCall()
			c.ID = "call-custom-123"
			return c
		}(),
		func() *aipapi.Call {
			c := freezeBaseCall()
			c.ID = "   "
			return c
		}(),
		func() *aipapi.Call {
			c := freezeBaseCall()
			c.Messages[0].Parts[0] = aipapi.TextPart("<div>&\"'\\</div>\u2028\u2029🧪 café \\u0041 \n\t\r")
			return c
		}(),
		func() *aipapi.Call {
			c := freezeBaseCall()
			c.Tools = []aipapi.ToolDef{{Name: "get_weather"}}
			c.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}
			return c
		}(),
		func() *aipapi.Call {
			return &aipapi.Call{
				Route: aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
				Items: []aipapi.Item{{
					Kind:    aipapi.ItemKindMessage,
					ID:      "m1",
					Status:  aipapi.ItemStatusCompleted,
					Role:    aipapi.RoleUser,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "freeze hello"}},
				}},
			}
		}(),
		func() *aipapi.Call {
			c := freezeBaseCall()
			c.Options = aipapi.GenerationOptions{
				MaxOutputTokens: new(512),
				Temperature:     new(0.5),
				ReasoningEffort: "low",
			}
			return c
		}(),
	}

	for i, call := range fixtures {
		wantSum := diag.StableCallSum(call)
		w, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{})
		if err != nil {
			t.Fatalf("fixture %d NewCallIdentityWriter: %v", i, err)
		}
		digest, err := w.WriteCall(call)
		if err != nil {
			t.Fatalf("fixture %d WriteCall: %v", i, err)
		}
		if digest.Sum() != wantSum {
			t.Fatalf("fixture %d sum mismatch:\ngot:  %x\nwant: %x", i, digest.Sum(), wantSum)
		}
	}
}

// TestIdentityWriter_ConvenienceFields tests that RouteSelector and SessionInput
// correctly populate their respective Call fields, and that a protocol-owned model
// extension supplied explicitly is preserved verbatim in the identity.
func TestIdentityWriter_ConvenienceFields(t *testing.T) {
	t.Parallel()

	cfg := largebody.CallIdentityConfig{
		RouteSelector: "stub:gpt-4o",
		// Protocol edge names its own model semantic extension; core injects none.
		Extensions: map[string]json.RawMessage{
			"openai.model": json.RawMessage(`"gpt-4o"`),
		},
		SessionInput: largebody.SessionInput{
			AuthoritativeSessionID: "auth-1",
			ClientSessionID:        "client-1",
			ALegID:                 "aleg-1",
			ResumeToken:            largebody.NewSensitiveString("secret-tok"),
		},
	}

	w, err := largebody.NewCallIdentityWriter(cfg)
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}
	digest, err := w.Digest()
	if err != nil {
		t.Fatalf("w.Digest: %v", err)
	}

	expectedCall := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:gpt-4o"},
		Extensions: map[string]json.RawMessage{
			"openai.model": json.RawMessage(`"gpt-4o"`),
		},
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: "auth-1",
			ClientSessionID:        "client-1",
			ALegID:                 "aleg-1",
			ResumeToken:            "secret-tok",
		},
	}
	wantSum := diag.StableCallSum(expectedCall)
	if digest.Sum() != wantSum {
		t.Fatalf("convenience fields sum mismatch:\ngot:  %x\nwant: %x", digest.Sum(), wantSum)
	}
}

// TestIdentityWriter_MultiPartAndMultiItemStreaming tests complex conversations
// with multiple parts and items.
func TestIdentityWriter_MultiPartAndMultiItemStreaming(t *testing.T) {
	t.Parallel()

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:gpt-4o"},
		Messages: []aipapi.Message{
			{
				Role: aipapi.RoleUser,
				Parts: []aipapi.Part{
					aipapi.TextPart("Part 1 text content"),
					aipapi.FilePart("file-ref-1", "application/pdf", "doc.pdf"),
					aipapi.TextPart("Part 3 text content"),
				},
			},
		},
	}

	wantSum := diag.StableCallSum(call)

	w, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
		Route: call.Route,
	})
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}

	mw, err := w.BeginMessage(aipapi.RoleUser)
	if err != nil {
		t.Fatalf("BeginMessage: %v", err)
	}

	// Stream part 1
	pw1, err := mw.BeginTextPart()
	if err != nil {
		t.Fatalf("BeginTextPart: %v", err)
	}
	if _, err := io.WriteString(pw1, "Part 1 text content"); err != nil {
		t.Fatalf("WriteString pw1: %v", err)
	}
	if err := pw1.Close(); err != nil {
		t.Fatalf("pw1.Close: %v", err)
	}

	// Add non-text part 2
	if err := mw.AddPart(call.Messages[0].Parts[1]); err != nil {
		t.Fatalf("AddPart 2: %v", err)
	}

	// Stream part 3
	pw3, err := mw.BeginTextPart()
	if err != nil {
		t.Fatalf("BeginTextPart 3: %v", err)
	}
	if _, err := io.WriteString(pw3, "Part 3 text content"); err != nil {
		t.Fatalf("WriteString pw3: %v", err)
	}
	if err := pw3.Close(); err != nil {
		t.Fatalf("pw3.Close: %v", err)
	}

	if err := mw.EndMessage(); err != nil {
		t.Fatalf("mw.EndMessage: %v", err)
	}

	digest, err := w.Digest()
	if err != nil {
		t.Fatalf("w.Digest: %v", err)
	}
	if digest.Sum() != wantSum {
		t.Fatalf("multi-part sum mismatch:\ngot:  %x\nwant: %x", digest.Sum(), wantSum)
	}
}
