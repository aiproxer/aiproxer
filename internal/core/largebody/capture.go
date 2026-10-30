package largebody

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// ErrNilRemainingBody is returned when constructing a CaptureReader without a remaining body.
var ErrNilRemainingBody = errors.New("largebody: remaining body reader must not be nil")

// CaptureReaderConfig configures a CaptureReader for lossless mid-capture
// canonical continuation (Requirements 1.4, 2.4, 20.6, 20.7, 20.8; design section 5).
type CaptureReaderConfig struct {
	// Spill is the optional SpillBuffer holding the retained prefix and/or unwritten suffix.
	// If provided and Spill.BytesWritten() > 0 and RetainedReader is nil, Spill.Open()
	// is automatically called to supply the retained prefix reader.
	Spill *SpillBuffer

	// RetainedReader optionally overrides the retained prefix reader.
	// If nil and Spill != nil and Spill.BytesWritten() > 0, Spill.Open() is used.
	RetainedReader io.ReadCloser

	// RetainedBytes is the byte length of the retained prefix if known.
	RetainedBytes int64

	// UnwrittenSuffix is any unwritten suffix bytes from the last chunk.
	// If nil and Spill != nil, Spill.TakeUnwrittenSuffix() is automatically consumed.
	UnwrittenSuffix []byte

	// Remaining is the still-unread client request body stream (must not be nil).
	Remaining io.ReadCloser

	// MaxBytes is the authoritative request body ceiling. If > 0, reads exceeding
	// MaxBytes return an *http.MaxBytesError honoring standard reqbody.TooLarge semantics.
	MaxBytes int64

	// ResponseWriter is an optional http.ResponseWriter passed to http.MaxBytesReader.
	ResponseWriter http.ResponseWriter
}

// CaptureReader is a lossless mid-capture canonical continuation reader
// (Requirements 1.4, 2.4, 20.6, 20.7, 20.8; design section 5).
//
// It composes:
//  1. Retained prefix: bytes successfully committed to storage before decline.
//  2. Unwritten suffix: bytes read from the client socket in the current chunk
//     that were not yet committed to storage.
//  3. Still-unread request body: the remaining unread client socket stream.
//
// Invariants:
//   - Never rereads or restarts the client socket (forward-only streaming; Requirement 20.8).
//   - Preserves exact request body ceiling semantics (returns *http.MaxBytesError; Requirement 2.6).
//   - Preserves non-limit read errors (cancellation, disconnect) without converting to 413.
//   - Closing the reader idempotently closes the underlying client body and releases
//     retained prefix resources (deleting temporary spill files; Requirement 20.9).
type CaptureReader struct {
	mu sync.Mutex

	spill *SpillBuffer

	retainedLen  int64
	unwrittenLen int64

	// reader is the active reader, optionally wrapped by http.MaxBytesReader
	reader io.ReadCloser

	// chained is the underlying sequential reader
	chained *chainedSequentialReadCloser

	totalRead int64
	closed    bool
}

var _ io.ReadCloser = (*CaptureReader)(nil)

// NewCaptureReader constructs an initialized CaptureReader from configuration.
func NewCaptureReader(cfg CaptureReaderConfig) (*CaptureReader, error) {
	if cfg.Remaining == nil {
		return nil, ErrNilRemainingBody
	}

	spill := cfg.Spill
	retained := cfg.RetainedReader
	retainedLen := cfg.RetainedBytes

	// If no explicit retained reader was provided, open one from SpillBuffer if available.
	if retained == nil && spill != nil && spill.BytesWritten() > 0 {
		rc, err := spill.Open()
		if err != nil {
			return nil, fmt.Errorf("largebody: open retained prefix from spill: %w", err)
		}
		retained = rc
		retainedLen = spill.BytesWritten()
	}

	// Consume unwritten suffix: if not provided explicitly, take from SpillBuffer.
	suffix := cfg.UnwrittenSuffix
	if len(suffix) == 0 && spill != nil {
		suffix = spill.TakeUnwrittenSuffix()
	}

	var suffixReader io.Reader
	if len(suffix) > 0 {
		suffixReader = bytes.NewReader(suffix)
	}

	chained := &chainedSequentialReadCloser{
		retained:  retained,
		suffix:    suffixReader,
		remaining: cfg.Remaining,
	}

	var reader io.ReadCloser = chained
	if cfg.MaxBytes > 0 {
		reader = http.MaxBytesReader(cfg.ResponseWriter, chained, cfg.MaxBytes)
	}

	return &CaptureReader{
		spill:        spill,
		retainedLen:  retainedLen,
		unwrittenLen: int64(len(suffix)),
		reader:       reader,
		chained:      chained,
	}, nil
}

