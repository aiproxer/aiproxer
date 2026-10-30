package largebody

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// DefaultCopyBufferSize is the fixed size of the reusable copy buffer (32 KiB)
	// used for streaming I/O. It bounds copy-buffer heap allocation (Requirement 20.1).
	DefaultCopyBufferSize = 32 * 1024
	// DefaultMemorySpoolBytes is the default per-capture in-memory ceiling (64 KiB)
	// before spilling excess request bytes to disk (Requirement 20.1).
	DefaultMemorySpoolBytes int64 = 64 * 1024
)

var (
	// ErrSpillClosed is returned when attempting an operation on a closed SpillBuffer.
	ErrSpillClosed = errors.New("largebody: spill buffer is closed")
	// ErrSpillFileCreationFailed is returned when creating a temporary spill file fails.
	ErrSpillFileCreationFailed = errors.New("largebody: failed to create spill file")
	// ErrSpillWriteFailed is returned when writing to the spill file fails.
	ErrSpillWriteFailed = errors.New("largebody: spill file write failed")
	// ErrInvalidSpillConfig is returned when a SpillConfig parameter violates bounds.
	ErrInvalidSpillConfig = errors.New("largebody: invalid spill configuration")
)

// ErrUnconsumedSuffix is returned when attempting to Write to a SpillBuffer that
// still holds an unwritten suffix from a previous failed or partial write.
// Callers must consume the suffix via TakeUnwrittenSuffix() before retrying new writes
// (Task 4.2 review suggestion; Requirement 20.6).
var ErrUnconsumedSuffix = errors.New("largebody: unwritten suffix must be consumed via TakeUnwrittenSuffix before new writes")

// SpillFile represents the file abstraction used for spill storage.
// It is implemented directly by *os.File in production and can be substituted
// for fault injection in tests (Requirement 20.10).
type SpillFile interface {
	io.ReadWriteCloser
	io.Seeker
	Sync() error
	Name() string
}

// SpillConfig configures bounded RAM + secure spill capture
// (Requirements 20; design section 5).
type SpillConfig struct {
	// SpoolDir is the directory where temporary spill files are created.
	// If empty, the OS default temp directory is used (Requirement 20.2;
	// config.LargePayloadFastPathConfig.EffectiveSpoolDir).
	SpoolDir string

	// MemorySpoolBytes bounds retained request bytes in Go heap before spilling
	// excess bytes to secondary storage. If 0, all data is spilled directly to disk.
	// Must be >= 0 (Requirement 20.1).
	MemorySpoolBytes int64

	// CopyBufferSize is the size in bytes of the fixed reusable copy buffer
	// used for streaming reads. If <= 0, DefaultCopyBufferSize (32 KiB) is used.
	CopyBufferSize int

	// Reservation is an optional logical spool reservation used for budget
	// accounting (Task 4.1, Requirement 20.4). If nil, no reservation ledger
	// accounting is performed.
	Reservation *SpoolReservation

	// CreateFile optionally overrides spill file creation (for fault injection).
	CreateFile func(dir string) (SpillFile, string, error)

	// OpenFile optionally overrides opening the spill file for readers.
	OpenFile func(path string) (io.ReadCloser, error)

	// RemoveFile optionally overrides spill file deletion.
	RemoveFile func(path string) error

	// OnRemovalFailure is an optional zero-argument observer invoked at most
	// once per lifecycle when immediate or deferred spill removal fails
	// (Requirement 4.3). It takes no arguments on purpose: the source cannot
	// leak a filesystem path, prompt bytes, digest, model, or raw filesystem
	// error through it. The shared lifecycle carries it through Complete, so
	// capture and completed readers report through one notice. `Source` stays
	// unchanged.
	OnRemovalFailure func()
}

