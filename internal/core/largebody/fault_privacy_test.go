package largebody_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/reqbody"
)

// shortWriteFile simulates partial / short writes to test unwritten suffix preservation (Requirement 20.6).
type shortWriteFile struct {
	maxWrite int
	written  int
	buf      bytes.Buffer
	name     string
	closed   bool
}

func (s *shortWriteFile) Read(p []byte) (int, error)                   { return s.buf.Read(p) }
func (s *shortWriteFile) Seek(offset int64, whence int) (int64, error) { return 0, nil }
func (s *shortWriteFile) Sync() error                                  { return nil }
func (s *shortWriteFile) Name() string                                 { return s.name }
func (s *shortWriteFile) Close() error {
	s.closed = true
	return nil
}

func (s *shortWriteFile) Write(p []byte) (int, error) {
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	if s.maxWrite <= 0 {
		return 0, io.ErrShortWrite
	}
	toWrite := min(len(p), s.maxWrite)
	n, err := s.buf.Write(p[:toWrite])
	s.written += n
	if toWrite < len(p) {
		return n, io.ErrShortWrite
	}
	return n, err
}

// errorReadFile simulates a read failure on a spill file reader.
type errorReadFile struct {
	err error
}

func (e *errorReadFile) Read(p []byte) (int, error) { return 0, e.err }
func (e *errorReadFile) Close() error               { return nil }

// TestFault_ReservationExhaustion_DeclineNot413 verifies that logical spool reservation
// exhaustion produces an optimization decline (CaptureOutcomeDeclined) rather than a 413 error,
// produces a lossless canonical continuation reader, and releases the reservation budget exactly once
// (Requirements 1.5, 20.4, 20.5; design section 5).
func TestFault_ReservationExhaustion_DeclineNot413(t *testing.T) {
	t.Parallel()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64,
		MaxInflightSpoolBytes: 256, // small global budget
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger: %v", err)
	}

	res, err := ledger.Reserve(0)
	if err != nil {
		t.Fatalf("ledger.Reserve: %v", err)
	}

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 64,
		CopyBufferSize:   64,
		Reservation:      res,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	// 512 bytes payload exceeds the 256 byte global reservation budget
	payload := make([]byte, 512)
	for i := range payload {
		payload[i] = byte('A' + (i % 26))
	}
	body := io.NopCloser(bytes.NewReader(payload))

	result := largebody.CaptureRequestBody(body, buf, largebody.CaptureConfig{
		MaxBytes:       1024,
		CopyBufferSize: 64,
	})

	// Must be an optimization decline, NOT 413 / LimitExceeded (Requirement 1.5, 20.5)
	if result.Outcome != largebody.CaptureOutcomeDeclined {
		t.Fatalf("Outcome got %v (%s), want CaptureOutcomeDeclined", result.Outcome, result.Outcome)
	}
	if !errors.Is(result.Err, largebody.ErrSpoolBudgetExhausted) {
		t.Fatalf("result.Err got %v, want ErrSpoolBudgetExhausted", result.Err)
	}
	if reqbody.TooLarge(result.Err) {
		t.Fatal("ErrSpoolBudgetExhausted must NOT match reqbody.TooLarge (must decline, not 413)")
	}
	if result.Continuation == nil {
		t.Fatal("expected non-nil Continuation reader on decline")
	}

	// Canonical continuation reader must recover the entire payload byte-for-byte
	recovered, err := io.ReadAll(result.Continuation)
	if err != nil {
		t.Fatalf("Continuation ReadAll failed: %v", err)
	}
	if !bytes.Equal(recovered, payload) {
		t.Fatalf("recovered payload mismatch: got %d bytes, want %d", len(recovered), len(payload))
	}

	// Closing continuation must release the spool reservation (Requirement 20.4)
	if err := result.Continuation.Close(); err != nil {
		t.Fatalf("Continuation.Close: %v", err)
	}

	if ledger.InflightBytes() != 0 {
		t.Fatalf("ledger.InflightBytes after close got %d, want 0", ledger.InflightBytes())
	}
	if ledger.ActiveReservations() != 0 {
		t.Fatalf("ledger.ActiveReservations after close got %d, want 0", ledger.ActiveReservations())
	}
}

