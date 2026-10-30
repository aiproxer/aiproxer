package frontendpipe_test

import (
	"bytes"
	"errors"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
)

// TestBudgetBoundedWriter_ChargesSharedFactBudget pins the exact accept/decline
// arithmetic of the envelope fact budget: an accepted write appends every byte to
// the destination and charges the shared counter; a write that would cross the
// ceiling writes nothing, charges nothing, and declines with an error that wraps
// largebody.ErrSemanticFactBudgetExceeded.
func TestBudgetBoundedWriter_ChargesSharedFactBudget(t *testing.T) {
	t.Parallel()

	var total int64
	dst := &bytes.Buffer{}
	w := &frontendpipe.BudgetBoundedWriter{Buf: dst, Total: &total, MaxBudget: 10}

	n, err := w.Write([]byte("gpt-5"))
	if err != nil {
		t.Fatalf("first write: unexpected error: %v", err)
	}
	if n != 5 {
		t.Fatalf("first write n = %d, want 5", n)
	}
	if total != 5 {
		t.Fatalf("total after first write = %d, want 5", total)
	}
	if dst.String() != "gpt-5" {
		t.Fatalf("destination after first write = %q, want %q", dst.String(), "gpt-5")
	}

	// Exactly at the ceiling is accepted.
	n, err = w.Write([]byte("12345"))
	if err != nil {
		t.Fatalf("boundary write: unexpected error: %v", err)
	}
	if n != 5 {
		t.Fatalf("boundary write n = %d, want 5", n)
	}
	if total != 10 {
		t.Fatalf("total after boundary write = %d, want 10", total)
	}
	if dst.String() != "gpt-512345" {
		t.Fatalf("destination after boundary write = %q, want %q", dst.String(), "gpt-512345")
	}

	// One byte over the ceiling declines without side effects.
	n, err = w.Write([]byte("6"))
	if err == nil {
		t.Fatal("over-budget write: expected error, got nil")
	}
	if n != 0 {
		t.Fatalf("over-budget write n = %d, want 0", n)
	}
	if !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("over-budget write error %v does not wrap %v", err, largebody.ErrSemanticFactBudgetExceeded)
	}
	const wantMessage = "largebody: semantic-fact budget exceeded (optimization decline): " +
		"envelope fact budget exceeded (11 > 10)"
	if err.Error() != wantMessage {
		t.Fatalf("over-budget write message = %q, want %q", err.Error(), wantMessage)
	}
	if total != 10 {
		t.Fatalf("total after declined write = %d, want 10 (unchanged)", total)
	}
	if dst.String() != "gpt-512345" {
		t.Fatalf("destination after declined write = %q, want %q (unchanged)", dst.String(), "gpt-512345")
	}
}

// TestBudgetBoundedWriter_SharedTotalAcrossWriters pins that two writers charged
// against one counter see each other's spending: the second writer declines once
// the first consumed the whole budget.
func TestBudgetBoundedWriter_SharedTotalAcrossWriters(t *testing.T) {
	t.Parallel()

	var total int64
	first := &bytes.Buffer{}
	second := &bytes.Buffer{}

	w1 := &frontendpipe.BudgetBoundedWriter{Buf: first, Total: &total, MaxBudget: 8}
	if _, err := w1.Write([]byte("12345678")); err != nil {
		t.Fatalf("first writer: unexpected error: %v", err)
	}

	w2 := &frontendpipe.BudgetBoundedWriter{Buf: second, Total: &total, MaxBudget: 8}
	n, err := w2.Write([]byte("x"))
	if err == nil {
		t.Fatal("second writer: expected budget error, got nil")
	}
	if n != 0 {
		t.Fatalf("second writer n = %d, want 0", n)
	}
	const wantMessage = "largebody: semantic-fact budget exceeded (optimization decline): " +
		"envelope fact budget exceeded (9 > 8)"
	if err.Error() != wantMessage {
		t.Fatalf("second writer message = %q, want %q", err.Error(), wantMessage)
	}
	if second.Len() != 0 {
		t.Fatalf("second writer destination length = %d, want 0", second.Len())
	}
}