// SpillBuffer provides bounded RAM capture with secure temporary file spill
// (Requirements 20; design section 5).
//
// Invariants (Task 4.2; Requirements 20.1, 20.2, 20.6, 20.9, 20.10):
//   - Memory retained in Go heap is bounded by configured MemorySpoolBytes plus
//     a fixed reusable copy buffer; it never uses a payload-growing bytes.Buffer.
//   - Excess bytes spill to an unpredictable file name (crypto/rand hex, no prompt/
//     session/model metadata) with restrictive permissions (0600 where supported).
//   - Preserves the current chunk/unwritten suffix if a write fails or is short,
//     ensuring no bytes read from the client socket are discarded (Requirement 20.6).
//   - Root Close() is idempotent and nonblocking; if readers are active, deletion
//     is marked pending and removal occurs when tracked readers close (Requirement 20.9).
//   - Incrementally hashes committed writes (SHA-256) during capture writes (Task 4.5;
//     Requirements 15, 16.5, 20).
//   - Thread-safe for concurrent read/close operations.
type SpillBuffer struct {
	mu sync.Mutex

	spoolDir         string
	memorySpoolBytes int64
	copyBuf          []byte
	reservation      *SpoolReservation

	// In-memory prefix: capacity bounded by memorySpoolBytes.
	mem []byte

	// Spill file state
	file     SpillFile
	filePath string

	bytesWritten int64
	fileBytes    int64
	hasher       hash.Hash

	// Unwritten suffix from a failed or partial write (Requirement 20.6).
	unwrittenSuffix []byte

	closed          bool
	completed       bool
	completedSource *CompletedSource

	lifecycle  *spillLifecycle
	createFile func(dir string) (SpillFile, string, error)
	openFile   func(path string) (io.ReadCloser, error)
	removeFile func(path string) error
}

var (
	_ Source        = (*SpillBuffer)(nil)
	_ io.Writer     = (*SpillBuffer)(nil)
	_ io.ReaderFrom = (*SpillBuffer)(nil)
	_ io.Closer     = (*SpillBuffer)(nil)
)

// NewSpillBuffer validates configuration and constructs an initialized SpillBuffer.
func NewSpillBuffer(cfg SpillConfig) (*SpillBuffer, error) {
	if cfg.MemorySpoolBytes < 0 {
		return nil, fmt.Errorf("%w: memory_spool_bytes must be >= 0, got %d",
			ErrInvalidSpillConfig, cfg.MemorySpoolBytes)
	}

	memSpool := cfg.MemorySpoolBytes
	if memSpool == 0 && cfg.Reservation != nil && cfg.Reservation.MemorySpoolBytes() > 0 {
		memSpool = cfg.Reservation.MemorySpoolBytes()
	}

	copySize := cfg.CopyBufferSize
	if copySize <= 0 {
		copySize = DefaultCopyBufferSize
	}

	spoolDir := strings.TrimSpace(cfg.SpoolDir)
	if spoolDir == "" {
		spoolDir = os.TempDir()
	}

	createFile := cfg.CreateFile
	if createFile == nil {
		createFile = defaultCreateSpillFile
	}
	openFile := cfg.OpenFile
	if openFile == nil {
		openFile = defaultOpenSpillFile
	}
	removeFile := cfg.RemoveFile
	if removeFile == nil {
		removeFile = os.Remove
	}

	var mem []byte
	if memSpool > 0 {
		mem = make([]byte, 0, memSpool)
	}

	return &SpillBuffer{
		spoolDir:         spoolDir,
		memorySpoolBytes: memSpool,
		copyBuf:          make([]byte, copySize),
		reservation:      cfg.Reservation,
		mem:              mem,
		hasher:           sha256.New(),
		createFile:       createFile,
		openFile:         openFile,
		removeFile:       removeFile,
		lifecycle:        newSpillLifecycle("", removeFile, cfg.OnRemovalFailure),
	}, nil
}

// defaultCreateSpillFile creates an unpredictable temporary file with 0600 permissions
// (Requirement 20.2; design section 5).
func defaultCreateSpillFile(dir string) (SpillFile, string, error) {
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("%w: failed to create spool dir %q: %v",
			ErrSpillFileCreationFailed, dir, err)
	}

	for range 3 {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, "", fmt.Errorf("%w: crypto/rand read failure: %v",
				ErrSpillFileCreationFailed, err)
		}

		fileName := "aip_spill_" + hex.EncodeToString(nonce[:]) + ".tmp"
		filePath := filepath.Join(dir, fileName)

		flags := os.O_RDWR | os.O_CREATE | os.O_EXCL
		f, err := os.OpenFile(filePath, flags, 0o600)
		if err == nil {
			return f, filePath, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", fmt.Errorf("%w: %v", ErrSpillFileCreationFailed, err)
		}
	}

	return nil, "", fmt.Errorf("%w: collision after 3 attempts", ErrSpillFileCreationFailed)
}

