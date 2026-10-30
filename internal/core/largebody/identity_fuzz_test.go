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

// FuzzStreamingEscapeWriter verifies that StreamingEscapeWriter produces byte-for-byte
// identical output to Go's json.Marshal for string content across arbitrary byte sequences,
// invalid UTF-8, HTML characters, control characters, and chunked feeds.
func FuzzStreamingEscapeWriter(f *testing.F) {
	seeds := [][]byte{
		[]byte(""),
		[]byte("hello world 123"),
		[]byte(`"quoted" and \escaped\`),
		[]byte("<div>&\"'</div>"),
		[]byte("\x00\x01\x1f\n\r\t\b\f"),
		[]byte("\u2028\u2029"),
		[]byte("🧪 café \\u0041 \n\t\r 🚀🌍"),
		[]byte("\x80"),
		[]byte("\xff\xfe\xfd"),
		[]byte("prefix\xe2\x82suffix"), // Incomplete UTF-8
		[]byte(strings.Repeat("abc <div>&\"'</div> \u2028\u2029 ", 32)),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// 1. Oracle: json.Marshal(string(data))
		str := string(data)
		wantBytes, err := json.Marshal(str)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}
		if len(wantBytes) < 2 || wantBytes[0] != '"' || wantBytes[len(wantBytes)-1] != '"' {
			t.Fatalf("unexpected json.Marshal output: %s", wantBytes)
		}
		wantEscaped := string(wantBytes[1 : len(wantBytes)-1])

		// 2. All-at-once write
		var bufAll bytes.Buffer
		swAll := largebody.NewStreamingEscapeWriter(&bufAll)
		if _, err := swAll.Write(data); err != nil {
			t.Fatalf("swAll.Write: %v", err)
		}
		if err := swAll.Close(); err != nil {
			t.Fatalf("swAll.Close: %v", err)
		}
		if got := bufAll.String(); got != wantEscaped {
			t.Fatalf("all-at-once mismatch:\ngot:  %q\nwant: %q", got, wantEscaped)
		}

		// 3. Chunked write (chunk size 1 to 7 bytes)
		chunkSizes := []int{1, 2, 5, 7}
		for _, cs := range chunkSizes {
			var bufChunk bytes.Buffer
			swChunk := largebody.NewStreamingEscapeWriter(&bufChunk)
			for offset := 0; offset < len(data); offset += cs {
				end := min(offset+cs, len(data))
				if _, err := swChunk.Write(data[offset:end]); err != nil {
					t.Fatalf("swChunk.Write [%d:%d]: %v", offset, end, err)
				}
			}
			if err := swChunk.Close(); err != nil {
				t.Fatalf("swChunk.Close: %v", err)
			}
			if got := bufChunk.String(); got != wantEscaped {
				t.Fatalf("chunk size %d mismatch:\ngot:  %q\nwant: %q", cs, got, wantEscaped)
			}
		}
	})
}

// FuzzCallIdentityWriter verifies that CallIdentityWriter produces the exact same
// canonical semantic digest, token, CallID, and Unix timestamp as diag.StableCallSum
// for arbitrary message payloads and chunked streaming deliveries.
func FuzzCallIdentityWriter(f *testing.F) {
	seeds := [][]byte{
		[]byte("hello"),
		[]byte(""),
		[]byte("test with <div>&\"'</div> and \u2028\u2029"),
		[]byte("emoji 🚀 and accents café"),
		[]byte("\x00\x01\x1f\n\r\t"),
		[]byte("\xff\xfe"),
		[]byte(strings.Repeat("prompt text ", 64)),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		text := string(data)
		call := &aipapi.Call{
			Route: aipapi.RouteIntent{Selector: "stub:fuzz"},
			Messages: []aipapi.Message{
				{
					Role:  aipapi.RoleUser,
					Parts: []aipapi.Part{aipapi.TextPart(text)},
				},
			},
		}

		wantSum := diag.StableCallSum(call)
		wantToken := diag.StableCallToken(call)
		wantCallID := diag.StableCallID(call)
		wantUnix := diag.StableUnix(call)

		// 1. WriteCall
		w, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{})
		if err != nil {
			t.Fatalf("NewCallIdentityWriter: %v", err)
		}
		digestWrite, err := w.WriteCall(call)
		if err != nil {
			t.Fatalf("WriteCall: %v", err)
		}
		if digestWrite.Sum() != wantSum {
			t.Fatalf("WriteCall sum mismatch:\ngot:  %x\nwant: %x", digestWrite.Sum(), wantSum)
		}
		if digestWrite.Token() != wantToken {
			t.Fatalf("WriteCall token mismatch: got %q, want %q", digestWrite.Token(), wantToken)
		}
		if digestWrite.CallID("") != wantCallID {
			t.Fatalf("WriteCall callID mismatch: got %q, want %q", digestWrite.CallID(""), wantCallID)
		}
		if digestWrite.Unix() != wantUnix {
			t.Fatalf("WriteCall unix mismatch: got %d, want %d", digestWrite.Unix(), wantUnix)
		}

		// 2. Streaming BeginMessage / BeginTextPart in small chunks
		wStream, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
			Route: call.Route,
		})
		if err != nil {
			t.Fatalf("NewCallIdentityWriter for streaming: %v", err)
		}
		mw, err := wStream.BeginMessage(aipapi.RoleUser)
		if err != nil {
			t.Fatalf("BeginMessage: %v", err)
		}
		tw, err := mw.BeginTextPart()
		if err != nil {
			t.Fatalf("BeginTextPart: %v", err)
		}

		// Write in 3-byte chunks to stress boundary crossings
		chunkSize := 3
		for offset := 0; offset < len(text); offset += chunkSize {
			end := min(offset+chunkSize, len(text))
			if _, err := io.WriteString(tw, text[offset:end]); err != nil {
				t.Fatalf("tw.WriteString [%d:%d]: %v", offset, end, err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatalf("tw.Close: %v", err)
		}
		if err := mw.EndMessage(); err != nil {
			t.Fatalf("mw.EndMessage: %v", err)
		}

		digestStream, err := wStream.Digest()
		if err != nil {
			t.Fatalf("wStream.Digest: %v", err)
		}
		if digestStream.Sum() != wantSum {
			t.Fatalf("streaming sum mismatch:\ngot:  %x\nwant: %x", digestStream.Sum(), wantSum)
		}
		if digestStream.Unix() != wantUnix {
			t.Fatalf("streaming unix mismatch: got %d, want %d", digestStream.Unix(), wantUnix)
		}

		// 3. Streaming BeginMessageItem / BeginTextContentPart in small chunks
		callItem := &aipapi.Call{
			Route: aipapi.RouteIntent{Selector: "stub:fuzz"},
			Items: []aipapi.Item{
				{
					Kind: aipapi.ItemKindMessage,
					Role: aipapi.RoleUser,
					Content: []aipapi.ContentPart{
						{Kind: aipapi.ContentPartText, Text: text},
					},
				},
			},
		}

		wantItemSum := diag.StableCallSum(callItem)
		wantItemToken := diag.StableCallToken(callItem)
		wantItemCallID := diag.StableCallID(callItem)
		wantItemUnix := diag.StableUnix(callItem)

		// 3a. WriteCall for item call
		wItemWrite, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{})
		if err != nil {
			t.Fatalf("NewCallIdentityWriter for item WriteCall: %v", err)
		}
		digestItemWrite, err := wItemWrite.WriteCall(callItem)
		if err != nil {
			t.Fatalf("WriteCall item mismatch: %v", err)
		}
		if digestItemWrite.Sum() != wantItemSum {
			t.Fatalf("WriteCall item sum mismatch:\ngot:  %x\nwant: %x", digestItemWrite.Sum(), wantItemSum)
		}
		if digestItemWrite.Token() != wantItemToken {
			t.Fatalf("WriteCall item token mismatch: got %q, want %q", digestItemWrite.Token(), wantItemToken)
		}
		if digestItemWrite.CallID("") != wantItemCallID {
			t.Fatalf("WriteCall item callID mismatch: got %q, want %q", digestItemWrite.CallID(""), wantItemCallID)
		}
		if digestItemWrite.Unix() != wantItemUnix {
			t.Fatalf("WriteCall item unix mismatch: got %d, want %d", digestItemWrite.Unix(), wantItemUnix)
		}

		// 3b. Streaming BeginMessageItem / BeginTextContentPart in small chunks
		wItemStream, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
			Route: callItem.Route,
		})
		if err != nil {
			t.Fatalf("NewCallIdentityWriter for item streaming: %v", err)
		}
		iw, err := wItemStream.BeginMessageItem("", "", aipapi.RoleUser, "")
		if err != nil {
			t.Fatalf("BeginMessageItem: %v", err)
		}
		itw, err := iw.BeginTextContentPart()
		if err != nil {
			t.Fatalf("BeginTextContentPart: %v", err)
		}

		for offset := 0; offset < len(text); offset += chunkSize {
			end := min(offset+chunkSize, len(text))
			if _, err := io.WriteString(itw, text[offset:end]); err != nil {
				t.Fatalf("itw.WriteString [%d:%d]: %v", offset, end, err)
			}
		}
		if err := itw.Close(); err != nil {
			t.Fatalf("itw.Close: %v", err)
		}
		if err := iw.EndItem(); err != nil {
			t.Fatalf("iw.EndItem: %v", err)
		}

		digestItemStream, err := wItemStream.Digest()
		if err != nil {
			t.Fatalf("wItemStream.Digest: %v", err)
		}
		if digestItemStream.Sum() != wantItemSum {
			t.Fatalf("streaming item sum mismatch:\ngot:  %x\nwant: %x", digestItemStream.Sum(), wantItemSum)
		}
		if digestItemStream.Unix() != wantItemUnix {
			t.Fatalf("streaming item unix mismatch: got %d, want %d", digestItemStream.Unix(), wantItemUnix)
		}
	})
}
