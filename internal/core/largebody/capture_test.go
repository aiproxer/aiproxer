package largebody_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// nonRewindableReader wraps an io.Reader and panics if any attempt to
// rewind, seek, or restart from offset 0 is made (Requirement 20.8).
type nonRewindableReader struct {
	r         io.Reader
	bytesRead int64
	closed    bool
}

func (n *nonRewindableReader) Read(p []byte) (int, error) {
	if n.closed {
		return 0, io.ErrClosedPipe
	}
	readCount, err := n.r.Read(p)
	atomic.AddInt64(&n.bytesRead, int64(readCount))
	return readCount, err
}

func (n *nonRewindableReader) Close() error {
	n.closed = true
	if rc, ok := n.r.(io.Closer); ok {
		return rc.Close()
	}
	return nil
}

// faultyReader injects an error after readLimit bytes.
type faultyReader struct {
	r         io.Reader
	readLimit int64
	bytesRead int64
	injectErr error
}

func (f *faultyReader) Read(p []byte) (int, error) {
	if f.bytesRead >= f.readLimit {
		return 0, f.injectErr
	}
	avail := f.readLimit - f.bytesRead
	toRead := p
	if int64(len(p)) > avail {
		toRead = p[:avail]
	}
	n, err := f.r.Read(toRead)
	f.bytesRead += int64(n)
	if f.bytesRead >= f.readLimit {
		if err == nil || errors.Is(err, io.EOF) {
			return n, f.injectErr
		}
	}
	return n, err
}

func (f *faultyReader) Close() error {
	if rc, ok := f.r.(io.Closer); ok {
		return rc.Close()
	}
	return nil
}

// helper to check if an error is *http.MaxBytesError
func isMaxBytesError(err error) bool {
	if err == nil {
		return false
	}
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}

// TestContinuationReader_ByteForByteParity_MemoryPrefix verifies that a continuation
// reader composing an in-memory prefix, unwritten suffix, and remaining client body
// yields output identical byte-for-byte to a direct read (Requirements 1.4, 20.7, 20.8).
func TestContinuationReader_ByteForByteParity_MemoryPrefix(t *testing.T) {
	t.Parallel()

	payload := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz!@#$%^&*()")
	split1 := 20 // retained prefix
	split2 := 35 // unwritten suffix is [split1:split2]
	// remaining is [split2:]

	spoolDir := t.TempDir()
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 1024,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	// Write retained prefix
	if _, err := buf.Write(payload[:split1]); err != nil {
		t.Fatalf("buf.Write prefix: %v", err)
	}

	unwrittenSuffix := payload[split1:split2]
	remainingBody := io.NopCloser(bytes.NewReader(payload[split2:]))

	cr, err := largebody.NewCaptureReader(largebody.CaptureReaderConfig{
		Spill:           buf,
		UnwrittenSuffix: unwrittenSuffix,
		Remaining:       remainingBody,
		MaxBytes:        int64(len(payload) + 100),
	})
	if err != nil {
		t.Fatalf("NewCaptureReader: %v", err)
	}
	defer func() { _ = cr.Close() }()

	got, err := io.ReadAll(cr)
	if err != nil {
		t.Fatalf("io.ReadAll continuation: %v", err)
	}

	if !bytes.Equal(got, payload) {
		t.Fatalf("continuation mismatch:\ngot:  %q\nwant: %q", got, payload)
	}
}

// TestContinuationReader_WithSpillToFile verifies byte-for-byte continuation when
// the retained prefix has already spilled to disk (Requirements 20.7, 20.8, 20.9).
func TestContinuationReader_WithSpillToFile(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 32, // 32 bytes memory, excess spills to disk
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}

	// 100 bytes total
	payload := make([]byte, 100)
	for i := range payload {
		payload[i] = byte(i)
	}

	split1 := 70 // 32 in mem, 38 on disk
	split2 := 85 // 15 bytes unwritten suffix
	// 15 bytes remaining

	if _, err := buf.Write(payload[:split1]); err != nil {
		t.Fatalf("buf.Write: %v", err)
	}
	if !buf.HasSpilled() {
		t.Fatal("expected buffer to have spilled to disk")
	}
	spillPath := buf.FilePath()

	unwrittenSuffix := payload[split1:split2]
	remainingBody := io.NopCloser(bytes.NewReader(payload[split2:]))

	cr, err := largebody.NewCaptureReader(largebody.CaptureReaderConfig{
		Spill:           buf,
		UnwrittenSuffix: unwrittenSuffix,
		Remaining:       remainingBody,
		MaxBytes:        int64(len(payload)),
	})
	if err != nil {
		t.Fatalf("NewCaptureReader: %v", err)
	}

	got, err := io.ReadAll(cr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("byte mismatch on spilled continuation: got len %d, want %d", len(got), len(payload))
	}

	// Closing cr should clean up the spill file
	if err := cr.Close(); err != nil {
		t.Fatalf("cr.Close: %v", err)
	}

	if _, err := os.Stat(spillPath); !os.IsNotExist(err) {
		t.Fatalf("spill file %q still exists after cr.Close()", spillPath)
	}
}

