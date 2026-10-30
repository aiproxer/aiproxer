package largebody

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Token returns the deterministic 16-hex-character fingerprint for this identity
// digest, matching diag.StableCallTokenFromSum.
func (d IdentityDigest) Token() string {
	return hex.EncodeToString(d.sum[:8])
}

// CallID returns explicitID when non-empty, otherwise "call_" followed by
// the 16-hex-character token, matching diag.StableCallIDFromSum.
func (d IdentityDigest) CallID(explicitID string) string {
	if id := strings.TrimSpace(explicitID); id != "" {
		return id
	}
	return "call_" + d.Token()
}

const stableTimestampBase = 1715620000

// Unix returns the deterministic Unix timestamp derived from the call sum,
// matching diag.StableUnixFromSum byte-for-byte and second-for-second.
func (d IdentityDigest) Unix() int64 {
	offset := int64(binary.BigEndian.Uint32(d.sum[:4]) % 86_400)
	return stableTimestampBase + offset
}

// StreamingEscapeWriter writes JSON-escaped string characters into an underlying
// io.Writer without retaining large strings in memory (Requirements 4, 16; design section 6).
// Output is byte-for-byte identical to the interior of Go's json.Marshal for strings,
// including HTML safety (<, >, &) and Unicode line separators (\u2028, \u2029).
type StreamingEscapeWriter struct {
	w       io.Writer
	tail    [4]byte
	tailLen int
	buf     [512]byte
	bufLen  int
	closed  bool
}

// NewStreamingEscapeWriter returns an initialized StreamingEscapeWriter.
func NewStreamingEscapeWriter(w io.Writer) *StreamingEscapeWriter {
	return &StreamingEscapeWriter{w: w}
}

func (s *StreamingEscapeWriter) flushBuf() error {
	if s.bufLen > 0 {
		if _, err := s.w.Write(s.buf[:s.bufLen]); err != nil {
			return err
		}
		s.bufLen = 0
	}
	return nil
}

func (s *StreamingEscapeWriter) writeDirect(p []byte) error {
	for len(p) > 0 {
		avail := len(s.buf) - s.bufLen
		if avail == 0 {
			if err := s.flushBuf(); err != nil {
				return err
			}
		}
		n := copy(s.buf[s.bufLen:], p)
		s.bufLen += n
		p = p[n:]
	}
	return nil
}

func (s *StreamingEscapeWriter) writeString(str string) error {
	return s.writeDirect([]byte(str))
}

func (s *StreamingEscapeWriter) writeByte(b byte) error {
	if s.bufLen >= len(s.buf) {
		if err := s.flushBuf(); err != nil {
			return err
		}
	}
	s.buf[s.bufLen] = b
	s.bufLen++
	return nil
}

const hexDigits = "0123456789abcdef"

// invalidUTF8Escape is how this toolchain's json.Marshal encodes one invalid
// UTF-8 byte inside a string: the \ufffd escape through Go 1.26, the raw U+FFFD
// rune from Go 1.27. StableCallSum hashes json.Marshal output, so the streaming
// writer must follow the same toolchain to keep the two digests identical.
var invalidUTF8Escape = func() string {
	encoded, err := json.Marshal("\x80")
	if err != nil || len(encoded) < 2 {
		return `\ufffd`
	}
	return string(encoded[1 : len(encoded)-1])
}()

// Write processes incoming unescaped bytes and writes their JSON-escaped representation.
func (s *StreamingEscapeWriter) Write(p []byte) (int, error) {
	if s.closed {
		return 0, errors.New("largebody: write to closed StreamingEscapeWriter")
	}
	if len(p) > math.MaxInt-s.tailLen {
		return 0, errors.New("largebody: escaped write buffer length overflows int")
	}
	origLen := len(p)

	// Combine with any trailing incomplete UTF-8 bytes from previous call
	if s.tailLen > 0 {
		combined := make([]byte, s.tailLen+len(p))
		copy(combined, s.tail[:s.tailLen])
		copy(combined[s.tailLen:], p)
		s.tailLen = 0
		p = combined
	}

	i := 0
	n := len(p)
	for i < n {
		b := p[i]
		if b < 0x80 {
			// Fast ASCII path
			switch b {
			case '"':
				if err := s.writeString(`\"`); err != nil {
					return 0, err
				}
			case '\\':
				if err := s.writeString(`\\`); err != nil {
					return 0, err
				}
			case '<':
				if err := s.writeString(`\u003c`); err != nil {
					return 0, err
				}
			case '>':
				if err := s.writeString(`\u003e`); err != nil {
					return 0, err
				}
			case '&':
				if err := s.writeString(`\u0026`); err != nil {
					return 0, err
				}
			case '\n':
				if err := s.writeString(`\n`); err != nil {
					return 0, err
				}
			case '\r':
				if err := s.writeString(`\r`); err != nil {
					return 0, err
				}
			case '\t':
				if err := s.writeString(`\t`); err != nil {
					return 0, err
				}
			case '\b':
				if err := s.writeString(`\b`); err != nil {
					return 0, err
				}
			case '\f':
				if err := s.writeString(`\f`); err != nil {
					return 0, err
				}
			default:
				if b < 0x20 {
					esc := []byte{
						'\\', 'u', '0', '0',
						hexDigits[b>>4],
						hexDigits[b&0x0F],
					}
					if err := s.writeDirect(esc); err != nil {
						return 0, err
					}
				} else {
					if err := s.writeByte(b); err != nil {
						return 0, err
					}
				}
			}
			i++
		} else {
			// Multi-byte UTF-8 or invalid byte
			remaining := p[i:]
			if !utf8.FullRune(remaining) {
				// Could be an incomplete rune at the end of the slice
				if len(remaining) < utf8.UTFMax && utf8.RuneStart(b) {
					s.tailLen = copy(s.tail[:], remaining)
					break
				}
			}
			r, size := utf8.DecodeRune(remaining)
			if r == utf8.RuneError && size == 1 {
				if err := s.writeString(invalidUTF8Escape); err != nil {
					return 0, err
				}
			} else if r == '\u2028' {
				if err := s.writeString(`\u2028`); err != nil {
					return 0, err
				}
			} else if r == '\u2029' {
				if err := s.writeString(`\u2029`); err != nil {
					return 0, err
				}
			} else {
				if err := s.writeDirect(remaining[:size]); err != nil {
					return 0, err
				}
			}
			i += size
		}
	}

	return origLen, nil
}

