package largebody

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
)

// ReplayChunkReaderConfig configures reading replay bytes in fixed-size buffers.
type ReplayChunkReaderConfig struct {
	Reader    io.Reader
	MaxBytes  int64
	ChunkSize int
	Scanner   *jsonshape.Scanner
	Hasher    hash.Hash
	OnChunk   func(chunk []byte) error
}

// ReplayChunkReaderResult summarizes the outcome of reading replay chunks.
type ReplayChunkReaderResult struct {
	BytesRead int64
	BodyHash  [32]byte
}

// ProcessReplayChunks reads replay bytes from Reader in fixed-size buffers (default 32 KiB),
// simultaneously streaming them into Hasher (if non-nil), Scanner (if non-nil),
// and OnChunk (if non-nil) in one pass without calling io.ReadAll.
// It enforces MaxBytes if > 0.
func ProcessReplayChunks(cfg ReplayChunkReaderConfig) (ReplayChunkReaderResult, error) {
	if cfg.Reader == nil {
		return ReplayChunkReaderResult{}, errors.New("largebody: nil replay reader")
	}
	chunkSize := cfg.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 32 * 1024
	}
	buf := make([]byte, chunkSize)
	var totalRead int64

	for {
		n, readErr := cfg.Reader.Read(buf)
		if n > 0 {
			totalRead += int64(n)
			if cfg.MaxBytes > 0 && totalRead > cfg.MaxBytes {
				return ReplayChunkReaderResult{BytesRead: totalRead},
					fmt.Errorf("largebody: replay exceeded max bytes (%d > %d)", totalRead, cfg.MaxBytes)
			}
			chunk := buf[:n]
			if cfg.Hasher != nil {
				cfg.Hasher.Write(chunk)
			}
			if cfg.Scanner != nil {
				if err := cfg.Scanner.Feed(chunk); err != nil {
					return ReplayChunkReaderResult{BytesRead: totalRead},
						fmt.Errorf("largebody: scanner feed: %w", err)
				}
			}
			if cfg.OnChunk != nil {
				if err := cfg.OnChunk(chunk); err != nil {
					return ReplayChunkReaderResult{BytesRead: totalRead}, err
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return ReplayChunkReaderResult{BytesRead: totalRead},
				fmt.Errorf("largebody: read replay chunk: %w", readErr)
		}
	}

	if cfg.Scanner != nil {
		if _, err := cfg.Scanner.Finish(); err != nil {
			return ReplayChunkReaderResult{BytesRead: totalRead},
				fmt.Errorf("largebody: scanner finish: %w", err)
		}
	}

	var bodyHash [32]byte
	if cfg.Hasher != nil {
		cfg.Hasher.Sum(bodyHash[:0])
	}

	return ReplayChunkReaderResult{
		BytesRead: totalRead,
		BodyHash:  bodyHash,
	}, nil
}

// StreamingProofConfig configures streaming proof compilation across replay chunks.
//
// Protocol neutrality: the wire-to-canonical string mapping is supplied by the
// owning protocol edge through StringWriterResolver. Core applies no default and
// never interprets wire field names itself.
type StreamingProofConfig struct {
	Reader             io.Reader
	MaxBytes           int64
	ChunkSize          int
	CallIdentityConfig CallIdentityConfig
	// StringWriterResolver, when non-nil, builds the wire string-value resolver
	// bound to the identity writer CompileStreamingProof creates.
	StringWriterResolver func(idw *CallIdentityWriter) jsonshape.StringWriterResolver
	ScannerLimits        jsonshape.Limits
	ScannerOptions       []jsonshape.Option
	OnChunk              func(chunk []byte) error
}

// StreamingProofResult returns the compiled proof output from streaming execution.
type StreamingProofResult struct {
	BytesRead int64
	BodyHash  [32]byte
	Digest    IdentityDigest
}

// CompileStreamingProof compiles proof and semantic identity from replay bytes in a single pass.
// It feeds replay bytes through jsonshape.Scanner, computes raw body SHA-256, and drives
// CallIdentityWriter without allocating or retaining the full body in memory.
//
// Mapping wire string values onto canonical messages is the protocol edge's job;
// pass cfg.StringWriterResolver to supply it.
func CompileStreamingProof(ctx context.Context, cfg StreamingProofConfig) (StreamingProofResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	idWriter, err := NewCallIdentityWriter(cfg.CallIdentityConfig)
	if err != nil {
		return StreamingProofResult{}, fmt.Errorf("largebody: new identity writer: %w", err)
	}

	hasher := sha256.New()

	var scannerOpts []jsonshape.Option
	if cfg.StringWriterResolver != nil {
		scannerOpts = append(scannerOpts, jsonshape.WithStringWriterResolver(cfg.StringWriterResolver(idWriter)))
	}
	scannerOpts = append(scannerOpts, cfg.ScannerOptions...)

	limits := cfg.ScannerLimits
	if limits.MaxBytes <= 0 && cfg.MaxBytes > 0 {
		limits.MaxBytes = cfg.MaxBytes
	}

	scanner := jsonshape.NewScanner(ctx, limits, scannerOpts...)

	readRes, err := ProcessReplayChunks(ReplayChunkReaderConfig{
		Reader:    cfg.Reader,
		MaxBytes:  cfg.MaxBytes,
		ChunkSize: cfg.ChunkSize,
		Scanner:   scanner,
		Hasher:    hasher,
		OnChunk:   cfg.OnChunk,
	})
	if err != nil {
		return StreamingProofResult{}, err
	}

	digest, err := idWriter.Digest()
	if err != nil {
		return StreamingProofResult{}, fmt.Errorf("largebody: digest: %w", err)
	}

	return StreamingProofResult{
		BytesRead: readRes.BytesRead,
		BodyHash:  readRes.BodyHash,
		Digest:    digest,
	}, nil
}

// DefaultMaxTrailingWhitespaceBytes is the maximum buffer size for pending trailing whitespace
// in TrimSpaceWriter to preserve bounded memory (Requirement 19.3 O(facts+buffers) invariant).
const DefaultMaxTrailingWhitespaceBytes = 64 * 1024

// TrimSpaceWriter wraps an io.Writer and trims leading and trailing Unicode whitespace
// in a streaming manner with bounded memory.
type TrimSpaceWriter struct {
	out                        io.Writer
	maxTrailingWhitespaceBytes int
	seenNonSpace               bool
	trailingWS                 []byte
	pendingRune                []byte
	trimmedBytes               int64
}

// NewTrimSpaceWriter returns an initialized TrimSpaceWriter wrapping out.
func NewTrimSpaceWriter(out io.Writer) *TrimSpaceWriter {
	return &TrimSpaceWriter{
		out:                        out,
		maxTrailingWhitespaceBytes: DefaultMaxTrailingWhitespaceBytes,
	}
}

// TrimmedBytes returns the total number of bytes written to the underlying writer
// after trimming leading and trailing whitespace.
func (w *TrimSpaceWriter) TrimmedBytes() int64 {
	return w.trimmedBytes
}

func (w *TrimSpaceWriter) Write(p []byte) (int, error) {
	origLen := len(p)
	if origLen == 0 {
		return 0, nil
	}

	maxCap := w.maxTrailingWhitespaceBytes
	if maxCap <= 0 {
		maxCap = DefaultMaxTrailingWhitespaceBytes
	}

	data := p
	if len(w.pendingRune) > 0 {
		data = append(w.pendingRune, p...)
		w.pendingRune = nil
	}

	// 1. Skip leading whitespace if non-space not yet seen
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

	// 2. Check incomplete rune at end of data
	if !utf8.FullRune(data) {
		_, size := utf8.DecodeLastRune(data)
		if size > 0 && !utf8.FullRune(data[len(data)-size:]) {
			w.pendingRune = append(w.pendingRune[:0], data[len(data)-size:]...)
			data = data[:len(data)-size]
		}
	}

	// 3. Find the last non-space rune in data.
	// Only the trailing run of whitespace can be trimmed here, so walk the data
	// backwards one rune at a time instead of decoding the whole chunk. Trailing
	// whitespace is the uncommon case, so this is normally a single decode.
	lastNonSpaceEnd := -1
	for end := len(data); end > 0; {
		r, size := utf8.DecodeLastRune(data[:end])
		if !unicode.IsSpace(r) {
			lastNonSpaceEnd = end
			break
		}
		end -= size
	}

	if lastNonSpaceEnd == -1 {
		// All runes in this chunk are whitespace: buffer them in trailingWS
		if len(w.trailingWS)+len(data) > maxCap {
			return 0, fmt.Errorf("%w: trailing whitespace exceeds buffer limit (%d > %d)",
				ErrSemanticFactBudgetExceeded, len(w.trailingWS)+len(data), maxCap)
		}
		w.trailingWS = append(w.trailingWS, data...)
		return origLen, nil
	}

	// Flush any previously buffered trailingWS
	if len(w.trailingWS) > 0 {
		if _, err := w.out.Write(w.trailingWS); err != nil {
			return 0, err
		}
		w.trimmedBytes += int64(len(w.trailingWS))
		w.trailingWS = w.trailingWS[:0]
	}

	// Write up to lastNonSpaceEnd in one call
	toWrite := data[:lastNonSpaceEnd]
	if len(toWrite) > 0 {
		if _, err := w.out.Write(toWrite); err != nil {
			return 0, err
		}
		w.trimmedBytes += int64(len(toWrite))
	}

	// Buffer trailing whitespace of this chunk
	if lastNonSpaceEnd < len(data) {
		tail := data[lastNonSpaceEnd:]
		if len(w.trailingWS)+len(tail) > maxCap {
			return 0, fmt.Errorf("%w: trailing whitespace exceeds buffer limit (%d > %d)",
				ErrSemanticFactBudgetExceeded, len(w.trailingWS)+len(tail), maxCap)
		}
		w.trailingWS = append(w.trailingWS, tail...)
	}

	return origLen, nil
}

func (w *TrimSpaceWriter) Close() error {
	w.trailingWS = nil
	w.pendingRune = nil
	if c, ok := w.out.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
