package largebody

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrSpliceClosed is returned when attempting an operation on a closed SpliceReader.
var ErrSpliceClosed = errors.New("largebody: splice reader is closed")

// EncodeModelToken validates and encodes a model name into a complete JSON string token
// with standard JSON escaping (Requirement 9.2).
//
// If model is already a complete, valid quoted JSON string token (e.g. from pre-encoding),
// it is returned directly to prevent redundant double-encoding.
func EncodeModelToken(model string) ([]byte, error) {
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("largebody: replacement model must not be empty")
	}
	// Check if already a valid quoted JSON string token.
	if len(model) >= 2 && model[0] == '"' && model[len(model)-1] == '"' {
		var decoded string
		if err := json.Unmarshal([]byte(model), &decoded); err == nil && strings.TrimSpace(decoded) != "" {
			return []byte(model), nil
		}
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("largebody: failed to JSON encode replacement model: %w", err)
	}
	return encoded, nil
}

// ValidateModelSpan validates that span describes a non-empty, in-bounds byte range
// for a body of sourceSize using checked int64 arithmetic (Requirements 9.1, 9.4).
func ValidateModelSpan(sourceSize int64, span Span) error {
	if sourceSize < 0 {
		return fmt.Errorf("largebody: source size must be >= 0, got %d", sourceSize)
	}
	if err := span.Validate(); err != nil {
		return fmt.Errorf("largebody: invalid span: %w", err)
	}
	if span.Length == 0 {
		return fmt.Errorf("largebody: span length must be > 0")
	}
	if span.Length < 2 {
		return fmt.Errorf("largebody: span length must be >= 2 for quoted JSON token, got %d", span.Length)
	}
	spanEnd, err := span.End()
	if err != nil {
		return fmt.Errorf("largebody: invalid span bounds: %w", err)
	}
	if span.Offset > sourceSize {
		return fmt.Errorf("largebody: span offset %d exceeds source size %d", span.Offset, sourceSize)
	}
	if spanEnd > sourceSize {
		return fmt.Errorf("largebody: span end %d exceeds source size %d", spanEnd, sourceSize)
	}
	return nil
}

// ValidateModelSpans validates that all provided spans are valid and non-overlapping
// within sourceSize (Requirement 9.5).
func ValidateModelSpans(sourceSize int64, spans ...Span) error {
	for i, s := range spans {
		if err := ValidateModelSpan(sourceSize, s); err != nil {
			return fmt.Errorf("largebody: span %d invalid: %w", i, err)
		}
	}
	for i := range spans {
		s1 := spans[i]
		end1, _ := s1.End()
		for j := i + 1; j < len(spans); j++ {
			s2 := spans[j]
			end2, _ := s2.End()
			if s1.Offset == s2.Offset && s1.Length == s2.Length {
				return fmt.Errorf("largebody: duplicate span at offset %d length %d", s1.Offset, s1.Length)
			}
			// Check overlap: max(start1, start2) < min(end1, end2)
			startMax := max(s2.Offset, s1.Offset)
			endMin := min(end2, end1)
			if startMax < endMin {
				return fmt.Errorf("largebody: overlapping spans [%d,%d) and [%d,%d)",
					s1.Offset, end1, s2.Offset, end2)
			}
		}
	}
	return nil
}

// SpliceReader streams a spliced body (prefix + replacement + suffix) without constructing
// a second full body in memory (Requirements 9.3, 9.4).
// It implements io.Reader, io.Closer, io.ReadCloser, and io.WriterTo.
type SpliceReader struct {
	source           io.Reader
	closer           io.Closer
	sourceSize       int64
	span             Span
	replacementBytes []byte
	rewrittenLength  int64

	prefixRemaining      int64
	spanDiscardRemaining int64
	replacementOffset    int
	suffixRemaining      int64
	bytesReadTotal       int64
	closed               bool
}

var (
	_ io.Reader     = (*SpliceReader)(nil)
	_ io.Closer     = (*SpliceReader)(nil)
	_ io.ReadCloser = (*SpliceReader)(nil)
	_ io.WriterTo   = (*SpliceReader)(nil)
)

// RewrittenLength reports the exact checked rewritten body length in bytes (Requirement 9.4).
func (s *SpliceReader) RewrittenLength() int64 {
	return s.rewrittenLength
}