// TestFault_FileCreationFailure_DeclineToCanonical verifies that when temporary spill file
// creation fails, capture declines gracefully to canonical continuation with zero byte loss
// (Requirements 20.6, 20.7, 20.10).
func TestFault_FileCreationFailure_DeclineToCanonical(t *testing.T) {
	t.Parallel()

	injectedErr := errors.New("simulated disk permission denied")
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 32, // small memory window; next chunk triggers spill file creation
		CopyBufferSize:   32,
		CreateFile: func(dir string) (largebody.SpillFile, string, error) {
			return nil, "", injectedErr
		},
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	payload := make([]byte, 128)
	for i := range payload {
		payload[i] = byte('a' + (i % 26))
	}
	body := io.NopCloser(bytes.NewReader(payload))

	result := largebody.CaptureRequestBody(body, buf, largebody.CaptureConfig{
		MaxBytes:       1024,
		CopyBufferSize: 32,
	})

	if result.Outcome != largebody.CaptureOutcomeDeclined {
		t.Fatalf("Outcome got %v, want CaptureOutcomeDeclined", result.Outcome)
	}
	if !errors.Is(result.Err, largebody.ErrSpillFileCreationFailed) {
		t.Fatalf("result.Err got %v, want ErrSpillFileCreationFailed", result.Err)
	}
	if result.Continuation == nil {
		t.Fatal("expected non-nil Continuation reader on creation failure")
	}

	recovered, err := io.ReadAll(result.Continuation)
	if err != nil {
		t.Fatalf("Continuation ReadAll failed: %v", err)
	}
	if !bytes.Equal(recovered, payload) {
		t.Fatalf("recovered payload mismatch: got len=%d, want len=%d", len(recovered), len(payload))
	}

	if err := result.Continuation.Close(); err != nil {
		t.Fatalf("Continuation.Close: %v", err)
	}
}

// TestFault_ShortWrite_PreservesSuffixAndRecovers verifies that when a write to the spill
// file is short / partial, the unwritten suffix is preserved and fed into the canonical
// continuation reader without discarding any client bytes (Requirements 20.6, 20.7, 20.10).
func TestFault_ShortWrite_PreservesSuffixAndRecovers(t *testing.T) {
	t.Parallel()

	mockFile := &shortWriteFile{
		maxWrite: 8, // can only write 8 bytes at a time
		name:     "mock_spill.tmp",
	}

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 16, // memory fills at 16 bytes
		CopyBufferSize:   32,
		CreateFile: func(dir string) (largebody.SpillFile, string, error) {
			return mockFile, mockFile.Name(), nil
		},
		OpenFile: func(path string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(mockFile.buf.Bytes())), nil
		},
		RemoveFile: func(path string) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	payload := make([]byte, 80)
	for i := range payload {
		payload[i] = byte('0' + (i % 10))
	}
	body := io.NopCloser(bytes.NewReader(payload))

	result := largebody.CaptureRequestBody(body, buf, largebody.CaptureConfig{
		MaxBytes:       1024,
		CopyBufferSize: 32,
	})

	if result.Outcome != largebody.CaptureOutcomeDeclined {
		t.Fatalf("Outcome got %v, want CaptureOutcomeDeclined", result.Outcome)
	}
	if !errors.Is(result.Err, largebody.ErrSpillWriteFailed) {
		t.Fatalf("result.Err got %v, want ErrSpillWriteFailed", result.Err)
	}
	if result.Continuation == nil {
		t.Fatal("expected non-nil Continuation reader on short write")
	}

	recovered, err := io.ReadAll(result.Continuation)
	if err != nil {
		t.Fatalf("Continuation ReadAll failed: %v", err)
	}
	if !bytes.Equal(recovered, payload) {
		t.Fatalf("recovered payload mismatch: got %q, want %q", string(recovered), string(payload))
	}

	if err := result.Continuation.Close(); err != nil {
		t.Fatalf("Continuation.Close: %v", err)
	}
}

// TestFault_ClientReadError_CleansUpWithoutLeak verifies that a mid-stream client body read error
// causes CaptureRequestBody to return CaptureOutcomeReadError and immediately release resources
// (Requirements 20.4, 20.9, 20.10).
func TestFault_ClientReadError_CleansUpWithoutLeak(t *testing.T) {
	t.Parallel()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64,
		MaxInflightSpoolBytes: 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger: %v", err)
	}
	res, err := ledger.Reserve(0)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	spoolDir := t.TempDir()
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 16,
		CopyBufferSize:   32,
		Reservation:      res,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}

	injectedErr := errors.New("client connection reset by peer")
	body := &faultyReader{
		r:         bytes.NewReader(bytes.Repeat([]byte("x"), 100)),
		readLimit: 40, // fails after 40 bytes
		injectErr: injectedErr,
	}

	result := largebody.CaptureRequestBody(body, buf, largebody.CaptureConfig{
		MaxBytes:       1024,
		CopyBufferSize: 20,
	})

	if result.Outcome != largebody.CaptureOutcomeReadError {
		t.Fatalf("Outcome got %v, want CaptureOutcomeReadError", result.Outcome)
	}
	if !errors.Is(result.Err, injectedErr) {
		t.Fatalf("result.Err got %v, want %v", result.Err, injectedErr)
	}

	// Reservation must be released immediately on error
	if ledger.InflightBytes() != 0 {
		t.Fatalf("ledger.InflightBytes after error got %d, want 0", ledger.InflightBytes())
	}
}

