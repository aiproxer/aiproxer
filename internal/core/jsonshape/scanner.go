package jsonshape

import (
	"context"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// EventType represents the syntactic token event observed during scanning.
type EventType uint8

const (
	EventNone EventType = iota
	EventObjectStart
	EventObjectEnd
	EventArrayStart
	EventArrayEnd
	EventKey
	EventString
	EventNumber
	EventTrue
	EventFalse
	EventNull
)

// Span represents an exact raw byte range [Offset, Offset+Length) in the source stream.
type Span struct {
	Offset int64
	Length int64
}

// Validate rejects negative bounds and checked-int64 overflow.
func (s Span) Validate() error {
	if s.Offset < 0 {
		return fmt.Errorf("jsonshape: span offset must be >= 0, got %d", s.Offset)
	}
	if s.Length < 0 {
		return fmt.Errorf("jsonshape: span length must be >= 0, got %d", s.Length)
	}
	if _, err := s.End(); err != nil {
		return err
	}
	return nil
}

// End returns the exclusive end offset using checked int64 math.
func (s Span) End() (int64, error) {
	if s.Offset > math.MaxInt64-s.Length {
		return 0, fmt.Errorf("jsonshape: span end overflows int64 (offset %d length %d)", s.Offset, s.Length)
	}
	return s.Offset + s.Length, nil
}

// Event describes a syntactic token event in the JSON stream.
// Giant scalar string contents are never retained in Event.
type Event struct {
	Type     EventType
	Offset   int64
	Length   int64
	Span     Span
	Depth    int
	Key      string
	Path     []string
	TopLevel bool
}

// IsTopLevelKey reports whether the event is an immediate member of the root object matching key.
func (e Event) IsTopLevelKey(key string) bool {
	return e.TopLevel && e.Key == key
}

// EventHandler receives stream events during scanning.
type EventHandler interface {
	OnEvent(e Event) error
}

// EventHandlerFunc is an adapter to allow ordinary functions as EventHandler.
type EventHandlerFunc func(e Event) error

func (f EventHandlerFunc) OnEvent(e Event) error {
	return f(e)
}

// TopLevelSpanTracker collects exact raw byte spans for selected top-level object values.
// It discriminates nested keys and values, recording only immediate root-object values.
type TopLevelSpanTracker struct {
	selected map[string]struct{}
	spans    map[string]Span
	counts   map[string]int
}

// NewTopLevelSpanTracker creates a tracker for the provided top-level keys.
func NewTopLevelSpanTracker(keys ...string) *TopLevelSpanTracker {
	selected := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		selected[k] = struct{}{}
	}
	return &TopLevelSpanTracker{
		selected: selected,
		spans:    make(map[string]Span),
		counts:   make(map[string]int),
	}
}

// OnEvent implements EventHandler.
func (t *TopLevelSpanTracker) OnEvent(e Event) error {
	if !e.TopLevel || e.Type == EventKey {
		return nil
	}
	if _, ok := t.selected[e.Key]; ok {
		switch e.Type {
		case EventString, EventNumber, EventTrue, EventFalse, EventNull, EventObjectEnd, EventArrayEnd:
			t.counts[e.Key]++
			t.spans[e.Key] = e.Span
		}
	}
	return nil
}

// Span returns the recorded raw byte span for the given top-level key.
func (t *TopLevelSpanTracker) Span(key string) (Span, bool) {
	s, ok := t.spans[key]
	return s, ok
}

// Spans returns a copy of all recorded top-level spans.
func (t *TopLevelSpanTracker) Spans() map[string]Span {
	res := make(map[string]Span, len(t.spans))
	maps.Copy(res, t.spans)
	return res
}

// Count returns the number of times a top-level value was recorded for the given key.
func (t *TopLevelSpanTracker) Count(key string) int {
	return t.counts[key]
}

// HasDuplicate reports whether more than one top-level value was encountered for the given key.
func (t *TopLevelSpanTracker) HasDuplicate(key string) bool {
	return t.counts[key] > 1
}

// Option configures a Scanner.
type Option func(*Scanner)

// WithEventHandler registers an event handler on the scanner.
func WithEventHandler(h EventHandler) Option {
	return func(s *Scanner) {
		s.handler = h
	}
}

// WithTrackedTopLevelSpans configures the scanner to record exact spans for selected top-level keys.
func WithTrackedTopLevelSpans(keys ...string) Option {
	return func(s *Scanner) {
		s.tracker = NewTopLevelSpanTracker(keys...)
	}
}

// StringContext describes the syntactic position of a string value being scanned.
type StringContext struct {
	Key      string
	Path     []string
	TopLevel bool
}

// StringWriterResolver resolves an optional io.Writer to receive unescaped string bytes
// as they are decoded during scanning. If the resolved writer implements io.Closer,
// it will be closed when the string ends or scanning terminates.
type StringWriterResolver func(ctx StringContext) (io.Writer, error)

// WithStringWriterResolver configures a resolver for streaming string values.
func WithStringWriterResolver(r StringWriterResolver) Option {
	return func(s *Scanner) {
		s.strWriterResolver = r
	}
}

type scanState uint8