// Span reports the exact scanner span being replaced.
func (s *SpliceReader) Span() Span {
	return s.span
}

// ReplacementToken returns the JSON-encoded replacement token as a string.
func (s *SpliceReader) ReplacementToken() string {
	return string(s.replacementBytes)
}

// ReplacementBytes returns a copy of the JSON-encoded replacement bytes.
func (s *SpliceReader) ReplacementBytes() []byte {
	out := make([]byte, len(s.replacementBytes))
	copy(out, s.replacementBytes)
	return out
}

// SourceSize reports the original source body length in bytes.
func (s *SpliceReader) SourceSize() int64 {
	return s.sourceSize
}

// Read implements io.Reader, streaming the prefix, replacement, and suffix in sequence.
func (s *SpliceReader) Read(p []byte) (int, error) {
	if s.closed {
		return 0, ErrSpliceClosed
	}
	if len(p) == 0 {
		return 0, nil
	}

	totalRead := 0

	for totalRead < len(p) {
		dest := p[totalRead:]

		// Phase 1: Stream prefix from source.
		if s.prefixRemaining > 0 {
			toRead := min(s.prefixRemaining, int64(len(dest)))
			n, err := s.source.Read(dest[:toRead])
			if n > 0 {
				s.prefixRemaining -= int64(n)
				s.bytesReadTotal += int64(n)
				totalRead += n
			}
			if err != nil {
				if errors.Is(err, io.EOF) && s.prefixRemaining > 0 {
					return totalRead, io.ErrUnexpectedEOF
				}
				if totalRead > 0 {
					return totalRead, nil
				}
				return 0, err
			}
			continue
		}

		// Phase 2: Discard original span bytes from source stream.
		if s.spanDiscardRemaining > 0 {
			if seeker, ok := s.source.(io.ReadSeeker); ok {
				_, err := seeker.Seek(s.spanDiscardRemaining, io.SeekCurrent)
				if err != nil {
					if totalRead > 0 {
						return totalRead, nil
					}
					return 0, err
				}
				s.spanDiscardRemaining = 0
			} else {
				var scratch [512]byte
				for s.spanDiscardRemaining > 0 {
					chunk := min(s.spanDiscardRemaining, int64(len(scratch)))
					n, err := s.source.Read(scratch[:chunk])
					s.spanDiscardRemaining -= int64(n)
					if err != nil {
						if errors.Is(err, io.EOF) && s.spanDiscardRemaining > 0 {
							return totalRead, io.ErrUnexpectedEOF
						}
						if totalRead > 0 {
							return totalRead, nil
						}
						return 0, err
					}
				}
			}
			continue
		}

		// Phase 3: Emit replacement token bytes.
		if s.replacementOffset < len(s.replacementBytes) {
			n := copy(dest, s.replacementBytes[s.replacementOffset:])
			s.replacementOffset += n
			s.bytesReadTotal += int64(n)
			totalRead += n
			continue
		}

		// Phase 4: Stream suffix from source.
		if s.suffixRemaining > 0 {
			toRead := min(s.suffixRemaining, int64(len(dest)))
			n, err := s.source.Read(dest[:toRead])
			if n > 0 {
				s.suffixRemaining -= int64(n)
				s.bytesReadTotal += int64(n)
				totalRead += n
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					if s.suffixRemaining > 0 {
						return totalRead, io.ErrUnexpectedEOF
					}
					return totalRead, io.EOF
				}
				if totalRead > 0 {
					return totalRead, nil
				}
				return 0, err
			}
			continue
		}

		// Phase 5: Everything has been read.
		break
	}

	if totalRead == 0 {
		return 0, io.EOF
	}
	return totalRead, nil
}

