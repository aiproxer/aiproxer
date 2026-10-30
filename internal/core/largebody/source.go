package largebody

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// ErrSourceClosed is returned when attempting an operation on a closed CompletedSource.
var ErrSourceClosed = errors.New("largebody: source is closed")

// ErrAlreadyCompleted is returned when attempting to complete or write to an already completed SpillBuffer.
var ErrAlreadyCompleted = errors.New("largebody: spill buffer is already completed")

// CompletedSourceConfig configures an immutable CompletedSource (Requirements 10, 20; design section 5).
type CompletedSourceConfig struct {
	// Memory is the in-memory prefix of the captured body (if any).
	Memory []byte

	// FilePath is the path to the spilled temporary file (if any).
	FilePath string

	// Size is the exact captured body size in bytes.
	Size int64

	// Digest is the replay/attempt source integrity digest (Requirements 15, 16, 20; design section 5).
	// If zero and data is in-memory without spill, it is automatically computed via SHA-256.
	Digest SourceDigest

	// Reservation is an optional logical spool reservation released on root close.
	Reservation *SpoolReservation

	// OpenFile optionally overrides opening the spill file for readers (for testing/fault injection).
	OpenFile func(path string) (io.ReadCloser, error)

	// RemoveFile optionally overrides spill file deletion (for testing/fault injection).
	RemoveFile func(path string) error

	// OnRemovalFailure is an optional zero-argument observer invoked at most
	// once per lifecycle when immediate or deferred spill removal fails
	// (Requirement 4.3). It takes no arguments on purpose: the source cannot
	// leak a filesystem path, prompt bytes, digest, model, or raw filesystem
	// error through it. The notice fires outside the lifecycle mutex after the
	// failure is recorded; observer panic is contained and never prevents
	// state publication or owned cleanup. `Source` stays unchanged.
	OnRemovalFailure func()
}

// CompletedSource represents an immutable completed replay capture (Requirements 10, 20; design section 5).
// It implements Source.
//
// Invariants:
//   - Completely immutable: exposes no write or mutation methods.
//   - Independent offset-zero readers: each Open() returns a fresh reader starting at offset zero.
//   - Parallel readers are completely isolated: reading, seeking, or closing one reader has zero effect on others.
//   - Root Close() is idempotent and nonblocking: does not deadlock on active or leaked readers (Requirement 20.9).
//   - Windows open-file deletion safety: if readers are active during root Close(), deletion is marked pending
//     and the spill file is removed only when the last active reader closes (Requirements 20.9, 20.10).
//   - No background cleanup goroutines required.
//   - Binds an immutable SourceDigest for replay/attempt evidence; explicitly not a substitute for canonical semantic identity (Requirements 15, 16.5).
//
// spillLifecycle manages the spill file path, reader refcount, deletion status, and removal.
// It is shared between SpillBuffer and CompletedSource to maintain lifecycle equivalence
// across transitions (Finding M4).
//
// Truthful removal (large-payload-lifecycle-correctness Requirement 4):
// one lifecycle elects exactly one unlink claimant between root Close and the
// final tracked reader Close. Private state distinguishes pending
// (deletePending), removal claimed/in progress, removed (confirmed absence),
// and failed/unresolved (path plus original error retained while reachable).
// A successful unlink or errors.Is(err, os.ErrNotExist) confirms absence and
// clears the path; any other failure keeps the path and original error and
// returns it to the triggering closer. There is exactly one unlink attempt per
// lifecycle: no retry, no persistence, no background worker.
type spillLifecycle struct {
	mu            sync.Mutex
	filePath      string
	activeReaders int
	deletePending bool
	removeFile    func(path string) error

	onRemovalFailure func()

	removalInProgress bool
	removalDone       bool
	removalFailed     bool
	removeErr         error
	notified          bool
}

func newSpillLifecycle(filePath string, removeFile func(path string) error, onRemovalFailure func()) *spillLifecycle {
	if removeFile == nil {
		removeFile = os.Remove
	}
	return &spillLifecycle{
		filePath:         filePath,
		removeFile:       removeFile,
		onRemovalFailure: onRemovalFailure,
	}
}

func (l *spillLifecycle) setFilePath(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.filePath = path
}

func (l *spillLifecycle) FilePath() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.filePath
}

func (l *spillLifecycle) HasSpilled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.filePath != ""
}

func (l *spillLifecycle) ActiveReaders() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.activeReaders
}

func (l *spillLifecycle) DeletePending() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.deletePending
}

func (l *spillLifecycle) addReader() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.activeReaders++
}