// TestChargeFactBudget_BeforeWriteOverflowSafe pins section 1.1-1.2 leaf
// mechanics: the shared counter uses remaining-budget arithmetic so a write
// that would cross the ceiling (including one that would overflow the counter)
// is declined before the buffer is touched, charges nothing, and wraps
// largebody.ErrSemanticFactBudgetExceeded. Exactly at the ceiling is accepted.
func TestChargeFactBudget_BeforeWriteOverflowSafe(t *testing.T) {
	t.Parallel()

	// Exact limit is accepted and charges the counter.
	var total int64
	dst := &bytes.Buffer{}
	w := &frontendpipe.BudgetBoundedWriter{Buf: dst, Total: &total, MaxBudget: 8}
	if _, err := w.Write([]byte("12345678")); err != nil {
		t.Fatalf("at-limit write: unexpected error: %v", err)
	}
	if total != 8 || dst.String() != "12345678" {
		t.Fatalf("at-limit state: total=%d dst=%q, want 8 %q", total, dst.String(), "12345678")
	}

	// Limit+1 declines before touching buffer or counter.
	if n, err := w.Write([]byte("x")); err == nil {
		t.Fatal("limit+1 write: expected budget error, got nil")
	} else if n != 0 {
		t.Fatalf("limit+1 write n = %d, want 0", n)
	} else if !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("limit+1 error %v does not wrap %v", err, largebody.ErrSemanticFactBudgetExceeded)
	}
	if total != 8 || dst.String() != "12345678" {
		t.Fatalf("declined state: total=%d dst=%q, want unchanged", total, dst.String())
	}

	// A write larger than the remaining budget cannot overflow the counter
	// into a false accept: total near MaxInt64 with a 10-byte write declines.
	var huge int64 = math.MaxInt64 - 5
	hugeDst := &bytes.Buffer{}
	hw := &frontendpipe.BudgetBoundedWriter{Buf: hugeDst, Total: &huge, MaxBudget: math.MaxInt64}
	if _, err := hw.Write([]byte("0123456789")); err == nil {
		t.Fatal("overflow write: expected budget error, got nil (counter wrapped)")
	} else if !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("overflow error %v does not wrap %v", err, largebody.ErrSemanticFactBudgetExceeded)
	}
	if huge != math.MaxInt64-5 || hugeDst.Len() != 0 {
		t.Fatalf("overflow state: total=%d dstLen=%d, want unchanged/empty", huge, hugeDst.Len())
	}

	// The cumulative counter never refunds buffer resets: Reset clears the
	// destination but the charged bytes stay spent.
	var cum int64
	cumDst := &bytes.Buffer{}
	cw := &frontendpipe.BudgetBoundedWriter{Buf: cumDst, Total: &cum, MaxBudget: 10}
	if _, err := cw.Write([]byte("12345")); err != nil {
		t.Fatalf("cumulative first write: %v", err)
	}
	cumDst.Reset()
	if _, err := cw.Write([]byte("12345")); err != nil {
		t.Fatalf("cumulative second write after Reset: %v", err)
	}
	if cum != 10 {
		t.Fatalf("cumulative total = %d, want 10 (Reset refunds nothing)", cum)
	}
	if _, err := cw.Write([]byte("x")); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("cumulative limit+1: expected budget error, got %v", err)
	}
}

// shortWriter accepts a bounded prefix of every write so counting can be pinned
// against the inner writer's reported count rather than the requested length.
type shortWriter struct {
	accept int
	err    error
	closed bool
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if w.accept >= len(p) {
		w.accept -= len(p)
		return len(p), nil
	}
	n := w.accept
	w.accept = 0
	return n, w.err
}

func (w *shortWriter) Close() error {
	w.closed = true
	return nil
}

