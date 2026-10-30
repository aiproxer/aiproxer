package largebody_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// TestSpillBuffer_MemoryOnly verifies that writes within the MemorySpoolBytes ceiling
// remain entirely in memory with no spill file created (Requirements 20.1, 20.2; design section 5).
func TestSpillBuffer_MemoryOnly(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	cfg := largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 1024, // 1 KiB ceiling
		CopyBufferSize:   512,
	}

	buf, err := largebody.NewSpillBuffer(cfg)
	if err != nil {
		t.Fatalf("NewSpillBuffer failed: %v", err)
	}
	defer func() { _ = buf.Close() }()

	payload := []byte("hello, bounded memory spool!")
	n, err := buf.Write(payload)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Write returned %d, want %d", n, len(payload))
	}

	if buf.HasSpilled() {
		t.Fatal("expected buffer NOT to have spilled")
	}
	if buf.FilePath() != "" {
		t.Fatalf("expected empty FilePath, got %q", buf.FilePath())
	}
	if buf.FileBytes() != 0 {
		t.Fatalf("expected 0 FileBytes, got %d", buf.FileBytes())
	}
	if buf.BytesWritten() != int64(len(payload)) {
		t.Fatalf("BytesWritten got %d, want %d", buf.BytesWritten(), len(payload))
	}
	if buf.Size() != int64(len(payload)) {
		t.Fatalf("Size got %d, want %d", buf.Size(), len(payload))
	}
	if !bytes.Equal(buf.MemoryBytes(), payload) {
		t.Fatalf("MemoryBytes mismatch: got %q, want %q", buf.MemoryBytes(), payload)
	}
	if buf.HasUnwrittenSuffix() {
		t.Fatalf("expected no unwritten suffix, got %q", buf.UnwrittenSuffix())
	}

	// Read back via Open()
	rc, err := buf.Open()
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = rc.Close() }()

	readData, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(readData, payload) {
		t.Fatalf("Read data mismatch: got %q, want %q", readData, payload)
	}
}

// TestSpillBuffer_SpillToFile verifies that writing beyond MemorySpoolBytes spills excess
// bytes to an unpredictable temporary file with restrictive permissions (Requirements 20.1, 20.2).
func TestSpillBuffer_SpillToFile(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	memCeiling := int64(64) // 64 bytes memory ceiling
	cfg := largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: memCeiling,
		CopyBufferSize:   128,
	}

	buf, err := largebody.NewSpillBuffer(cfg)
	if err != nil {
		t.Fatalf("NewSpillBuffer failed: %v", err)
	}
	defer func() { _ = buf.Close() }()

	// Write 100 bytes: 64 to memory, 36 to spill file
	payload := make([]byte, 100)
	for i := range payload {
		payload[i] = byte(i)
	}

	n, err := buf.Write(payload)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Write returned %d, want %d", n, len(payload))
	}

	if !buf.HasSpilled() {
		t.Fatal("expected buffer to have spilled to file")
	}
	filePath := buf.FilePath()
	if filePath == "" {
		t.Fatal("expected non-empty FilePath")
	}

	// Verify file path is within spoolDir
	rel, err := filepath.Rel(spoolDir, filePath)
	if err != nil || filepath.IsAbs(rel) || rel == ".." {
		t.Fatalf("filePath %q is not within spoolDir %q", filePath, spoolDir)
	}

	// Verify filename pattern: aip_spill_<hex32>.tmp
	fileName := filepath.Base(filePath)
	matched, err := regexp.MatchString(`^aip_spill_[0-9a-f]{32}\.tmp$`, fileName)
	if err != nil || !matched {
		t.Fatalf("fileName %q does not match unpredictable hex pattern", fileName)
	}

	// Check restrictive permissions (0600) on non-Windows
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(filePath)
		if statErr != nil {
			t.Fatalf("Stat failed: %v", statErr)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("spill file permissions got %04o, want 0600", perm)
		}
	}

	if int64(len(buf.MemoryBytes())) != memCeiling {
		t.Fatalf("MemoryBytes len got %d, want %d", len(buf.MemoryBytes()), memCeiling)
	}
	if !bytes.Equal(buf.MemoryBytes(), payload[:memCeiling]) {
		t.Fatalf("MemoryBytes content mismatch")
	}
	if buf.FileBytes() != 36 {
		t.Fatalf("FileBytes got %d, want 36", buf.FileBytes())
	}
	if buf.BytesWritten() != 100 {
		t.Fatalf("BytesWritten got %d, want 100", buf.BytesWritten())
	}
	if buf.HasUnwrittenSuffix() {
		t.Fatalf("expected no unwritten suffix, got %q", buf.UnwrittenSuffix())
	}

	// Read back complete payload via Open()
	rc, err := buf.Open()
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = rc.Close() }()

	readData, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(readData, payload) {
		t.Fatalf("Read data mismatch")
	}
}

