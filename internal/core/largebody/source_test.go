package largebody_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// TestCompletedSource_MemoryOnly verifies that an immutable in-memory completed source
// provides fresh offset-zero readers and idempotent close (Requirements 10.1, 10.2, 20.9).
func TestCompletedSource_MemoryOnly(t *testing.T) {
	t.Parallel()

	payload := []byte("hello completed in-memory source")
	src := largebody.NewMemorySource(payload)

	if src.Size() != int64(len(payload)) {
		t.Fatalf("Size got %d, want %d", src.Size(), len(payload))
	}
	if src.HasSpilled() {
		t.Fatal("expected HasSpilled to be false for memory source")
	}
	if src.FilePath() != "" {
		t.Fatalf("expected empty FilePath, got %q", src.FilePath())
	}
	if !bytes.Equal(src.MemoryBytes(), payload) {
		t.Fatalf("MemoryBytes mismatch: got %q, want %q", src.MemoryBytes(), payload)
	}

	// First reader
	r1, err := src.Open()
	if err != nil {
		t.Fatalf("Open r1 failed: %v", err)
	}
	defer func() { _ = r1.Close() }()

	data1, err := io.ReadAll(r1)
	if err != nil {
		t.Fatalf("ReadAll r1 failed: %v", err)
	}
	if !bytes.Equal(data1, payload) {
		t.Fatalf("r1 data mismatch: got %q, want %q", data1, payload)
	}

	// Second reader: fresh offset-zero reader (Requirement 10.1, 10.2)
	r2, err := src.Open()
	if err != nil {
		t.Fatalf("Open r2 failed: %v", err)
	}
	defer func() { _ = r2.Close() }()

	data2, err := io.ReadAll(r2)
	if err != nil {
		t.Fatalf("ReadAll r2 failed: %v", err)
	}
	if !bytes.Equal(data2, payload) {
		t.Fatalf("r2 data mismatch: got %q, want %q", data2, payload)
	}

	// Close root source
	if err := src.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !src.IsClosed() {
		t.Fatal("expected IsClosed to be true")
	}

	// Idempotent close
	if err := src.Close(); err != nil {
		t.Fatalf("subsequent Close failed: %v", err)
	}

	// Open after close fails with ErrSourceClosed
	if _, err := src.Open(); !errors.Is(err, largebody.ErrSourceClosed) {
		t.Fatalf("Open after Close got %v, want ErrSourceClosed", err)
	}
}

