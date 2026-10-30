package stdhttp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// Task 7.4: stdhttp owns only the http.Server. It must reject reload triggers
// before draining HTTP, drain HTTP and the serve worker, close the optional
// management listener, and then invoke the canonical Host.Close exactly once.
// It must never coordinate Manager, ProcessServices, or tracing shutdown.

type serveOrderLog struct {
	mu     sync.Mutex
	events []string
}

func (l *serveOrderLog) add(event string) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *serveOrderLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

func assertServeOrder(t *testing.T, log *serveOrderLog, want ...string) {
	t.Helper()
	if got := log.snapshot(); !slices.Equal(got, want) {
		t.Fatalf("shutdown order=%v want %v", got, want)
	}
}

// serveHostStub is the focused host seam RunWithGenerationHost depends on.
type serveHostStub struct {
	log          *serveOrderLog
	handler      http.Handler
	closeErr     error
	closes       atomic.Int32
	begins       atomic.Int32
	handlerCalls atomic.Int32
}

// stableServeHandler is a comparable handler so tests can assert the exact
// host-provided dispatcher instance reaches the http.Server.
type stableServeHandler struct{}

func (stableServeHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

// altServeHandler is a distinct handler type used to prove a second
// HTTPHandler return cannot be mounted after validation resolved the first.
type altServeHandler struct{ id int }

func (altServeHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

func newServeHostStub(log *serveOrderLog) *serveHostStub {
	return &serveHostStub{log: log, handler: &stableServeHandler{}}
}

func (h *serveHostStub) HTTPHandler() http.Handler {
	h.handlerCalls.Add(1)
	return h.handler
}

func (h *serveHostStub) BeginShutdown() {
	h.begins.Add(1)
	h.log.add("begin-shutdown")
}

func (h *serveHostStub) Close(context.Context) error {
	h.closes.Add(1)
	h.log.add("host-close")
	return h.closeErr
}

type serveManagementStub struct {
	log  *serveOrderLog
	err  error
	call atomic.Int32
}

func (m *serveManagementStub) Shutdown(context.Context) error {
	m.call.Add(1)
	m.log.add("management-shutdown")
	return m.err
}

func serveTestConfig() *config.Config {
	return &config.Config{Server: config.ServerConfig{Address: "127.0.0.1:0"}}
}

func serveTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubServeListener replaces the listener with a barrier the test controls and
// returns a function that blocks until the serve worker is running.
func stubServeListener(t *testing.T, log *serveOrderLog, shutdownErr error) (awaitStarted func()) {
	t.Helper()
	origListen := listenAndServe
	origShutdown := httpServerShutdown
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		stop()
		listenAndServe = origListen
		httpServerShutdown = origShutdown
	})

	listenAndServe = func(*http.Server) error {
		close(started)
		<-release
		return http.ErrServerClosed
	}
	httpServerShutdown = func(context.Context, *http.Server) error {
		log.add("http-drain")
		if shutdownErr != nil {
			return shutdownErr
		}
		stop()
		return nil
	}
	return func() { <-started }
}

// TestRunWithGenerationHost_ShutdownOrderRejectsThenDrainsThenClosesHost pins
// the canonical serve shutdown order and proves Host.Close runs exactly once.
//
//nolint:paralleltest // mutates package-level listenAndServe / httpServerShutdown
func TestRunWithGenerationHost_ShutdownOrderRejectsThenDrainsThenClosesHost(t *testing.T) {
	withRunningAsAdmin(t, func() (bool, error) { return false, nil })
	log := &serveOrderLog{}
	awaitStarted := stubServeListener(t, log, nil)
	host := newServeHostStub(log)
	mgmt := &serveManagementStub{log: log}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWithGenerationHost(ctx, GenerationHostInput{
			Config:          serveTestConfig(),
			Log:             serveTestLogger(),
			Host:            host,
			Management:      mgmt,
			ShutdownTimeout: 10 * time.Second,
		})
	}()
	awaitStarted()
	cancel()

	if err := <-errCh; err != nil {
		t.Fatalf("RunWithGenerationHost: %v", err)
	}
	assertServeOrder(t, log, "begin-shutdown", "http-drain", "management-shutdown", "host-close")
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want exactly 1", got)
	}
	if got := host.begins.Load(); got != 1 {
		t.Fatalf("BeginShutdown calls=%d want 1", got)
	}
}

// TestRunWithGenerationHost_HTTPShutdownFailureSkipsHostClose proves live
// handlers are never closed underneath a failed HTTP drain.
//
//nolint:paralleltest // mutates package-level listenAndServe / httpServerShutdown
func TestRunWithGenerationHost_HTTPShutdownFailureSkipsHostClose(t *testing.T) {
	withRunningAsAdmin(t, func() (bool, error) { return false, nil })
	log := &serveOrderLog{}
	awaitStarted := stubServeListener(t, log, context.DeadlineExceeded)
	host := newServeHostStub(log)
	mgmt := &serveManagementStub{log: log}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWithGenerationHost(ctx, GenerationHostInput{
			Config:          serveTestConfig(),
			Log:             serveTestLogger(),
			Host:            host,
			Management:      mgmt,
			ShutdownTimeout: 10 * time.Second,
		})
	}()
	awaitStarted()
	cancel()

	err := <-errCh
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v want HTTP drain failure", err)
	}
	if got := host.closes.Load(); got != 0 {
		t.Fatalf("Host.Close calls=%d want 0 after failed HTTP drain", got)
	}
	assertServeOrder(t, log, "begin-shutdown", "http-drain")
}