// NewContinuationReader is a convenience constructor creating a CaptureReader
// from a SpillBuffer and remaining body under the given ceiling.
func NewContinuationReader(spill *SpillBuffer, remaining io.ReadCloser, maxBytes int64) (*CaptureReader, error) {
	return NewCaptureReader(CaptureReaderConfig{
		Spill:     spill,
		Remaining: remaining,
		MaxBytes:  maxBytes,
	})
}

// Read reads from the continuation stream (implements io.Reader).
// It serves bytes from the retained prefix first, then the unwritten suffix,
// then the still-unread client body.
func (c *CaptureReader) Read(p []byte) (int, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	r := c.reader
	c.mu.Unlock()

	n, err := r.Read(p)
	if n > 0 {
		c.mu.Lock()
		c.totalRead += int64(n)
		c.mu.Unlock()
	}
	return n, err
}

// Close closes the continuation reader (implements io.Closer).
// It is idempotent and closes the underlying client body, retained prefix reader,
// and associated SpillBuffer (deleting temporary spill files if pending).
func (c *CaptureReader) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	var errs []error
	if c.reader != nil {
		if err := c.reader.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if c.spill != nil {
		if err := c.spill.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// TotalRead reports total bytes read across all segments through this reader.
func (c *CaptureReader) TotalRead() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalRead
}

// RetainedBytes reports the byte size of the retained prefix.
func (c *CaptureReader) RetainedBytes() int64 {
	return c.retainedLen
}

// UnwrittenBytes reports the byte size of the unwritten suffix.
func (c *CaptureReader) UnwrittenBytes() int64 {
	return c.unwrittenLen
}

// chainedSequentialReadCloser sequentially reads from retained, suffix, and remaining.
type chainedSequentialReadCloser struct {
	retained  io.ReadCloser
	suffix    io.Reader
	remaining io.ReadCloser
	closed    bool
}

func (c *chainedSequentialReadCloser) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	for {
		if c.retained != nil {
			n, err := c.retained.Read(p)
			if n > 0 {
				if errors.Is(err, io.EOF) {
					_ = c.retained.Close()
					c.retained = nil
					return n, nil
				}
				return n, err
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					_ = c.retained.Close()
					c.retained = nil
					continue
				}
				return 0, err
			}
			continue
		}

		if c.suffix != nil {
			n, err := c.suffix.Read(p)
			if n > 0 {
				if errors.Is(err, io.EOF) {
					c.suffix = nil
					return n, nil
				}
				return n, err
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					c.suffix = nil
					continue
				}
				return 0, err
			}
			continue
		}

		if c.remaining != nil {
			return c.remaining.Read(p)
		}

		return 0, io.EOF
	}
}

func (c *chainedSequentialReadCloser) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true

	var errs []error
	if c.retained != nil {
		if err := c.retained.Close(); err != nil {
			errs = append(errs, err)
		}
		c.retained = nil
	}
	if c.remaining != nil {
		if err := c.remaining.Close(); err != nil {
			errs = append(errs, err)
		}
		c.remaining = nil
	}
	c.suffix = nil
	return errors.Join(errs...)
}

// CaptureConfig configures capturing an incoming request body (Requirement 20).
type CaptureConfig struct {
	// MaxBytes is the authoritative request body ceiling.
	MaxBytes int64

	// ResponseWriter is an optional http.ResponseWriter for ceiling enforcement.
	ResponseWriter http.ResponseWriter

	// CopyBufferSize is the chunk size used for reading from the client body.
	// Defaults to DefaultCopyBufferSize (32 KiB).
	CopyBufferSize int

	// OnChunk is an optional callback invoked for each chunk read from the client body
	// before writing to storage (e.g. for streaming JSON scanner in Task 5).
	OnChunk func(chunk []byte) error
}

// CaptureOutcome indicates the result of a CaptureRequestBody operation.
type CaptureOutcome uint8