// WriteString processes incoming unescaped string and writes its JSON-escaped representation
// without allocating heap byte slices.
func (s *StreamingEscapeWriter) WriteString(str string) (int, error) {
	if s.closed {
		return 0, errors.New("largebody: write to closed StreamingEscapeWriter")
	}
	if len(str) > math.MaxInt-s.tailLen {
		return 0, errors.New("largebody: escaped write buffer length overflows int")
	}
	origLen := len(str)

	if s.tailLen > 0 {
		combined := make([]byte, s.tailLen+len(str))
		copy(combined, s.tail[:s.tailLen])
		copy(combined[s.tailLen:], str)
		s.tailLen = 0
		return s.Write(combined)
	}

	i := 0
	n := len(str)
	for i < n {
		b := str[i]
		if b < 0x80 {
			switch b {
			case '"':
				if err := s.writeString(`\"`); err != nil {
					return 0, err
				}
			case '\\':
				if err := s.writeString(`\\`); err != nil {
					return 0, err
				}
			case '<':
				if err := s.writeString(`\u003c`); err != nil {
					return 0, err
				}
			case '>':
				if err := s.writeString(`\u003e`); err != nil {
					return 0, err
				}
			case '&':
				if err := s.writeString(`\u0026`); err != nil {
					return 0, err
				}
			case '\n':
				if err := s.writeString(`\n`); err != nil {
					return 0, err
				}
			case '\r':
				if err := s.writeString(`\r`); err != nil {
					return 0, err
				}
			case '\t':
				if err := s.writeString(`\t`); err != nil {
					return 0, err
				}
			case '\b':
				if err := s.writeString(`\b`); err != nil {
					return 0, err
				}
			case '\f':
				if err := s.writeString(`\f`); err != nil {
					return 0, err
				}
			default:
				if b < 0x20 {
					esc := []byte{
						'\\', 'u', '0', '0',
						hexDigits[b>>4],
						hexDigits[b&0x0F],
					}
					if err := s.writeDirect(esc); err != nil {
						return 0, err
					}
				} else {
					if err := s.writeByte(b); err != nil {
						return 0, err
					}
				}
			}
			i++
		} else {
			remaining := str[i:]
			if !utf8.FullRuneInString(remaining) {
				if len(remaining) < utf8.UTFMax && utf8.RuneStart(b) {
					s.tailLen = copy(s.tail[:], remaining)
					break
				}
			}
			r, size := utf8.DecodeRuneInString(remaining)
			if r == utf8.RuneError && size == 1 {
				if err := s.writeString(invalidUTF8Escape); err != nil {
					return 0, err
				}
			} else if r == '\u2028' {
				if err := s.writeString(`\u2028`); err != nil {
					return 0, err
				}
			} else if r == '\u2029' {
				if err := s.writeString(`\u2029`); err != nil {
					return 0, err
				}
			} else {
				var runeBuf [4]byte
				rn := utf8.EncodeRune(runeBuf[:], r)
				if err := s.writeDirect(runeBuf[:rn]); err != nil {
					return 0, err
				}
			}
			i += size
		}
	}

	return origLen, nil
}

// Close flushes buffered content and handles any incomplete trailing UTF-8 sequence.
func (s *StreamingEscapeWriter) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	// Any remaining bytes in tail at EOF are incomplete/invalid UTF-8
	for j := 0; j < s.tailLen; j++ {
		if err := s.writeString(invalidUTF8Escape); err != nil {
			return err
		}
	}
	s.tailLen = 0
	return s.flushBuf()
}