// TestContinuationReader_RandomChunkingAndFaults generates random payloads, splits them
// across random chunk boundaries, injects faults (reservation decline, short write),
// and verifies that the continuation reader reconstructs the exact payload byte-for-byte
// compared to a direct canonical read (Requirements 1.4, 20.7, 20.8).
func TestContinuationReader_RandomChunkingAndFaults(t *testing.T) {
	t.Parallel()

	for iter := range 40 {
		// Random payload between 512 and 16384 bytes
		nBig, _ := rand.Int(rand.Reader, big.NewInt(15872))
		payloadLen := 512 + int(nBig.Int64())
		payload := make([]byte, payloadLen)
		_, _ = rand.Read(payload)

		// Random splits
		s1Big, _ := rand.Int(rand.Reader, big.NewInt(int64(payloadLen/2)))
		split1 := 1 + int(s1Big.Int64()) // retained prefix length

		s2Big, _ := rand.Int(rand.Reader, big.NewInt(int64((payloadLen-split1)/2+1)))
		split2 := split1 + int(s2Big.Int64()) // unwritten suffix end

		spoolDir := t.TempDir()
		memCeilBig, _ := rand.Int(rand.Reader, big.NewInt(256))
		memCeil := 32 + memCeilBig.Int64()

		buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
			SpoolDir:         spoolDir,
			MemorySpoolBytes: memCeil,
		})
		if err != nil {
			t.Fatalf("iter %d NewSpillBuffer: %v", iter, err)
		}

		// Write retained prefix in random chunks
		prefix := payload[:split1]
		written := 0
		for written < len(prefix) {
			chunkSizeBig, _ := rand.Int(rand.Reader, big.NewInt(64))
			chunkSize := 1 + int(chunkSizeBig.Int64())
			if written+chunkSize > len(prefix) {
				chunkSize = len(prefix) - written
			}
			nw, err := buf.Write(prefix[written : written+chunkSize])
			if err != nil {
				t.Fatalf("iter %d buf.Write prefix: %v", iter, err)
			}
			written += nw
		}

		unwrittenSuffix := payload[split1:split2]
		remaining := &nonRewindableReader{r: bytes.NewReader(payload[split2:])}

		cr, err := largebody.NewCaptureReader(largebody.CaptureReaderConfig{
			Spill:           buf,
			UnwrittenSuffix: unwrittenSuffix,
			Remaining:       remaining,
			MaxBytes:        int64(payloadLen),
		})
		if err != nil {
			t.Fatalf("iter %d NewCaptureReader: %v", iter, err)
		}

		// Read back using random read chunk sizes
		var out bytes.Buffer
		readBufSizeBig, _ := rand.Int(rand.Reader, big.NewInt(128))
		readBuf := make([]byte, 1+int(readBufSizeBig.Int64()))
		for {
			nr, rErr := cr.Read(readBuf)
			if nr > 0 {
				out.Write(readBuf[:nr])
			}
			if rErr != nil {
				if errors.Is(rErr, io.EOF) {
					break
				}
				t.Fatalf("iter %d cr.Read: %v", iter, rErr)
			}
		}

		if err := cr.Close(); err != nil {
			t.Fatalf("iter %d cr.Close: %v", iter, err)
		}

		got := out.Bytes()
		if !bytes.Equal(got, payload) {
			t.Fatalf("iter %d: byte mismatch (got %d bytes, want %d bytes)",
				iter, len(got), len(payload))
		}
	}
}