func (l *spillLifecycle) readerClosed() error {
	l.mu.Lock()
	l.activeReaders--
	if l.activeReaders < 0 {
		l.activeReaders = 0
	}

	var path string
	if l.deletePending && l.activeReaders == 0 {
		path = l.claimRemovalLocked()
	}
	l.mu.Unlock()

	if path == "" {
		return nil
	}
	return l.runRemoval(path)
}

func (l *spillLifecycle) close() error {
	l.mu.Lock()
	var path string
	if l.activeReaders == 0 {
		path = l.claimRemovalLocked()
	} else {
		l.deletePending = true
	}
	l.mu.Unlock()

	if path == "" {
		return nil
	}
	return l.runRemoval(path)
}

// claimRemovalLocked elects at most one unlink claimant for this lifecycle.
// Callers must hold l.mu. It returns the path to unlink, or "" when there is
// nothing to remove (never spilled, already confirmed absent, already failed
// with no retry, or another closer already claimed removal).
func (l *spillLifecycle) claimRemovalLocked() string {
	if l.filePath == "" || l.removalDone || l.removalFailed || l.removalInProgress {
		return ""
	}
	l.removalInProgress = true
	return l.filePath
}

// runRemoval performs the claimed unlink outside the lifecycle mutex, records
// the result under it, and fires the once-only failure notice outside it. No
// callback, I/O, or wait occurs under this mutex. It returns the removal
// error with its original identity, or nil when absence is confirmed.
func (l *spillLifecycle) runRemoval(path string) error {
	err := l.removeFile(path)

	l.mu.Lock()
	notify := l.finishRemovalLocked(err)
	l.mu.Unlock()

	fireRemovalNotice(notify)

	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// finishRemovalLocked records the unlink result. Callers must hold l.mu.
// Success or os.ErrNotExist confirms absence and clears the path. Any other
// failure retains the path and original error and arms the once-only failure
// notice. It returns the notice callback to fire outside the mutex, or nil.
func (l *spillLifecycle) finishRemovalLocked(err error) (notify func()) {
	l.removalInProgress = false
	if err == nil || errors.Is(err, os.ErrNotExist) {
		l.filePath = ""
		l.removalDone = true
		l.removeErr = nil
		return nil
	}
	l.removalFailed = true
	l.removeErr = err
	if l.onRemovalFailure != nil && !l.notified {
		l.notified = true
		notify = l.onRemovalFailure
	}
	return notify
}

// fireRemovalNotice delivers the failure notice outside the lifecycle mutex.
// Observer panic is contained so it cannot prevent state publication or owned
// cleanup. The zero-argument callback cannot receive a path, prompt bytes,
// digest, model, or raw filesystem error.
func fireRemovalNotice(notify func()) {
	if notify == nil {
		return
	}
	defer func() { _ = recover() }()
	notify()
}

type CompletedSource struct {
	mu sync.Mutex

	mem         []byte
	size        int64
	digest      SourceDigest
	reservation *SpoolReservation
	closed      bool

	lifecycle *spillLifecycle
	openFile  func(path string) (io.ReadCloser, error)
}

var (
	_ Source    = (*CompletedSource)(nil)
	_ io.Closer = (*CompletedSource)(nil)
)

// NewCompletedSource validates configuration and constructs an initialized CompletedSource.
func NewCompletedSource(cfg CompletedSourceConfig) (*CompletedSource, error) {
	if cfg.Size < 0 {
		return nil, fmt.Errorf("largebody: completed source size must be >= 0, got %d", cfg.Size)
	}
	if len(cfg.Memory) == 0 && cfg.FilePath == "" && cfg.Size > 0 {
		return nil, fmt.Errorf("largebody: completed source with non-zero size must have memory or file backing")
	}

	openFile := cfg.OpenFile
	if openFile == nil {
		openFile = defaultOpenSpillFile
	}
	removeFile := cfg.RemoveFile
	if removeFile == nil {
		removeFile = os.Remove
	}

	size := cfg.Size
	var mem []byte
	if len(cfg.Memory) > 0 {
		mem = make([]byte, len(cfg.Memory))
		copy(mem, cfg.Memory)
		if size == 0 {
			size = int64(len(mem))
		}
	}

	digest := cfg.Digest
	if digest.IsZero() && len(mem) > 0 && cfg.FilePath == "" {
		digest = NewSourceDigest(sha256.Sum256(mem))
	}

	return &CompletedSource{
		mem:         mem,
		size:        size,
		digest:      digest,
		reservation: cfg.Reservation,
		openFile:    openFile,
		lifecycle:   newSpillLifecycle(cfg.FilePath, removeFile, cfg.OnRemovalFailure),
	}, nil
}

// NewMemorySource constructs an immutable in-memory CompletedSource from data.
func NewMemorySource(data []byte) *CompletedSource {
	var mem []byte
	var digest SourceDigest
	if len(data) > 0 {
		mem = make([]byte, len(data))
		copy(mem, data)
		digest = NewSourceDigest(sha256.Sum256(mem))
	}
	return &CompletedSource{
		mem:       mem,
		size:      int64(len(mem)),
		digest:    digest,
		openFile:  defaultOpenSpillFile,
		lifecycle: newSpillLifecycle("", os.Remove, nil),
	}
}

// Size reports the exact captured body size in bytes (implements Source).
func (s *CompletedSource) Size() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}