// TestByteCountWriter_CountsAcceptedBytes pins that the counter advances by the
// inner writer's reported count, that an inner error is propagated untouched,
// that a nil counter is tolerated, and that Close reaches a closable inner.
func TestByteCountWriter_CountsAcceptedBytes(t *testing.T) {
	t.Parallel()

	inner := &shortWriter{accept: 7, err: errors.New("inner refused")}
	var count int64
	w := &frontendpipe.ByteCountWriter{Inner: inner, Count: &count}

	n, err := w.Write([]byte("0123456789"))
	if err == nil || err.Error() != "inner refused" {
		t.Fatalf("write error = %v, want %q", err, "inner refused")
	}
	if n != 7 {
		t.Fatalf("write n = %d, want 7 (inner-reported)", n)
	}
	if count != 7 {
		t.Fatalf("count = %d, want 7 (inner-reported)", count)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close: unexpected error: %v", err)
	}
	if !inner.closed {
		t.Fatal("close: inner writer was not closed")
	}

	// A nil counter must not panic and must not be written through.
	if _, err := (&frontendpipe.ByteCountWriter{Inner: io.Discard}).Write([]byte("abcd")); err != nil {
		t.Fatalf("nil-counter write: unexpected error: %v", err)
	}
	// A non-closable inner closes to nil.
	if err := (&frontendpipe.ByteCountWriter{Inner: io.Discard}).Close(); err != nil {
		t.Fatalf("non-closable inner close: unexpected error: %v", err)
	}
}

// scriptedReader hands out one scripted chunk per Read.
type scriptedReader struct {
	chunks []string
	calls  int
}

func (r *scriptedReader) Read(p []byte) (int, error) {
	if r.calls >= len(r.chunks) {
		return 0, io.EOF
	}
	chunk := r.chunks[r.calls]
	r.calls++
	return copy(p, chunk), nil
}

// TestChunkHookReader_HookSeesChunkBeforeCallerConsumes pins the hook contract the
// proof scanners rely on: the hook observes exactly the bytes the inner reader
// produced (p[:n]) while Read is still on the stack, it fires on a chunk that
// arrives together with an error, and it stays silent when no bytes were produced.
// The test reuses the caller's buffer immediately after Read to prove the hook has
// already seen the chunk bytes by then.
func TestChunkHookReader_HookSeesChunkBeforeCallerConsumes(t *testing.T) {
	t.Parallel()

	src := &scriptedReader{chunks: []string{"AAAA", "BBBB"}}
	var log []string
	var stillReading bool

	r := &frontendpipe.ChunkHookReader{
		R: src,
		OnRead: func(chunk []byte) {
			if !stillReading {
				t.Errorf("hook ran after Read returned (chunk %q)", string(chunk))
			}
			log = append(log, string(chunk))
		},
	}

	buf := make([]byte, 4)

	stillReading = true
	n, err := r.Read(buf)
	stillReading = false
	if err != nil {
		t.Fatalf("first read: unexpected error: %v", err)
	}
	if n != 4 || string(buf[:n]) != "AAAA" {
		t.Fatalf("first read = (%d, %q), want (4, %q)", n, string(buf[:n]), "AAAA")
	}
	// Clobber the caller's buffer before the next Read: a hook that observed its
	// chunk only after Read returned would record these bytes, not "AAAA".
	copy(buf, "ZZZZ")

	stillReading = true
	n, err = r.Read(buf)
	stillReading = false
	if err != nil {
		t.Fatalf("second read: unexpected error: %v", err)
	}
	if n != 4 || string(buf[:n]) != "BBBB" {
		t.Fatalf("second read = (%d, %q), want (4, %q)", n, string(buf[:n]), "BBBB")
	}

	stillReading = true
	_, err = r.Read(buf)
	stillReading = false
	if !errors.Is(err, io.EOF) {
		t.Fatalf("third read error = %v, want %v", err, io.EOF)
	}

	want := []string{"AAAA", "BBBB"}
	if len(log) != len(want) {
		t.Fatalf("hook log = %q, want %q", log, want)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Fatalf("hook log = %q, want %q", log, want)
		}
	}
}