// TestSpillBuffer_ParallelIndependentReaders verifies that multiple Open() readers
// can read independently from offset zero without interference (design section 5, Task 4.4 prep).
func TestSpillBuffer_ParallelIndependentReaders(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	cfg := largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 32,
	}

	buf, err := largebody.NewSpillBuffer(cfg)
	if err != nil {
		t.Fatalf("NewSpillBuffer failed: %v", err)
	}
	defer func() { _ = buf.Close() }()

	payload := make([]byte, 256)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand.Read failed: %v", err)
	}
	if _, err := buf.Write(payload); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	var wg sync.WaitGroup
	for i := range 5 {
		wg.Add(1)
		go func(readerIdx int) {
			defer wg.Done()
			rc, err := buf.Open()
			if err != nil {
				t.Errorf("reader %d: Open failed: %v", readerIdx, err)
				return
			}
			defer func() { _ = rc.Close() }()

			got, err := io.ReadAll(rc)
			if err != nil {
				t.Errorf("reader %d: ReadAll failed: %v", readerIdx, err)
				return
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("reader %d: data mismatch", readerIdx)
			}
		}(i)
	}
	wg.Wait()
}

// TestSpillBuffer_ReservationAccounting verifies integration with SpoolReservation from Task 4.1
// (Requirements 20.4, 20.5, 20.6; design section 5).
func TestSpillBuffer_ReservationAccounting(t *testing.T) {
	t.Parallel()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      128,
		MaxInflightSpoolBytes: 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	res, err := ledger.BeginReservation()
	if err != nil {
		t.Fatalf("BeginReservation failed: %v", err)
	}

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 128,
		Reservation:      res,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer failed: %v", err)
	}
	defer func() { _ = buf.Close() }()

	// Write 64 bytes (fits in memory)
	chunk1 := bytes.Repeat([]byte("A"), 64)
	if _, err := buf.Write(chunk1); err != nil {
		t.Fatalf("Write chunk1 failed: %v", err)
	}
	if res.ReservedBytes() != 64 {
		t.Fatalf("ReservedBytes got %d, want 64", res.ReservedBytes())
	}
	if res.MemoryBytes() != 64 {
		t.Fatalf("MemoryBytes got %d, want 64", res.MemoryBytes())
	}
	if res.SpillBytes() != 0 {
		t.Fatalf("SpillBytes got %d, want 0", res.SpillBytes())
	}

	// Write 128 bytes more (total 192 bytes: 128 memory + 64 spill)
	chunk2 := bytes.Repeat([]byte("B"), 128)
	if _, err := buf.Write(chunk2); err != nil {
		t.Fatalf("Write chunk2 failed: %v", err)
	}
	if res.ReservedBytes() != 192 {
		t.Fatalf("ReservedBytes got %d, want 192", res.ReservedBytes())
	}
	if res.MemoryBytes() != 128 {
		t.Fatalf("MemoryBytes got %d, want 128", res.MemoryBytes())
	}
	if res.SpillBytes() != 64 {
		t.Fatalf("SpillBytes got %d, want 64", res.SpillBytes())
	}

	// Shrink reservation down to actual committed bytes (already 192, should succeed)
	if err := buf.ShrinkReservation(); err != nil {
		t.Fatalf("ShrinkReservation failed: %v", err)
	}

	// Close buffer should release reservation
	if err := buf.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if res.ReservedBytes() != 0 {
		t.Fatalf("ReservedBytes after close got %d, want 0", res.ReservedBytes())
	}
	if ledger.InflightBytes() != 0 {
		t.Fatalf("InflightBytes after close got %d, want 0", ledger.InflightBytes())
	}
}