const (
	// CaptureOutcomeCompleted indicates the entire request body was captured
	// to EOF without error or decline.
	CaptureOutcomeCompleted CaptureOutcome = iota

	// CaptureOutcomeDeclined indicates capture encountered a recoverable decline
	// (spool reservation exhausted, spill write/create failure, or callback decline)
	// and produced a lossless canonical continuation reader.
	CaptureOutcomeDeclined

	// CaptureOutcomeLimitExceeded indicates the request body exceeded MaxBytes.
	CaptureOutcomeLimitExceeded

	// CaptureOutcomeReadError indicates reading from the client socket failed
	// with a non-recoverable error (e.g. client cancellation or disconnect).
	CaptureOutcomeReadError

	// CaptureOutcomeTerminalError indicates capture could not continue or fall back
	// due to an unrecoverable failure (such as failure to construct the lossless continuation reader).
	CaptureOutcomeTerminalError
)

// String returns a bounded diagnostic representation.
func (o CaptureOutcome) String() string {
	switch o {
	case CaptureOutcomeCompleted:
		return "completed"
	case CaptureOutcomeDeclined:
		return "declined"
	case CaptureOutcomeLimitExceeded:
		return "limit_exceeded"
	case CaptureOutcomeReadError:
		return "read_error"
	case CaptureOutcomeTerminalError:
		return "terminal_error"
	default:
		return "unknown"
	}
}

// CaptureResult is the outcome returned by CaptureRequestBody.
type CaptureResult struct {
	Outcome      CaptureOutcome
	Source       Source
	Continuation *CaptureReader
	Digest       SourceDigest
	BytesRead    int64
	Err          error
}

