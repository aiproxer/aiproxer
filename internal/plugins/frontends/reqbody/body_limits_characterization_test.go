package reqbody_test

// Task 1.3 characterization: freeze request-body limit, framing, gzip, and
// read-error ownership (requirements 1, 2; design 1, 2, 5-shared-replay oracle,
// 16 canonical characterization).
//
// No production fast path exists yet. These tests ratchet the current
// reqbody.ReadAll oracle that a future bounded capture must preserve:
// exact limit/limit+1, framing-independent enforcement, gzip decompressed-limit
// enforcement, and non-limit read errors staying distinct from 413.

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/reqbody"
)

func TestReadAll_exactLimitPassesLimitPlusOneFails(t *testing.T) {
	t.Parallel()
	const limit int64 = 64
	exact := bytes.Repeat([]byte("a"), int(limit))
	over := bytes.Repeat([]byte("a"), int(limit)+1)

	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(exact))
	w := httptest.NewRecorder()
	got, err := reqbody.ReadAll(w, r, limit)
	if err != nil {
		t.Fatalf("exact-limit read: %v", err)
	}
	if len(got) != int(limit) {
		t.Fatalf("exact-limit len=%d want %d", len(got), limit)
	}

	r = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(over))
	w = httptest.NewRecorder()
	if _, err := reqbody.ReadAll(w, r, limit); err == nil || !reqbody.TooLarge(err) {
		t.Fatalf("limit+1 err=%v, want TooLarge", err)
	}
}

func TestReadAll_chunkedAndKnownLengthEnforceSameLimit(t *testing.T) {
	t.Parallel()
	const limit int64 = 32
	payload := bytes.Repeat([]byte("b"), int(limit)+1)

	// Known length framing (httptest sets ContentLength).
	known := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	if known.ContentLength != int64(len(payload)) {
		t.Fatalf("known ContentLength=%d want %d", known.ContentLength, len(payload))
	}
	if _, err := reqbody.ReadAll(httptest.NewRecorder(), known, limit); err == nil || !reqbody.TooLarge(err) {
		t.Fatalf("known-length over-limit err=%v, want TooLarge", err)
	}

	// Chunked framing: unknown length must enforce the same ceiling.
	chunked := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	chunked.ContentLength = -1
	if _, err := reqbody.ReadAll(httptest.NewRecorder(), chunked, limit); err == nil || !reqbody.TooLarge(err) {
		t.Fatalf("chunked over-limit err=%v, want TooLarge", err)
	}

	// Under-limit bodies pass under both framings.
	small := bytes.Repeat([]byte("b"), int(limit)-1)
	for name, framing := range map[string]int64{"known": int64(len(small)), "chunked": -1} {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(small))
		req.ContentLength = framing
		got, err := reqbody.ReadAll(httptest.NewRecorder(), req, limit)
		if err != nil {
			t.Fatalf("%s under-limit: %v", name, err)
		}
		if len(got) != len(small) {
			t.Fatalf("%s len=%d want %d", name, len(got), len(small))
		}
	}
}

func TestReadAll_gzipCompressedLengthNeverMasksDecodedLimit(t *testing.T) {
	t.Parallel()
	// Highly compressible: compressed form is far below the limit while the
	// decoded form exceeds it. The decoded byte domain must govern (req 2.7).
	const limit int64 = 1024
	decoded := bytes.Repeat([]byte("a"), 4096)
	var compressed bytes.Buffer
	gw := gzip.NewWriter(&compressed)
	if _, err := gw.Write(decoded); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if int64(compressed.Len()) >= limit {
		t.Fatalf("fixture invalid: compressed=%d must be < limit=%d to prove the invariant", compressed.Len(), limit)
	}
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(compressed.Bytes()))
	r.Header.Set("Content-Encoding", "gzip")
	if _, err := reqbody.ReadAll(httptest.NewRecorder(), r, limit); err == nil || !reqbody.TooLarge(err) {
		t.Fatalf("gzip over-decoded-limit err=%v, want TooLarge", err)
	}
}

func TestReadAll_gzipInvalidIsNotTooLarge(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("not-gzip-bytes")))
	r.Header.Set("Content-Encoding", "gzip")
	_, err := reqbody.ReadAll(httptest.NewRecorder(), r, 1<<20)
	if err == nil {
		t.Fatal("expected error for corrupt gzip body")
	}
	if reqbody.TooLarge(err) {
		t.Fatalf("corrupt gzip err=%v must not report TooLarge (owns read-failed, not 413)", err)
	}
}

type canceledBody struct {
	err error
}

func (b *canceledBody) Read([]byte) (int, error) { return 0, b.err }

func (b *canceledBody) Close() error { return nil }

func TestReadAll_clientCancelIsNotTooLarge(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodPost, "/", &canceledBody{err: context.Canceled})
	_, err := reqbody.ReadAll(httptest.NewRecorder(), r, 1<<20)
	if err == nil {
		t.Fatal("expected client-cancel read error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled in chain", err)
	}
	if reqbody.TooLarge(err) {
		t.Fatalf("cancel err=%v must not report TooLarge", err)
	}
}

func TestReadAll_bodyReadErrorIsNotTooLarge(t *testing.T) {
	t.Parallel()
	want := errors.New("client disconnect")
	r := httptest.NewRequest(http.MethodPost, "/", &canceledBody{err: want})
	// io.ReadAll propagates the first non-EOF read error.
	_, err := reqbody.ReadAll(httptest.NewRecorder(), r, 1<<20)
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("err=%v, want client disconnect in chain", err)
	}
	if reqbody.TooLarge(err) {
		t.Fatalf("read err=%v must not report TooLarge", err)
	}
	var _ io.Reader = &canceledBody{}
}