// TestContinuationReader_BodyCeiling_ExactLimitPasses verifies that when total bytes
// equal MaxBytes, read succeeds without error (Requirements 2.4, 2.5).
func TestContinuationReader_BodyCeiling_ExactLimitPasses(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte("A"), 100)
	const limit int64 = 100

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 64,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	if _, err := buf.Write(payload[:40]); err != nil {
		t.Fatalf("Write: %v", err)
	}

	cr, err := largebody.NewCaptureReader(largebody.CaptureReaderConfig{
		Spill:           buf,
		UnwrittenSuffix: payload[40:60],
		Remaining:       io.NopCloser(bytes.NewReader(payload[60:])),
		MaxBytes:        limit,
	})
	if err != nil {
		t.Fatalf("NewCaptureReader: %v", err)
	}
	defer func() { _ = cr.Close() }()

	got, err := io.ReadAll(cr)
	if err != nil {
		t.Fatalf("expected read to pass exact limit, got: %v", err)
	}
	if int64(len(got)) != limit {
		t.Fatalf("got %d bytes, want %d", len(got), limit)
	}
}

// TestContinuationReader_BodyCeiling_LimitPlusOneFails verifies that reading limit+1 bytes
// returns an *http.MaxBytesError matching reqbody.TooLarge (Requirement 2.6).
func TestContinuationReader_BodyCeiling_LimitPlusOneFails(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte("B"), 101)
	const limit int64 = 100

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 64,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	if _, err := buf.Write(payload[:40]); err != nil {
		t.Fatalf("Write: %v", err)
	}

	cr, err := largebody.NewCaptureReader(largebody.CaptureReaderConfig{
		Spill:           buf,
		UnwrittenSuffix: payload[40:60],
		Remaining:       io.NopCloser(bytes.NewReader(payload[60:])),
		MaxBytes:        limit,
	})
	if err != nil {
		t.Fatalf("NewCaptureReader: %v", err)
	}
	defer func() { _ = cr.Close() }()

	_, err = io.ReadAll(cr)
	if err == nil {
		t.Fatal("expected error for limit+1 read, got nil")
	}
	if !isMaxBytesError(err) {
		t.Fatalf("expected *http.MaxBytesError, got: %T: %v", err, err)
	}
}