// TestSpillBuffer_ReservationExhaustion_PreservesUnwrittenSuffix verifies that when the
// spool reservation is exhausted, the write fails with ErrSpoolBudgetExhausted and the
// current chunk is retained in UnwrittenSuffix (Requirement 20.6; design section 5).
func TestSpillBuffer_ReservationExhaustion_PreservesUnwrittenSuffix(t *testing.T) {
	t.Parallel()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64,
		MaxInflightSpoolBytes: 100, // strictly capped at 100 bytes
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	res, err := ledger.BeginReservation()
	if err != nil {
		t.Fatalf("BeginReservation failed: %v", err)
	}

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 64,
		Reservation:      res,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer failed: %v", err)
	}
	defer func() { _ = buf.Close() }()

	// Write 60 bytes successfully
	chunk1 := bytes.Repeat([]byte("X"), 60)
	if _, err := buf.Write(chunk1); err != nil {
		t.Fatalf("Write chunk1 failed: %v", err)
	}

	// Now try to write 50 bytes (60 + 50 = 110 > 100 budget limit)
	chunk2 := bytes.Repeat([]byte("Y"), 50)
	n, err := buf.Write(chunk2)
	if err == nil {
		t.Fatal("expected ErrSpoolBudgetExhausted, got nil error")
	}
	if !errors.Is(err, largebody.ErrSpoolBudgetExhausted) {
		t.Fatalf("expected ErrSpoolBudgetExhausted, got: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 bytes written, got %d", n)
	}

	// Invariant check (Requirement 20.6):
	// The unwritten chunk2 MUST be preserved intact!
	if !buf.HasUnwrittenSuffix() {
		t.Fatal("expected HasUnwrittenSuffix to be true")
	}
	suffix := buf.UnwrittenSuffix()
	if !bytes.Equal(suffix, chunk2) {
		t.Fatalf("UnwrittenSuffix mismatch: got %d bytes, want %d bytes", len(suffix), len(chunk2))
	}

	// Previous successfully committed bytes remain intact
	if buf.BytesWritten() != 60 {
		t.Fatalf("BytesWritten got %d, want 60", buf.BytesWritten())
	}
	if !bytes.Equal(buf.MemoryBytes(), chunk1) {
		t.Fatalf("MemoryBytes mismatch after failed write")
	}

	// TakeUnwrittenSuffix hands over ownership and clears it
	taken := buf.TakeUnwrittenSuffix()
	if !bytes.Equal(taken, chunk2) {
		t.Fatalf("TakeUnwrittenSuffix mismatch")
	}
	if buf.HasUnwrittenSuffix() {
		t.Fatal("expected HasUnwrittenSuffix to be false after TakeUnwrittenSuffix")
	}
}

// TestSpillBuffer_ShortWrite_PreservesUnwrittenSuffix verifies that if a write to the spill file
// is short or partially fails, the unwritten suffix is preserved (Requirement 20.6, 20.10).
func TestSpillBuffer_ShortWrite_PreservesUnwrittenSuffix(t *testing.T) {
	t.Parallel()

	// Fault injector mock file
	mockFile := &mockFaultFile{
		maxWrites: 10, // allows writing only 10 bytes before failing
	}

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 0, // spill immediately
		CreateFile: func(dir string) (largebody.SpillFile, string, error) {
			return mockFile, filepath.Join(dir, "mock_spill.tmp"), nil
		},
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer failed: %v", err)
	}
	defer func() { _ = buf.Close() }()

	chunk := []byte("0123456789ABCDEFGHIJ") // 20 bytes
	n, err := buf.Write(chunk)
	if err == nil {
		t.Fatal("expected write error from mockFile, got nil")
	}
	if n != 10 {
		t.Fatalf("expected 10 bytes written, got %d", n)
	}

	// Verify unwritten suffix contains chunk[10:]
	if !buf.HasUnwrittenSuffix() {
		t.Fatal("expected HasUnwrittenSuffix to be true")
	}
	suffix := buf.UnwrittenSuffix()
	expectedSuffix := []byte("ABCDEFGHIJ")
	if !bytes.Equal(suffix, expectedSuffix) {
		t.Fatalf("UnwrittenSuffix got %q, want %q", suffix, expectedSuffix)
	}
	if buf.BytesWritten() != 10 {
		t.Fatalf("BytesWritten got %d, want 10", buf.BytesWritten())
	}
}

