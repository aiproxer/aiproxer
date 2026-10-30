package anthropic

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Claim (#1511): provider errors survive unchanged, but redirects never replay
// original credentials. Mutants: follow redirects or rewrite provider errors.
func TestHybrid_NativeProviderErrorAndRedirect(t *testing.T) {
	for _, status := range []int{429, 307} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := newHybridHandler(nil, hybridConfig{Enabled: true}, aipsdk.FrontendMountOptions{})
			calls := 0
			h.native.Transport = hybridRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("x-api-key") != "synthetic" || r.Header.Get("Authorization") != "" {
					t.Error("wrong credential")
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://evil.example"}, "Retry-After": []string{"3"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"provider error"}}`))}, nil
			})
			r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-test"}`))
			r.RemoteAddr = "127.0.0.1:1"
			r.Host = "127.0.0.1:8080"
			r.Header.Set("User-Agent", "claude-cli/2.1.310")
			r.Header.Set("x-app", "cli")
			r.Header.Set("x-api-key", "synthetic")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := status
			if status == 307 {
				want = 502
			}
			if calls != 1 || w.Code != want || w.Header().Get("Location") != "" {
				t.Fatalf("upstream=%d status=%d headers=%v", calls, w.Code, w.Header())
			}
			if status == 429 && (w.Body.String() != `{"error":{"message":"provider error"}}` || w.Header().Get("Retry-After") != "3") {
				t.Fatalf("provider error changed: %v %s", w.Header(), w.Body.String())
			}
		})
	}
}

// Claim (#1511): an admitted downstream write budget can deliver a native SSE
// chunk near the five-minute exchange limit. Mutant: use the default 120s budget.
// net.Pipe keeps all HTTP I/O inside synctest's clock, not a multi-minute test.
func TestHybrid_NativeStreamWithinExchangeLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHybridHandler(nil, hybridConfig{Enabled: true}, aipsdk.FrontendMountOptions{})
		h.native.Transport = hybridRoundTrip(func(r *http.Request) (*http.Response, error) {
			reader, writer := io.Pipe()
			go func() {
				defer func() { _ = writer.Close() }()
				_, _ = io.WriteString(writer, "data: first\n\n")
				select {
				case <-time.After(299 * time.Second):
					_, _ = io.WriteString(writer, "data: last\n\n")
				case <-r.Context().Done():
				}
			}()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
		})
		clientConn, serverConn := net.Pipe()
		listener := &hybridPipeListener{connections: make(chan net.Conn, 1)}
		listener.connections <- hybridLoopbackConn{serverConn}
		server := &http.Server{Handler: h, WriteTimeout: ClaudeCodeHybridMinimumWriteTimeout}
		serveErr := make(chan error, 1)
		go func() { serveErr <- server.Serve(listener) }()
		defer func() {
			_ = server.Close()
			if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("serve: %v", err)
			}
		}()
		transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return clientConn, nil }, DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport}
		r, err := http.NewRequest("POST", "http://127.0.0.1/v1/messages", strings.NewReader(`{"model":"claude-test"}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("User-Agent", "claude-cli/2.1.310")
		r.Header.Set("x-app", "cli")
		r.Header.Set("Authorization", "Bearer synthetic")
		start := time.Now()
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != 200 || string(body) != "data: first\n\ndata: last\n\n" || time.Since(start) < 299*time.Second {
			t.Fatalf("late SSE delivery: status=%d body=%q elapsed=%s err=%v", resp.StatusCode, body, time.Since(start), err)
		}
	})
}

type hybridLoopbackConn struct{ net.Conn }

func (hybridLoopbackConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}
}

type hybridPipeListener struct{ connections chan net.Conn }

func (l *hybridPipeListener) Accept() (net.Conn, error) {
	conn, ok := <-l.connections
	if !ok {
		return nil, net.ErrClosed
	}
	return conn, nil
}
func (l *hybridPipeListener) Close() error { close(l.connections); return nil }
func (*hybridPipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

// Claim (#1511): cancellation and generation retirement stop an already-open
// native request. Mutant: construct native request with Background context.
func TestHybrid_NativeCancellationAndRetirement(t *testing.T) {
	for _, retire := range []bool{false, true} {
		t.Run(map[bool]string{false: "client-abort", true: "generation-retirement"}[retire], func(t *testing.T) {
			generation, retireGeneration := context.WithCancel(context.Background())
			defer retireGeneration()
			request, abort := context.WithCancel(context.Background())
			defer abort()
			h := newHybridHandler(nil, hybridConfig{Enabled: true}, aipsdk.FrontendMountOptions{GenerationContext: generation})
			opened := make(chan struct{})
			done := make(chan struct{})
			h.native.Transport = hybridRoundTrip(func(r *http.Request) (*http.Response, error) {
				reader, writer := io.Pipe()
				go func() {
					_, _ = io.WriteString(writer, "data: first\n\n")
					close(opened)
					<-r.Context().Done()
					_ = writer.CloseWithError(r.Context().Err())
				}()
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
			})
			r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-test"}`)).WithContext(request)
			r.RemoteAddr = "127.0.0.1:1"
			r.Host = "127.0.0.1:8080"
			r.Header.Set("User-Agent", "claude-cli/2.1.310")
			r.Header.Set("x-app", "cli")
			r.Header.Set("Authorization", "Bearer synthetic")
			go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), r) }()
			select {
			case <-opened:
			case <-time.After(3 * time.Second):
				t.Fatal("native request never opened")
			}
			if retire {
				retireGeneration()
			} else {
				abort()
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("native request outlived owner")
			}
		})
	}
}