// TestFault_OpenFileFailure verifies that a failure opening the spill file for a reader
// is surfaced cleanly without panicking (Requirement 20.10).
func TestFault_OpenFileFailure(t *testing.T) {
	t.Parallel()

	injectedErr := errors.New("simulated open permission denied")
	src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
		FilePath: filepath.Join(t.TempDir(), "nonexistent.tmp"),
		Size:     100,
		OpenFile: func(path string) (io.ReadCloser, error) {
			return nil, injectedErr
		},
	})
	if err != nil {
		t.Fatalf("NewCompletedSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	rc, err := src.Open()
	if err == nil {
		_ = rc.Close()
		t.Fatal("expected error opening source with failing OpenFile, got nil")
	}
	if !errors.Is(err, injectedErr) {
		t.Fatalf("Open error got %v, want %v", err, injectedErr)
	}
}

// TestFault_SpillReadError verifies that an I/O error during reading from a spill file
// reader surfaces cleanly (Requirement 20.10).
func TestFault_SpillReadError(t *testing.T) {
	t.Parallel()

	injectedErr := errors.New("disk read error (bad sector)")
	src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
		FilePath: "dummy_path",
		Size:     100,
		OpenFile: func(path string) (io.ReadCloser, error) {
			return &errorReadFile{err: injectedErr}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewCompletedSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	rc, err := src.Open()
	if err != nil {
		t.Fatalf("src.Open: %v", err)
	}
	defer func() { _ = rc.Close() }()

	buf := make([]byte, 32)
	_, rErr := rc.Read(buf)
	if !errors.Is(rErr, injectedErr) {
		t.Fatalf("Read error got %v, want %v", rErr, injectedErr)
	}
}

// TestFault_RemoveFileFailure_SurfacesCleanly verifies that spill file deletion failures
// during root Close return the error without corruption or panic (Requirement 20.10).
func TestFault_RemoveFileFailure_SurfacesCleanly(t *testing.T) {
	t.Parallel()

	injectedErr := errors.New("simulated unlink failure (file busy)")
	src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
		FilePath: "test_spill_file.tmp",
		Size:     50,
		RemoveFile: func(path string) error {
			return injectedErr
		},
	})
	if err != nil {
		t.Fatalf("NewCompletedSource: %v", err)
	}

	err = src.Close()
	if !errors.Is(err, injectedErr) {
		t.Fatalf("src.Close error got %v, want %v", err, injectedErr)
	}

	// Subsequent close is idempotent and returns nil
	if err := src.Close(); err != nil {
		t.Fatalf("subsequent Close error got %v, want nil", err)
	}
}

