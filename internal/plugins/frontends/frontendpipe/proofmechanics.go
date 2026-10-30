package frontendpipe

import (
	"bytes"
	"fmt"
	"io"

	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// This file owns the protocol-neutral leaf mechanics a frontend proof pass needs
// while streaming replay bytes through jsonshape.Scanner: a fact-budgeted string
// sink, a pass-through byte counter, a chunk-observing reader, and the recovery of
// a raw number token that straddles a chunk boundary. Each certified frontend
// profile supplies its own body-key, whitespace, ordered-item and decline policy;
// nothing here decides policy.

// BudgetBoundedWriter appends envelope string facts into Buf while charging every
// accepted byte against the semantic-fact budget. Total is the caller-owned
// counter shared by every budgeted writer of one proof compilation, so the
// budget covers the whole envelope rather than a single field.
//
// A write that would push the shared counter past MaxBudget is declined before
// Buf is touched: it writes nothing, charges nothing, and returns an error
// wrapping largebody.ErrSemanticFactBudgetExceeded so the caller falls back to the
// canonical decode path.
type BudgetBoundedWriter struct {
	// Buf receives the accepted bytes.
	Buf *bytes.Buffer
	// Total is the shared running fact-byte counter for this proof compilation.
	Total *int64
	// MaxBudget is the semantic-fact budget in bytes.
	MaxBudget int64
}

// Write implements io.Writer.
func (w *BudgetBoundedWriter) Write(p []byte) (int, error) {
	if err := ChargeFactBudget(w.Total, int64(len(p)), w.MaxBudget); err != nil {
		return 0, err
	}
	return w.Buf.Write(p)
}

// ChargeFactBudget reserves n bytes against the shared proof-compilation
// counter before the caller retains them, using remaining-budget arithmetic so
// an oversized write cannot overflow the counter. On success the counter
// advances by n and the caller may retain the bytes; on failure nothing is
// charged and the returned error wraps
// largebody.ErrSemanticFactBudgetExceeded so callers fall back to the canonical
// decode path. The counter is cumulative for the scan: buffer resets and
// whitespace normalization never refund bytes.
func ChargeFactBudget(total *int64, n int64, maxBudget int64) error {
	if n < 0 {
		return fmt.Errorf("%w: negative fact charge %d",
			largebody.ErrSemanticFactBudgetExceeded, n)
	}
	if *total > maxBudget {
		return fmt.Errorf("%w: envelope fact budget exceeded (%d > %d)",
			largebody.ErrSemanticFactBudgetExceeded, *total, maxBudget)
	}
	remaining := maxBudget - *total
	if n > remaining {
		attempted := maxBudget
		if delta := n - remaining; delta > 0 && attempted < int64(^uint64(0)>>1)-delta {
			attempted += delta
		}
		return fmt.Errorf("%w: envelope fact budget exceeded (%d > %d)",
			largebody.ErrSemanticFactBudgetExceeded, attempted, maxBudget)
	}
	*total += n
	return nil
}

// ByteCountWriter forwards writes to Inner and accumulates the number of bytes
// Inner actually accepted into Count, which stays nil-safe for callers that do not
// track a count.
type ByteCountWriter struct {
	// Inner is the destination writer.
	Inner io.Writer
	// Count accumulates the accepted byte count when non-nil.
	Count *int64
}

// Write implements io.Writer.
func (w *ByteCountWriter) Write(p []byte) (int, error) {
	n, err := w.Inner.Write(p)
	if n > 0 && w.Count != nil {
		*w.Count += int64(n)
	}
	return n, err
}

// Close closes Inner when it is an io.Closer and is a no-op otherwise.
func (w *ByteCountWriter) Close() error {
	if c, ok := w.Inner.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// ChunkHookReader wraps a replay source and hands each chunk it produced to
// OnRead before the caller sees those bytes. Proof passes rely on that ordering:
// the scanner handler resolves offsets against the chunk currently being consumed,
// so the hook must have published it while Read is still on the stack. A hook that
// ran after the consumer had the bytes would resolve against stale state.
//
// OnRead receives exactly the p[:n] slice the inner reader filled, fires for a
// chunk that arrived together with an error, and stays silent when no bytes were
// produced.
type ChunkHookReader struct {
	// R is the underlying replay source.
	R io.Reader
	// OnRead observes each chunk before Read returns.
	OnRead func(chunk []byte)
}

// Read implements io.Reader.
func (c *ChunkHookReader) Read(p []byte) (int, error) {
	n, err := c.R.Read(p)
	if n > 0 && c.OnRead != nil {
		c.OnRead(p[:n])
	}
	return n, err
}

// ExtractNumberToken recovers the raw text of the JSON number token the shape
// scanner reported for event e.
//
// currentChunk is the bounded chunk being consumed, chunkStartOffset is its
// absolute offset in the replay stream, and prevChunkTail is the retained tail of
// the previous chunk. A token fully inside currentChunk, or one that starts inside
// prevChunkTail and finishes inside currentChunk, is returned verbatim.
//
// Every other shape is fail-closed: its opening bytes are already gone, or its
// trailing bytes have not arrived. ExtractNumberToken then declines with
// declineLabel (the frontend name that owns the body key) so the caller routes the
// request to the canonical decode path instead of guessing.
func ExtractNumberToken(
	e jsonshape.Event,
	currentChunk, prevChunkTail []byte,
	chunkStartOffset int64,
	declineLabel string,
) (string, error) {
	// 1. Fully contained in current chunk
	if e.Offset >= chunkStartOffset && e.Offset+e.Length <= chunkStartOffset+int64(len(currentChunk)) {
		start := e.Offset - chunkStartOffset
		return string(currentChunk[start : start+e.Length]), nil
	}
	// 2. Starts in prevChunkTail and completes in currentChunk
	if e.Offset < chunkStartOffset && e.Offset+e.Length <= chunkStartOffset+int64(len(currentChunk)) {
		prevLen := chunkStartOffset - e.Offset
		if prevLen > 0 && prevLen <= int64(len(prevChunkTail)) {
			startInPrev := int64(len(prevChunkTail)) - prevLen
			part1 := prevChunkTail[startInPrev:]
			part2 := currentChunk[:e.Offset+e.Length-chunkStartOffset]
			return string(part1) + string(part2), nil
		}
	}
	// 3. Spanning shape not fully contained in buffers -> fail-closed decline
	return "", fmt.Errorf("%s: %s spanning chunk boundary requires canonical decode", declineLabel, e.Key)
}