// failingChunkReader returns one chunk together with a non-nil error.
type failingChunkReader struct {
	done bool
}

func (r *failingChunkReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	n := copy(p, "XY")
	return n, io.ErrUnexpectedEOF
}

// TestChunkHookReader_HookFiresOnChunkWithError pins that a chunk returned next to
// an error is still reported, and that a zero-byte read is never reported.
func TestChunkHookReader_HookFiresOnChunkWithError(t *testing.T) {
	t.Parallel()

	var seen []string
	r := &frontendpipe.ChunkHookReader{
		R:      &failingChunkReader{},
		OnRead: func(chunk []byte) { seen = append(seen, string(chunk)) },
	}

	buf := make([]byte, 8)
	n, err := r.Read(buf)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read error = %v, want %v", err, io.ErrUnexpectedEOF)
	}
	if n != 2 || string(buf[:n]) != "XY" {
		t.Fatalf("read = (%d, %q), want (2, %q)", n, string(buf[:n]), "XY")
	}

	if _, err := r.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("second read error = %v, want %v", err, io.EOF)
	}

	if len(seen) != 1 || seen[0] != "XY" {
		t.Fatalf("hook observations = %q, want [%q]", seen, "XY")
	}
}

// TestChunkHookReader_NilHookIsInert pins that an unset hook is a no-op that still
// forwards the inner reader's bytes.
func TestChunkHookReader_NilHookIsInert(t *testing.T) {
	t.Parallel()

	r := &frontendpipe.ChunkHookReader{R: strings.NewReader("abcdef")}
	buf := make([]byte, 3)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("read: unexpected error: %v", err)
	}
	if n != 3 || string(buf[:n]) != "abc" {
		t.Fatalf("read = (%d, %q), want (3, %q)", n, string(buf[:n]), "abc")
	}
}

// TestExtractNumberToken_FullyInsideCurrentChunk pins case 1: a token that starts
// and ends inside the current chunk is returned verbatim, including one that ends
// exactly on the chunk's last byte.
func TestExtractNumberToken_FullyInsideCurrentChunk(t *testing.T) {
	t.Parallel()

	// currentChunk covers replay bytes [300, 310) and holds "0.5,tru".
	current := []byte("0.5,tru")

	got, err := frontendpipe.ExtractNumberToken(
		jsonshape.Event{Key: "temperature", Offset: 300, Length: 3},
		current, nil, 300, "openailegacy",
	)
	if err != nil {
		t.Fatalf("in-chunk token: unexpected error: %v", err)
	}
	if got != "0.5" {
		t.Fatalf("in-chunk token = %q, want %q", got, "0.5")
	}

	// Ending exactly on the chunk boundary is still fully contained: the current
	// chunk covers [300, 307), so the token at [304, 307) is the last three bytes.
	got, err = frontendpipe.ExtractNumberToken(
		jsonshape.Event{Key: "temperature", Offset: 304, Length: 3},
		current, nil, 300, "openailegacy",
	)
	if err != nil {
		t.Fatalf("chunk-tail token: unexpected error: %v", err)
	}
	if got != "tru" {
		t.Fatalf("chunk-tail token = %q, want %q", got, "tru")
	}

	// One byte past the chunk end is not fully contained and declines.
	got, err = frontendpipe.ExtractNumberToken(
		jsonshape.Event{Key: "temperature", Offset: 307, Length: 3},
		current, nil, 300, "openailegacy",
	)
	if err == nil {
		t.Fatalf("past-chunk-end token: expected decline, got %q", got)
	}
	const wantPastEnd = "openailegacy: temperature spanning chunk boundary requires canonical decode"
	if err.Error() != wantPastEnd {
		t.Fatalf("past-chunk-end message = %q, want %q", err.Error(), wantPastEnd)
	}
}