// CallIdentityConfig specifies metadata for Call identity derivation
// (Requirements 4, 16, 17; design section 6).
//
// Protocol neutrality: the writer serializes exactly the canonical fields it is
// given. Protocol-specific semantic extensions — including any protocol's model
// extension — are supplied by the owning protocol edge through Extensions or
// SetClientModel. Core never injects a protocol-specific extension name, so a
// new protocol reuses this mechanism without depending on another protocol's
// naming convention.
type CallIdentityConfig struct {
	ExplicitID         string
	Session            aipapi.SessionRef
	SessionInput       SessionInput
	Route              aipapi.RouteIntent
	RouteSelector      string
	Instructions       []aipapi.Message
	PreviousResponseID string
	PromptCacheKey     string
	SemanticExtensions []aipapi.SemanticExtension
	Tools              []aipapi.ToolDef
	ToolChoice         aipapi.ToolChoice
	Options            aipapi.GenerationOptions
	Extensions         map[string]json.RawMessage
}

// CanonicalCallIdentity computes the exact canonical semantic identity digest
// for call, producing output byte-for-byte identical to diag.StableCallSum
// (Requirements 16, 17; design section 6).
func CanonicalCallIdentity(call *aipapi.Call) IdentityDigest {
	var zero [32]byte
	if call == nil {
		return IdentityDigest{sum: zero}
	}
	cp := *call
	cp.ID = ""
	b, err := json.Marshal(cp)
	if err != nil {
		return IdentityDigest{sum: zero}
	}
	return IdentityDigest{sum: sha256.Sum256(b)}
}

// CallIdentityWriter constructs the exact canonical semantic identity digest
// for a Call without retaining large string payloads (Requirements 4, 16, 17;
// design section 6).
type CallIdentityWriter struct {
	hasher          hash.Hash
	cfg             CallIdentityConfig
	prefixDone      bool
	messagesStarted bool
	messagesClosed  bool
	msgCount        int
	itemsStarted    bool
	itemsClosed     bool
	itemCount       int
	currMsg         *MessageIdentityWriter
	currItem        *ItemIdentityWriter
	closed          bool
	// wholeMessageMarshal selects the whole-message json.Marshal path instead of
	// the streaming fixed-buffer path. Production constructors leave it false;
	// tests force it per instance via ForceWholeMessageMarshalForTest.
	wholeMessageMarshal bool
}

// NewCallIdentityWriter returns a new CallIdentityWriter configured with cfg.
func NewCallIdentityWriter(cfg CallIdentityConfig) (*CallIdentityWriter, error) {
	// Normalize convenience fields
	if cfg.Session.ClientSessionID == "" && cfg.Session.AuthoritativeSessionID == "" && cfg.Session.ALegID == "" && cfg.Session.ResumeToken == "" &&
		(cfg.SessionInput.AuthoritativeSessionID != "" || cfg.SessionInput.ClientSessionID != "" || cfg.SessionInput.ALegID != "" || !cfg.SessionInput.ResumeToken.IsZero()) {
		cfg.Session = aipapi.SessionRef{
			AuthoritativeSessionID: cfg.SessionInput.AuthoritativeSessionID,
			ClientSessionID:        cfg.SessionInput.ClientSessionID,
			ALegID:                 cfg.SessionInput.ALegID,
			ResumeToken:            cfg.SessionInput.ResumeToken.Reveal(),
		}
	}
	if cfg.Route.Selector == "" && cfg.RouteSelector != "" {
		cfg.Route.Selector = cfg.RouteSelector
	}
	return &CallIdentityWriter{
		hasher: sha256.New(),
		cfg:    cfg,
	}, nil
}

// SetClientModel sets or overrides the client model under the semantic extension
// keys named by the calling protocol edge. At least one key is required: core has
// no default extension name, so the wire convention stays owned by the adapter.
// This supports late-discovered models (e.g. model field appearing after messages in JSON).
func (w *CallIdentityWriter) SetClientModel(model string, extKeys ...string) error {
	if w.closed {
		return errors.New("largebody: CallIdentityWriter already closed")
	}
	if len(extKeys) == 0 {
		return errors.New("largebody: SetClientModel requires an explicit semantic extension key")
	}
	b, err := json.Marshal(model)
	if err != nil {
		return fmt.Errorf("largebody: marshal client model: %w", err)
	}
	if w.cfg.Extensions == nil {
		w.cfg.Extensions = make(map[string]json.RawMessage, len(extKeys))
	}
	for _, k := range extKeys {
		w.cfg.Extensions[k] = b
	}
	return nil
}

// SetRouteSelector sets the route selector if the prefix has not yet been written.
func (w *CallIdentityWriter) SetRouteSelector(sel string) error {
	if w.closed {
		return errors.New("largebody: CallIdentityWriter already closed")
	}
	if w.prefixDone {
		return errors.New("largebody: cannot set route selector after prefix has been written")
	}
	w.cfg.RouteSelector = sel
	w.cfg.Route.Selector = sel
	return nil
}