// CaptureRequestBody streams from body into spillBuffer up to maxBytes.
//
// Invariants:
//   - If capture completes to EOF, it returns CaptureOutcomeCompleted.
//   - If a recoverable decline occurs (reservation exhausted, spill failure, etc.),
//     it constructs and returns a lossless CaptureReader (canonical continuation)
//     composing retained prefix + unwritten suffix + remaining client body.
//     Declined ⇒ non-nil continuation is enforced by construction. If continuation
//     reader construction fails, it returns CaptureOutcomeTerminalError instead of
//     CaptureOutcomeDeclined, releasing all resources.
//   - If body exceeds maxBytes, it returns CaptureOutcomeLimitExceeded with *http.MaxBytesError.
//   - If reading from body fails, it returns CaptureOutcomeReadError with the read error.
//   - Consumes any unwritten suffix from spillBuffer before retrying new writes
//     (Task 4.2 review suggestion).
func CaptureRequestBody(body io.ReadCloser, spill *SpillBuffer, cfg CaptureConfig) CaptureResult {
	if body == nil {
		return CaptureResult{
			Outcome: CaptureOutcomeReadError,
			Err:     errors.New("largebody: body reader is nil"),
		}
	}
	if spill == nil {
		cont, err := NewCaptureReader(CaptureReaderConfig{
			Remaining:      body,
			MaxBytes:       cfg.MaxBytes,
			ResponseWriter: cfg.ResponseWriter,
		})
		if err != nil {
			_ = body.Close()
			return CaptureResult{
				Outcome: CaptureOutcomeTerminalError,
				Err:     err,
			}
		}
		return CaptureResult{
			Outcome:      CaptureOutcomeDeclined,
			Continuation: cont,
		}
	}

	copySize := cfg.CopyBufferSize
	if copySize <= 0 {
		copySize = DefaultCopyBufferSize
	}
	buf := make([]byte, copySize)

	var totalRead int64

	// Consume any existing unwritten suffix before retrying new writes (Task 4.2 review suggestion).
	if spill.HasUnwrittenSuffix() {
		suffix := spill.TakeUnwrittenSuffix()
		if len(suffix) > 0 {
			if cfg.MaxBytes > 0 && totalRead+int64(len(suffix)) > cfg.MaxBytes {
				_ = body.Close()
				_ = spill.Close()
				return CaptureResult{
					Outcome:   CaptureOutcomeLimitExceeded,
					BytesRead: totalRead + int64(len(suffix)),
					Err:       &http.MaxBytesError{Limit: cfg.MaxBytes},
				}
			}
			totalRead += int64(len(suffix))
			nw, wErr := spill.Write(suffix)
			if wErr != nil {
				unwritten := spill.TakeUnwrittenSuffix()
				cont, cErr := NewCaptureReader(CaptureReaderConfig{
					Spill:           spill,
					UnwrittenSuffix: unwritten,
					Remaining:       body,
					MaxBytes:        cfg.MaxBytes,
					ResponseWriter:  cfg.ResponseWriter,
				})
				if cErr != nil {
					_ = body.Close()
					_ = spill.Close()
					return CaptureResult{
						Outcome:   CaptureOutcomeTerminalError,
						BytesRead: totalRead,
						Err:       errors.Join(wErr, cErr),
					}
				}
				return CaptureResult{
					Outcome:      CaptureOutcomeDeclined,
					Continuation: cont,
					BytesRead:    totalRead,
					Err:          wErr,
				}
			}
			_ = nw
		}
	}

	for {
		nr, rErr := body.Read(buf)
		if nr > 0 {
			chunk := buf[:nr]

			// Enforce request body ceiling
			if cfg.MaxBytes > 0 && totalRead+int64(nr) > cfg.MaxBytes {
				_ = body.Close()
				_ = spill.Close()
				return CaptureResult{
					Outcome:   CaptureOutcomeLimitExceeded,
					BytesRead: totalRead + int64(nr),
					Err:       &http.MaxBytesError{Limit: cfg.MaxBytes},
				}
			}
			totalRead += int64(nr)

			// Optional chunk inspection (e.g. streaming JSON scanner)
			if cfg.OnChunk != nil {
				if err := cfg.OnChunk(chunk); err != nil {
					// Callback declined capture: chunk is unwritten suffix
					cont, cErr := NewCaptureReader(CaptureReaderConfig{
						Spill:           spill,
						UnwrittenSuffix: chunk,
						Remaining:       body,
						MaxBytes:        cfg.MaxBytes,
						ResponseWriter:  cfg.ResponseWriter,
					})
					if cErr != nil {
						_ = body.Close()
						_ = spill.Close()
						return CaptureResult{
							Outcome:   CaptureOutcomeTerminalError,
							BytesRead: totalRead,
							Err:       errors.Join(err, cErr),
						}
					}
					return CaptureResult{
						Outcome:      CaptureOutcomeDeclined,
						Continuation: cont,
						BytesRead:    totalRead,
						Err:          err,
					}
				}
			}

			// Write chunk to spill
			_, wErr := spill.Write(chunk)
			if wErr != nil {
				// Recoverable write failure: unwritten portion is in spill.UnwrittenSuffix()
				unwritten := spill.TakeUnwrittenSuffix()
				cont, cErr := NewCaptureReader(CaptureReaderConfig{
					Spill:           spill,
					UnwrittenSuffix: unwritten,
					Remaining:       body,
					MaxBytes:        cfg.MaxBytes,
					ResponseWriter:  cfg.ResponseWriter,
				})
				if cErr != nil {
					_ = body.Close()
					_ = spill.Close()
					return CaptureResult{
						Outcome:   CaptureOutcomeTerminalError,
						BytesRead: totalRead,
						Err:       errors.Join(wErr, cErr),
					}
				}
				return CaptureResult{
					Outcome:      CaptureOutcomeDeclined,
					Continuation: cont,
					BytesRead:    totalRead,
					Err:          wErr,
				}
			}
		}

		if rErr != nil {
			if errors.Is(rErr, io.EOF) {
				var src Source
				var digest SourceDigest
				if spill != nil {
					compSrc, err := spill.Complete()
					if err != nil {
						_ = body.Close()
						_ = spill.Close()
						return CaptureResult{
							Outcome:   CaptureOutcomeReadError,
							BytesRead: totalRead,
							Err:       err,
						}
					}
					src = compSrc
					digest = compSrc.Digest()
				}
				return CaptureResult{
					Outcome:   CaptureOutcomeCompleted,
					Source:    src,
					Digest:    digest,
					BytesRead: totalRead,
				}
			}
			_ = body.Close()
			_ = spill.Close()
			return CaptureResult{
				Outcome:   CaptureOutcomeReadError,
				BytesRead: totalRead,
				Err:       rErr,
			}
		}
	}
}
