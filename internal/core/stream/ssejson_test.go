package stream

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFlushSSEDataJSON_roundTrip(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	var fr struct {
		Hello string `json:"hello"`
	}
	fr.Hello = "world"
	if err := FlushSSEDataJSON(rec, rec, fr); err != nil {
		t.Fatal(err)
	}
	s := rec.Body.String()
	if !strings.Contains(s, `data: {"hello":"world"}`) {
		t.Fatalf("body %q", s)
	}
}

func TestFlushSSEEventJSON_roundTrip(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	payload := map[string]string{"k": "v"}
	if err := FlushSSEEventJSON(rec, rec, "evt", payload); err != nil {
		t.Fatal(err)
	}
	s := rec.Body.String()
	if !strings.Contains(s, "event: evt") || !strings.Contains(s, `"k":"v"`) {
		t.Fatalf("body %q", s)
	}
}

func TestFlushSSEDataJSON_decode(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	type row struct {
		N int `json:"n"`
	}
	if err := FlushSSEDataJSON(rec, rec, row{N: 42}); err != nil {
		t.Fatal(err)
	}
	// Encoder appends newline after JSON object
	lines := bytes.Split(rec.Body.Bytes(), []byte("\n"))
	var dataJSON []byte
	for _, ln := range lines {
		rest, ok := bytes.CutPrefix(ln, []byte("data: "))
		if ok {
			dataJSON = rest
			break
		}
	}
	var got row
	if err := json.Unmarshal(dataJSON, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", dataJSON, err)
	}
	if got.N != 42 {
		t.Fatalf("got %+v", got)
	}
}

//nolint:paralleltest,staticcheck // mutates package-level sseRawBufPool; Put uses a non-pointer probe intentionally
func TestAcquireSSERawBuffer_nonBufferPoolEntryAllocatesFresh(t *testing.T) {
	// Mutates package-level sseRawBufPool; do not run parallel with other tests that rely on pool purity.
	sseRawBufPool.Put("not-a-buffer")
	buf := acquireSSERawBuffer()
	if buf == nil {
		t.Fatal("expected non-nil buffer")
	}
	buf.Reset()
	buf.WriteString("ok")
	if buf.String() != "ok" {
		t.Fatalf("buffer: %q", buf.String())
	}
	putSSERawBuffer(buf)
}

//nolint:paralleltest,staticcheck // mutates sseJSONPool
func TestAcquireSSEJSONBuf_nonJSONBufPoolEntryAllocatesFresh(t *testing.T) {
	sseJSONPool.Put("not-json-buf")
	s := acquireSSEJSONBuf()
	if s == nil || s.buf == nil || s.enc == nil {
		t.Fatal("expected non-nil sseJSONBuf")
	}
	s.buf.Reset()
	s.buf.WriteString("ok")
	if s.buf.String() != "ok" {
		t.Fatalf("buffer: %q", s.buf.String())
	}
	putSSEJSONBuf(s)
}

func TestFlushSSEDataJoined_roundTrip(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	prefix := []byte(`{"a":`)
	mid := []byte(`"z"`)
	suffix := []byte(`,"b":2}`)
	if err := FlushSSEDataJoined(rec, rec, prefix, mid, suffix); err != nil {
		t.Fatal(err)
	}
	raw := rec.Body.Bytes()
	i := bytes.Index(raw, []byte("data: "))
	if i < 0 {
		t.Fatalf("missing data: %q", raw)
	}
	line := bytes.TrimPrefix(raw[i:], []byte("data: "))
	line = bytes.TrimSpace(line)
	var v map[string]any
	if err := json.Unmarshal(line, &v); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	if v["a"] != "z" {
		t.Fatalf("a: %v", v["a"])
	}
}