func (w *CallIdentityWriter) ensurePrefix() error {
	if w.prefixDone {
		return nil
	}
	w.prefixDone = true

	// Field 1: ID is always emitted as "" (canonical stable identity clears ID)
	if _, err := io.WriteString(w.hasher, `{"ID":""`); err != nil {
		return err
	}

	// Field 2: Session
	sessionBytes, err := json.Marshal(w.cfg.Session)
	if err != nil {
		return fmt.Errorf("largebody: marshal session: %w", err)
	}
	if _, err := io.WriteString(w.hasher, `,"Session":`); err != nil {
		return err
	}
	if _, err := w.hasher.Write(sessionBytes); err != nil {
		return err
	}

	// Field 3: Route
	routeBytes, err := json.Marshal(w.cfg.Route)
	if err != nil {
		return fmt.Errorf("largebody: marshal route: %w", err)
	}
	if _, err := io.WriteString(w.hasher, `,"Route":`); err != nil {
		return err
	}
	if _, err := w.hasher.Write(routeBytes); err != nil {
		return err
	}

	// Field 4: Instructions
	instructionsBytes, err := json.Marshal(w.cfg.Instructions)
	if err != nil {
		return fmt.Errorf("largebody: marshal instructions: %w", err)
	}
	if _, err := io.WriteString(w.hasher, `,"Instructions":`); err != nil {
		return err
	}
	if _, err := w.hasher.Write(instructionsBytes); err != nil {
		return err
	}

	return nil
}

// StartMessages explicitly opens the Messages array, ensuring an empty array []
// is emitted if no messages are added (preserving non-nil empty vs nil semantics).
func (w *CallIdentityWriter) StartMessages() error {
	if err := w.ensurePrefix(); err != nil {
		return err
	}
	if !w.messagesStarted {
		w.messagesStarted = true
		if _, err := io.WriteString(w.hasher, `,"Messages":[`); err != nil {
			return err
		}
	}
	return nil
}

func (w *CallIdentityWriter) closeMessages() error {
	if w.currMsg != nil {
		if err := w.currMsg.EndMessage(); err != nil {
			return err
		}
	}
	if !w.messagesStarted {
		w.messagesStarted = true
		w.messagesClosed = true
		if _, err := io.WriteString(w.hasher, `,"Messages":null`); err != nil {
			return err
		}
		return nil
	}
	if !w.messagesClosed {
		w.messagesClosed = true
		if _, err := io.WriteString(w.hasher, `]`); err != nil {
			return err
		}
	}
	return nil
}

// StartItems explicitly opens the Items array, ensuring an empty array []
// is emitted if no items are added (preserving non-nil empty vs nil semantics).
func (w *CallIdentityWriter) StartItems() error {
	if err := w.ensurePrefix(); err != nil {
		return err
	}
	if err := w.closeMessages(); err != nil {
		return err
	}
	if !w.itemsStarted {
		w.itemsStarted = true
		if _, err := io.WriteString(w.hasher, `,"Items":[`); err != nil {
			return err
		}
	}
	return nil
}

func (w *CallIdentityWriter) closeItems() error {
	if w.currItem != nil {
		if err := w.currItem.EndItem(); err != nil {
			return err
		}
	}
	if !w.itemsStarted {
		w.itemsStarted = true
		w.itemsClosed = true
		if _, err := io.WriteString(w.hasher, `,"Items":null`); err != nil {
			return err
		}
		return nil
	}
	if !w.itemsClosed {
		w.itemsClosed = true
		if _, err := io.WriteString(w.hasher, `]`); err != nil {
			return err
		}
	}
	return nil
}

func isPlainStreamingTextPart(part aipapi.Part) bool {
	return part.Kind == aipapi.PartText &&
		part.ImageRef == "" &&
		part.ImageMIME == "" &&
		part.FileRef == "" &&
		part.FileMIME == "" &&
		part.FileName == "" &&
		part.ToolCallID == "" &&
		part.ToolName == "" &&
		len(part.Content) == 0 &&
		part.Reasoning == nil
}

func (w *CallIdentityWriter) addMessageWholeMarshal(msg aipapi.Message) error {
	if w.currMsg != nil {
		if err := w.currMsg.EndMessage(); err != nil {
			return err
		}
	}
	if err := w.ensurePrefix(); err != nil {
		return err
	}
	if !w.messagesStarted {
		w.messagesStarted = true
		if _, err := io.WriteString(w.hasher, `,"Messages":[`); err != nil {
			return err
		}
	}
	if w.msgCount > 0 {
		if _, err := io.WriteString(w.hasher, `,`); err != nil {
			return err
		}
	}
	w.msgCount++
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("largebody: marshal message: %w", err)
	}
	_, err = w.hasher.Write(msgBytes)
	return err
}