// TestExtractNumberToken_StartsInPreviousChunkTail pins case 2: a token whose head
// is in the retained tail of the previous chunk is stitched from both pieces.
func TestExtractNumberToken_StartsInPreviousChunkTail(t *testing.T) {
	t.Parallel()

	// prevChunkTail holds the last 5 bytes of the previous chunk ("xx123"),
	// current chunk starts at absolute offset 100 and holds "456,".
	prevTail := []byte("xx123")
	current := []byte("456,")

	got, err := frontendpipe.ExtractNumberToken(
		jsonshape.Event{Key: "max_output_tokens", Offset: 97, Length: 6},
		current, prevTail, 100, "openresponses",
	)
	if err != nil {
		t.Fatalf("spanning token: unexpected error: %v", err)
	}
	if got != "123456" {
		t.Fatalf("spanning token = %q, want %q", got, "123456")
	}

	// The whole retained tail may be consumed: Offset == 95 uses all 5 tail bytes.
	got, err = frontendpipe.ExtractNumberToken(
		jsonshape.Event{Key: "max_output_tokens", Offset: 95, Length: 6},
		current, prevTail, 100, "openresponses",
	)
	if err != nil {
		t.Fatalf("full-tail token: unexpected error: %v", err)
	}
	if got != "xx1234" {
		t.Fatalf("full-tail token = %q, want %q", got, "xx1234")
	}
}

// TestExtractNumberToken_DeclinesUnrecoverableSpans pins case 3 (fail-closed): a
// token whose head predates the retained tail, or whose tail has not arrived yet,
// declines to the canonical decode path with the frontend's own label.
func TestExtractNumberToken_DeclinesUnrecoverableSpans(t *testing.T) {
	t.Parallel()

	// prevChunkTail keeps only 4 bytes, but the token needs 6 from the previous chunk.
	prevTail := []byte("xx12")
	current := []byte("3456")

	got, err := frontendpipe.ExtractNumberToken(
		jsonshape.Event{Key: "temperature", Offset: 94, Length: 6},
		current, prevTail, 100, "openailegacy",
	)
	if err == nil {
		t.Fatalf("truncated-tail token: expected decline, got %q", got)
	}
	if got != "" {
		t.Fatalf("truncated-tail token = %q, want empty string", got)
	}
	const wantTruncated = "openailegacy: temperature spanning chunk boundary requires canonical decode"
	if err.Error() != wantTruncated {
		t.Fatalf("truncated-tail message = %q, want %q", err.Error(), wantTruncated)
	}

	// The token's end is still beyond the current chunk: nothing to return yet.
	got, err = frontendpipe.ExtractNumberToken(
		jsonshape.Event{Key: "top_p", Offset: 98, Length: 40},
		current, prevTail, 100, "openresponses",
	)
	if err == nil {
		t.Fatalf("unfinished token: expected decline, got %q", got)
	}
	const wantUnfinished = "openresponses: top_p spanning chunk boundary requires canonical decode"
	if err.Error() != wantUnfinished {
		t.Fatalf("unfinished message = %q, want %q", err.Error(), wantUnfinished)
	}
}

// TestExtractNumberToken_DeclinesWithoutPreviousTail pins that the very first chunk
// (no previous tail retained yet) cannot recover a token that starts before it.
func TestExtractNumberToken_DeclinesWithoutPreviousTail(t *testing.T) {
	t.Parallel()

	got, err := frontendpipe.ExtractNumberToken(
		jsonshape.Event{Key: "max_tokens", Offset: -4, Length: 3},
		[]byte("123"), nil, 0, "openairesponses",
	)
	if err == nil {
		t.Fatalf("first-chunk token: expected decline, got %q", got)
	}
	const want = "openairesponses: max_tokens spanning chunk boundary requires canonical decode"
	if err.Error() != want {
		t.Fatalf("first-chunk message = %q, want %q", err.Error(), want)
	}
}