// TestCompletedSource_SpillToFile_ParallelIndependentReaders verifies that multiple
// parallel readers read independently from offset zero without race or cross-talk
// (Requirements 10.1, 10.3, 10.6; design section 5).
func TestCompletedSource_SpillToFile_ParallelIndependentReaders(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	filePath := filepath.Join(spoolDir, "test_completed_spill.tmp")

	// 512 bytes payload: 128 in memory, 384 on disk
	payload := make([]byte, 512)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand.Read failed: %v", err)
	}

	memPrefix := payload[:128]
	diskSuffix := payload[128:]

	if err := os.WriteFile(filePath, diskSuffix, 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
		Memory:   memPrefix,
		FilePath: filePath,
		Size:     int64(len(payload)),
	})
	if err != nil {
		t.Fatalf("NewCompletedSource failed: %v", err)
	}
	defer func() { _ = src.Close() }()

	if src.Size() != int64(len(payload)) {
		t.Fatalf("Size got %d, want %d", src.Size(), len(payload))
	}
	if !src.HasSpilled() {
		t.Fatal("expected HasSpilled to be true")
	}
	if src.FilePath() != filePath {
		t.Fatalf("FilePath got %q, want %q", src.FilePath(), filePath)
	}

	// Launch 10 parallel readers
	const numReaders = 10
	var wg sync.WaitGroup
	errs := make(chan error, numReaders)

	for i := range numReaders {
		wg.Add(1)
		go func(readerIdx int) {
			defer wg.Done()

			rc, err := src.Open()
			if err != nil {
				errs <- fmt.Errorf("reader %d: Open failed: %w", readerIdx, err)
				return
			}
			defer func() { _ = rc.Close() }()

			// Read chunk by chunk with varied buffer sizes to test independence
			bufSize := 17 + (readerIdx*11)%64
			buf := make([]byte, bufSize)
			var readBytes []byte

			for {
				n, rErr := rc.Read(buf)
				if n > 0 {
					readBytes = append(readBytes, buf[:n]...)
				}
				if rErr != nil {
					if errors.Is(rErr, io.EOF) {
						break
					}
					errs <- fmt.Errorf("reader %d: Read error: %w", readerIdx, rErr)
					return
				}
			}

			if !bytes.Equal(readBytes, payload) {
				errs <- fmt.Errorf("reader %d: data mismatch, read %d bytes", readerIdx, len(readBytes))
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

// TestCompletedSource_RetrySecondAttempt verifies that retrying an operation obtains
// a completely fresh offset-zero reader and closing a failed first attempt does not
// affect the second attempt (Requirements 10.2, 10.6).
func TestCompletedSource_RetrySecondAttempt(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 32,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}

	payload := []byte("attempt-1-failed-retry-attempt-2-succeeded-completely-with-identical-bytes")
	if _, err := buf.Write(payload); err != nil {
		t.Fatalf("buf.Write: %v", err)
	}

	src, err := buf.Complete()
	if err != nil {
		t.Fatalf("buf.Complete: %v", err)
	}
	defer func() { _ = src.Close() }()

	// Attempt 1: read partially (simulate connection failure mid-stream), then close
	r1, err := src.Open()
	if err != nil {
		t.Fatalf("attempt 1 Open: %v", err)
	}
	firstPart := make([]byte, 15)
	n1, err := io.ReadFull(r1, firstPart)
	if err != nil || n1 != 15 {
		t.Fatalf("attempt 1 ReadFull: n=%d, err=%v", n1, err)
	}
	// Simulate failover / attempt failure: close r1
	if err := r1.Close(); err != nil {
		t.Fatalf("attempt 1 Close: %v", err)
	}

	// Attempt 2: retry must obtain a fresh offset-zero reader and read full payload (Requirement 10.2)
	r2, err := src.Open()
	if err != nil {
		t.Fatalf("attempt 2 Open: %v", err)
	}
	defer func() { _ = r2.Close() }()

	fullData, err := io.ReadAll(r2)
	if err != nil {
		t.Fatalf("attempt 2 ReadAll: %v", err)
	}
	if !bytes.Equal(fullData, payload) {
		t.Fatalf("attempt 2 data mismatch: got %q, want %q", fullData, payload)
	}
}

// TestCompletedSource_PendingDeletionOnActiveReaders_WindowsSafe verifies that root Close()
// is nonblocking and idempotent when readers are active, marks deletion pending, and final
// removal of the spill file occurs only after all readers close (Requirements 20.9, 20.10).
func TestCompletedSource_PendingDeletionOnActiveReaders_WindowsSafe(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 16,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}

	payload := bytes.Repeat([]byte("W"), 256)
	if _, err := buf.Write(payload); err != nil {
		t.Fatalf("buf.Write: %v", err)
	}

	src, err := buf.Complete()
	if err != nil {
		t.Fatalf("buf.Complete: %v", err)
	}

	filePath := src.FilePath()
	if filePath == "" {
		t.Fatal("expected spill file to exist")
	}

	// Open reader 1 and reader 2
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

	// Root close while readers are active (must be nonblocking and return immediately; Requirement 20.9)
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- src.Close()
	}()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked on active readers (must be nonblocking)")
	}

	if !src.IsClosed() {
		t.Fatal("expected IsClosed to be true")
	}
	if !src.IsDeletePending() {
		t.Fatal("expected IsDeletePending to be true")
	}

	// File MUST still exist on disk because readers are open (Windows open-file protection)
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("spill file missing while readers active: %v", err)
	}

	// Readers must still be able to read completely to EOF
	data1, err := io.ReadAll(r1)
	if err != nil {
		t.Fatalf("r1 ReadAll failed: %v", err)
	}
	if !bytes.Equal(data1, payload) {
		t.Fatalf("r1 payload mismatch")
	}

	// Close reader 1 (reader 2 is still active)
	if err := r1.Close(); err != nil {
		t.Fatalf("r1 Close: %v", err)
	}
	if src.ActiveReaders() != 1 {
		t.Fatalf("ActiveReaders after r1 close got %d, want 1", src.ActiveReaders())
	}
	// File must STILL exist because reader 2 is active
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("spill file deleted prematurely while r2 active: %v", err)
	}

	// Read from reader 2
	data2, err := io.ReadAll(r2)
	if err != nil {
		t.Fatalf("r2 ReadAll failed: %v", err)
	}
	if !bytes.Equal(data2, payload) {
		t.Fatalf("r2 payload mismatch")
	}

	// Close reader 2: this triggers final deletion!
	if err := r2.Close(); err != nil {
		t.Fatalf("r2 Close: %v", err)
	}
	if src.ActiveReaders() != 0 {
		t.Fatalf("ActiveReaders after r2 close got %d, want 0", src.ActiveReaders())
	}

	// File MUST now be removed from disk
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("expected file removed from disk, but stat err=%v", err)
	}
}

