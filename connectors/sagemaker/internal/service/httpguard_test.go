package service

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
)

// trackBody serves up to total 'x' bytes while recording Read/Close calls.
type trackBody struct {
	remain int64
	served int64
	reads  int
	closed bool
}

func (b *trackBody) Read(p []byte) (int, error) {
	b.reads++
	if b.remain <= 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), b.remain)
	for i := range n {
		p[i] = 'x'
	}
	b.remain -= n
	b.served += n
	return int(n), nil
}

func (b *trackBody) Close() error { b.closed = true; return nil }

type stubHTTPDoer struct {
	fn func(*http.Request) (*http.Response, error)
}

func (s stubHTTPDoer) Do(r *http.Request) (*http.Response, error) { return s.fn(r) }

func guardTestRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://example.invalid/endpoints/ep/invocations", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return req
}

func TestBoundedHTTPClient_DeclaredHugeContentLength_FailsFast(t *testing.T) {
	t.Parallel()
	for _, declared := range []int64{int64(maxSageMakerResponseBytes) + 1, 1 << 40} {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			tb := &trackBody{remain: 1 << 30}
			guard := &boundedResponseHTTPClient{inner: stubHTTPDoer{fn: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: tb, ContentLength: declared}, nil
			}}}
			_, err := guard.Do(guardTestRequest(t))
			if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
				t.Fatalf("declared ContentLength %d: expected exceeds-limit error, got: %v", declared, err)
			}
			if tb.reads != 0 {
				t.Fatalf("declared ContentLength %d: body was read %d times; must fail before reading", declared, tb.reads)
			}
			if !tb.closed {
				t.Fatalf("declared ContentLength %d: body was not closed", declared)
			}
		})
	}
}

func TestBoundedHTTPClient_LyingLengthOverflow_Errors(t *testing.T) {
	t.Parallel()
	for _, declared := range []int64{10, -1} {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			const actual = 8 << 20
			tb := &trackBody{remain: actual}
			guard := &boundedResponseHTTPClient{inner: stubHTTPDoer{fn: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: tb, ContentLength: declared}, nil
			}}}
			resp, err := guard.Do(guardTestRequest(t))
			if err != nil {
				t.Fatalf("Do failed: %v", err)
			}
			if resp.ContentLength != declared {
				t.Fatalf("ContentLength=%d want declared %d (must not inflate unknown/small lengths)", resp.ContentLength, declared)
			}
			data, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr == nil || !strings.Contains(readErr.Error(), "exceeds limit") {
				t.Fatalf("expected exceeds-limit read error, got data=%d err=%v", len(data), readErr)
			}
			if int64(len(data)) != int64(maxSageMakerResponseBytes)+1 {
				t.Fatalf("delivered %d bytes, want cap %d", len(data), int64(maxSageMakerResponseBytes)+1)
			}
			if tb.served != int64(maxSageMakerResponseBytes)+1 {
				t.Fatalf("underlying body served %d bytes, want exactly the %d-byte cap", tb.served, int64(maxSageMakerResponseBytes)+1)
			}
		})
	}
}

func TestBoundedHTTPClient_ExactLimit_PassesByteIdentical(t *testing.T) {
	t.Parallel()
	for _, declared := range []int64{int64(maxSageMakerResponseBytes), -1} {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			tb := &trackBody{remain: int64(maxSageMakerResponseBytes)}
			guard := &boundedResponseHTTPClient{inner: stubHTTPDoer{fn: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: tb, ContentLength: declared}, nil
			}}}
			resp, err := guard.Do(guardTestRequest(t))
			if err != nil {
				t.Fatalf("Do failed: %v", err)
			}
			if resp.ContentLength != declared {
				t.Fatalf("ContentLength=%d want %d", resp.ContentLength, declared)
			}
			data, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				t.Fatalf("exact-limit body must pass through, got: %v", readErr)
			}
			if len(data) != maxSageMakerResponseBytes {
				t.Fatalf("delivered %d bytes, want %d", len(data), maxSageMakerResponseBytes)
			}
			for i, b := range data {
				if b != 'x' {
					t.Fatalf("byte %d = %q, want 'x' (body must be byte-identical)", i, b)
				}
			}
		})
	}
}

func TestBoundedHTTPClient_SmallBody_PassesWithLengthIntact(t *testing.T) {
	t.Parallel()
	guard := &boundedResponseHTTPClient{inner: stubHTTPDoer{fn: func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("hello")), ContentLength: 5}, nil
	}}}
	resp, err := guard.Do(guardTestRequest(t))
	if err != nil {
		t.Fatalf("Do failed: %v", err)
	}
	if resp.ContentLength != 5 {
		t.Fatalf("ContentLength=%d want 5", resp.ContentLength)
	}
	data, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatalf("ReadAll failed: %v", readErr)
	}
	if string(data) != "hello" {
		t.Fatalf("data=%q want %q", data, "hello")
	}
}

func TestBoundedHTTPClient_InnerError_Propagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("boom")
	guard := &boundedResponseHTTPClient{inner: stubHTTPDoer{fn: func(*http.Request) (*http.Response, error) {
		return nil, sentinel
	}}}
	_, err := guard.Do(guardTestRequest(t))
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected inner error to propagate, got: %v", err)
	}
}

// Guarded default client against a real chunked oversized HTTP body: the
// overflow must surface as an explicit error after at most max+1 bytes.
func TestBoundedHTTPClient_RealHTTPChunkedOverflow_Errors(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		chunk := make([]byte, 64<<10)
		for i := range chunk {
			chunk[i] = 'x'
		}
		for range 96 { // 6 MiB total, chunked (unknown length)
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	guard := &boundedResponseHTTPClient{inner: awshttp.NewBuildableClient()}
	req, err := http.NewRequest(http.MethodPost, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := guard.Do(req)
	if err != nil {
		t.Fatalf("Do failed: %v", err)
	}
	data, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr == nil || !strings.Contains(readErr.Error(), "exceeds limit") {
		t.Fatalf("expected exceeds-limit read error, got data=%d err=%v", len(data), readErr)
	}
	if int64(len(data)) != int64(maxSageMakerResponseBytes)+1 {
		t.Fatalf("delivered %d bytes, want cap %d", len(data), int64(maxSageMakerResponseBytes)+1)
	}
}