// TestRunWithGenerationHost_ManagementFailureStillClosesHost proves a failed
// management shutdown is joined truthfully but never blocks Host.Close.
//
//nolint:paralleltest // mutates package-level listenAndServe / httpServerShutdown
func TestRunWithGenerationHost_ManagementFailureStillClosesHost(t *testing.T) {
	withRunningAsAdmin(t, func() (bool, error) { return false, nil })
	log := &serveOrderLog{}
	awaitStarted := stubServeListener(t, log, nil)
	mgmtFailure := errors.New("management shutdown boom")
	host := newServeHostStub(log)
	host.closeErr = errors.New("host close boom")
	mgmt := &serveManagementStub{log: log, err: mgmtFailure}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWithGenerationHost(ctx, GenerationHostInput{
			Config:          serveTestConfig(),
			Log:             serveTestLogger(),
			Host:            host,
			Management:      mgmt,
			ShutdownTimeout: 10 * time.Second,
		})
	}()
	awaitStarted()
	cancel()

	err := <-errCh
	if !errors.Is(err, mgmtFailure) {
		t.Fatalf("err=%v must preserve the management failure", err)
	}
	if err == nil || !strings.Contains(err.Error(), "host close boom") {
		t.Fatalf("err=%v must join the host close failure", err)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want 1", got)
	}
	assertServeOrder(t, log, "begin-shutdown", "http-drain", "management-shutdown", "host-close")
}

// TestRunWithGenerationHost_StartupSecurityFailureUsesHostCloseSeam proves the
// pre-listen rejection path reuses the same single Host.Close seam.
//
//nolint:paralleltest // mutates package-level runningAsAdmin
func TestRunWithGenerationHost_StartupSecurityFailureUsesHostCloseSeam(t *testing.T) {
	withRunningAsAdmin(t, func() (bool, error) { return false, nil })
	log := &serveOrderLog{}
	host := newServeHostStub(log)
	mgmt := &serveManagementStub{log: log}

	err := RunWithGenerationHost(context.Background(), GenerationHostInput{
		Config: &config.Config{Server: config.ServerConfig{
			Address:  "0.0.0.0:8080",
			AuthMode: config.AuthModeNoAuth,
		}},
		Log:             serveTestLogger(),
		Host:            host,
		Management:      mgmt,
		ShutdownTimeout: 10 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "no_auth") {
		t.Fatalf("err=%v want startup security rejection", err)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want 1 on startup security failure", got)
	}
	assertServeOrder(t, log, "begin-shutdown", "management-shutdown", "host-close")
}

// TestRunWithGenerationHost_ServesStableHostHandler proves the data plane is
// the host-owned dispatcher rather than a locally reconstructed one.
//
//nolint:paralleltest // mutates package-level listenAndServe / httpServerShutdown
func TestRunWithGenerationHost_ServesStableHostHandler(t *testing.T) {
	withRunningAsAdmin(t, func() (bool, error) { return false, nil })
	log := &serveOrderLog{}
	origListen := listenAndServe
	origShutdown := httpServerShutdown
	t.Cleanup(func() {
		listenAndServe = origListen
		httpServerShutdown = origShutdown
	})

	host := newServeHostStub(log)
	seen := make(chan http.Handler, 1)
	release := make(chan struct{})
	listenAndServe = func(srv *http.Server) error {
		seen <- srv.Handler
		<-release
		return http.ErrServerClosed
	}
	httpServerShutdown = func(context.Context, *http.Server) error {
		close(release)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWithGenerationHost(ctx, GenerationHostInput{
			Config:          serveTestConfig(),
			Log:             serveTestLogger(),
			Host:            host,
			ShutdownTimeout: 10 * time.Second,
		})
	}()
	got := <-seen
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("RunWithGenerationHost: %v", err)
	}
	if got != host.handler {
		t.Fatal("http.Server must serve the host-provided stable handler")
	}
}

