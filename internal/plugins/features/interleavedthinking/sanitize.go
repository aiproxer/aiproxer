package interleavedthinking

import "strings"

// sanitizeDelta strips memo wrapper tags from a content delta and returns the
// visible content to emit. A prefix that could be the start of either wrapper
// tag is buffered in r.sanPending until it completes a full tag (dropped) or
// diverges (flushed as content), so tags split across deltas are stripped.
func (r *Recorder) sanitizeDelta(delta string) string {
	if delta == "" {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(delta); i++ {
		c := delta[i]
		if !r.sanInTag {
			if c == '<' {
				r.sanPending.WriteByte(c)
				r.sanInTag = true
				continue
			}
			b.WriteByte(c)
			continue
		}
		candidate := r.sanPending.String() + string(c)
		switch {
		case candidate == MemoOpenTag, candidate == MemoCloseTag:
			r.sanPending.Reset()
			r.sanInTag = false
		case strings.HasPrefix(MemoOpenTag, candidate), strings.HasPrefix(MemoCloseTag, candidate):
			r.sanPending.WriteByte(c)
		default:
			b.WriteString(r.sanPending.String())
			b.WriteByte(c)
			r.sanPending.Reset()
			r.sanInTag = false
		}
	}
	return b.String()
}

// flushSanitizer returns any buffered partial-tag content as a single chunk so
// that a trailing incomplete tag is not lost from the visible stream.
func (r *Recorder) flushSanitizer() string {
	if !r.sanInTag || r.sanPending.Len() == 0 {
		r.sanInTag = false
		r.sanPending.Reset()
		return ""
	}
	s := r.sanPending.String()
	r.sanPending.Reset()
	r.sanInTag = false
	return s
}