const (
	stateExpectValue scanState = iota
	stateExpectObjectKeyOrEnd
	stateExpectObjectKey
	stateExpectColon
	stateExpectObjectValue
	stateExpectObjectCommaOrEnd
	stateExpectArrayValueOrEnd
	stateExpectArrayValue
	stateExpectArrayCommaOrEnd
	stateInString
	stateInNumber
	stateInLiteral
	stateRootDone
)

type numSubState uint8

const (
	numStateMinus numSubState = iota
	numStateZero
	numStateInt
	numStateDot
	numStateFrac
	numStateExp
	numStateExpSign
	numStateExpDigits
)

type scanFrame struct {
	object      bool
	count       int
	seen        map[string]struct{}
	startOffset int64
	currentKey  string
}

func newScanObjectFrame(startOffset int64, rejectDuplicates bool) scanFrame {
	f := scanFrame{
		object:      true,
		startOffset: startOffset,
	}
	if rejectDuplicates {
		f.seen = make(map[string]struct{})
	}
	return f
}

// Scanner performs incremental JSON safety scanning and token event emission.
type Scanner struct {
	ctx     context.Context
	limits  Limits
	handler EventHandler
	tracker *TopLevelSpanTracker

	state      scanState
	frames     []scanFrame
	rootValues int
	bytes      int64
	tokens     int
	maxDepth   int
	err        error

	tokStartOffset int64
	tokDepth       int

	// String state
	strDecodedBytes   int
	strIsKey          bool
	keyBuf            []byte
	strInEscape       bool
	escapeBuf         [12]byte
	escapeLen         int
	strWriterResolver StringWriterResolver
	activeStrWriter   io.Writer
	activeStrCloser   io.Closer

	// Number state
	numState numSubState
	numLen   int

	// Literal state
	literalExpected string
	literalIdx      int
	literalType     EventType

	// Pending UTF-8 across chunks
	pendingUTF8    [4]byte
	pendingUTF8Len int
}