// TestCompletedSource_ImmediateDeletion_NoActiveReaders verifies that closing a completed
// source with no active readers removes the file immediately from disk (Requirement 20.9).
func TestCompletedSource_ImmediateDeletion_NoActiveReaders(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 8,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}

	payload := []byte("excess data spilled to disk for immediate deletion test")
	if _, err := buf.Write(payload); err != nil {
		t.Fatalf("buf.Write: %v", err)
	}

	src, err := buf.Complete()
	if err != nil {
		t.Fatalf("buf.Complete: %v", err)
	}

	filePath := src.FilePath()
	if filePath == "" {
		t.Fatal("expected spill file to exist")
	}

	// Close immediately without opening any readers
	if err := src.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Verify file is immediately removed
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("expected file to be removed immediately, stat err=%v", err)
	}
}

// TestCompletedSource_LeakedReader_NonblockingClose verifies that if a reader leaks (is never closed),
// root Close() does not deadlock waiting for it (Requirement 20.9).
func TestCompletedSource_LeakedReader_NonblockingClose(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 8,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}

	if _, err := buf.Write([]byte("some spilled payload data")); err != nil {
		t.Fatalf("buf.Write: %v", err)
	}

	src, err := buf.Complete()
	if err != nil {
		t.Fatalf("buf.Complete: %v", err)
	}

	// Open a reader that we intentionally leak
	leakedReader, err := src.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = leakedReader // intentionally not calling Close()

	// Root close must NOT deadlock (Requirement 20.9)
	done := make(chan error, 1)
	go func() {
		done <- src.Close()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close deadlocked on leaked reader")
	}

	if !src.IsClosed() {
		t.Fatal("expected IsClosed to be true")
	}
	if !src.IsDeletePending() {
		t.Fatal("expected IsDeletePending to be true")
	}

	// Clean up the leaked reader so file can be removed
	_ = leakedReader.Close()
}

// TestCompletedSource_FaultInjection_OpenFileFailure verifies proper error propagation
// and reader count stability when OpenFile fails (Requirement 20.10).
func TestCompletedSource_FaultInjection_OpenFileFailure(t *testing.T) {
	t.Parallel()

	injectedErr := errors.New("injected open file error")
	src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
		FilePath: "some_file.tmp",
		Size:     100,
		OpenFile: func(path string) (io.ReadCloser, error) {
			return nil, injectedErr
		},
	})
	if err != nil {
		t.Fatalf("NewCompletedSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	_, err = src.Open()
	if !errors.Is(err, injectedErr) {
		t.Fatalf("Open got %v, want injectedErr", err)
	}
	if src.ActiveReaders() != 0 {
		t.Fatalf("ActiveReaders got %d, want 0 after failed open", src.ActiveReaders())
	}
}

// TestCompletedSource_FaultInjection_RemoveFileFailure verifies that RemoveFile errors
// are propagated on Close() (Requirement 20.10).
func TestCompletedSource_FaultInjection_RemoveFileFailure(t *testing.T) {
	t.Parallel()

	injectedErr := errors.New("injected remove file error")
	src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
		FilePath: "some_file.tmp",
		Size:     100,
		RemoveFile: func(path string) error {
			return injectedErr
		},
	})
	if err != nil {
		t.Fatalf("NewCompletedSource: %v", err)
	}

	err = src.Close()
	if !errors.Is(err, injectedErr) {
		t.Fatalf("Close got %v, want injectedErr", err)
	}
}