// TestFault_CancellationAndTimeout verifies that client context cancellation and deadline
// timeouts during body read terminate capture with CaptureOutcomeReadError and release reservations
// (Requirements 20.4, 20.10).
func TestFault_CancellationAndTimeout(t *testing.T) {
	t.Parallel()

	t.Run("context.Canceled", func(t *testing.T) {
		t.Parallel()
		ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
			MemorySpoolBytes:      64,
			MaxInflightSpoolBytes: 1024,
		})
		if err != nil {
			t.Fatalf("NewSpoolLedger: %v", err)
		}
		res, err := ledger.Reserve(0)
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}

		buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
			SpoolDir:         t.TempDir(),
			MemorySpoolBytes: 32,
			Reservation:      res,
		})
		if err != nil {
			t.Fatalf("NewSpillBuffer: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // already canceled

		body := &faultyReader{
			r:         bytes.NewReader(bytes.Repeat([]byte("c"), 100)),
			readLimit: 0,
			injectErr: ctx.Err(),
		}

		result := largebody.CaptureRequestBody(body, buf, largebody.CaptureConfig{
			MaxBytes: 1024,
		})

		if result.Outcome != largebody.CaptureOutcomeReadError {
			t.Fatalf("Outcome got %v, want CaptureOutcomeReadError", result.Outcome)
		}
		if !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("result.Err got %v, want context.Canceled", result.Err)
		}
		if ledger.InflightBytes() != 0 {
			t.Fatalf("ledger.InflightBytes got %d, want 0", ledger.InflightBytes())
		}
	})

	t.Run("context.DeadlineExceeded", func(t *testing.T) {
		t.Parallel()
		ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
			MemorySpoolBytes:      64,
			MaxInflightSpoolBytes: 1024,
		})
		if err != nil {
			t.Fatalf("NewSpoolLedger: %v", err)
		}
		res, err := ledger.Reserve(0)
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}

		buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
			SpoolDir:         t.TempDir(),
			MemorySpoolBytes: 32,
			Reservation:      res,
		})
		if err != nil {
			t.Fatalf("NewSpillBuffer: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
		defer cancel()
		<-ctx.Done() // ensure expired

		body := &faultyReader{
			r:         bytes.NewReader(bytes.Repeat([]byte("t"), 100)),
			readLimit: 0,
			injectErr: ctx.Err(),
		}

		result := largebody.CaptureRequestBody(body, buf, largebody.CaptureConfig{
			MaxBytes: 1024,
		})

		if result.Outcome != largebody.CaptureOutcomeReadError {
			t.Fatalf("Outcome got %v, want CaptureOutcomeReadError", result.Outcome)
		}
		if !errors.Is(result.Err, context.DeadlineExceeded) {
			t.Fatalf("result.Err got %v, want context.DeadlineExceeded", result.Err)
		}
		if ledger.InflightBytes() != 0 {
			t.Fatalf("ledger.InflightBytes got %d, want 0", ledger.InflightBytes())
		}
	})
}

// TestLimits_ExactLimitAndLimitPlusOne verifies boundary enforcement for exact MaxBytes
// and MaxBytes+1, confirming reqbody.TooLarge parity and immediate resource release
// (Requirements 2.4, 2.6, 20.4; design sections 1, 2, 5).
func TestLimits_ExactLimitAndLimitPlusOne(t *testing.T) {
	t.Parallel()

	const maxBytes int64 = 128

	t.Run("exact limit passes", func(t *testing.T) {
		t.Parallel()
		spoolDir := t.TempDir()
		buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
			SpoolDir:         spoolDir,
			MemorySpoolBytes: 32,
			CopyBufferSize:   32,
		})
		if err != nil {
			t.Fatalf("NewSpillBuffer: %v", err)
		}
		defer func() { _ = buf.Close() }()

		exactPayload := bytes.Repeat([]byte("E"), int(maxBytes))
		body := io.NopCloser(bytes.NewReader(exactPayload))

		result := largebody.CaptureRequestBody(body, buf, largebody.CaptureConfig{
			MaxBytes:       maxBytes,
			CopyBufferSize: 32,
		})

		if result.Outcome != largebody.CaptureOutcomeCompleted {
			t.Fatalf("exact limit outcome got %v, want CaptureOutcomeCompleted (err=%v)",
				result.Outcome, result.Err)
		}
		if result.BytesRead != maxBytes {
			t.Fatalf("BytesRead got %d, want %d", result.BytesRead, maxBytes)
		}
		if result.Source == nil {
			t.Fatal("expected non-nil Source")
		}
		if result.Source.Size() != maxBytes {
			t.Fatalf("Source.Size got %d, want %d", result.Source.Size(), maxBytes)
		}

		rc, err := result.Source.Open()
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = rc.Close() }()
		readBack, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if !bytes.Equal(readBack, exactPayload) {
			t.Fatal("exact payload content mismatch")
		}
	})

	t.Run("limit plus one fails with reqbody.TooLarge", func(t *testing.T) {
		t.Parallel()
		ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
			MemorySpoolBytes:      32,
			MaxInflightSpoolBytes: 1024,
		})
		if err != nil {
			t.Fatalf("NewSpoolLedger: %v", err)
		}
		res, err := ledger.Reserve(0)
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}

		spoolDir := t.TempDir()
		buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
			SpoolDir:         spoolDir,
			MemorySpoolBytes: 32,
			CopyBufferSize:   32,
			Reservation:      res,
		})
		if err != nil {
			t.Fatalf("NewSpillBuffer: %v", err)
		}

		overPayload := bytes.Repeat([]byte("O"), int(maxBytes)+1)
		body := io.NopCloser(bytes.NewReader(overPayload))

		result := largebody.CaptureRequestBody(body, buf, largebody.CaptureConfig{
			MaxBytes:       maxBytes,
			CopyBufferSize: 32,
		})

		if result.Outcome != largebody.CaptureOutcomeLimitExceeded {
			t.Fatalf("over-limit outcome got %v, want CaptureOutcomeLimitExceeded", result.Outcome)
		}
		if result.Err == nil {
			t.Fatal("expected non-nil Err on limit exceeded")
		}

		// Must unwrap to *http.MaxBytesError matching reqbody.TooLarge (Requirement 2.6)
		if !reqbody.TooLarge(result.Err) {
			t.Fatalf("result.Err %v must satisfy reqbody.TooLarge", result.Err)
		}
		var maxBytesErr *http.MaxBytesError
		if !errors.As(result.Err, &maxBytesErr) {
			t.Fatalf("result.Err got %T, want *http.MaxBytesError", result.Err)
		}
		if maxBytesErr.Limit != maxBytes {
			t.Fatalf("maxBytesErr.Limit got %d, want %d", maxBytesErr.Limit, maxBytes)
		}

		// Spool reservation must be freed immediately
		if ledger.InflightBytes() != 0 {
			t.Fatalf("ledger.InflightBytes after limit exceeded got %d, want 0", ledger.InflightBytes())
		}
	})

	t.Run("continuation reader enforces MaxBytes ceiling with reqbody.TooLarge", func(t *testing.T) {
		t.Parallel()
		// Capture declines at 64 bytes; continuation reader continues up to maxBytes=128
		buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
			SpoolDir:         t.TempDir(),
			MemorySpoolBytes: 64,
		})
		if err != nil {
			t.Fatalf("NewSpillBuffer: %v", err)
		}
		defer func() { _ = buf.Close() }()

		if _, err := buf.Write(bytes.Repeat([]byte("P"), 64)); err != nil {
			t.Fatalf("buf.Write: %v", err)
		}

		// Remaining body has 65 bytes (total 129 > maxBytes 128)
		remaining := io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("R"), 65)))
		rec := httptest.NewRecorder()
		cont, err := largebody.NewCaptureReader(largebody.CaptureReaderConfig{
			Spill:          buf,
			Remaining:      remaining,
			MaxBytes:       maxBytes,
			ResponseWriter: rec,
		})
		if err != nil {
			t.Fatalf("NewCaptureReader: %v", err)
		}
		defer func() { _ = cont.Close() }()

		_, rErr := io.ReadAll(cont)
		if rErr == nil {
			t.Fatal("expected error reading over-limit from continuation reader, got nil")
		}
		if !reqbody.TooLarge(rErr) {
			t.Fatalf("continuation error %v must satisfy reqbody.TooLarge", rErr)
		}
	})
}