// NewScanner constructs an incremental JSON safety scanner with normalized limits.
func NewScanner(ctx context.Context, limits Limits, opts ...Option) *Scanner {
	if ctx == nil {
		ctx = context.Background()
	}
	s := &Scanner{
		ctx:    ctx,
		limits: NormalizeLimits(limits),
		state:  stateExpectValue,
		frames: make([]scanFrame, 0, 8),
		keyBuf: make([]byte, 0, 64),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// SetEventHandler sets or updates the event handler.
func (s *Scanner) SetEventHandler(h EventHandler) {
	s.handler = h
}

// TopLevelSpan returns the recorded raw span for the selected top-level key.
func (s *Scanner) TopLevelSpan(key string) (Span, bool) {
	if s.tracker == nil {
		return Span{}, false
	}
	return s.tracker.Span(key)
}

// TopLevelSpans returns all recorded top-level spans.
func (s *Scanner) TopLevelSpans() map[string]Span {
	if s.tracker == nil {
		return nil
	}
	return s.tracker.Spans()
}

func (s *Scanner) currentPath() []string {
	var path []string
	for i := range s.frames {
		if s.frames[i].currentKey != "" {
			path = append(path, s.frames[i].currentKey)
		}
	}
	return path
}

func (s *Scanner) valueContext() (key string, path []string, topLevel bool) {
	if len(s.frames) == 0 {
		return "", nil, false
	}
	top := &s.frames[len(s.frames)-1]
	if top.object {
		key = top.currentKey
	}
	topLevel = len(s.frames) == 1 && top.object
	path = s.currentPath()
	return key, path, topLevel
}

func (s *Scanner) emitEvent(t EventType, offset, length int64, key string, path []string, topLevel bool) error {
	if s.handler == nil && s.tracker == nil {
		return nil
	}
	var pathCopy []string
	if len(path) > 0 {
		pathCopy = slices.Clone(path)
	}
	e := Event{
		Type:     t,
		Offset:   offset,
		Length:   length,
		Span:     Span{Offset: offset, Length: length},
		Depth:    s.tokDepth,
		Key:      key,
		Path:     pathCopy,
		TopLevel: topLevel,
	}
	if s.tracker != nil {
		if err := s.tracker.OnEvent(e); err != nil {
			return err
		}
	}
	if s.handler != nil {
		return s.handler.OnEvent(e)
	}
	return nil
}

func (s *Scanner) checkTokenLimit() error {
	s.tokens++
	if s.tokens > s.limits.MaxTokens {
		return &Error{Kind: KindTooManyTokens, Limit: s.limits.MaxTokens, Value: s.tokens}
	}
	return nil
}

func (s *Scanner) valueFinished() {
	if len(s.frames) == 0 {
		s.state = stateRootDone
	} else if s.frames[len(s.frames)-1].object {
		s.frames[len(s.frames)-1].currentKey = ""
		s.state = stateExpectObjectCommaOrEnd
	} else {
		s.state = stateExpectArrayCommaOrEnd
	}
}

// Feed processes an incremental chunk of JSON bytes.
func (s *Scanner) Feed(chunk []byte) error {
	if s.err != nil {
		return s.err
	}
	if err := s.ctx.Err(); err != nil {
		s.err = canceledError(err)
		return s.err
	}

	s.bytes += int64(len(chunk))
	if s.bytes > s.limits.MaxBytes {
		s.err = &Error{Kind: KindTooLarge, Limit: int(s.limits.MaxBytes), Value: int(s.bytes)}
		return s.err
	}

	var data []byte
	if s.pendingUTF8Len > 0 {
		data = make([]byte, s.pendingUTF8Len+len(chunk))
		copy(data, s.pendingUTF8[:s.pendingUTF8Len])
		copy(data[s.pendingUTF8Len:], chunk)
		s.pendingUTF8Len = 0
	} else {
		data = chunk
	}

	i := 0
	n := len(data)
	for i < n {
		if err := s.ctx.Err(); err != nil {
			s.err = canceledError(err)
			return s.err
		}

		b := data[i]

		// Handle string scanning
		if s.state == stateInString {
			if s.strInEscape {
				s.escapeBuf[s.escapeLen] = b
				s.escapeLen++
				i++

				if s.escapeLen == 1 {
					esc := s.escapeBuf[0]
					switch esc {
					case '"', '\\', '/':
						s.strDecodedBytes++
						if s.strIsKey {
							s.keyBuf = append(s.keyBuf, esc)
						} else {
							if err := s.writeActiveStr([]byte{esc}); err != nil {
								return s.err
							}
						}
						s.strInEscape = false
						s.escapeLen = 0
					case 'b':
						s.strDecodedBytes++
						if s.strIsKey {
							s.keyBuf = append(s.keyBuf, '\b')
						} else {
							if err := s.writeActiveStr([]byte{'\b'}); err != nil {
								return s.err
							}
						}
						s.strInEscape = false
						s.escapeLen = 0
					case 'f':
						s.strDecodedBytes++
						if s.strIsKey {
							s.keyBuf = append(s.keyBuf, '\f')
						} else {
							if err := s.writeActiveStr([]byte{'\f'}); err != nil {
								return s.err
							}
						}
						s.strInEscape = false
						s.escapeLen = 0
					case 'n':
						s.strDecodedBytes++
						if s.strIsKey {
							s.keyBuf = append(s.keyBuf, '\n')
						} else {
							if err := s.writeActiveStr([]byte{'\n'}); err != nil {
								return s.err
							}
						}
						s.strInEscape = false
						s.escapeLen = 0
					case 'r':
						s.strDecodedBytes++
						if s.strIsKey {
							s.keyBuf = append(s.keyBuf, '\r')
						} else {
							if err := s.writeActiveStr([]byte{'\r'}); err != nil {
								return s.err
							}
						}
						s.strInEscape = false
						s.escapeLen = 0
					case 't':
						s.strDecodedBytes++
						if s.strIsKey {
							s.keyBuf = append(s.keyBuf, '\t')
						} else {
							if err := s.writeActiveStr([]byte{'\t'}); err != nil {
								return s.err
							}
						}
						s.strInEscape = false
						s.escapeLen = 0
					case 'u':
						// wait for hex digits
					default:
						s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
						return s.err
					}
				} else if s.escapeBuf[0] == 'u' {
					switch s.escapeLen {
					case 5:
						r, ok := parseHex4(s.escapeBuf[1:5])
						if !ok {
							s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
							return s.err
						}
						if utf16.IsSurrogate(r) {
							if 0xD800 <= r && r <= 0xDBFF {
								// high surrogate: wait for \uYYYY
							} else {
								// isolated low surrogate
								s.strDecodedBytes += 3
								if s.strIsKey {
									s.keyBuf = utf8.AppendRune(s.keyBuf, utf8.RuneError)
								} else {
									if err := s.writeActiveRune(utf8.RuneError); err != nil {
										return s.err
									}
								}
								s.strInEscape = false
								s.escapeLen = 0
							}
						} else {
							rlen := utf8.RuneLen(r)
							s.strDecodedBytes += rlen
							if s.strIsKey {
								s.keyBuf = utf8.AppendRune(s.keyBuf, r)
							} else {
								if err := s.writeActiveRune(r); err != nil {
									return s.err
								}
							}
							s.strInEscape = false
							s.escapeLen = 0
						}
					case 6:
						if b != '\\' {
							// not followed by escape: previous was isolated high surrogate
							s.strDecodedBytes += 3
							if s.strIsKey {
								s.keyBuf = utf8.AppendRune(s.keyBuf, utf8.RuneError)
							} else {
								if err := s.writeActiveRune(utf8.RuneError); err != nil {
									return s.err
								}
							}
							s.strInEscape = false
							s.escapeLen = 0
							i-- // re-process b
						}
					case 7:
						if b != 'u' {
							s.strDecodedBytes += 3
							if s.strIsKey {
								s.keyBuf = utf8.AppendRune(s.keyBuf, utf8.RuneError)
							} else {
								if err := s.writeActiveRune(utf8.RuneError); err != nil {
									return s.err
								}
							}
							s.escapeBuf[0] = b
							s.escapeLen = 1
							s.strInEscape = true

							switch b {
							case '"', '\\', '/':
								s.strDecodedBytes++
								if s.strIsKey {
									s.keyBuf = append(s.keyBuf, b)
								} else {
									if err := s.writeActiveStr([]byte{b}); err != nil {
										return s.err
									}
								}
								s.strInEscape = false
								s.escapeLen = 0
							case 'b':
								s.strDecodedBytes++
								if s.strIsKey {
									s.keyBuf = append(s.keyBuf, '\b')
								} else {
									if err := s.writeActiveStr([]byte{'\b'}); err != nil {
										return s.err
									}
								}
								s.strInEscape = false
								s.escapeLen = 0
							case 'f':
								s.strDecodedBytes++
								if s.strIsKey {
									s.keyBuf = append(s.keyBuf, '\f')
								} else {
									if err := s.writeActiveStr([]byte{'\f'}); err != nil {
										return s.err
									}
								}
								s.strInEscape = false
								s.escapeLen = 0
							case 'n':
								s.strDecodedBytes++
								if s.strIsKey {
									s.keyBuf = append(s.keyBuf, '\n')
								} else {
									if err := s.writeActiveStr([]byte{'\n'}); err != nil {
										return s.err
									}
								}
								s.strInEscape = false
								s.escapeLen = 0
							case 'r':
								s.strDecodedBytes++
								if s.strIsKey {
									s.keyBuf = append(s.keyBuf, '\r')
								} else {
									if err := s.writeActiveStr([]byte{'\r'}); err != nil {
										return s.err
									}
								}
								s.strInEscape = false
								s.escapeLen = 0
							case 't':
								s.strDecodedBytes++
								if s.strIsKey {
									s.keyBuf = append(s.keyBuf, '\t')
								} else {
									if err := s.writeActiveStr([]byte{'\t'}); err != nil {
										return s.err
									}
								}
								s.strInEscape = false
								s.escapeLen = 0
							default:
								s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
								return s.err
							}
						}
					case 11:
						r1, _ := parseHex4(s.escapeBuf[1:5])
						r2, ok := parseHex4(s.escapeBuf[7:11])
						if !ok {
							s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
							return s.err
						}
						if 0xDC00 <= r2 && r2 <= 0xDFFF {
							combined := utf16.DecodeRune(r1, r2)
							s.strDecodedBytes += 4
							if s.strIsKey {
								s.keyBuf = utf8.AppendRune(s.keyBuf, combined)
							} else {
								if err := s.writeActiveRune(combined); err != nil {
									return s.err
								}
							}
							s.strInEscape = false
							s.escapeLen = 0
						} else {
							// r1 was isolated surrogate, evaluate r2 anew
							s.strDecodedBytes += 3
							if s.strIsKey {
								s.keyBuf = utf8.AppendRune(s.keyBuf, utf8.RuneError)
							} else {
								if err := s.writeActiveRune(utf8.RuneError); err != nil {
									return s.err
								}
							}
							s.escapeBuf[0] = 'u'
							copy(s.escapeBuf[1:5], s.escapeBuf[7:11])
							s.escapeLen = 5
							if utf16.IsSurrogate(r2) {
								if 0xD800 <= r2 && r2 <= 0xDBFF {
									// high surrogate again, wait
								} else {
									s.strDecodedBytes += 3
									if s.strIsKey {
										s.keyBuf = utf8.AppendRune(s.keyBuf, utf8.RuneError)
									} else {
										if err := s.writeActiveRune(utf8.RuneError); err != nil {
											return s.err
										}
									}
									s.strInEscape = false
									s.escapeLen = 0
								}
							} else {
								rlen := utf8.RuneLen(r2)
								s.strDecodedBytes += rlen
								if s.strIsKey {
									s.keyBuf = utf8.AppendRune(s.keyBuf, r2)
								} else {
									if err := s.writeActiveRune(r2); err != nil {
										return s.err
									}
								}
								s.strInEscape = false
								s.escapeLen = 0
							}
						}
					}
				}

				// Check length limits during string accumulation
				if s.strIsKey {
					if len(s.keyBuf) > s.limits.MaxKeyBytes {
						s.err = &Error{Kind: KindKeyTooLong, Limit: s.limits.MaxKeyBytes, Value: len(s.keyBuf)}
						return s.err
					}
				} else {
					if s.strDecodedBytes > s.limits.MaxStringBytes {
						s.err = &Error{Kind: KindStringTooLong, Limit: s.limits.MaxStringBytes, Value: s.strDecodedBytes}
						return s.err
					}
				}
				continue
			}

			if b == '\\' {
				s.strInEscape = true
				s.escapeLen = 0
				i++
				continue
			}

			if b == '"' {
				i++
				if err := s.checkTokenLimit(); err != nil {
					s.err = err
					return s.err
				}

				curOffset := s.bytes - int64(n-i)
				length := curOffset - s.tokStartOffset

				if s.strIsKey {
					if len(s.keyBuf) > s.limits.MaxKeyBytes {
						s.err = &Error{Kind: KindKeyTooLong, Limit: s.limits.MaxKeyBytes, Value: len(s.keyBuf)}
						return s.err
					}
					frame := &s.frames[len(s.frames)-1]
					keyStr := string(s.keyBuf)
					if s.limits.RejectDuplicateNames {
						if _, dup := frame.seen[keyStr]; dup {
							s.err = &Error{Kind: KindDuplicateName, Msg: "duplicate object member name"}
							return s.err
						}
						frame.seen[keyStr] = struct{}{}
					}
					frame.count++
					if frame.count > s.limits.MaxObjectKeys {
						s.err = &Error{Kind: KindTooManyItems, Limit: s.limits.MaxObjectKeys, Value: frame.count}
						return s.err
					}
					topLevel := len(s.frames) == 1 && s.frames[0].object
					path := append(s.currentPath(), keyStr)
					if err := s.emitEvent(EventKey, s.tokStartOffset, length, keyStr, path, topLevel); err != nil {
						s.err = err
						return s.err
					}
					frame.currentKey = keyStr
					s.state = stateExpectColon
				} else {
					if s.activeStrCloser != nil {
						if err := s.activeStrCloser.Close(); err != nil {
							s.err = err
							return s.err
						}
					}
					s.activeStrWriter = nil
					s.activeStrCloser = nil
					if s.strDecodedBytes > s.limits.MaxStringBytes {
						s.err = &Error{Kind: KindStringTooLong, Limit: s.limits.MaxStringBytes, Value: s.strDecodedBytes}
						return s.err
					}
					key, path, topLevel := s.valueContext()
					if err := s.emitEvent(EventString, s.tokStartOffset, length, key, path, topLevel); err != nil {
						s.err = err
						return s.err
					}
					s.valueFinished()
				}
				continue
			}

			if b < 0x20 {
				s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
				return s.err
			}

			if b < 0x80 {
				k := 0
				for i+k < n && data[i+k] >= 0x20 && data[i+k] < 0x80 && data[i+k] != '\\' && data[i+k] != '"' {
					k++
				}
				if k == 0 {
					s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
					return s.err
				}
				run := data[i : i+k]
				s.strDecodedBytes += k
				if s.strIsKey {
					s.keyBuf = append(s.keyBuf, run...)
					if len(s.keyBuf) > s.limits.MaxKeyBytes {
						s.err = &Error{Kind: KindKeyTooLong, Limit: s.limits.MaxKeyBytes, Value: len(s.keyBuf)}
						return s.err
					}
				} else {
					if err := s.writeActiveStr(run); err != nil {
						return s.err
					}
					if s.strDecodedBytes > s.limits.MaxStringBytes {
						s.err = &Error{Kind: KindStringTooLong, Limit: s.limits.MaxStringBytes, Value: s.strDecodedBytes}
						return s.err
					}
				}
				i += k
				continue
			}

			// Multibyte UTF-8
			if !utf8.FullRune(data[i:]) {
				copy(s.pendingUTF8[:], data[i:])
				s.pendingUTF8Len = n - i
				return nil
			}
			r, sz := utf8.DecodeRune(data[i:])
			if r == utf8.RuneError && sz == 1 {
				s.err = &Error{Kind: KindInvalidUTF8, Msg: "invalid UTF-8"}
				return s.err
			}
			s.strDecodedBytes += sz
			if s.strIsKey {
				s.keyBuf = append(s.keyBuf, data[i:i+sz]...)
				if len(s.keyBuf) > s.limits.MaxKeyBytes {
					s.err = &Error{Kind: KindKeyTooLong, Limit: s.limits.MaxKeyBytes, Value: len(s.keyBuf)}
					return s.err
				}
			} else {
				if err := s.writeActiveStr(data[i : i+sz]); err != nil {
					return s.err
				}
				if s.strDecodedBytes > s.limits.MaxStringBytes {
					s.err = &Error{Kind: KindStringTooLong, Limit: s.limits.MaxStringBytes, Value: s.strDecodedBytes}
					return s.err
				}
			}
			i += sz
			continue
		}

		// Handle number scanning
		if s.state == stateInNumber {
			canContinue := false
			switch s.numState {
			case numStateMinus:
				if b == '0' {
					s.numState = numStateZero
					canContinue = true
				} else if '1' <= b && b <= '9' {
					s.numState = numStateInt
					canContinue = true
				}
			case numStateZero:
				if b == '.' {
					s.numState = numStateDot
					canContinue = true
				} else if b == 'e' || b == 'E' {
					s.numState = numStateExp
					canContinue = true
				} else if '0' <= b && b <= '9' {
					s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
					return s.err
				}
			case numStateInt:
				if '0' <= b && b <= '9' {
					canContinue = true
				} else if b == '.' {
					s.numState = numStateDot
					canContinue = true
				} else if b == 'e' || b == 'E' {
					s.numState = numStateExp
					canContinue = true
				}
			case numStateDot:
				if '0' <= b && b <= '9' {
					s.numState = numStateFrac
					canContinue = true
				}
			case numStateFrac:
				if '0' <= b && b <= '9' {
					canContinue = true
				} else if b == 'e' || b == 'E' {
					s.numState = numStateExp
					canContinue = true
				}
			case numStateExp:
				if b == '+' || b == '-' {
					s.numState = numStateExpSign
					canContinue = true
				} else if '0' <= b && b <= '9' {
					s.numState = numStateExpDigits
					canContinue = true
				}
			case numStateExpSign:
				if '0' <= b && b <= '9' {
					s.numState = numStateExpDigits
					canContinue = true
				}
			case numStateExpDigits:
				if '0' <= b && b <= '9' {
					canContinue = true
				}
			}

			if canContinue {
				s.numLen++
				if s.numLen > s.limits.MaxNumberBytes {
					s.err = &Error{Kind: KindNumberTooLong, Limit: s.limits.MaxNumberBytes, Value: s.numLen}
					return s.err
				}
				i++
				continue
			}

			// Number ended; verify valid end substate
			if s.numState == numStateMinus || s.numState == numStateDot || s.numState == numStateExp || s.numState == numStateExpSign {
				s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
				return s.err
			}

			if !isDelimiterOrWhitespace(b) {
				s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
				return s.err
			}

			if err := s.checkTokenLimit(); err != nil {
				s.err = err
				return s.err
			}
			key, path, topLevel := s.valueContext()
			if err := s.emitEvent(EventNumber, s.tokStartOffset, int64(s.numLen), key, path, topLevel); err != nil {
				s.err = err
				return s.err
			}
			s.valueFinished()
			// Do not increment i; re-process delimiter/whitespace
			continue
		}

		// Handle literal scanning
		if s.state == stateInLiteral {
			if s.literalIdx < len(s.literalExpected) {
				if b == s.literalExpected[s.literalIdx] {
					s.literalIdx++
					i++
					continue
				}
				s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
				return s.err
			}

			// Matched full literal name
			if !isDelimiterOrWhitespace(b) {
				s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
				return s.err
			}

			if err := s.checkTokenLimit(); err != nil {
				s.err = err
				return s.err
			}
			key, path, topLevel := s.valueContext()
			if err := s.emitEvent(s.literalType, s.tokStartOffset, int64(len(s.literalExpected)), key, path, topLevel); err != nil {
				s.err = err
				return s.err
			}
			s.valueFinished()
			// Do not increment i; re-process delimiter/whitespace
			continue
		}

		// Check for UTF-8 validity outside string
		if b >= 0x80 {
			if !utf8.FullRune(data[i:]) {
				copy(s.pendingUTF8[:], data[i:])
				s.pendingUTF8Len = n - i
				return nil
			}
			r, sz := utf8.DecodeRune(data[i:])
			if r == utf8.RuneError && sz == 1 {
				s.err = &Error{Kind: KindInvalidUTF8, Msg: "invalid UTF-8"}
				return s.err
			}
			s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
			return s.err
		}

		// Skip whitespace
		if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
			i++
			continue
		}

		// Check unexpected closing delimiter
		switch b {
		case '}':
			if len(s.frames) == 0 || !s.frames[len(s.frames)-1].object {
				s.err = &Error{Kind: KindMalformed, Reason: MalformedUnexpectedClosing, Msg: "unexpected closing delimiter"}
				return s.err
			}
		case ']':
			if len(s.frames) == 0 || s.frames[len(s.frames)-1].object {
				s.err = &Error{Kind: KindMalformed, Reason: MalformedUnexpectedClosing, Msg: "unexpected closing delimiter"}
				return s.err
			}
		}

		// Handle states between tokens
		switch s.state {
		case stateExpectValue, stateExpectObjectValue, stateExpectArrayValueOrEnd, stateExpectArrayValue:
			if s.state == stateExpectArrayValueOrEnd && b == ']' {
				i++
				if err := s.checkTokenLimit(); err != nil {
					s.err = err
					return s.err
				}
				curOffset := s.bytes - int64(n-i)
				frame := s.frames[len(s.frames)-1]
				s.frames = s.frames[:len(s.frames)-1]
				length := curOffset - frame.startOffset
				key, path, topLevel := s.valueContext()
				if err := s.emitEvent(EventArrayEnd, frame.startOffset, length, key, path, topLevel); err != nil {
					s.err = err
					return s.err
				}
				s.valueFinished()
				continue
			}

			switch s.state {
			case stateExpectValue:
				s.rootValues++
				if s.rootValues > 1 {
					s.err = &Error{Kind: KindMalformed, Reason: MalformedMultipleValues, Msg: "multiple JSON values"}
					return s.err
				}
			case stateExpectArrayValueOrEnd, stateExpectArrayValue:
				frame := &s.frames[len(s.frames)-1]
				frame.count++
				if frame.count > s.limits.MaxArrayElems {
					s.err = &Error{Kind: KindTooManyItems, Limit: s.limits.MaxArrayElems, Value: frame.count}
					return s.err
				}
			}

			if err := s.startValue(b, int64(n-i)); err != nil {
				return err
			}
			i++

		case stateExpectObjectKeyOrEnd:
			if b == '}' {
				i++
				if err := s.checkTokenLimit(); err != nil {
					s.err = err
					return s.err
				}
				curOffset := s.bytes - int64(n-i)
				frame := s.frames[len(s.frames)-1]
				s.frames = s.frames[:len(s.frames)-1]
				length := curOffset - frame.startOffset
				key, path, topLevel := s.valueContext()
				if err := s.emitEvent(EventObjectEnd, frame.startOffset, length, key, path, topLevel); err != nil {
					s.err = err
					return s.err
				}
				s.valueFinished()
				continue
			}
			if b == '"' {
				s.startString(true, int64(n-i))
				i++
				continue
			}
			s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
			return s.err

		case stateExpectObjectKey:
			if b == '"' {
				s.startString(true, int64(n-i))
				i++
				continue
			}
			s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
			return s.err

		case stateExpectColon:
			if b == ':' {
				s.state = stateExpectObjectValue
				i++
				continue
			}
			s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
			return s.err

		case stateExpectObjectCommaOrEnd:
			if b == ',' {
				s.state = stateExpectObjectKey
				i++
				continue
			}
			if b == '}' {
				i++
				if err := s.checkTokenLimit(); err != nil {
					s.err = err
					return s.err
				}
				curOffset := s.bytes - int64(n-i)
				frame := s.frames[len(s.frames)-1]
				s.frames = s.frames[:len(s.frames)-1]
				length := curOffset - frame.startOffset
				key, path, topLevel := s.valueContext()
				if err := s.emitEvent(EventObjectEnd, frame.startOffset, length, key, path, topLevel); err != nil {
					s.err = err
					return s.err
				}
				s.valueFinished()
				continue
			}
			s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
			return s.err

		case stateExpectArrayCommaOrEnd:
			if b == ',' {
				s.state = stateExpectArrayValue
				i++
				continue
			}
			if b == ']' {
				i++
				if err := s.checkTokenLimit(); err != nil {
					s.err = err
					return s.err
				}
				curOffset := s.bytes - int64(n-i)
				frame := s.frames[len(s.frames)-1]
				s.frames = s.frames[:len(s.frames)-1]
				length := curOffset - frame.startOffset
				key, path, topLevel := s.valueContext()
				if err := s.emitEvent(EventArrayEnd, frame.startOffset, length, key, path, topLevel); err != nil {
					s.err = err
					return s.err
				}
				s.valueFinished()
				continue
			}
			s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
			return s.err

		case stateRootDone:
			if b == '{' || b == '[' || b == '"' || b == '-' || ('0' <= b && b <= '9') {
				s.err = &Error{Kind: KindMalformed, Reason: MalformedMultipleValues, Msg: "multiple JSON values"}
				return s.err
			}
			if b == 't' || b == 'f' || b == 'n' {
				if hasLiteralPrefix(data[i:]) {
					s.err = &Error{Kind: KindMalformed, Reason: MalformedMultipleValues, Msg: "multiple JSON values"}
				} else {
					s.err = &Error{Kind: KindMalformed, Reason: MalformedTrailingData, Msg: "trailing data after JSON value"}
				}
				return s.err
			}
			s.err = &Error{Kind: KindMalformed, Reason: MalformedTrailingData, Msg: "trailing data after JSON value"}
			return s.err
		}
	}

	return nil
}

func (s *Scanner) startValue(b byte, remainingBytes int64) error {
	s.tokStartOffset = s.bytes - remainingBytes
	s.tokDepth = len(s.frames) + 1

	switch b {
	case '{':
		if err := checkDepth(len(s.frames)+1, s.limits.MaxDepth); err != nil {
			s.err = err
			return s.err
		}
		if err := s.checkTokenLimit(); err != nil {
			s.err = err
			return s.err
		}
		key, path, topLevel := s.valueContext()
		if err := s.emitEvent(EventObjectStart, s.tokStartOffset, 1, key, path, topLevel); err != nil {
			s.err = err
			return s.err
		}
		s.frames = append(s.frames, newScanObjectFrame(s.tokStartOffset, s.limits.RejectDuplicateNames))
		s.maxDepth = max(s.maxDepth, len(s.frames))
		s.state = stateExpectObjectKeyOrEnd
	case '[':
		if err := checkDepth(len(s.frames)+1, s.limits.MaxDepth); err != nil {
			s.err = err
			return s.err
		}
		if err := s.checkTokenLimit(); err != nil {
			s.err = err
			return s.err
		}
		key, path, topLevel := s.valueContext()
		if err := s.emitEvent(EventArrayStart, s.tokStartOffset, 1, key, path, topLevel); err != nil {
			s.err = err
			return s.err
		}
		s.frames = append(s.frames, scanFrame{object: false, startOffset: s.tokStartOffset})
		s.maxDepth = max(s.maxDepth, len(s.frames))
		s.state = stateExpectArrayValueOrEnd
	case '"':
		s.startString(false, remainingBytes)
	case '-':
		s.numLen = 1
		s.numState = numStateMinus
		s.state = stateInNumber
	case '0':
		s.numLen = 1
		s.numState = numStateZero
		s.state = stateInNumber
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		s.numLen = 1
		s.numState = numStateInt
		s.state = stateInNumber
	case 't':
		s.literalExpected = "true"
		s.literalIdx = 1
		s.literalType = EventTrue
		s.state = stateInLiteral
	case 'f':
		s.literalExpected = "false"
		s.literalIdx = 1
		s.literalType = EventFalse
		s.state = stateInLiteral
	case 'n':
		s.literalExpected = "null"
		s.literalIdx = 1
		s.literalType = EventNull
		s.state = stateInLiteral
	default:
		s.err = &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
		return s.err
	}
	return nil
}

func (s *Scanner) startString(isKey bool, remainingBytes int64) {
	s.tokStartOffset = s.bytes - remainingBytes
	s.tokDepth = len(s.frames)
	if !isKey {
		s.tokDepth = len(s.frames) + 1
	}
	s.strIsKey = isKey
	s.strDecodedBytes = 0
	s.strInEscape = false
	s.escapeLen = 0
	s.keyBuf = s.keyBuf[:0]
	s.state = stateInString
	if !isKey && s.strWriterResolver != nil {
		key, path, topLevel := s.valueContext()
		var pathCopy []string
		if len(path) > 0 {
			pathCopy = slices.Clone(path)
		}
		w, err := s.strWriterResolver(StringContext{
			Key:      key,
			Path:     pathCopy,
			TopLevel: topLevel,
		})
		if err != nil {
			s.err = err
			return
		}
		s.activeStrWriter = w
		if c, ok := w.(io.Closer); ok {
			s.activeStrCloser = c
		} else {
			s.activeStrCloser = nil
		}
	}
}

func (s *Scanner) writeActiveStr(p []byte) error {
	if s.activeStrWriter != nil {
		if _, err := s.activeStrWriter.Write(p); err != nil {
			s.err = err
			return err
		}
	}
	return nil
}

func (s *Scanner) writeActiveRune(r rune) error {
	if s.activeStrWriter != nil {
		var buf [4]byte
		n := utf8.EncodeRune(buf[:], r)
		if _, err := s.activeStrWriter.Write(buf[:n]); err != nil {
			s.err = err
			return err
		}
	}
	return nil
}

// Finish signals end-of-stream and validates that the JSON document is complete.
func (s *Scanner) Finish() (Result, error) {
	if s.activeStrCloser != nil {
		_ = s.activeStrCloser.Close()
		s.activeStrWriter = nil
		s.activeStrCloser = nil
	}
	if err := s.ctx.Err(); err != nil {
		s.err = canceledError(err)
		return Result{}, s.err
	}
	if s.err != nil {
		return Result{}, s.err
	}

	if s.pendingUTF8Len > 0 {
		s.err = &Error{Kind: KindInvalidUTF8, Msg: "invalid UTF-8"}
		return Result{}, s.err
	}

	switch s.state {
	case stateInNumber:
		switch s.numState {
		case numStateMinus, numStateDot, numStateExp, numStateExpSign:
			s.err = &Error{Kind: KindMalformed, Reason: MalformedIncomplete, Msg: "incomplete JSON body"}
			return Result{}, s.err
		}
		if err := s.checkTokenLimit(); err != nil {
			s.err = err
			return Result{}, s.err
		}
		key, path, topLevel := s.valueContext()
		if err := s.emitEvent(EventNumber, s.tokStartOffset, int64(s.numLen), key, path, topLevel); err != nil {
			s.err = err
			return Result{}, s.err
		}
		s.valueFinished()
	case stateInLiteral:
		if s.literalIdx < len(s.literalExpected) {
			s.err = &Error{Kind: KindMalformed, Reason: MalformedIncomplete, Msg: "incomplete JSON body"}
			return Result{}, s.err
		}
		if err := s.checkTokenLimit(); err != nil {
			s.err = err
			return Result{}, s.err
		}
		key, path, topLevel := s.valueContext()
		if err := s.emitEvent(s.literalType, s.tokStartOffset, int64(len(s.literalExpected)), key, path, topLevel); err != nil {
			s.err = err
			return Result{}, s.err
		}
		s.valueFinished()
	}

	if s.state == stateInString {
		s.err = &Error{Kind: KindMalformed, Reason: MalformedIncomplete, Msg: "incomplete JSON body"}
		return Result{}, s.err
	}

	if s.rootValues == 0 {
		s.err = &Error{Kind: KindMalformed, Reason: MalformedEmpty, Msg: "empty JSON body"}
		return Result{}, s.err
	}

	if len(s.frames) > 0 {
		s.err = &Error{Kind: KindMalformed, Reason: MalformedIncomplete, Msg: "incomplete JSON body"}
		return Result{}, s.err
	}

	if s.state != stateRootDone {
		s.err = &Error{Kind: KindMalformed, Reason: MalformedIncomplete, Msg: "incomplete JSON body"}
		return Result{}, s.err
	}

	return s.Result(), nil
}

// End is an alias for Finish.
func (s *Scanner) End() (Result, error) {
	return s.Finish()
}

// Result returns the current validation facts.
func (s *Scanner) Result() Result {
	return Result{
		Bytes:    int(s.bytes),
		Tokens:   s.tokens,
		MaxDepth: s.maxDepth,
	}
}

func parseHex4(b []byte) (rune, bool) {
	if len(b) < 4 {
		return 0, false
	}
	var r rune
	for _, c := range b[:4] {
		var d rune
		switch {
		case '0' <= c && c <= '9':
			d = rune(c - '0')
		case 'a' <= c && c <= 'f':
			d = rune(c - 'a' + 10)
		case 'A' <= c && c <= 'F':
			d = rune(c - 'A' + 10)
		default:
			return 0, false
		}
		r = (r << 4) | d
	}
	return r, true
}

func isDelimiterOrWhitespace(b byte) bool {
	switch b {
	case ' ', '\t', '\r', '\n', ',', ']', '}':
		return true
	default:
		return false
	}
}

func hasLiteralPrefix(data []byte) bool {
	s := string(data)
	for _, lit := range []string{"true", "false", "null"} {
		if strings.HasPrefix(s, lit) {
			if len(s) == len(lit) || isDelimiterOrWhitespace(s[len(lit)]) {
				return true
			}
		}
	}
	return false
}
