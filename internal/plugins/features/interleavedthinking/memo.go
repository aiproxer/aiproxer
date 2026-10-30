package interleavedthinking

import (
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking/state"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Memo wrapper tags are no longer required or prompted for. They are tolerated
// defensively: complete wrapper tags are stripped from the stored memo and from
// the visible stream in case a model still emits them from cached prompts.
const (
	MemoOpenTag  = "<proxy_thinker_memo>"
	MemoCloseTag = "</proxy_thinker_memo>"

	// ExtractionSourceFull marks a memo captured as the full bounded thinker
	// output. Whole-output capture is the only extraction path since memo
	// wrapper tags were removed from the thinker prompt.
	ExtractionSourceFull = "full"
)

// Recorder observes canonical thinker stream events, captures the entire
// thinker output as a bounded memo, and produces a bounded MemoState at stream
// completion.
//
// Observe also sanitizes the visible stream: residual memo wrapper tags are
// stripped (including across delta boundaries) and thinker content is emitted
// as canonical EventReasoningDelta so wrapper tags never surface as ordinary
// assistant text. The stored memo is bounded by MaxMemoBytes; a zero
// MaxMemoBytes disables bounding, matching the memo store convention.
//
// Identity metadata fields are set by the caller before Finish and copied into
// the resulting MemoState. Injection metadata (InjectedCount, VisibleToClient)
// is left zero at capture time.
type Recorder struct {
	MaxMemoBytes int

	// Identity metadata copied into the resulting MemoState.
	SourceSelector        string
	Backend               string
	Model                 string
	RequestID             string
	RegularTurnsRemaining int

	buf        strings.Builder
	truncated  bool
	sawContent bool

	sanInTag   bool
	sanPending strings.Builder
}

// Observe processes one canonical event: it feeds text/reasoning deltas to
// memo capture and returns sanitized visible events. Returned content deltas
// are always EventReasoningDelta with residual memo wrapper tags stripped.
// Non-content events pass through unchanged; call FlushVisibleSanitizer at
// stream end to emit any buffered partial-tag visible content.
func (r *Recorder) Observe(ev aipapi.Event) []aipapi.Event {
	switch ev.Kind {
	case aipapi.EventTextDelta, aipapi.EventReasoningDelta:
		r.ingest(ev.Delta)
		if sanitized := r.sanitizeDelta(ev.Delta); sanitized != "" {
			return []aipapi.Event{{Kind: aipapi.EventReasoningDelta, Delta: sanitized}}
		}
		return nil
	default:
		return []aipapi.Event{ev}
	}
}

// ingest appends the delta to the bounded memo aggregate.
func (r *Recorder) ingest(delta string) {
	if delta == "" {
		return
	}
	r.sawContent = true
	if r.truncated {
		return
	}
	limit := r.MaxMemoBytes
	if limit <= 0 {
		r.buf.WriteString(delta)
		return
	}
	remaining := limit - r.buf.Len()
	if remaining <= 0 {
		r.truncated = true
		return
	}
	if len(delta) <= remaining {
		r.buf.WriteString(delta)
		return
	}
	r.buf.WriteString(delta[:remaining])
	r.truncated = true
}

// HadContent reports whether any non-empty content delta was observed. It lets
// callers distinguish an empty memo (content observed, nothing extractable
// after normalization) from a stream that never produced content at all.
func (r *Recorder) HadContent() bool {
	return r.sawContent
}

// FlushVisibleSanitizer returns any buffered partial-tag visible content and
// should be called once at stream end before Finish when surfacing thinker output.
func (r *Recorder) FlushVisibleSanitizer() []aipapi.Event {
	if flushed := r.flushSanitizer(); flushed != "" {
		return []aipapi.Event{{Kind: aipapi.EventReasoningDelta, Delta: flushed}}
	}
	return nil
}

// Finish returns the captured MemoState. The memo is the complete bounded
// thinker output with residual wrapper tags stripped, trimmed of surrounding
// whitespace. interrupted marks the stored memo as captured from an
// interrupted stream.
func (r *Recorder) Finish(interrupted bool) state.MemoState {
	s := state.MemoState{
		SourceSelector:        r.SourceSelector,
		Backend:               r.Backend,
		Model:                 r.Model,
		RequestID:             r.RequestID,
		CreatedAt:             time.Now(),
		RegularTurnsRemaining: r.RegularTurnsRemaining,
		ExtractionSource:      ExtractionSourceFull,
		StreamInterrupted:     interrupted,
	}
	s.Memo = strings.TrimSpace(StripResidualMemoTags(r.buf.String()))
	return s
}

// StripResidualMemoTags removes complete <proxy_thinker_memo> and
// </proxy_thinker_memo> wrapper tags from text. It is a defensive cleanup for
// models that still emit wrapper tags from cached prompts; the tags are never
// required or prompted for. Matching is case-insensitive, treats a tag name
// followed by a boundary (whitespace, '/' or '>') as a tag, and preserves
// incomplete tag fragments as literal content.
func StripResidualMemoTags(text string) string {
	const (
		openName  = "<proxy_thinker_memo"
		closeName = "</proxy_thinker_memo"
	)
	if text == "" || !strings.Contains(strings.ToLower(text), openName[1:]) {
		return text
	}
	var b strings.Builder
	lowered := strings.ToLower(text)
	for i := 0; i < len(text); {
		if text[i] != '<' {
			b.WriteByte(text[i])
			i++
			continue
		}
		prefix := ""
		switch {
		case strings.HasPrefix(lowered[i:], openName):
			prefix = openName
		case strings.HasPrefix(lowered[i:], closeName):
			prefix = closeName
		}
		if prefix == "" {
			b.WriteByte(text[i])
			i++
			continue
		}
		after := i + len(prefix)
		if after >= len(text) || !isTagNameBoundary(text[after]) {
			// Incomplete or lookalike tag: preserve as literal content.
			b.WriteByte(text[i])
			i++
			continue
		}
		if end := strings.IndexByte(text[after:], '>'); end >= 0 {
			i = after + end + 1
			continue
		}
		// Tag name complete but no closing '>' anywhere: drop the fragment,
		// matching the visible-stream sanitizer's flush behavior.
		return b.String()
	}
	return b.String()
}

// isTagNameBoundary reports whether the byte after a tag name is a legal tag
// boundary. Anything else means the text only looks like a tag name.
func isTagNameBoundary(c byte) bool {
	return c == '>' || c == '/' || c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