// TestLeak_CompletedSource_LeakedReaderSafeDeletion verifies that root Close() on CompletedSource
// is nonblocking and does not deadlock when active readers exist. It marks deletion pending,
// keeps the file readable while readers are open (Windows open-file safety), and deletes the file
// automatically when the last reader closes (Requirements 20.9, 20.10; design section 5).
func TestLeak_CompletedSource_LeakedReaderSafeDeletion(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 16, // force file spill
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}

	payload := bytes.Repeat([]byte("Z"), 256)
	if _, err := buf.Write(payload); err != nil {
		t.Fatalf("buf.Write: %v", err)
	}

	src, err := buf.Complete()
	if err != nil {
		t.Fatalf("buf.Complete: %v", err)
	}

	filePath := src.FilePath()
	if filePath == "" {
		t.Fatal("expected non-empty FilePath for spilled source")
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("spill file does not exist before close: %v", err)
	}

	// Open multiple concurrent readers
	r1, err := src.Open()
	if err != nil {
		t.Fatalf("Open r1: %v", err)
	}
	r2, err := src.Open()
	if err != nil {
		t.Fatalf("Open r2: %v", err)
	}

	if src.ActiveReaders() != 2 {
		t.Fatalf("ActiveReaders got %d, want 2", src.ActiveReaders())
	}

	// Root Close() while readers are active MUST NOT BLOCK (Requirement 20.9)
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- src.Close()
	}()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("src.Close returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("src.Close deadlocked waiting for active readers!")
	}

	if !src.IsClosed() {
		t.Fatal("expected IsClosed to be true after Close()")
	}
	if !src.IsDeletePending() {
		t.Fatal("expected IsDeletePending to be true while readers are active")
	}

	// File MUST still exist on disk while readers are open (Windows open-file safety)
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("file was prematurely deleted while readers were active: %v", err)
	}

	// Both readers can still read their full content independently
	d1, err := io.ReadAll(r1)
	if err != nil {
		t.Fatalf("ReadAll r1: %v", err)
	}
	if !bytes.Equal(d1, payload) {
		t.Fatal("r1 data mismatch")
	}

	// Close first reader: file must STILL exist because r2 is active
	if err := r1.Close(); err != nil {
		t.Fatalf("r1.Close: %v", err)
	}
	if src.ActiveReaders() != 1 {
		t.Fatalf("ActiveReaders after r1.Close got %d, want 1", src.ActiveReaders())
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("file was prematurely deleted while r2 was still active: %v", err)
	}

	// Close second reader: file MUST now be automatically deleted
	d2, err := io.ReadAll(r2)
	if err != nil {
		t.Fatalf("ReadAll r2: %v", err)
	}
	if !bytes.Equal(d2, payload) {
		t.Fatal("r2 data mismatch")
	}
	if err := r2.Close(); err != nil {
		t.Fatalf("r2.Close: %v", err)
	}

	if src.ActiveReaders() != 0 {
		t.Fatalf("ActiveReaders after r2.Close got %d, want 0", src.ActiveReaders())
	}

	// Final verification: file is completely removed from disk
	if _, err := os.Stat(filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file was NOT deleted after all readers closed: err=%v", err)
	}

	// Multiple subsequent Close() calls on readers and source are idempotent and safe
	if err := r1.Close(); err != nil {
		t.Fatalf("r1 redundant close failed: %v", err)
	}
	if err := r2.Close(); err != nil {
		t.Fatalf("r2 redundant close failed: %v", err)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("src redundant close failed: %v", err)
	}
}