// AddMessage appends a fully formed Message to the identity stream.
func (w *CallIdentityWriter) AddMessage(msg aipapi.Message) error {
	if w.wholeMessageMarshal {
		return w.addMessageWholeMarshal(msg)
	}
	if msg.Parts == nil {
		if w.currMsg != nil {
			if err := w.currMsg.EndMessage(); err != nil {
				return err
			}
		}
		if err := w.ensurePrefix(); err != nil {
			return err
		}
		if !w.messagesStarted {
			w.messagesStarted = true
			if _, err := io.WriteString(w.hasher, `,"Messages":[`); err != nil {
				return err
			}
		}
		if w.msgCount > 0 {
			if _, err := io.WriteString(w.hasher, `,`); err != nil {
				return err
			}
		}
		w.msgCount++
		roleBytes, err := json.Marshal(msg.Role)
		if err != nil {
			return fmt.Errorf("largebody: marshal role: %w", err)
		}
		if _, err := io.WriteString(w.hasher, `{"Role":`); err != nil {
			return err
		}
		if _, err := w.hasher.Write(roleBytes); err != nil {
			return err
		}
		if _, err := io.WriteString(w.hasher, `,"Parts":null}`); err != nil {
			return err
		}
		return nil
	}

	mw, err := w.BeginMessage(msg.Role)
	if err != nil {
		return err
	}
	for _, part := range msg.Parts {
		if isPlainStreamingTextPart(part) {
			pw, err := mw.BeginTextPart()
			if err != nil {
				return err
			}
			const chunkSize = 32 * 1024
			for offset := 0; offset < len(part.Text); offset += chunkSize {
				end := min(offset+chunkSize, len(part.Text))
				chunk := part.Text[offset:end]
				if sw, ok := pw.(io.StringWriter); ok {
					if _, err := sw.WriteString(chunk); err != nil {
						_ = pw.Close()
						return err
					}
				} else {
					if _, err := io.WriteString(pw, chunk); err != nil {
						_ = pw.Close()
						return err
					}
				}
			}
			if err := pw.Close(); err != nil {
				return err
			}
		} else {
			if err := mw.AddPart(part); err != nil {
				return err
			}
		}
	}
	return mw.EndMessage()
}

// BeginMessage opens a new message for streaming parts.
func (w *CallIdentityWriter) BeginMessage(role aipapi.Role) (*MessageIdentityWriter, error) {
	if w.currMsg != nil {
		if err := w.currMsg.EndMessage(); err != nil {
			return nil, err
		}
	}
	if err := w.ensurePrefix(); err != nil {
		return nil, err
	}
	if !w.messagesStarted {
		w.messagesStarted = true
		if _, err := io.WriteString(w.hasher, `,"Messages":[`); err != nil {
			return nil, err
		}
	}
	if w.msgCount > 0 {
		if _, err := io.WriteString(w.hasher, `,`); err != nil {
			return nil, err
		}
	}
	w.msgCount++

	roleBytes, err := json.Marshal(role)
	if err != nil {
		return nil, fmt.Errorf("largebody: marshal role: %w", err)
	}
	if _, err := io.WriteString(w.hasher, `{"Role":`); err != nil {
		return nil, err
	}
	if _, err := w.hasher.Write(roleBytes); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(w.hasher, `,"Parts":[`); err != nil {
		return nil, err
	}

	w.currMsg = &MessageIdentityWriter{parent: w}
	return w.currMsg, nil
}

func isPlainMessageItem(item aipapi.Item) bool {
	return item.Kind == aipapi.ItemKindMessage &&
		item.Reference == nil &&
		item.ToolCall == nil &&
		item.ToolResult == nil &&
		item.Reasoning == nil &&
		item.Compaction == nil &&
		item.Extension == nil
}

func isPlainContentPart(cp aipapi.ContentPart) bool {
	return cp.ImageRef == "" &&
		cp.ImageMIME == "" &&
		cp.FileRef == "" &&
		cp.FileData == "" &&
		cp.FileMIME == "" &&
		cp.FileName == "" &&
		cp.VideoRef == "" &&
		cp.VideoMIME == "" &&
		cp.Refusal == "" &&
		cp.Reasoning == nil &&
		cp.Summary == "" &&
		cp.Annotation == nil &&
		cp.AssistantRef == "" &&
		cp.Extension == nil
}

func (w *CallIdentityWriter) addItemWholeMarshal(item aipapi.Item) error {
	if w.currItem != nil {
		if err := w.currItem.EndItem(); err != nil {
			return err
		}
	}
	if err := w.ensurePrefix(); err != nil {
		return err
	}
	// Items come after Messages; close Messages first
	if err := w.closeMessages(); err != nil {
		return err
	}
	if !w.itemsStarted {
		w.itemsStarted = true
		if _, err := io.WriteString(w.hasher, `,"Items":[`); err != nil {
			return err
		}
	}
	if w.itemCount > 0 {
		if _, err := io.WriteString(w.hasher, `,`); err != nil {
			return err
		}
	}
	w.itemCount++
	itemBytes, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("largebody: marshal item: %w", err)
	}
	_, err = w.hasher.Write(itemBytes)
	return err
}

