package reqbody_test

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/reqbody"
)

type errCloseBody struct {
	r      io.Reader
	err    error
	closed bool
}

func (e *errCloseBody) Read(p []byte) (int, error) { return e.r.Read(p) }

func (e *errCloseBody) Close() error {
	e.closed = true
	return e.err
}

func TestReadAll_tooLarge(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest("POST", "/", bytes.NewReader(bytes.Repeat([]byte("a"), 200)))
	w := httptest.NewRecorder()
	_, err := reqbody.ReadAll(w, r, 100)
	if err == nil || !reqbody.TooLarge(err) {
		t.Fatalf("expected too-large error, got %v", err)
	}
}

func TestReadAll_ok(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"x":1}`)
	r := httptest.NewRequest("POST", "/", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	b, err := reqbody.ReadAll(w, r, int64(len(payload)+10))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(payload) {
		t.Fatalf("body mismatch")
	}
}

func TestTooLarge_falseOnOtherErrors(t *testing.T) {
	t.Parallel()
	if reqbody.TooLarge(nil) || reqbody.TooLarge(io.EOF) {
		t.Fatal("expected false")
	}
}

func TestTooLarge_maxBytesErrorWrapped(t *testing.T) {
	t.Parallel()
	base := &http.MaxBytesError{Limit: 99}
	wrapped := fmt.Errorf("upstream: %w", base)
	if !reqbody.TooLarge(wrapped) {
		t.Fatal("expected TooLarge for errors.As(*http.MaxBytesError)")
	}
}

func TestReadAll_closeError(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("close failed")
	body := &errCloseBody{r: bytes.NewReader([]byte("ok")), err: closeErr}
	r := httptest.NewRequest("POST", "/", body)
	w := httptest.NewRecorder()
	_, err := reqbody.ReadAll(w, r, 100)
	if err == nil {
		t.Fatal("expected error from Close")
	}
	if !body.closed {
		t.Fatal("expected body Close to run")
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected unwrap to close error, got %v", err)
	}
}

func TestReadAll_readErrorJoinedWithCloseError(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("close failed")
	// Short read: MaxBytesReader returns error; Close may also fail.
	r := httptest.NewRequest("POST", "/", &errCloseBody{
		r:   io.LimitReader(bytes.NewReader(bytes.Repeat([]byte("a"), 500)), 500),
		err: closeErr,
	})
	w := httptest.NewRecorder()
	_, err := reqbody.ReadAll(w, r, 100)
	if err == nil {
		t.Fatal("expected error")
	}
	if !reqbody.TooLarge(err) {
		t.Fatalf("expected too-large read error in chain, got %v", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected close error joined in chain, got %v", err)
	}
}

func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(payload); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestReadAll_decompressesGzipContentEncoding(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"model":"openai-codex:gpt-5.4-mini","messages":[{"role":"user","content":"hi"}]}`)
	gz := gzipBytes(t, payload)
	r := httptest.NewRequest("POST", "/", bytes.NewReader(gz))
	r.Header.Set("Content-Encoding", "gzip")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	b, err := reqbody.ReadAll(w, r, int64(len(payload)+64))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(b, payload) {
		t.Fatalf("expected decompressed JSON payload, got %q", string(b))
	}
}

func TestReadAll_gzipTooLargeAfterDecompression(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("a"), 1000)
	gz := gzipBytes(t, payload)
	r := httptest.NewRequest("POST", "/", bytes.NewReader(gz))
	r.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	_, err := reqbody.ReadAll(w, r, 100)
	if err == nil || !reqbody.TooLarge(err) {
		t.Fatalf("expected too-large error after decompression, got %v", err)
	}
}

func TestReadAll_gzipSourceCloseErrorIsDistinguishable(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("source close failed")
	payload := []byte(`{"x":1}`)
	body := &errCloseBody{r: bytes.NewReader(gzipBytes(t, payload)), err: closeErr}
	r := httptest.NewRequest("POST", "/", body)
	r.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()

	_, err := reqbody.ReadAll(w, r, int64(len(payload)+64))
	if err == nil {
		t.Fatal("expected gzip source close error")
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected close error in chain, got %v", err)
	}
	if !strings.Contains(err.Error(), "reqbody: close gzip source body") {
		t.Fatalf("close error label = %q, want gzip source body", err.Error())
	}
}

func TestReadAll_multiLineContentEncoding(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"model":"openai-codex:gpt-5.4-mini","messages":[{"role":"user","content":"multiline"}]}`)
	gz := gzipBytes(t, payload)
	r := httptest.NewRequest("POST", "/", bytes.NewReader(gz))
	r.Header.Add("Content-Encoding", "identity")
	r.Header.Add("Content-Encoding", "gzip")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	b, err := reqbody.ReadAll(w, r, int64(len(payload)+64))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(b, payload) {
		t.Fatalf("expected decompressed JSON payload, got %q", string(b))
	}
}
