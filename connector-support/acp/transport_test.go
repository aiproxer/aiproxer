package acp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewHTTPTransport_nilClient_requiresCallerOwned(t *testing.T) {
	t.Parallel()

	_, err := newHTTPTransport("http://example.com", nil)
	if err == nil {
		t.Fatal("expected error for nil HTTPClient")
	}
	if !strings.Contains(err.Error(), "HTTPClient is required") {
		t.Fatalf("error = %v, want HTTPClient is required", err)
	}
}

func TestNewHTTPTransport_preservesProvidedClient(t *testing.T) {
	t.Parallel()

	custom := &http.Client{Timeout: 7 * time.Second}
	tr, err := newHTTPTransport("http://example.com", custom)
	if err != nil {
		t.Fatal(err)
	}
	if tr.hc != custom {
		t.Fatal("expected provided *http.Client instance to be preserved")
	}
	if tr.hc.Timeout != 7*time.Second {
		t.Fatalf("timeout mutated: got %v want %v", tr.hc.Timeout, 7*time.Second)
	}
}

func TestHTTPTransport_CallUnary_rejectsOversizedBody(t *testing.T) {
	t.Parallel()

	const over = maxUnaryHTTPResponseBytes + 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		src := io.LimitReader(bytes.NewReader(bytes.Repeat([]byte("x"), over)), int64(over))
		_, _ = io.Copy(w, src)
	}))
	t.Cleanup(srv.Close)

	tr, err := newHTTPTransport(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	_, err = tr.CallUnary(context.Background(), []byte(`{}`), http.StatusOK)
	if err == nil {
		t.Fatal("expected error for oversized unary response body")
	}
	if !strings.Contains(err.Error(), "response body exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPTransport_CallUnary_okWithinLimit(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte(`{"jsonrpc":"2.0","result":{}}`), 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	tr, err := newHTTPTransport(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	got, err := tr.CallUnary(context.Background(), []byte(`{}`), http.StatusOK)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("body mismatch: len(got)=%d want %d", len(got), len(payload))
	}
}

func TestHTTPTransport_CallUnary_httpErrorUsesLimitedBody(t *testing.T) {
	t.Parallel()

	body := bytes.Repeat([]byte("e"), 6000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	tr, err := newHTTPTransport(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	_, err = tr.CallUnary(context.Background(), []byte(`{}`), http.StatusOK)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if len(msg) > maxErrorSnippetBytes+300 {
		t.Fatalf("HTTP error diagnostics unexpectedly large: %d", len(msg))
	}
	if !strings.Contains(msg, "HTTP 400") {
		t.Fatalf("expected status in error: %s", msg)
	}
}

func TestHTTPTransport_SendJSONRPC_successOK(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{}}`))
	}))
	t.Cleanup(srv.Close)

	tr, err := newHTTPTransport(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	if err := tr.SendJSONRPC(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)); err != nil {
		t.Fatalf("SendJSONRPC ok response: %v", err)
	}
}

func TestHTTPTransport_SendJSONRPC_successBoundedDiscardDoesNotError(t *testing.T) {
	t.Parallel()

	// Success body larger than the discard cap must not error — the discard is
	// bounded to avoid unbounded allocation, but a capped success is still success.
	over := maxErrorSnippetBytes + 4096
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, io.LimitReader(bytes.NewReader(bytes.Repeat([]byte("x"), over)), int64(over)))
	}))
	t.Cleanup(srv.Close)

	tr, err := newHTTPTransport(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	if err := tr.SendJSONRPC(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)); err != nil {
		t.Fatalf("SendJSONRPC bounded discard should not error: %v", err)
	}
}

func TestHTTPTransport_SendJSONRPC_nonOKReturnsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`busy`))
	}))
	t.Cleanup(srv.Close)

	tr, err := newHTTPTransport(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	err = tr.SendJSONRPC(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	if err == nil {
		t.Fatal("expected error for non-200 SendJSONRPC")
	}
	if !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("expected HTTP 503 in error: %s", err.Error())
	}
}