// Open returns an independent offset-zero reader over the captured body (implements Source).
// Multiple readers can be opened concurrently and read independently.
func (s *CompletedSource) Open() (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, ErrSourceClosed
	}

	path := s.lifecycle.FilePath()
	if path == "" {
		s.lifecycle.addReader()
		return &completedSourceReader{
			lifecycle: s.lifecycle,
			r:         bytes.NewReader(s.mem),
		}, nil
	}

	rc, err := s.openFile(path)
	if err != nil {
		return nil, err
	}

	s.lifecycle.addReader()
	if len(s.mem) > 0 {
		multi := io.MultiReader(bytes.NewReader(s.mem), rc)
		return &completedSourceReader{
			lifecycle: s.lifecycle,
			r:         multi,
			f:         rc,
		}, nil
	}

	return &completedSourceReader{
		lifecycle: s.lifecycle,
		r:         rc,
		f:         rc,
	}, nil
}

// Close releases the completed source (implements io.Closer and Source).
//
// Invariants (Requirement 20.9):
//   - Idempotent and nonblocking.
//   - Releases any associated spool reservation.
//   - If active readers exist, deletion is marked pending and final removal
//     occurs when the last reader closes (Windows open-file safety).
func (s *CompletedSource) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true

	if s.reservation != nil {
		s.reservation.Release()
		s.reservation = nil
	}
	s.mu.Unlock()

	return s.lifecycle.close()
}

func (s *CompletedSource) readerClosed() {
	_ = s.lifecycle.readerClosed()
}

// MemoryBytes returns a copy of the in-memory portion of the source.
func (s *CompletedSource) MemoryBytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.mem) == 0 {
		return nil
	}
	out := make([]byte, len(s.mem))
	copy(out, s.mem)
	return out
}

// FilePath returns the spill file path, or empty string if not spilled.
func (s *CompletedSource) FilePath() string {
	return s.lifecycle.FilePath()
}

// HasSpilled reports whether the source is backed by secondary spill storage.
func (s *CompletedSource) HasSpilled() bool {
	return s.lifecycle.HasSpilled()
}

// ActiveReaders reports the current count of open readers.
func (s *CompletedSource) ActiveReaders() int {
	return s.lifecycle.ActiveReaders()
}

// IsClosed reports whether root Close() has been called.
func (s *CompletedSource) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// IsDeletePending reports whether file deletion is deferred until all readers close.
func (s *CompletedSource) IsDeletePending() bool {
	return s.lifecycle.DeletePending()
}

// Digest reports the bound source integrity digest (Requirements 15, 16, 20; design section 5).
// The source digest is for replay/attempt evidence only and is never a substitute for
// canonical semantic identity (Requirement 16.5).
func (s *CompletedSource) Digest() SourceDigest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.digest
}

// SourceDigest reports the bound source integrity digest (alias for Digest).
func (s *CompletedSource) SourceDigest() SourceDigest {
	return s.Digest()
}

// String returns a bounded diagnostic representation without leaking prompt
// content, model names, or filesystem paths (Requirement 20.3).
func (s *CompletedSource) String() string {
	if s == nil {
		return "<nil>"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("CompletedSource{size=%d, mem=%d, spilled=%t, readers=%d, closed=%t, digest=%s}",
		s.size, len(s.mem), s.lifecycle.HasSpilled(), s.lifecycle.ActiveReaders(), s.closed, s.digest.String())
}

// completedSourceReader wraps an io.Reader and tracks reader lifetime on the parent CompletedSource.
type completedSourceReader struct {
	lifecycle *spillLifecycle
	r         io.Reader
	f         io.Closer
	once      sync.Once
}

func (r *completedSourceReader) Read(p []byte) (int, error) {
	return r.r.Read(p)
}

func (r *completedSourceReader) Close() error {
	var err error
	r.once.Do(func() {
		// The physical descriptor closes before the lifecycle census is
		// released. The deferred-removal error joins the descriptor error so
		// the final reader exposes truthful cleanup failure. Second Close is
		// a no-op returning nil (existing behavior preserved).
		var descErr error
		if r.f != nil {
			descErr = r.f.Close()
		}
		remErr := r.lifecycle.readerClosed()
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