// WriteTo implements io.WriterTo, efficiently streaming spliced bytes directly into w.
func (s *SpliceReader) WriteTo(w io.Writer) (int64, error) {
	if s.closed {
		return 0, ErrSpliceClosed
	}

	var written int64

	// 1. Prefix
	if s.prefixRemaining > 0 {
		n, err := io.CopyN(w, s.source, s.prefixRemaining)
		written += n
		s.prefixRemaining -= n
		s.bytesReadTotal += n
		if err != nil {
			if errors.Is(err, io.EOF) && s.prefixRemaining > 0 {
				return written, io.ErrUnexpectedEOF
			}
			return written, err
		}
	}

	// 2. Discard span
	if s.spanDiscardRemaining > 0 {
		if seeker, ok := s.source.(io.ReadSeeker); ok {
			_, err := seeker.Seek(s.spanDiscardRemaining, io.SeekCurrent)
			if err != nil {
				return written, err
			}
			s.spanDiscardRemaining = 0
		} else {
			n, err := io.CopyN(io.Discard, s.source, s.spanDiscardRemaining)
			s.spanDiscardRemaining -= n
			if err != nil {
				if errors.Is(err, io.EOF) && s.spanDiscardRemaining > 0 {
					return written, io.ErrUnexpectedEOF
				}
				return written, err
			}
		}
	}

	// 3. Replacement
	if s.replacementOffset < len(s.replacementBytes) {
		rem := s.replacementBytes[s.replacementOffset:]
		n, err := w.Write(rem)
		written += int64(n)
		s.replacementOffset += n
		s.bytesReadTotal += int64(n)
		if err != nil {
			return written, err
		}
	}

	// 4. Suffix
	if s.suffixRemaining > 0 {
		n, err := io.CopyN(w, s.source, s.suffixRemaining)
		written += n
		s.suffixRemaining -= n
		s.bytesReadTotal += n
		if err != nil {
			if errors.Is(err, io.EOF) && s.suffixRemaining > 0 {
				return written, io.ErrUnexpectedEOF
			}
			return written, err
		}
	}

	return written, nil
}

// Close releases the underlying source reader if it implements io.Closer.
func (s *SpliceReader) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.closer != nil {
		return s.closer.Close()
	}
	return nil
}

// SpliceModelToken constructs a streaming SpliceReader for source, replacing span
// with the JSON-encoded replacement model (Requirements 9.1–9.4).
//
// Supported source types:
//   - []byte
//   - string
//   - Source (largebody.Source)
//   - *CompletedSource
//   - *SpillBuffer
//   - io.ReadSeeker (with known size or seekable)
//   - io.ReaderAt (with Size() int64)
func SpliceModelToken(source any, span Span, replacement string) (*SpliceReader, error) {
	if source == nil {
		return nil, fmt.Errorf("largebody: source must not be nil")
	}

	encodedToken, err := EncodeModelToken(replacement)
	if err != nil {
		return nil, err
	}

	var (
		reader     io.Reader
		closer     io.Closer
		sourceSize int64
		inspectBuf []byte
	)

	switch s := source.(type) {
	case []byte:
		sourceSize = int64(len(s))
		reader = bytes.NewReader(s)
		inspectBuf = s
	case string:
		sourceSize = int64(len(s))
		reader = strings.NewReader(s)
		inspectBuf = []byte(s)
	case Source:
		// *CompletedSource and *SpillBuffer implement Source and are handled
		// by this clause; dedicated cases below them would be unreachable.
		sourceSize = s.Size()
		rc, err := s.Open()
		if err != nil {
			return nil, fmt.Errorf("largebody: failed to open source: %w", err)
		}
		reader = rc
		closer = rc
	case io.ReadSeeker:
		if sizer, ok := s.(interface{ Size() int64 }); ok {
			sourceSize = sizer.Size()
		} else {
			sz, err := s.Seek(0, io.SeekEnd)
			if err != nil {
				return nil, fmt.Errorf("largebody: failed to seek source to end: %w", err)
			}
			if _, err := s.Seek(0, io.SeekStart); err != nil {
				return nil, fmt.Errorf("largebody: failed to rewind source: %w", err)
			}
			sourceSize = sz
		}
		reader = s
		if cl, ok := s.(io.Closer); ok {
			closer = cl
		}
	case io.ReaderAt:
		if sizer, ok := s.(interface{ Size() int64 }); ok {
			sourceSize = sizer.Size()
			reader = io.NewSectionReader(s, 0, sourceSize)
			if cl, ok := s.(io.Closer); ok {
				closer = cl
			}
		} else {
			return nil, fmt.Errorf("largebody: io.ReaderAt source must implement Size() int64; use SpliceModelTokenAt instead")
		}
	default:
		return nil, fmt.Errorf("largebody: unsupported source type %T; use SpliceModelTokenReader for generic io.Reader", source)
	}

	// Validate span bounds and checked math.
	if err := ValidateModelSpan(sourceSize, span); err != nil {
		if closer != nil {
			_ = closer.Close()
		}
		return nil, err
	}

	// If source data is inspectable in memory, verify span points to a quoted JSON string token.
	if inspectBuf != nil {
		if inspectBuf[span.Offset] != '"' || inspectBuf[span.Offset+span.Length-1] != '"' {
			return nil, fmt.Errorf("largebody: span does not point to a quoted JSON string token in source")
		}
	}

	spanEnd, err := span.End()
	if err != nil {
		if closer != nil {
			_ = closer.Close()
		}
		return nil, err
	}

	prefixLen := span.Offset
	replacementLen := int64(len(encodedToken))
	suffixLen := sourceSize - spanEnd

	rewrittenLen, err := CheckedSpliceLength(prefixLen, replacementLen, suffixLen)
	if err != nil {
		if closer != nil {
			_ = closer.Close()
		}
		return nil, err
	}

	return &SpliceReader{
		source:               reader,
		closer:               closer,
		sourceSize:           sourceSize,
		span:                 span,
		replacementBytes:     encodedToken,
		rewrittenLength:      rewrittenLen,
		prefixRemaining:      prefixLen,
		spanDiscardRemaining: span.Length,
		suffixRemaining:      suffixLen,
	}, nil
}