// defaultOpenSpillFile opens an independent read-only handle to the spill file.
func defaultOpenSpillFile(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Write writes p to the bounded buffer. If within MemorySpoolBytes, p is appended
// to the in-memory buffer. Excess bytes are spilled to a secure temporary file.
//
// Invariants (Requirement 20.6):
// If the write fails or is short, the unwritten portion of p is preserved in
// UnwrittenSuffix() and can be recovered for canonical continuation.
func (b *SpillBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.completed {
		return 0, ErrAlreadyCompleted
	}
	if b.closed {
		b.setUnwrittenSuffix(p)
		return 0, ErrSpillClosed
	}
	if len(b.unwrittenSuffix) > 0 {
		return 0, ErrUnconsumedSuffix
	}
	if len(p) == 0 {
		return 0, nil
	}

	// 1. Spool reservation accounting (Task 4.1, Requirement 20.4)
	if b.reservation != nil {
		reserved := b.reservation.ReservedBytes()
		neededTotal := b.bytesWritten + int64(len(p))
		if neededTotal > reserved {
			need := neededTotal - reserved
			if err := b.reservation.ReserveMore(need); err != nil {
				b.setUnwrittenSuffix(p)
				return 0, err
			}
		}
	}

	// 2. Memory vs Spill distribution
	if b.file == nil && b.memorySpoolBytes > 0 {
		availMem := b.memorySpoolBytes - int64(len(b.mem))
		if int64(len(p)) <= availMem {
			// Entire chunk fits in memory
			b.mem = append(b.mem, p...)
			b.bytesWritten += int64(len(p))
			if b.hasher != nil {
				b.hasher.Write(p)
			}
			b.unwrittenSuffix = nil
			return len(p), nil
		}

		// Fits partially in memory: fill memory first
		if availMem > 0 {
			b.mem = append(b.mem, p[:availMem]...)
			b.bytesWritten += availMem
			if b.hasher != nil {
				b.hasher.Write(p[:availMem])
			}
		}
		toSpill := p[availMem:]

		// Create spill file for excess
		f, path, err := b.createFile(b.spoolDir)
		if err != nil {
			b.setUnwrittenSuffix(toSpill)
			return int(availMem), fmt.Errorf("%w: %v", ErrSpillFileCreationFailed, err)
		}
		b.file = f
		b.filePath = path
		b.lifecycle.setFilePath(path)

		// Write toSpill to file
		nw, err := b.file.Write(toSpill)
		if nw > 0 {
			b.bytesWritten += int64(nw)
			b.fileBytes += int64(nw)
			if b.hasher != nil {
				b.hasher.Write(toSpill[:nw])
			}
		}
		if err != nil || nw < len(toSpill) {
			unwritten := toSpill[nw:]
			b.setUnwrittenSuffix(unwritten)
			if err == nil {
				err = io.ErrShortWrite
			}
			return int(availMem) + nw, fmt.Errorf("%w: %v", ErrSpillWriteFailed, err)
		}

		b.unwrittenSuffix = nil
		return len(p), nil
	}

	// Spill directly to file (either MemorySpoolBytes == 0 or file already open)
	if b.file == nil {
		f, path, err := b.createFile(b.spoolDir)
		if err != nil {
			b.setUnwrittenSuffix(p)
			return 0, fmt.Errorf("%w: %v", ErrSpillFileCreationFailed, err)
		}
		b.file = f
		b.filePath = path
		b.lifecycle.setFilePath(path)
	}

	nw, err := b.file.Write(p)
	if nw > 0 {
		b.bytesWritten += int64(nw)
		b.fileBytes += int64(nw)
		if b.hasher != nil {
			b.hasher.Write(p[:nw])
		}
	}
	if err != nil || nw < len(p) {
		unwritten := p[nw:]
		b.setUnwrittenSuffix(unwritten)
		if err == nil {
			err = io.ErrShortWrite
		}
		return nw, fmt.Errorf("%w: %v", ErrSpillWriteFailed, err)
	}

	b.unwrittenSuffix = nil
	return len(p), nil
}

// ReadFrom reads from r until EOF or error using a fixed reusable copy buffer.
// It never allocates a payload-growing buffer (Requirement 20.1).
func (b *SpillBuffer) ReadFrom(r io.Reader) (int64, error) {
	b.mu.Lock()
	if b.completed {
		b.mu.Unlock()
		return 0, ErrAlreadyCompleted
	}
	if b.closed {
		b.mu.Unlock()
		return 0, ErrSpillClosed
	}
	buf := b.copyBuf
	b.mu.Unlock()

	var totalRead int64
	for {
		nr, rErr := r.Read(buf)
		if nr > 0 {
			nw, wErr := b.Write(buf[:nr])
			totalRead += int64(nw)
			if wErr != nil {
				return totalRead, wErr
			}
		}
		if rErr != nil {
			if errors.Is(rErr, io.EOF) {
				return totalRead, nil
			}
			return totalRead, rErr
		}
	}
}

// Size reports the exact captured body size in bytes (implements Source).
func (b *SpillBuffer) Size() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bytesWritten
}