// AddItem appends a fully formed Item to the identity stream.
func (w *CallIdentityWriter) AddItem(item aipapi.Item) error {
	if w.wholeMessageMarshal {
		return w.addItemWholeMarshal(item)
	}
	if isPlainMessageItem(item) {
		iw, err := w.BeginMessageItem(item.ID, item.Status, item.Role, item.Phase)
		if err != nil {
			return err
		}
		for _, cp := range item.Content {
			if cp.Kind == aipapi.ContentPartText && isPlainContentPart(cp) {
				pw, err := iw.BeginTextContentPart()
				if err != nil {
					return err
				}
				const chunkSize = 32 * 1024
				for offset := 0; offset < len(cp.Text); offset += chunkSize {
					end := min(offset+chunkSize, len(cp.Text))
					chunk := cp.Text[offset:end]
					if sw, ok := pw.(io.StringWriter); ok {
						if _, err := sw.WriteString(chunk); err != nil {
							_ = pw.Close()
							return err
						}
					} else {
						if _, err := io.WriteString(pw, chunk); err != nil {
							_ = pw.Close()
							return err
						}
					}
				}
				if err := pw.Close(); err != nil {
					return err
				}
			} else {
				if err := iw.AddContentPart(cp); err != nil {
					return err
				}
			}
		}
		return iw.EndItem()
	}
	return w.addItemWholeMarshal(item)
}

// BeginMessageItem opens a new message Item for streaming content parts.
func (w *CallIdentityWriter) BeginMessageItem(id string, status aipapi.ItemStatus, role aipapi.Role, phase aipapi.AssistantPhase) (*ItemIdentityWriter, error) {
	if w.currItem != nil {
		if err := w.currItem.EndItem(); err != nil {
			return nil, err
		}
	}
	if err := w.ensurePrefix(); err != nil {
		return nil, err
	}
	// Items come after Messages; close Messages first
	if err := w.closeMessages(); err != nil {
		return nil, err
	}
	if !w.itemsStarted {
		w.itemsStarted = true
		if _, err := io.WriteString(w.hasher, `,"Items":[`); err != nil {
			return nil, err
		}
	}
	if w.itemCount > 0 {
		if _, err := io.WriteString(w.hasher, `,`); err != nil {
			return nil, err
		}
	}
	w.itemCount++

	template := aipapi.Item{
		Kind:   aipapi.ItemKindMessage,
		ID:     id,
		Status: status,
		Role:   role,
		Phase:  phase,
	}
	tplBytes, err := json.Marshal(template)
	if err != nil {
		return nil, fmt.Errorf("largebody: marshal item template: %w", err)
	}
	// Strip trailing '}' to append ,"content":[
	if len(tplBytes) == 0 || tplBytes[len(tplBytes)-1] != '}' {
		return nil, fmt.Errorf("largebody: invalid item template json")
	}
	if _, err := w.hasher.Write(tplBytes[:len(tplBytes)-1]); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(w.hasher, `,"content":[`); err != nil {
		return nil, err
	}

	w.currItem = &ItemIdentityWriter{parent: w, openContent: true}
	return w.currItem, nil
}

// WriteCall streams all fields of call through the writer and returns its IdentityDigest.
func (w *CallIdentityWriter) WriteCall(call *aipapi.Call) (IdentityDigest, error) {
	if call == nil {
		return IdentityDigest{}, nil
	}
	w.cfg.ExplicitID = call.ID
	w.cfg.Session = call.Session
	w.cfg.Route = call.Route
	w.cfg.Instructions = call.Instructions
	w.cfg.PreviousResponseID = call.PreviousResponseID
	w.cfg.PromptCacheKey = call.PromptCacheKey
	w.cfg.SemanticExtensions = call.SemanticExtensions
	w.cfg.Tools = call.Tools
	w.cfg.ToolChoice = call.ToolChoice
	w.cfg.Options = call.Options
	w.cfg.Extensions = call.Extensions

	if call.Messages != nil {
		if err := w.StartMessages(); err != nil {
			return IdentityDigest{}, err
		}
		for _, msg := range call.Messages {
			if err := w.AddMessage(msg); err != nil {
				return IdentityDigest{}, err
			}
		}
	}
	if call.Items != nil {
		if err := w.StartItems(); err != nil {
			return IdentityDigest{}, err
		}
		for _, item := range call.Items {
			if err := w.AddItem(item); err != nil {
				return IdentityDigest{}, err
			}
		}
	}
	return w.Digest()
}