// TestContinuationReader_ClientCancelIsNotTooLarge verifies that client context cancellation
// propagates as context.Canceled and is NOT reported as MaxBytesError (Requirement 1.6).
func TestContinuationReader_ClientCancelIsNotTooLarge(t *testing.T) {
	t.Parallel()

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 64,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	if _, err := buf.Write([]byte("prefix_bytes")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	faulty := &faultyReader{
		r:         bytes.NewReader([]byte("more_bytes")),
		readLimit: 2,
		injectErr: context.Canceled,
	}

	cr, err := largebody.NewCaptureReader(largebody.CaptureReaderConfig{
		Spill:     buf,
		Remaining: faulty,
		MaxBytes:  1024,
	})
	if err != nil {
		t.Fatalf("NewCaptureReader: %v", err)
	}
	defer func() { _ = cr.Close() }()

	_, err = io.ReadAll(cr)
	if err == nil {
		t.Fatal("expected cancel error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if isMaxBytesError(err) {
		t.Fatal("cancel error must not be reported as MaxBytesError")
	}
}

// TestContinuationReader_ClientDisconnectIsNotTooLarge verifies non-limit client disconnects
// are propagated cleanly (Requirement 1.6).
func TestContinuationReader_ClientDisconnectIsNotTooLarge(t *testing.T) {
	t.Parallel()

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 64,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	disconnectErr := errors.New("client connection reset by peer")
	faulty := &faultyReader{
		r:         bytes.NewReader([]byte("some_data")),
		readLimit: 0,
		injectErr: disconnectErr,
	}

	cr, err := largebody.NewCaptureReader(largebody.CaptureReaderConfig{
		Spill:     buf,
		Remaining: faulty,
		MaxBytes:  1024,
	})
	if err != nil {
		t.Fatalf("NewCaptureReader: %v", err)
	}
	defer func() { _ = cr.Close() }()

	_, err = io.ReadAll(cr)
	if err == nil {
		t.Fatal("expected disconnect error, got nil")
	}
	if !errors.Is(err, disconnectErr) {
		t.Fatalf("expected disconnectErr, got: %v", err)
	}
	if isMaxBytesError(err) {
		t.Fatal("disconnect error must not be reported as MaxBytesError")
	}
}

// TestContinuationReader_NeverRereadsOrRestartsSocket verifies the client socket
// is never rewound or reread from offset 0 (Requirement 20.8).
func TestContinuationReader_NeverRereadsOrRestartsSocket(t *testing.T) {
	t.Parallel()

	payload := []byte("0123456789ABCDEF")
	socket := &nonRewindableReader{r: bytes.NewReader(payload[8:])} // already read 8 bytes

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 64,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	if _, err := buf.Write(payload[:4]); err != nil {
		t.Fatalf("Write: %v", err)
	}

	cr, err := largebody.NewCaptureReader(largebody.CaptureReaderConfig{
		Spill:           buf,
		UnwrittenSuffix: payload[4:8],
		Remaining:       socket,
		MaxBytes:        100,
	})
	if err != nil {
		t.Fatalf("NewCaptureReader: %v", err)
	}
	defer func() { _ = cr.Close() }()

	got, err := io.ReadAll(cr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %q, want %q", got, payload)
	}

	// Verify socket only read the remaining 8 bytes, never the earlier bytes!
	if socket.bytesRead != 8 {
		t.Fatalf("socket bytesRead = %d, want 8", socket.bytesRead)
	}
}

// TestContinuationReader_CloseIdempotentAndCleansUp verifies Close() closes both
// the retained prefix and remaining body, and is idempotent.
func TestContinuationReader_CloseIdempotentAndCleansUp(t *testing.T) {
	t.Parallel()

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 64,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}

	socket := &nonRewindableReader{r: bytes.NewReader([]byte("socket_bytes"))}

	cr, err := largebody.NewCaptureReader(largebody.CaptureReaderConfig{
		Spill:     buf,
		Remaining: socket,
		MaxBytes:  100,
	})
	if err != nil {
		t.Fatalf("NewCaptureReader: %v", err)
	}

	if err := cr.Close(); err != nil {
		t.Fatalf("first cr.Close: %v", err)
	}
	if !socket.closed {
		t.Fatal("expected socket to be closed after cr.Close()")
	}

	// Second Close must be idempotent and return nil
	if err := cr.Close(); err != nil {
		t.Fatalf("second cr.Close: %v", err)
	}
}

// TestSpillBuffer_UnconsumedSuffix_RejectsWrite verifies the Task 4.2 review suggestion:
// when a write fails and leaves an unwritten suffix, calling Write() again before
// consuming TakeUnwrittenSuffix() is rejected with ErrUnconsumedSuffix.
func TestSpillBuffer_UnconsumedSuffix_RejectsWrite(t *testing.T) {
	t.Parallel()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      32,
		MaxInflightSpoolBytes: 100,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger: %v", err)
	}
	res, err := ledger.BeginReservation()
	if err != nil {
		t.Fatalf("BeginReservation: %v", err)
	}

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 32,
		Reservation:      res,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	// Write 40 bytes (succeeds)
	if _, err := buf.Write(bytes.Repeat([]byte("A"), 40)); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// Write 70 bytes (fails budget: 40+70 = 110 > 100)
	unwrittenChunk := bytes.Repeat([]byte("B"), 70)
	_, err = buf.Write(unwrittenChunk)
	if err == nil || !errors.Is(err, largebody.ErrSpoolBudgetExhausted) {
		t.Fatalf("expected ErrSpoolBudgetExhausted, got: %v", err)
	}
	if !buf.HasUnwrittenSuffix() {
		t.Fatal("expected HasUnwrittenSuffix to be true")
	}

	// Now try to Write again WITHOUT consuming TakeUnwrittenSuffix()
	// Must fail with ErrUnconsumedSuffix!
	_, err = buf.Write([]byte("C"))
	if err == nil || !errors.Is(err, largebody.ErrUnconsumedSuffix) {
		t.Fatalf("expected ErrUnconsumedSuffix, got: %v", err)
	}

	// Now consume the suffix via TakeUnwrittenSuffix()
	taken := buf.TakeUnwrittenSuffix()
	if !bytes.Equal(taken, unwrittenChunk) {
		t.Fatalf("TakeUnwrittenSuffix got %q, want %q", taken, unwrittenChunk)
	}

	// Writing 50 bytes now fits within budget: 40 + 50 = 90 <= 100!
	fitChunk := taken[:50]
	if _, err := buf.Write(fitChunk); err != nil {
		t.Fatalf("retrying write after TakeUnwrittenSuffix: %v", err)
	}
	if buf.HasUnwrittenSuffix() {
		t.Fatal("expected no unwritten suffix after successful write")
	}
}