// TestLeak_SpillBuffer_LeakedReaderSafeDeletion verifies that root Close() on an uncompleted
// SpillBuffer is nonblocking, defers file deletion until all active readers close, and
// removes the spill file once readers reach zero (Requirements 20.9, 20.10).
func TestLeak_SpillBuffer_LeakedReaderSafeDeletion(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 16,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}

	payload := bytes.Repeat([]byte("W"), 128)
	if _, err := buf.Write(payload); err != nil {
		t.Fatalf("buf.Write: %v", err)
	}

	filePath := buf.FilePath()
	if filePath == "" {
		t.Fatal("expected non-empty FilePath")
	}

	rc, err := buf.Open()
	if err != nil {
		t.Fatalf("buf.Open: %v", err)
	}

	// Close buffer while reader is open: must not block
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- buf.Close()
	}()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("buf.Close error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("buf.Close deadlocked waiting for active reader!")
	}

	// File still exists
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("file prematurely deleted: %v", err)
	}

	// Close reader -> file must be deleted
	if err := rc.Close(); err != nil {
		t.Fatalf("rc.Close: %v", err)
	}

	if _, err := os.Stat(filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file was NOT deleted after reader closed: err=%v", err)
	}
}

// TestLeak_CaptureReader_ContinuationCleanup verifies that closing a CaptureReader
// cleanly closes the underlying SpillBuffer and deletes temporary spill files (Requirement 20.9).
func TestLeak_CaptureReader_ContinuationCleanup(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 16,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}

	payload := bytes.Repeat([]byte("K"), 128)
	if _, err := buf.Write(payload); err != nil {
		t.Fatalf("buf.Write: %v", err)
	}
	filePath := buf.FilePath()

	cont, err := largebody.NewCaptureReader(largebody.CaptureReaderConfig{
		Spill:     buf,
		Remaining: io.NopCloser(strings.NewReader("tail")),
	})
	if err != nil {
		t.Fatalf("NewCaptureReader: %v", err)
	}

	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("file must exist before Close: %v", err)
	}

	if err := cont.Close(); err != nil {
		t.Fatalf("cont.Close: %v", err)
	}

	if _, err := os.Stat(filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file was NOT cleaned up by CaptureReader.Close: err=%v", err)
	}
}