// TestSpillBuffer_FileCreateFailure_PreservesUnwrittenSuffix verifies that when creating
// the spill file fails, any bytes not fitting into memory are preserved in UnwrittenSuffix.
func TestSpillBuffer_FileCreateFailure_PreservesUnwrittenSuffix(t *testing.T) {
	t.Parallel()

	memCeiling := int64(10)
	createErr := errors.New("injected create error")

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: memCeiling,
		CreateFile: func(dir string) (largebody.SpillFile, string, error) {
			return nil, "", createErr
		},
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer failed: %v", err)
	}
	defer func() { _ = buf.Close() }()

	payload := []byte("0123456789EXCESS_BYTES") // 10 memory bytes + 12 excess
	n, err := buf.Write(payload)
	if err == nil {
		t.Fatal("expected create error, got nil")
	}
	if !errors.Is(err, createErr) && !errors.Is(err, largebody.ErrSpillFileCreationFailed) {
		t.Fatalf("expected create error, got %v", err)
	}
	if int64(n) != memCeiling {
		t.Fatalf("expected %d bytes written into memory, got %d", memCeiling, n)
	}

	// Memory has first 10 bytes
	if !bytes.Equal(buf.MemoryBytes(), []byte("0123456789")) {
		t.Fatalf("MemoryBytes got %q, want '0123456789'", buf.MemoryBytes())
	}

	// Unwritten suffix has remaining 12 bytes
	suffix := buf.UnwrittenSuffix()
	if !bytes.Equal(suffix, []byte("EXCESS_BYTES")) {
		t.Fatalf("UnwrittenSuffix got %q, want 'EXCESS_BYTES'", suffix)
	}
}

// TestSpillBuffer_ReadFrom_FixedCopyBuffer verifies that ReadFrom streams from an io.Reader
// using a fixed reusable copy buffer without allocating a payload-growing buffer (Requirement 20.1).
func TestSpillBuffer_ReadFrom_FixedCopyBuffer(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	cfg := largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 128,
		CopyBufferSize:   64, // fixed 64-byte reusable buffer
	}

	buf, err := largebody.NewSpillBuffer(cfg)
	if err != nil {
		t.Fatalf("NewSpillBuffer failed: %v", err)
	}
	defer func() { _ = buf.Close() }()

	// 1024 bytes payload
	payload := make([]byte, 1024)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	src := bytes.NewReader(payload)
	n, err := buf.ReadFrom(src)
	if err != nil {
		t.Fatalf("ReadFrom failed: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("ReadFrom got %d, want %d", n, len(payload))
	}
	if buf.BytesWritten() != int64(len(payload)) {
		t.Fatalf("BytesWritten got %d, want %d", buf.BytesWritten(), len(payload))
	}

	rc, err := buf.Open()
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = rc.Close() }()

	readData, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(readData, payload) {
		t.Fatal("ReadAll payload mismatch")
	}
}

// TestSpillBuffer_PendingDeletionOnActiveReaders verifies Windows open-file deletion behavior:
// root Close() marks deletion pending while readers are active, and final removal occurs when
// the last reader closes, without blocking root Close() or deadlocking (Requirements 20.9, 20.10).
func TestSpillBuffer_PendingDeletionOnActiveReaders(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	cfg := largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 16,
	}

	buf, err := largebody.NewSpillBuffer(cfg)
	if err != nil {
		t.Fatalf("NewSpillBuffer failed: %v", err)
	}

	payload := make([]byte, 128)
	if _, err := buf.Write(payload); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	filePath := buf.FilePath()
	if filePath == "" {
		t.Fatal("expected spill file to exist")
	}

	// Open a reader
	rc, err := buf.Open()
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Close root buffer while reader is still active
	if err := buf.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// File may still exist on disk because reader is open
	// Read through reader
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll from active reader failed: %v", err)
	}
	if len(data) != len(payload) {
		t.Fatalf("got %d bytes, want %d", len(data), len(payload))
	}

	// Close the reader - this must trigger final cleanup
	if err := rc.Close(); err != nil {
		t.Fatalf("reader Close failed: %v", err)
	}

	// Verify file is now removed from disk
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("expected file to be removed after last reader closed, but stat err=%v", err)
	}
}