// SpliceModelTokenReader creates a streaming SpliceReader from an arbitrary io.Reader with known sourceSize.
func SpliceModelTokenReader(r io.Reader, sourceSize int64, span Span, replacement string) (*SpliceReader, error) {
	if r == nil {
		return nil, fmt.Errorf("largebody: source reader must not be nil")
	}
	encodedToken, err := EncodeModelToken(replacement)
	if err != nil {
		return nil, err
	}
	if err := ValidateModelSpan(sourceSize, span); err != nil {
		return nil, err
	}
	spanEnd, err := span.End()
	if err != nil {
		return nil, err
	}

	prefixLen := span.Offset
	replacementLen := int64(len(encodedToken))
	suffixLen := sourceSize - spanEnd

	rewrittenLen, err := CheckedSpliceLength(prefixLen, replacementLen, suffixLen)
	if err != nil {
		return nil, err
	}

	var closer io.Closer
	if cl, ok := r.(io.Closer); ok {
		closer = cl
	}

	return &SpliceReader{
		source:               r,
		closer:               closer,
		sourceSize:           sourceSize,
		span:                 span,
		replacementBytes:     encodedToken,
		rewrittenLength:      rewrittenLen,
		prefixRemaining:      prefixLen,
		spanDiscardRemaining: span.Length,
		suffixRemaining:      suffixLen,
	}, nil
}

// SpliceModelTokenAt creates a streaming SpliceReader from an io.ReaderAt with known sourceSize.
func SpliceModelTokenAt(r io.ReaderAt, sourceSize int64, span Span, replacement string) (*SpliceReader, error) {
	if r == nil {
		return nil, fmt.Errorf("largebody: source reader must not be nil")
	}
	secReader := io.NewSectionReader(r, 0, sourceSize)
	return SpliceModelTokenReader(secReader, sourceSize, span, replacement)
}

// SpliceModelTokenBytes is a convenience helper that splices in-memory bytes and returns
// the complete rewritten byte slice.
func SpliceModelTokenBytes(source []byte, span Span, replacement string) ([]byte, error) {
	r, err := SpliceModelToken(source, span, replacement)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

// SpliceModelTokenPlan applies a certified RewritePlan to a source.
// It verifies that the plan is valid, uses plan.Rewrite.Span(), encodes
// plan.ReplacementModel, and asserts that the computed rewritten length
// matches plan.RewrittenLength (Requirements 9.1–9.4).
func SpliceModelTokenPlan(source any, plan RewritePlan, maxFactBytes int64) (*SpliceReader, error) {
	if err := plan.Validate(maxFactBytes); err != nil {
		return nil, fmt.Errorf("largebody: invalid rewrite plan: %w", err)
	}
	r, err := SpliceModelToken(source, plan.Rewrite.Span(), plan.ReplacementModel)
	if err != nil {
		return nil, err
	}
	if r.RewrittenLength() != plan.RewrittenLength {
		_ = r.Close()
		return nil, fmt.Errorf("largebody: splice rewritten length %d does not match plan rewritten length %d",
			r.RewrittenLength(), plan.RewrittenLength)
	}
	return r, nil
}