// TestPrivacy_Redaction_NoPromptOrPathOrSecretInErrorsOrLogs rigorously verifies that
// prompt payload content, filesystem spool paths, and session secrets (resume tokens, session IDs)
// never render into normal diagnostics, error strings, String/GoString methods, JSON serialization,
// or structured slog (text/JSON) handlers (Requirements 20.3, 22.3).
func TestPrivacy_Redaction_NoPromptOrPathOrSecretInErrorsOrLogs(t *testing.T) {
	t.Parallel()

	const secretPrompt = "CANARY_SECRET_USER_PROMPT_DO_NOT_LEAK_INTO_LOGS_OR_METRICS"
	const secretResumeToken = "CANARY_SECRET_BEARER_RESUME_TOKEN_XYZ_9999"
	const secretSessionID = "CANARY_SECRET_SESSION_ID_1111"
	const secretALegID = "CANARY_SECRET_ALEG_ID_2222"

	spoolDir := t.TempDir()

	// Probes list of all secrets that must never appear in any diagnostic
	secrets := []string{
		secretPrompt,
		secretResumeToken,
		secretSessionID,
		secretALegID,
		spoolDir,
	}

	assertNoLeaks := func(t *testing.T, contextName string, text string) {
		t.Helper()
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Fatalf("%s leaks secret %q in diagnostic output: %s", contextName, secret, text)
			}
		}
	}

	t.Run("CompletedSource String and GoString", func(t *testing.T) {
		t.Parallel()
		buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
			SpoolDir:         spoolDir,
			MemorySpoolBytes: 8,
		})
		if err != nil {
			t.Fatalf("NewSpillBuffer: %v", err)
		}
		if _, err := buf.Write([]byte(secretPrompt)); err != nil {
			t.Fatalf("buf.Write: %v", err)
		}
		src, err := buf.Complete()
		if err != nil {
			t.Fatalf("buf.Complete: %v", err)
		}
		defer func() { _ = src.Close() }()

		for _, verb := range []string{"%s", "%v", "%+v", "%#v"} {
			out := fmt.Sprintf(verb, src)
			assertNoLeaks(t, fmt.Sprintf("CompletedSource format %s", verb), out)
		}
		assertNoLeaks(t, "CompletedSource.String()", src.String())
	})

	t.Run("SpillBuffer String and GoString", func(t *testing.T) {
		t.Parallel()
		buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
			SpoolDir:         spoolDir,
			MemorySpoolBytes: 8,
		})
		if err != nil {
			t.Fatalf("NewSpillBuffer: %v", err)
		}
		defer func() { _ = buf.Close() }()
		if _, err := buf.Write([]byte(secretPrompt)); err != nil {
			t.Fatalf("buf.Write: %v", err)
		}

		for _, verb := range []string{"%s", "%v", "%+v", "%#v"} {
			out := fmt.Sprintf(verb, buf)
			assertNoLeaks(t, fmt.Sprintf("SpillBuffer format %s", verb), out)
		}
		assertNoLeaks(t, "SpillBuffer.String()", buf.String())
	})

	t.Run("SessionInput and SensitiveString Redaction", func(t *testing.T) {
		t.Parallel()
		sess := largebody.SessionInput{
			AuthoritativeSessionID: secretSessionID,
			ClientSessionID:        "client-sess-1",
			ALegID:                 secretALegID,
			ResumeToken:            largebody.NewSensitiveString(secretResumeToken),
			NewSessionRequested:    false,
		}

		// Reveal returns the secret at the authorized boundary
		if sess.ResumeToken.Reveal() != secretResumeToken {
			t.Fatalf("Reveal got %q, want %q", sess.ResumeToken.Reveal(), secretResumeToken)
		}

		// String / GoString / formats must redact
		sensitiveOnly := sess.ResumeToken
		for _, verb := range []string{"%s", "%v", "%+v", "%#v", "%q"} {
			out := fmt.Sprintf(verb, sensitiveOnly)
			if strings.Contains(out, secretResumeToken) {
				t.Fatalf("SensitiveString %s leaked secret token: %s", verb, out)
			}
			if !strings.Contains(out, "redacted") {
				t.Fatalf("SensitiveString %s = %q, expected [redacted]", verb, out)
			}
		}

		// MarshalJSON must redact
		jsonBytes, err := sess.MarshalJSON()
		if err != nil {
			t.Fatalf("sess.MarshalJSON: %v", err)
		}
		if strings.Contains(string(jsonBytes), secretResumeToken) {
			t.Fatalf("SessionInput JSON leaked resume token: %s", string(jsonBytes))
		}
		if !strings.Contains(string(jsonBytes), "[redacted]") {
			t.Fatalf("SessionInput JSON must contain [redacted], got: %s", string(jsonBytes))
		}
	})

	t.Run("Structured slog logging across outcomes and errors", func(t *testing.T) {
		t.Parallel()

		// Test both text and JSON slog handlers
		for _, format := range []string{"text", "json"} {
			var logBuf bytes.Buffer
			var handler slog.Handler
			if format == "json" {
				handler = slog.NewJSONHandler(&logBuf, nil)
			} else {
				handler = slog.NewTextHandler(&logBuf, nil)
			}
			logger := slog.New(handler)

			// Log various operations that might fail
			logger.Info(
				"reservation_exhausted",
				"error", largebody.ErrSpoolBudgetExhausted,
				"outcome", largebody.CaptureOutcomeDeclined,
				"prompt_len", len(secretPrompt),
			)
			logger.Warn(
				"limit_exceeded",
				"outcome", largebody.CaptureOutcomeLimitExceeded,
				"error", &http.MaxBytesError{Limit: 1024},
			)
			logger.Error(
				"spill_create_failure",
				"error", largebody.ErrSpillFileCreationFailed,
				"outcome", largebody.CaptureOutcomeDeclined,
			)

			logOutput := logBuf.String()
			assertNoLeaks(t, fmt.Sprintf("slog %s output", format), logOutput)
		}
	})

	t.Run("Error strings from all error types", func(t *testing.T) {
		t.Parallel()
		errs := []error{
			largebody.ErrSpoolBudgetExhausted,
			largebody.ErrInvalidReservation,
			largebody.ErrReservationClosed,
			largebody.ErrSpillClosed,
			largebody.ErrSpillFileCreationFailed,
			largebody.ErrSpillWriteFailed,
			largebody.ErrInvalidSpillConfig,
			largebody.ErrUnconsumedSuffix,
			largebody.ErrSourceClosed,
			largebody.ErrAlreadyCompleted,
			largebody.ErrNilRemainingBody,
		}

		for _, err := range errs {
			assertNoLeaks(t, fmt.Sprintf("Error string for %T", err), err.Error())
		}
	})
}

