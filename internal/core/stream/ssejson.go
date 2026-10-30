package stream

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
)

// Flusher is the flush-only surface SSE writers need from an HTTP response
// writer; net/http.ResponseWriter satisfies it structurally.
type Flusher interface {
	Flush()
}

const maxPooledSSEBufferCap = 4 << 20 // drop oversized buffers instead of pooling them

// sseJSONBuf pairs a buffer with a long-lived json.Encoder so hot SSE flushes avoid
// per-frame json.NewEncoder allocations (encoder always writes to the same buffer).
type sseJSONBuf struct {
	buf *bytes.Buffer
	enc *json.Encoder
}

var sseJSONPool = sync.Pool{
	New: func() any {
		buf := new(bytes.Buffer)
		enc := json.NewEncoder(buf)
		enc.SetEscapeHTML(false)
		return &sseJSONBuf{buf: buf, enc: enc}
	},
}

func putSSEJSONBuf(s *sseJSONBuf) {
	if s == nil || s.buf == nil {
		return
	}
	s.buf.Reset()
	if s.buf.Cap() > maxPooledSSEBufferCap {
		return
	}
	sseJSONPool.Put(s)
}

func acquireSSEJSONBuf() *sseJSONBuf {
	v := sseJSONPool.Get()
	if s, ok := v.(*sseJSONBuf); ok && s != nil && s.buf != nil && s.enc != nil {
		s.buf.Reset()
		return s
	}
	buf := new(bytes.Buffer)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	return &sseJSONBuf{buf: buf, enc: enc}
}

var sseRawBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

func putSSERawBuffer(buf *bytes.Buffer) {
	if buf == nil {
		return
	}
	buf.Reset()
	if buf.Cap() > maxPooledSSEBufferCap {
		return
	}
	sseRawBufPool.Put(buf)
}

func acquireSSERawBuffer() *bytes.Buffer {
	v := sseRawBufPool.Get()
	if buf, ok := v.(*bytes.Buffer); ok && buf != nil {
		return buf
	}
	return new(bytes.Buffer)
}

// FlushSSEEventJSON writes one SSE record: "event: name\ndata: <json>\n\n" using a pooled buffer.
func FlushSSEEventJSON(w io.Writer, fl Flusher, eventName string, payload any) error {
	s := acquireSSEJSONBuf()
	defer putSSEJSONBuf(s)
	buf := s.buf
	buf.WriteString("event: ")
	buf.WriteString(eventName)
	buf.WriteString("\ndata: ")
	if err := s.enc.Encode(payload); err != nil {
		return err
	}
	buf.WriteByte('\n')
	if _, err := w.Write(buf.Bytes()); err != nil {
		return err
	}
	fl.Flush()
	return nil
}

// FlushSSEDataJSON writes one SSE data line: "data: <json>\n\n" using a pooled buffer.
func FlushSSEDataJSON(w io.Writer, fl Flusher, payload any) error {
	s := acquireSSEJSONBuf()
	defer putSSEJSONBuf(s)
	buf := s.buf
	buf.WriteString("data: ")
	if err := s.enc.Encode(payload); err != nil {
		return err
	}
	buf.WriteByte('\n')
	if _, err := w.Write(buf.Bytes()); err != nil {
		return err
	}
	fl.Flush()
	return nil
}

// FlushSSEDataJoined writes data: prefix+mid+suffix followed by "\n\n" using a pooled buffer.
// prefix+mid+suffix must concatenate to one valid JSON value (no leading "data: " prefix).
func FlushSSEDataJoined(w io.Writer, fl Flusher, prefix, mid, suffix []byte) error {
	buf := acquireSSERawBuffer()
	defer putSSERawBuffer(buf)
	buf.Reset()
	buf.WriteString("data: ")
	buf.Write(prefix)
	buf.Write(mid)
	buf.Write(suffix)
	buf.WriteString("\n\n")
	if _, err := w.Write(buf.Bytes()); err != nil {
		return err
	}
	fl.Flush()
	return nil
}