// Digest finalizes the stream and returns the exact canonical IdentityDigest.
func (w *CallIdentityWriter) Digest() (IdentityDigest, error) {
	if w.closed {
		return IdentityDigest{}, errors.New("largebody: CallIdentityWriter already closed")
	}
	w.closed = true

	if err := w.ensurePrefix(); err != nil {
		return IdentityDigest{}, err
	}
	if err := w.closeMessages(); err != nil {
		return IdentityDigest{}, err
	}
	if err := w.closeItems(); err != nil {
		return IdentityDigest{}, err
	}

	// Suffix Field 7: PreviousResponseID
	prevBytes, err := json.Marshal(w.cfg.PreviousResponseID)
	if err != nil {
		return IdentityDigest{}, fmt.Errorf("largebody: marshal previous response id: %w", err)
	}
	if _, err := io.WriteString(w.hasher, `,"PreviousResponseID":`); err != nil {
		return IdentityDigest{}, err
	}
	if _, err := w.hasher.Write(prevBytes); err != nil {
		return IdentityDigest{}, err
	}

	// Suffix Field 8: PromptCacheKey
	cacheBytes, err := json.Marshal(w.cfg.PromptCacheKey)
	if err != nil {
		return IdentityDigest{}, fmt.Errorf("largebody: marshal prompt cache key: %w", err)
	}
	if _, err := io.WriteString(w.hasher, `,"PromptCacheKey":`); err != nil {
		return IdentityDigest{}, err
	}
	if _, err := w.hasher.Write(cacheBytes); err != nil {
		return IdentityDigest{}, err
	}

	// Suffix Field 9: SemanticExtensions
	semBytes, err := json.Marshal(w.cfg.SemanticExtensions)
	if err != nil {
		return IdentityDigest{}, fmt.Errorf("largebody: marshal semantic extensions: %w", err)
	}
	if _, err := io.WriteString(w.hasher, `,"SemanticExtensions":`); err != nil {
		return IdentityDigest{}, err
	}
	if _, err := w.hasher.Write(semBytes); err != nil {
		return IdentityDigest{}, err
	}

	// Suffix Field 10: Tools
	toolsBytes, err := json.Marshal(w.cfg.Tools)
	if err != nil {
		return IdentityDigest{}, fmt.Errorf("largebody: marshal tools: %w", err)
	}
	if _, err := io.WriteString(w.hasher, `,"Tools":`); err != nil {
		return IdentityDigest{}, err
	}
	if _, err := w.hasher.Write(toolsBytes); err != nil {
		return IdentityDigest{}, err
	}

	// Suffix Field 11: ToolChoice
	tcBytes, err := json.Marshal(w.cfg.ToolChoice)
	if err != nil {
		return IdentityDigest{}, fmt.Errorf("largebody: marshal tool choice: %w", err)
	}
	if _, err := io.WriteString(w.hasher, `,"ToolChoice":`); err != nil {
		return IdentityDigest{}, err
	}
	if _, err := w.hasher.Write(tcBytes); err != nil {
		return IdentityDigest{}, err
	}

	// Suffix Field 12: Options
	optBytes, err := json.Marshal(w.cfg.Options)
	if err != nil {
		return IdentityDigest{}, fmt.Errorf("largebody: marshal options: %w", err)
	}
	if _, err := io.WriteString(w.hasher, `,"Options":`); err != nil {
		return IdentityDigest{}, err
	}
	if _, err := w.hasher.Write(optBytes); err != nil {
		return IdentityDigest{}, err
	}

	// Suffix Field 13: Extensions
	extBytes, err := json.Marshal(w.cfg.Extensions)
	if err != nil {
		return IdentityDigest{}, fmt.Errorf("largebody: marshal extensions: %w", err)
	}
	if _, err := io.WriteString(w.hasher, `,"Extensions":`); err != nil {
		return IdentityDigest{}, err
	}
	if _, err := w.hasher.Write(extBytes); err != nil {
		return IdentityDigest{}, err
	}

	// Close root Call object
	if _, err := io.WriteString(w.hasher, `}`); err != nil {
		return IdentityDigest{}, err
	}

	var sum [32]byte
	w.hasher.Sum(sum[:0])
	return IdentityDigest{sum: sum}, nil
}

// MessageIdentityWriter writes message parts to the identity stream.
type MessageIdentityWriter struct {
	parent    *CallIdentityWriter
	partCount int
	currText  *textPartCloser
	ended     bool
}

// AddPart appends a completed Part to the message.
func (m *MessageIdentityWriter) AddPart(part aipapi.Part) error {
	if m.currText != nil {
		if err := m.currText.Close(); err != nil {
			return err
		}
	}
	if m.partCount > 0 {
		if _, err := io.WriteString(m.parent.hasher, `,`); err != nil {
			return err
		}
	}
	m.partCount++
	partBytes, err := json.Marshal(part)
	if err != nil {
		return fmt.Errorf("largebody: marshal part: %w", err)
	}
	_, err = m.parent.hasher.Write(partBytes)
	return err
}