// TestCaptureRequestBody_CompleteSuccess verifies that a body within budget
// captures completely to EOF (Requirement 20).
func TestCaptureRequestBody_CompleteSuccess(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte("X"), 1024)
	src := io.NopCloser(bytes.NewReader(payload))

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 2048,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	result := largebody.CaptureRequestBody(src, buf, largebody.CaptureConfig{
		MaxBytes:       2048,
		CopyBufferSize: 256,
	})

	if result.Outcome != largebody.CaptureOutcomeCompleted {
		t.Fatalf("expected CaptureOutcomeCompleted, got %v (err=%v)", result.Outcome, result.Err)
	}
	if result.BytesRead != int64(len(payload)) {
		t.Fatalf("BytesRead got %d, want %d", result.BytesRead, len(payload))
	}
	if result.Continuation != nil {
		t.Fatal("expected nil Continuation on completion")
	}

	rc, err := buf.Open()
	if err != nil {
		t.Fatalf("buf.Open: %v", err)
	}
	defer func() { _ = rc.Close() }()
	captured, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(captured, payload) {
		t.Fatalf("captured payload mismatch")
	}
}

// TestCaptureRequestBody_MidCaptureBudgetExhaustion_YieldsContinuation verifies that
// when the spool budget is exhausted mid-capture, CaptureRequestBody produces a
// lossless continuation reader that yields the exact full payload (Requirements 1.4, 20.7).
func TestCaptureRequestBody_MidCaptureBudgetExhaustion_YieldsContinuation(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte("M"), 200)
	src := &nonRewindableReader{r: bytes.NewReader(payload)}

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64,
		MaxInflightSpoolBytes: 100, // budget only allows 100 bytes
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger: %v", err)
	}
	res, err := ledger.BeginReservation()
	if err != nil {
		t.Fatalf("BeginReservation: %v", err)
	}

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 64,
		Reservation:      res,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}

	result := largebody.CaptureRequestBody(src, buf, largebody.CaptureConfig{
		MaxBytes:       500,
		CopyBufferSize: 40, // 40, 40 (80 ok), then 40 (120 > 100 fails!)
	})

	if result.Outcome != largebody.CaptureOutcomeDeclined {
		t.Fatalf("expected CaptureOutcomeDeclined, got %v (err=%v)", result.Outcome, result.Err)
	}
	if result.Continuation == nil {
		t.Fatal("expected non-nil Continuation reader on decline")
	}
	defer func() { _ = result.Continuation.Close() }()

	// Read full payload through continuation
	got, err := io.ReadAll(result.Continuation)
	if err != nil {
		t.Fatalf("ReadAll continuation: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("continuation payload mismatch: got %d bytes, want %d bytes", len(got), len(payload))
	}
}

// TestCaptureRequestBody_ExceedsCeiling_FailsImmediately verifies that if the incoming
// body exceeds MaxBytes, CaptureRequestBody returns CaptureOutcomeLimitExceeded with
// *http.MaxBytesError (Requirement 2.6).
func TestCaptureRequestBody_ExceedsCeiling_FailsImmediately(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte("Z"), 105)
	src := io.NopCloser(bytes.NewReader(payload))

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 64,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	result := largebody.CaptureRequestBody(src, buf, largebody.CaptureConfig{
		MaxBytes:       100, // ceiling is 100
		CopyBufferSize: 30,
	})

	if result.Outcome != largebody.CaptureOutcomeLimitExceeded {
		t.Fatalf("expected CaptureOutcomeLimitExceeded, got %v", result.Outcome)
	}
	if !isMaxBytesError(result.Err) {
		t.Fatalf("expected *http.MaxBytesError, got: %T: %v", result.Err, result.Err)
	}
}

// TestCaptureRequestBody_ClientReadError verifies that client socket read errors
// are returned with CaptureOutcomeReadError and not reported as MaxBytesError (Requirement 1.6).
func TestCaptureRequestBody_ClientReadError(t *testing.T) {
	t.Parallel()

	expectedErr := errors.New("socket reset")
	src := &faultyReader{
		r:         bytes.NewReader([]byte("partial_data")),
		readLimit: 10,
		injectErr: expectedErr,
	}

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 64,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	result := largebody.CaptureRequestBody(src, buf, largebody.CaptureConfig{
		MaxBytes:       1024,
		CopyBufferSize: 32,
	})

	if result.Outcome != largebody.CaptureOutcomeReadError {
		t.Fatalf("expected CaptureOutcomeReadError, got %v", result.Outcome)
	}
	if !errors.Is(result.Err, expectedErr) {
		t.Fatalf("expected %v, got %v", expectedErr, result.Err)
	}
	if isMaxBytesError(result.Err) {
		t.Fatal("socket error must not be reported as MaxBytesError")
	}
}
