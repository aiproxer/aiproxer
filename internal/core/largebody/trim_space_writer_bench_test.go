package largebody_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// referenceTrimSpaceWriter re-implements the streaming trim contract with the
// original forward scan. TrimSpaceWriter must produce identical output, errors
// and trimmed-byte counts for any sequence of chunk writes, including chunks
// that split multi-byte runes or carry invalid UTF-8.
type referenceTrimSpaceWriter struct {
	out                        *bytes.Buffer
	maxTrailingWhitespaceBytes int
	seenNonSpace               bool
	trailingWS, pendingRune    []byte
	trimmedBytes               int64
}

func (w *referenceTrimSpaceWriter) Write(p []byte) (int, error) {
	origLen := len(p)
	if origLen == 0 {
		return 0, nil
	}
	maxCap := w.maxTrailingWhitespaceBytes
	if maxCap <= 0 {
		maxCap = largebody.DefaultMaxTrailingWhitespaceBytes
	}
	data := p
	if len(w.pendingRune) > 0 {
		data = append(w.pendingRune, p...)
		w.pendingRune = nil
	}
	if !w.seenNonSpace {
		for len(data) > 0 {
			r, size := utf8.DecodeRune(data)
			if r == utf8.RuneError && size == 1 && !utf8.FullRune(data) {
				w.pendingRune = append(w.pendingRune[:0], data...)
				return origLen, nil
			}
			if !unicode.IsSpace(r) {
				w.seenNonSpace = true
				break
			}
			data = data[size:]
		}
		if len(data) == 0 {
			return origLen, nil
		}
	}
	if !utf8.FullRune(data) {
		_, size := utf8.DecodeLastRune(data)
		if size > 0 && !utf8.FullRune(data[len(data)-size:]) {
			w.pendingRune = append(w.pendingRune[:0], data[len(data)-size:]...)
			data = data[:len(data)-size]
		}
	}
	lastNonSpaceEnd := -1
	idx := 0
	for sub := data; len(sub) > 0; {
		r, size := utf8.DecodeRune(sub)
		if !unicode.IsSpace(r) {
			lastNonSpaceEnd = idx + size
		}
		idx += size
		sub = sub[size:]
	}
	if lastNonSpaceEnd == -1 {
		if len(w.trailingWS)+len(data) > maxCap {
			return 0, fmt.Errorf("%w (%d > %d)", largebody.ErrSemanticFactBudgetExceeded, len(w.trailingWS)+len(data), maxCap)
		}
		w.trailingWS = append(w.trailingWS, data...)
		return origLen, nil
	}
	if len(w.trailingWS) > 0 {
		w.out.Write(w.trailingWS)
		w.trimmedBytes += int64(len(w.trailingWS))
		w.trailingWS = w.trailingWS[:0]
	}
	if toWrite := data[:lastNonSpaceEnd]; len(toWrite) > 0 {
		w.out.Write(toWrite)
		w.trimmedBytes += int64(len(toWrite))
	}
	if lastNonSpaceEnd < len(data) {
		tail := data[lastNonSpaceEnd:]
		if len(w.trailingWS)+len(tail) > maxCap {
			return 0, fmt.Errorf("%w (%d > %d)", largebody.ErrSemanticFactBudgetExceeded, len(w.trailingWS)+len(tail), maxCap)
		}
		w.trailingWS = append(w.trailingWS, tail...)
	}
	return origLen, nil
}

// TestTrimSpaceWriter_MatchesForwardScanReference is the behaviour-preservation
// proof for the backward scan: for random payloads and chunk boundaries the
// writer emits the same bytes and trimmed-byte count as the forward scan, and
// fails at the same trailing-whitespace budget.
func TestTrimSpaceWriter_MatchesForwardScanReference(t *testing.T) {
	alphabet := []string{
		"a", "Z", "0", " ", "\t", "\n", "\r", "\v", "\f", "\u00a0", "\u0085",
		"\u2028", "\u3000", "\u00e9", "\u20ac", "\U0001F600", "\xff", "\xc3", "\xe2\x82",
	}
	rng := rand.New(rand.NewSource(20261026))
	for caseIdx := range 600 {
		var payload strings.Builder
		for i := 0; i < rng.Intn(30); i++ {
			payload.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		body := payload.String()

		var chunks [][]byte
		for offset := 0; offset < len(body); {
			n := 1 + rng.Intn(6)
			if offset+n > len(body) {
				n = len(body) - offset
			}
			chunks = append(chunks, []byte(body[offset:offset+n]))
			offset += n
		}

		var gotOut, wantOut bytes.Buffer
		got := largebody.NewTrimSpaceWriter(&gotOut)
		want := &referenceTrimSpaceWriter{out: &wantOut}
		for _, chunk := range chunks {
			gotN, gotErr := got.Write(chunk)
			wantN, wantErr := want.Write(chunk)
			if gotN != wantN || (gotErr != nil) != (wantErr != nil) {
				t.Fatalf("case %d (%q): write = (%d, %v), want (%d, %v)", caseIdx, body, gotN, gotErr, wantN, wantErr)
			}
			if gotErr != nil {
				if !errors.Is(gotErr, largebody.ErrSemanticFactBudgetExceeded) {
					t.Fatalf("case %d: unexpected error %v", caseIdx, gotErr)
				}
				break
			}
		}
		if got.TrimmedBytes() != want.trimmedBytes {
			t.Fatalf("case %d (%q): trimmed bytes = %d, want %d", caseIdx, body, got.TrimmedBytes(), want.trimmedBytes)
		}
		if gotOut.String() != wantOut.String() {
			t.Fatalf("case %d (%q): output = %q, want %q", caseIdx, body, gotOut.String(), wantOut.String())
		}
	}
}

// benchTrimSpaceChunks streams body through a TrimSpaceWriter in 32 KiB chunks,
// mirroring the wire-to-canonical string hand-off for large bodies.
func benchTrimSpaceChunks(b *testing.B, body []byte) {
	b.Helper()
	const chunkSize = 32 << 10
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		w := largebody.NewTrimSpaceWriter(io.Discard)
		for offset := 0; offset < len(body); offset += chunkSize {
			end := min(offset+chunkSize, len(body))
			if _, err := w.Write(body[offset:end]); err != nil {
				b.Fatalf("write: %v", err)
			}
		}
		if err := w.Close(); err != nil {
			b.Fatalf("close: %v", err)
		}
	}
}

// BenchmarkTrimSpaceWriter_PlainPayload is the realistic large-body case: a
// multi-megabyte string value with no trailing whitespace.
func BenchmarkTrimSpaceWriter_PlainPayload(b *testing.B) {
	benchTrimSpaceChunks(b, bytes.Repeat([]byte("a"), 4<<20))
}

// BenchmarkTrimSpaceWriter_TrailingWhitespace keeps trailing whitespace that
// must be trimmed from the emitted payload.
func BenchmarkTrimSpaceWriter_TrailingWhitespace(b *testing.B) {
	benchTrimSpaceChunks(b, append(bytes.Repeat([]byte("a"), 4<<20), []byte(" \t\r\n \u3000")...))
}