// BytesWritten reports total committed bytes across memory and spill storage.
func (b *SpillBuffer) BytesWritten() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bytesWritten
}

// MemoryBytes returns the in-memory portion of the captured body.
func (b *SpillBuffer) MemoryBytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.mem) == 0 {
		return nil
	}
	out := make([]byte, len(b.mem))
	copy(out, b.mem)
	return out
}

// FileBytes reports the count of bytes spilled to secondary storage.
func (b *SpillBuffer) FileBytes() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.fileBytes
}

// HasSpilled reports whether any bytes have spilled to secondary storage.
func (b *SpillBuffer) HasSpilled() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lifecycle.HasSpilled()
}

// FilePath reports the spill file path, or empty string if not spilled.
func (b *SpillBuffer) FilePath() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lifecycle.FilePath()
}

// ActiveReaders reports the current count of open readers.
func (b *SpillBuffer) ActiveReaders() int {
	return b.lifecycle.ActiveReaders()
}

// IsDeletePending reports whether file deletion is deferred until all readers close.
func (b *SpillBuffer) IsDeletePending() bool {
	return b.lifecycle.DeletePending()
}

// UnwrittenSuffix returns any unwritten portion of the last chunk from a failed
// or short write (Requirement 20.6).
func (b *SpillBuffer) UnwrittenSuffix() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.unwrittenSuffix) == 0 {
		return nil
	}
	out := make([]byte, len(b.unwrittenSuffix))
	copy(out, b.unwrittenSuffix)
	return out
}

// HasUnwrittenSuffix reports whether an unwritten suffix is currently held.
func (b *SpillBuffer) HasUnwrittenSuffix() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.unwrittenSuffix) > 0
}

// TakeUnwrittenSuffix returns the unwritten suffix and clears it from the buffer,
// transferring ownership to the caller (e.g. for canonical continuation).
func (b *SpillBuffer) TakeUnwrittenSuffix() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	suffix := b.unwrittenSuffix
	b.unwrittenSuffix = nil
	return suffix
}

func (b *SpillBuffer) setUnwrittenSuffix(p []byte) {
	if len(p) == 0 {
		b.unwrittenSuffix = nil
		return
	}
	b.unwrittenSuffix = make([]byte, len(p))
	copy(b.unwrittenSuffix, p)
}

// ShrinkReservation shrinks the associated logical spool reservation to the actual
// bytes committed, returning any over-reserved budget back to the global ledger.
func (b *SpillBuffer) ShrinkReservation() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reservation == nil || b.closed {
		return nil
	}
	return b.reservation.ShrinkTo(b.bytesWritten)
}

// Reservation returns the associated spool reservation, or nil if none.
func (b *SpillBuffer) Reservation() *SpoolReservation {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reservation
}

// DetachReservation releases association with the reservation and returns it,
// transferring ownership to the caller.
func (b *SpillBuffer) DetachReservation() *SpoolReservation {
	b.mu.Lock()
	defer b.mu.Unlock()
	res := b.reservation
	b.reservation = nil
	return res
}

// Open returns an independent offset-zero reader over the captured body (implements Source).
// Multiple readers can be opened concurrently and read independently.
func (b *SpillBuffer) Open() (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil, ErrSpillClosed
	}

	if b.completed && b.completedSource != nil {
		return b.completedSource.Open()
	}

	path := b.lifecycle.FilePath()
	if path == "" {
		// All data resides in memory
		b.lifecycle.addReader()
		return &spillReader{
			lifecycle: b.lifecycle,
			r:         bytes.NewReader(b.mem),
		}, nil
	}

	// Data is partially in memory, partially spilled to file
	if b.file != nil {
		_ = b.file.Sync()
	}

	rc, err := b.openFile(path)
	if err != nil {
		return nil, err
	}

	b.lifecycle.addReader()
	if len(b.mem) > 0 {
		multi := io.MultiReader(bytes.NewReader(b.mem), rc)
		return &spillReader{
			lifecycle: b.lifecycle,
			r:         multi,
			f:         rc,
		}, nil
	}

	return &spillReader{
		lifecycle: b.lifecycle,
		r:         rc,
		f:         rc,
	}, nil
}