// TestCompletedSource_Confidentiality verifies String() does not leak prompt content,
// model names, or filesystem paths (Requirement 20.3).
func TestCompletedSource_Confidentiality(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 8,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}

	secretPrompt := "super-secret-user-prompt-must-not-be-in-logs"
	if _, err := buf.Write([]byte(secretPrompt)); err != nil {
		t.Fatalf("buf.Write: %v", err)
	}

	src, err := buf.Complete()
	if err != nil {
		t.Fatalf("buf.Complete: %v", err)
	}
	defer func() { _ = src.Close() }()

	str := fmt.Sprint(src)
	if bytes.Contains([]byte(str), []byte(secretPrompt)) {
		t.Fatalf("String() leaks prompt: %s", str)
	}
	if bytes.Contains([]byte(str), []byte(spoolDir)) {
		t.Fatalf("String() leaks spoolDir: %s", str)
	}
	if src.FilePath() != "" && bytes.Contains([]byte(str), []byte(src.FilePath())) {
		t.Fatalf("String() leaks FilePath: %s", str)
	}
}

// TestSpillBuffer_Complete_TransitionsToImmutableSource verifies that calling Complete()
// on SpillBuffer seals it against further writes and transfers ownership to CompletedSource.
func TestSpillBuffer_Complete_TransitionsToImmutableSource(t *testing.T) {
	t.Parallel()

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 32,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}

	payload := []byte("spill buffer payload to complete")
	if _, err := buf.Write(payload); err != nil {
		t.Fatalf("buf.Write: %v", err)
	}

	src, err := buf.Complete()
	if err != nil {
		t.Fatalf("buf.Complete: %v", err)
	}
	defer func() { _ = src.Close() }()

	// Subsequent Write on buf MUST fail (immutable completed source; Requirement 10.1)
	if _, err := buf.Write([]byte("cannot write after complete")); !errors.Is(err, largebody.ErrAlreadyCompleted) && !errors.Is(err, largebody.ErrSpillClosed) {
		t.Fatalf("Write after Complete got %v, want ErrAlreadyCompleted or ErrSpillClosed", err)
	}

	// Subsequent Complete on buf MUST fail
	if _, err := buf.Complete(); !errors.Is(err, largebody.ErrAlreadyCompleted) && !errors.Is(err, largebody.ErrSpillClosed) {
		t.Fatalf("Complete after Complete got %v, want ErrAlreadyCompleted", err)
	}

	// Open on buf still succeeds by delegating to CompletedSource
	rc, err := buf.Open()
	if err != nil {
		t.Fatalf("buf.Open after Complete: %v", err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("data mismatch")
	}
}

// TestCaptureRequestBody_CompletesToSource verifies that CaptureRequestBody sets
// Source on CaptureResult when capture completes to EOF.
func TestCaptureRequestBody_CompletesToSource(t *testing.T) {
	t.Parallel()

	payload := []byte("full body captured to EOF")
	body := io.NopCloser(bytes.NewReader(payload))

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 16,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	result := largebody.CaptureRequestBody(body, buf, largebody.CaptureConfig{
		MaxBytes: 1024,
	})

	if result.Outcome != largebody.CaptureOutcomeCompleted {
		t.Fatalf("expected CaptureOutcomeCompleted, got %v (err=%v)", result.Outcome, result.Err)
	}
	if result.Source == nil {
		t.Fatal("expected non-nil Source on CaptureResult upon completion")
	}
	if result.Source.Size() != int64(len(payload)) {
		t.Fatalf("Source.Size got %d, want %d", result.Source.Size(), len(payload))
	}

	rc, err := result.Source.Open()
	if err != nil {
		t.Fatalf("Source.Open: %v", err)
	}
	defer func() { _ = rc.Close() }()

	captured, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(captured, payload) {
		t.Fatalf("captured mismatch: got %q, want %q", captured, payload)
	}
}