// TestSpillBuffer_IdempotentClose verifies that calling Close() multiple times is safe and returns nil.
func TestSpillBuffer_IdempotentClose(t *testing.T) {
	t.Parallel()

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 32,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer failed: %v", err)
	}

	if _, err := buf.Write([]byte("some data to write")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	for i := range 3 {
		if err := buf.Close(); err != nil {
			t.Fatalf("call %d: Close failed: %v", i+1, err)
		}
	}

	// Subsequent operations return ErrSpillClosed
	if _, err := buf.Write([]byte("more")); !errors.Is(err, largebody.ErrSpillClosed) {
		t.Fatalf("Write after close got err=%v, want ErrSpillClosed", err)
	}
	if _, err := buf.Open(); !errors.Is(err, largebody.ErrSpillClosed) {
		t.Fatalf("Open after close got err=%v, want ErrSpillClosed", err)
	}
}

// TestSpillBuffer_ZeroMemorySpool verifies that MemorySpoolBytes: 0 spills everything to disk immediately.
func TestSpillBuffer_ZeroMemorySpool(t *testing.T) {
	t.Parallel()

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 0,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer failed: %v", err)
	}
	defer func() { _ = buf.Close() }()

	payload := []byte("spill immediately without memory buffering")
	if _, err := buf.Write(payload); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if !buf.HasSpilled() {
		t.Fatal("expected buffer to have spilled immediately")
	}
	if len(buf.MemoryBytes()) != 0 {
		t.Fatalf("expected empty MemoryBytes, got %d", len(buf.MemoryBytes()))
	}
	if buf.FileBytes() != int64(len(payload)) {
		t.Fatalf("FileBytes got %d, want %d", buf.FileBytes(), len(payload))
	}
}

// TestSpillBuffer_Confidentiality verifies String() does not leak spool file path or body content
// (Requirement 20.3).
func TestSpillBuffer_Confidentiality(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 16,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer failed: %v", err)
	}
	defer func() { _ = buf.Close() }()

	secretPrompt := "super-secret-user-prompt-content-12345"
	if _, err := buf.Write([]byte(secretPrompt)); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	str := fmt.Sprint(buf)
	if bytes.Contains([]byte(str), []byte(secretPrompt)) {
		t.Fatalf("String() leaks secret prompt: %s", str)
	}
	if bytes.Contains([]byte(str), []byte(spoolDir)) {
		t.Fatalf("String() leaks spool dir: %s", str)
	}
	if buf.FilePath() != "" && bytes.Contains([]byte(str), []byte(buf.FilePath())) {
		t.Fatalf("String() leaks file path: %s", str)
	}
}

// mockFaultFile implements SpillFile for short writes / fault injection.
type mockFaultFile struct {
	mu        sync.Mutex
	written   []byte
	maxWrites int
	closed    bool
}

func (m *mockFaultFile) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return 0, errors.New("mock file closed")
	}

	avail := m.maxWrites - len(m.written)
	if avail <= 0 {
		return 0, errors.New("mock write quota exhausted")
	}

	toWrite := len(p)
	var err error
	if toWrite > avail {
		toWrite = avail
		err = errors.New("mock short write failure")
	}

	m.written = append(m.written, p[:toWrite]...)
	return toWrite, err
}

func (m *mockFaultFile) Read(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return 0, io.EOF
}

func (m *mockFaultFile) Seek(offset int64, whence int) (int64, error) {
	return 0, nil
}

func (m *mockFaultFile) Sync() error {
	return nil
}

func (m *mockFaultFile) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockFaultFile) Name() string {
	return "mock_fault_file.tmp"
}
