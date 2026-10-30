package http_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	corehttp "github.com/aiproxer/aiproxer/internal/core/http"
)

type flushSpy struct {
	*httptest.ResponseRecorder
	flushes int
}

func (f *flushSpy) Flush() {
	f.flushes++
}

func TestResponseStatusRecorder_implementsFlusherWhenUnderlyingDoes(t *testing.T) {
	t.Parallel()
	base := httptest.NewRecorder()
	spy := &flushSpy{ResponseRecorder: base}
	var w http.ResponseWriter = &corehttp.ResponseStatusRecorder{ResponseWriter: spy}
	fl, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("expected ResponseStatusRecorder to implement http.Flusher")
	}
	fl.Flush()
	if spy.flushes != 1 {
		t.Fatalf("Flush calls = %d want 1", spy.flushes)
	}
}

func TestResponseStatusRecorder_flushNoOpWhenUnderlyingNotFlusher(t *testing.T) {
	t.Parallel()
	base := struct{ http.ResponseWriter }{httptest.NewRecorder()}
	var w http.ResponseWriter = &corehttp.ResponseStatusRecorder{ResponseWriter: base.ResponseWriter}
	fl, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("expected wrapper to implement http.Flusher")
	}
	fl.Flush() // must not panic
}

func TestResponseStatusRecorder_ReadFromForwards(t *testing.T) {
	t.Parallel()
	base := httptest.NewRecorder()
	rr := &corehttp.ResponseStatusRecorder{ResponseWriter: base}
	n, err := rr.ReadFrom(bytes.NewBufferString("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("ReadFrom n = %d", n)
	}
	if base.Body.String() != "hello" {
		t.Fatalf("body = %q", base.Body.String())
	}
	if rr.Status != http.StatusOK {
		t.Fatalf("Status after ReadFrom = %d want %d", rr.Status, http.StatusOK)
	}
}

func TestResponseStatusRecorder_ReadFromFallsBackToCopy(t *testing.T) {
	t.Parallel()
	base := struct{ http.ResponseWriter }{httptest.NewRecorder()}
	rr := &corehttp.ResponseStatusRecorder{ResponseWriter: base.ResponseWriter}
	n, err := rr.ReadFrom(bytes.NewBufferString("x"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("n = %d", n)
	}
	if rr.Status != http.StatusOK {
		t.Fatalf("Status after ReadFrom = %d want %d", rr.Status, http.StatusOK)
	}
}

func TestResponseStatusRecorder_Write_setsImplicitOKWithoutWriteHeader(t *testing.T) {
	t.Parallel()
	base := httptest.NewRecorder()
	rr := &corehttp.ResponseStatusRecorder{ResponseWriter: base}
	if _, err := rr.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if rr.Status != http.StatusOK {
		t.Fatalf("Status = %d want %d", rr.Status, http.StatusOK)
	}
}

var _ io.ReaderFrom = (*corehttp.ResponseStatusRecorder)(nil)