// TestRunWithGenerationHost_MalformedHostInputUsesCleanupSeam proves every
// post-Host input validation failure converges on BeginShutdown + Host.Close
// once (and optional management), without stranding process/tracing ownership.
//
//nolint:paralleltest // mutates package-level runningAsAdmin
func TestRunWithGenerationHost_MalformedHostInputUsesCleanupSeam(t *testing.T) {
	withRunningAsAdmin(t, func() (bool, error) { return false, nil })
	cases := []struct {
		name string
		in   func(host *serveHostStub, mgmt *serveManagementStub) GenerationHostInput
		want string
	}{
		{
			name: "nil_config",
			in: func(host *serveHostStub, mgmt *serveManagementStub) GenerationHostInput {
				return GenerationHostInput{Log: serveTestLogger(), Host: host, Management: mgmt, ShutdownTimeout: time.Second}
			},
			want: "nil config",
		},
		{
			name: "nil_log",
			in: func(host *serveHostStub, mgmt *serveManagementStub) GenerationHostInput {
				return GenerationHostInput{Config: serveTestConfig(), Host: host, Management: mgmt, ShutdownTimeout: time.Second}
			},
			want: "nil logger",
		},
		{
			name: "nil_handler",
			in: func(host *serveHostStub, mgmt *serveManagementStub) GenerationHostInput {
				host.handler = nil
				return GenerationHostInput{Config: serveTestConfig(), Log: serveTestLogger(), Host: host, Management: mgmt, ShutdownTimeout: time.Second}
			},
			want: "nil generation handler",
		},
		{
			name: "startup_security",
			in: func(host *serveHostStub, mgmt *serveManagementStub) GenerationHostInput {
				return GenerationHostInput{
					Config: &config.Config{Server: config.ServerConfig{
						Address:  "0.0.0.0:8080",
						AuthMode: config.AuthModeNoAuth,
					}},
					Log: serveTestLogger(), Host: host, Management: mgmt, ShutdownTimeout: time.Second,
				}
			},
			want: "no_auth",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := &serveOrderLog{}
			host := newServeHostStub(log)
			mgmt := &serveManagementStub{log: log}
			err := RunWithGenerationHost(context.Background(), tc.in(host, mgmt))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want substring %q", err, tc.want)
			}
			if got := host.closes.Load(); got != 1 {
				t.Fatalf("Host.Close calls=%d want 1", got)
			}
			if got := host.begins.Load(); got != 1 {
				t.Fatalf("BeginShutdown calls=%d want 1", got)
			}
			if got := mgmt.call.Load(); got != 1 {
				t.Fatalf("management Shutdown calls=%d want 1", got)
			}
			assertServeOrder(t, log, "begin-shutdown", "management-shutdown", "host-close")
		})
	}
}

// TestRunWithGenerationHost_NilContextOrHostSkipsCleanup proves there is no
// usable ownership seam when context or Host is missing.
func TestRunWithGenerationHost_NilContextOrHostSkipsCleanup(t *testing.T) {
	t.Parallel()
	log := &serveOrderLog{}
	host := newServeHostStub(log)
	if err := RunWithGenerationHost(
		nil, //nolint:staticcheck // SA1012: intentional nil context contract
		GenerationHostInput{Host: host},
	); err == nil || !strings.Contains(err.Error(), "nil context") {
		t.Fatalf("err=%v want nil context", err)
	}
	if got := host.closes.Load(); got != 0 {
		t.Fatalf("Host.Close calls=%d want 0 for nil context", got)
	}
	err := RunWithGenerationHost(context.Background(), GenerationHostInput{
		Config: serveTestConfig(),
		Log:    serveTestLogger(),
	})
	if err == nil || !strings.Contains(err.Error(), "nil generation serve host") {
		t.Fatalf("err=%v want nil host", err)
	}
}

// mutatingServeHost returns a different handler on the second HTTPHandler call
// so a double-read across validation/mount would mount the wrong value.
type mutatingServeHost struct {
	serveHostStub
	second http.Handler
}

func (h *mutatingServeHost) HTTPHandler() http.Handler {
	n := h.handlerCalls.Add(1)
	if n == 1 {
		return h.handler
	}
	return h.second
}

// TestRunWithGenerationHost_ResolvesHTTPHandlerOnce proves validation and mount
// share one resolved handler: HTTPHandler is invoked once and a mutable second
// return value cannot be mounted.
//
//nolint:paralleltest // mutates package-level listenAndServe / httpServerShutdown
func TestRunWithGenerationHost_ResolvesHTTPHandlerOnce(t *testing.T) {
	withRunningAsAdmin(t, func() (bool, error) { return false, nil })
	log := &serveOrderLog{}
	origListen := listenAndServe
	origShutdown := httpServerShutdown
	t.Cleanup(func() {
		listenAndServe = origListen
		httpServerShutdown = origShutdown
	})

	first := &stableServeHandler{}
	second := &altServeHandler{}
	host := &mutatingServeHost{
		serveHostStub: serveHostStub{log: log, handler: first},
		second:        second,
	}
	seen := make(chan http.Handler, 1)
	release := make(chan struct{})
	listenAndServe = func(srv *http.Server) error {
		seen <- srv.Handler
		<-release
		return http.ErrServerClosed
	}
	httpServerShutdown = func(context.Context, *http.Server) error {
		close(release)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWithGenerationHost(ctx, GenerationHostInput{
			Config:          serveTestConfig(),
			Log:             serveTestLogger(),
			Host:            host,
			ShutdownTimeout: 10 * time.Second,
		})
	}()
	got := <-seen
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("RunWithGenerationHost: %v", err)
	}
	if got != first {
		t.Fatal("http.Server must mount the handler resolved during validation")
	}
	if got == second {
		t.Fatal("second HTTPHandler return must not be mounted")
	}
	if calls := host.handlerCalls.Load(); calls != 1 {
		t.Fatalf("HTTPHandler calls=%d want 1", calls)
	}
}