// Digest returns the running source integrity digest computed over all bytes
// committed so far (Requirements 15, 16, 20; design section 5).
// It returns a zero SourceDigest if no bytes have been written yet.
func (b *SpillBuffer) Digest() SourceDigest {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bytesWritten == 0 || b.hasher == nil {
		return SourceDigest{}
	}
	var sum [32]byte
	copy(sum[:], b.hasher.Sum(nil))
	return NewSourceDigest(sum)
}

// SourceDigest returns the running source integrity digest computed so far.
func (b *SpillBuffer) SourceDigest() SourceDigest {
	return b.Digest()
}

// Complete transitions the SpillBuffer into an immutable CompletedSource (Requirements 10, 20; design section 5).
// It syncs and closes the write file handle (if any), shrinks the spool reservation to actual bytes written,
// binds the incrementally computed SourceDigest, seals the buffer against further writes, and returns the immutable CompletedSource.
func (b *SpillBuffer) Complete() (*CompletedSource, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.completed {
		return nil, ErrAlreadyCompleted
	}
	if b.closed {
		return nil, ErrSpillClosed
	}
	if len(b.unwrittenSuffix) > 0 {
		return nil, ErrUnconsumedSuffix
	}

	if b.file != nil {
		if err := b.file.Sync(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSpillWriteFailed, err)
		}
		if err := b.file.Close(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSpillWriteFailed, err)
		}
		b.file = nil
	}

	if b.reservation != nil {
		_ = b.reservation.ShrinkTo(b.bytesWritten)
	}

	var digest SourceDigest
	if b.hasher != nil && b.bytesWritten > 0 {
		var sum [32]byte
		copy(sum[:], b.hasher.Sum(nil))
		digest = NewSourceDigest(sum)
	}

	b.completed = true

	src := &CompletedSource{
		mem:         b.mem,
		size:        b.bytesWritten,
		digest:      digest,
		reservation: b.reservation,
		openFile:    b.openFile,
		lifecycle:   b.lifecycle,
	}
	b.completedSource = src
	return src, nil
}

// IsCompleted reports whether the buffer has been transitioned to a CompletedSource.
func (b *SpillBuffer) IsCompleted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.completed
}

// Close releases the spill buffer (implements io.Closer and Source).
//
// Invariants (Requirement 20.9):
//   - Idempotent and nonblocking.
//   - Releases the associated spool reservation back to the ledger.
//   - Closes the write file handle immediately.
//   - If active readers exist, deletion is marked pending and final removal
//     occurs when the last reader closes (Windows open-file safety).
func (b *SpillBuffer) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true

	if b.completed && b.completedSource != nil {
		b.mu.Unlock()
		return b.completedSource.Close()
	}

	res := b.reservation
	b.reservation = nil

	var closeErr error
	if b.file != nil {
		closeErr = b.file.Close()
		b.file = nil
	}
	b.mu.Unlock()

	if res != nil {
		res.Release()
	}

	if err := b.lifecycle.close(); err != nil && closeErr == nil {
		closeErr = err
	}

	return closeErr
}

func (b *SpillBuffer) readerClosed() {
	_ = b.lifecycle.readerClosed()
}

// String returns a bounded diagnostic representation without leaking prompt
// content, model names, or filesystem paths (Requirement 20.3).
func (b *SpillBuffer) String() string {
	if b == nil {
		return "<nil>"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return fmt.Sprintf("SpillBuffer{written=%d, mem=%d, fileBytes=%d, spilled=%t}",
		b.bytesWritten, len(b.mem), b.fileBytes, b.lifecycle.HasSpilled())
}

// GoString implements fmt.GoStringer without leaking internal fields, prompt content, or filesystem paths.
func (b *SpillBuffer) GoString() string { return b.String() }

// spillReader wraps an io.Reader and tracks reader lifetime on the parent SpillBuffer.
type spillReader struct {
	lifecycle *spillLifecycle
	r         io.Reader
	f         io.Closer
	once      sync.Once
}

func (s *spillReader) Read(p []byte) (int, error) {
	return s.r.Read(p)
}

func (s *spillReader) Close() error {
	var err error
	s.once.Do(func() {
		// The physical descriptor closes before the lifecycle census is
		// released. The deferred-removal error joins the descriptor error so
		// the final reader exposes truthful cleanup failure. Second Close is
		// a no-op returning nil (existing behavior preserved).
		var descErr error
		if s.f != nil {
			descErr = s.f.Close()
		}
		remErr := s.lifecycle.readerClosed()
		switch {
		case descErr != nil && remErr != nil:
			err = errors.Join(descErr, remErr)
		case descErr != nil:
			err = descErr
		default:
			err = remErr
		}
	})
	return err
}