// BeginTextPart opens a streaming writer for a Text part.
func (m *MessageIdentityWriter) BeginTextPart() (io.WriteCloser, error) {
	if m.currText != nil {
		if err := m.currText.Close(); err != nil {
			return nil, err
		}
	}
	if m.partCount > 0 {
		if _, err := io.WriteString(m.parent.hasher, `,`); err != nil {
			return nil, err
		}
	}
	m.partCount++

	// Prefix for aipapi.Part with Kind=PartText
	if _, err := io.WriteString(m.parent.hasher, `{"Kind":"text","Text":"`); err != nil {
		return nil, err
	}

	sw := NewStreamingEscapeWriter(m.parent.hasher)
	m.currText = &textPartCloser{
		sw:           sw,
		suffix:       `","ImageRef":"","ImageMIME":"","FileRef":"","FileMIME":"","FileName":"","ToolCallID":"","ToolName":"","Content":null,"Reasoning":null}`,
		parentHasher: m.parent.hasher,
	}
	return m.currText, nil
}

// EndMessage closes the Parts array and message object.
func (m *MessageIdentityWriter) EndMessage() error {
	if m.ended {
		return nil
	}
	m.ended = true
	if m.currText != nil {
		if err := m.currText.Close(); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(m.parent.hasher, `]}`); err != nil {
		return err
	}
	m.parent.currMsg = nil
	return nil
}

type textPartCloser struct {
	sw           *StreamingEscapeWriter
	suffix       string
	parentHasher io.Writer
	closed       bool
}

func (c *textPartCloser) Write(p []byte) (int, error) {
	return c.sw.Write(p)
}

func (c *textPartCloser) WriteString(s string) (int, error) {
	return c.sw.WriteString(s)
}

func (c *textPartCloser) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	if err := c.sw.Close(); err != nil {
		return err
	}
	_, err := io.WriteString(c.parentHasher, c.suffix)
	return err
}

type itemTextPartCloser struct {
	sw           *StreamingEscapeWriter
	parentHasher io.Writer
	started      bool
	closed       bool
}

func (c *itemTextPartCloser) Write(p []byte) (int, error) {
	if c.closed {
		return 0, errors.New("largebody: write to closed item text part")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if !c.started {
		if _, err := io.WriteString(c.parentHasher, `{"kind":"text","text":"`); err != nil {
			return 0, err
		}
		c.started = true
	}
	return c.sw.Write(p)
}

func (c *itemTextPartCloser) WriteString(s string) (int, error) {
	if c.closed {
		return 0, errors.New("largebody: write to closed item text part")
	}
	if len(s) == 0 {
		return 0, nil
	}
	if !c.started {
		if _, err := io.WriteString(c.parentHasher, `{"kind":"text","text":"`); err != nil {
			return 0, err
		}
		c.started = true
	}
	return c.sw.WriteString(s)
}

func (c *itemTextPartCloser) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	if !c.started {
		_, err := io.WriteString(c.parentHasher, `{"kind":"text"}`)
		return err
	}
	if err := c.sw.Close(); err != nil {
		return err
	}
	_, err := io.WriteString(c.parentHasher, `"}`)
	return err
}

// ItemIdentityWriter writes item content parts to the identity stream.
type ItemIdentityWriter struct {
	parent      *CallIdentityWriter
	openContent bool
	partCount   int
	currText    *itemTextPartCloser
	ended       bool
}

// AddContentPart appends a completed ContentPart to the item.
func (it *ItemIdentityWriter) AddContentPart(part aipapi.ContentPart) error {
	if it.currText != nil {
		if err := it.currText.Close(); err != nil {
			return err
		}
		it.currText = nil
	}
	if it.partCount > 0 {
		if _, err := io.WriteString(it.parent.hasher, `,`); err != nil {
			return err
		}
	}
	it.partCount++
	partBytes, err := json.Marshal(part)
	if err != nil {
		return fmt.Errorf("largebody: marshal content part: %w", err)
	}
	_, err = it.parent.hasher.Write(partBytes)
	return err
}

// BeginTextContentPart opens a streaming writer for an Item ContentPart of kind text.
func (it *ItemIdentityWriter) BeginTextContentPart() (io.WriteCloser, error) {
	if it.currText != nil {
		if err := it.currText.Close(); err != nil {
			return nil, err
		}
		it.currText = nil
	}
	if it.partCount > 0 {
		if _, err := io.WriteString(it.parent.hasher, `,`); err != nil {
			return nil, err
		}
	}
	it.partCount++

	it.currText = &itemTextPartCloser{
		sw:           NewStreamingEscapeWriter(it.parent.hasher),
		parentHasher: it.parent.hasher,
	}
	return it.currText, nil
}

// EndItem closes the content array and item object.
func (it *ItemIdentityWriter) EndItem() error {
	if it.ended {
		return nil
	}
	it.ended = true
	if it.currText != nil {
		if err := it.currText.Close(); err != nil {
			return err
		}
		it.currText = nil
	}
	if it.openContent {
		if _, err := io.WriteString(it.parent.hasher, `]}`); err != nil {
			return err
		}
	}
	it.parent.currItem = nil
	return nil
}