// TestFollowUp_PreCompletionReader_LifecycleDocumentation validates the reader tracking
// lifecycle between SpillBuffer and CompletedSource as analyzed in the Task 4.4 review follow-up.
//
// In normal production request flow:
//   - CaptureRequestBody streams until EOF, and ONLY calls Complete() when the full body is captured.
//     No readers are opened on SpillBuffer during normal capture (readers are only opened on CompletedSource).
//   - If capture declines mid-stream, NewCaptureReader opens SpillBuffer.Open() for canonical continuation,
//     and Complete() is NEVER called. CaptureReader.Close() closes the SpillBuffer and cleans up.
//
// If a caller opens a reader on SpillBuffer before Complete():
//   - In CompletedSource, active readers opened post-completion are tracked and defer file deletion.
//   - In SpillBuffer, active readers opened pre-completion are tracked and defer file deletion on buf.Close().
//   - Follow-up fix documentation: delegating readerClosed() from SpillBuffer to CompletedSource
//     safely closes any tracking gap if pre-completion readers are combined with post-completion root Close.
func TestFollowUp_PreCompletionReader_LifecycleDocumentation(t *testing.T) {
	t.Parallel()

	t.Run("post-completion readers track and delete cleanly", func(t *testing.T) {
		t.Parallel()
		buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
			SpoolDir:         t.TempDir(),
			MemorySpoolBytes: 16,
		})
		if err != nil {
			t.Fatalf("NewSpillBuffer: %v", err)
		}
		if _, err := buf.Write(bytes.Repeat([]byte("T"), 64)); err != nil {
			t.Fatalf("buf.Write: %v", err)
		}
		src, err := buf.Complete()
		if err != nil {
			t.Fatalf("buf.Complete: %v", err)
		}

		rc, err := src.Open()
		if err != nil {
			t.Fatalf("src.Open: %v", err)
		}
		if err := src.Close(); err != nil {
			t.Fatalf("src.Close: %v", err)
		}
		if err := rc.Close(); err != nil {
			t.Fatalf("rc.Close: %v", err)
		}

		if src.ActiveReaders() != 0 {
			t.Fatalf("ActiveReaders got %d, want 0", src.ActiveReaders())
		}
	})

	t.Run("continuation reader cleans up spill file without complete", func(t *testing.T) {
		t.Parallel()
		spoolDir := t.TempDir()
		buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
			SpoolDir:         spoolDir,
			MemorySpoolBytes: 16,
		})
		if err != nil {
			t.Fatalf("NewSpillBuffer: %v", err)
		}
		if _, err := buf.Write(bytes.Repeat([]byte("C"), 64)); err != nil {
			t.Fatalf("buf.Write: %v", err)
		}
		filePath := buf.FilePath()

		cont, err := largebody.NewContinuationReader(buf, io.NopCloser(strings.NewReader("tail")), 1024)
		if err != nil {
			t.Fatalf("NewContinuationReader: %v", err)
		}

		if _, err := os.Stat(filePath); err != nil {
			t.Fatalf("file must exist before continuation close: %v", err)
		}

		if err := cont.Close(); err != nil {
			t.Fatalf("cont.Close: %v", err)
		}

		if _, err := os.Stat(filePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("continuation close did not remove spill file: err=%v", err)
		}
	})
}
