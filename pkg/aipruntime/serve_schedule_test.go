package aipruntime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// Task 5.1 C4 schedule tests: Serve admits exactly one run, Close converges on
// cancel/drain/join with the ordered stdhttp shutdown, and every rejection
// happens before the host is touched. These tests drive the real retained
// stdhttp worker and a real loopback listener through a host double.

// serveScheduleHost is the Runtime host double for serve schedule tests. It
// also satisfies the stdhttp.GenerationServeHost seam, so Serve runs the real
// retained stdhttp worker and http.Server.
type serveScheduleHost struct {
	handler  http.Handler
	begins   atomic.Int32
	closes   atomic.Int32
	closeErr error
}

func (h *serveScheduleHost) ExecutorView() aipsdk.ExecutorView { return nil }
func (h *serveScheduleHost) Ready() bool                       { return true }
func (h *serveScheduleHost) Capabilities() HostCapabilities    { return HostCapabilities{} }
func (h *serveScheduleHost) MeteringQuerier() metering.Querier { return nil }
func (h *serveScheduleHost) ReadinessReport() controlplane.ReadinessReportReader {
	return nil
}
func (h *serveScheduleHost) RefreshSnapshots(context.Context) error { return nil }
func (h *serveScheduleHost) Reload(context.Context, sdkreload.Trigger) sdkreload.Result {
	return sdkreload.Result{}
}
func (h *serveScheduleHost) Status() sdkreload.Status { return sdkreload.Status{} }
func (h *serveScheduleHost) Close(context.Context) error {
	h.closes.Add(1)
	return h.closeErr
}
func (h *serveScheduleHost) HTTPHandler() http.Handler { return h.handler }
func (h *serveScheduleHost) BeginShutdown()            { h.begins.Add(1) }

func newServeScheduleRuntime(t *testing.T, host *serveScheduleHost, cfg *config.Config) *Runtime {
	t.Helper()
	return &Runtime{
		host:      host,
		serveCfg:  cfg,
		serveLog:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		serveHost: host,
	}
}

// serveScheduleConfig reserves a free loopback port and returns a server
// config whose drain timeout fits the test.
func serveScheduleConfig(t *testing.T, drainTimeout string) *config.Config {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("release reserved address: %v", err)
	}
	return &config.Config{Server: config.ServerConfig{Address: addr, ShutdownTimeout: drainTimeout}}
}

func newScheduleClient(t *testing.T) *http.Client {
	t.Helper()
	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func scheduleGet(t *testing.T, client *http.Client, addr string) *http.Response {
	t.Helper()
	resp, err := client.Get("http://" + addr + "/schedule-probe")
	if err != nil {
		t.Fatalf("GET %s: %v", addr, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
	return resp
}

// awaitScheduleServeReady polls the loopback listener with a bounded deadline
// (real HTTP client; the listener needs a real bind to accept connections).
func awaitScheduleServeReady(t *testing.T, client *http.Client, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + addr + "/schedule-ready")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("serve listener never became reachable")
}

func awaitScheduleError(t *testing.T, errCh <-chan error) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("serve run did not converge")
		return nil
	}
}

// blockingScheduleHandler holds every request until released, so the test can
// prove Close never tears the host down under an accepted handler.
type blockingScheduleHandler struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingScheduleHandler() *blockingScheduleHandler {
	return &blockingScheduleHandler{entered: make(chan struct{}), release: make(chan struct{})}
}

func (h *blockingScheduleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/held" {
		w.WriteHeader(http.StatusOK)
		return
	}
	h.once.Do(func() { close(h.entered) })
	<-h.release
	w.WriteHeader(http.StatusOK)
}

// TestServe_CloseCancelsDrainsJoinsAndClosesOnce proves an active Close
// cancels the serve run, waits for it, and leaves the host open while an
// admitted handler is held; once released, a later Close resumes the ordered
// drain and closes the host exactly once, reporting the recorded serve result.
func TestServe_CloseCancelsDrainsJoinsAndClosesOnce(t *testing.T) {
	t.Parallel()
	handler := newBlockingScheduleHandler()
	host := &serveScheduleHost{handler: handler}
	cfg := serveScheduleConfig(t, "750ms")
	rt := newServeScheduleRuntime(t, host, cfg)
	client := newScheduleClient(t)
	addr := cfg.Server.Address

	serveCtx := t.Context()
	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- rt.Serve(serveCtx) }()
	awaitScheduleServeReady(t, client, addr)

	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		resp, err := client.Get("http://" + addr + "/held")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-handler.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("admitted handler never entered")
	}

	// A Close whose own deadline expires while the accepted handler is held
	// returns the deadline while the owned serve run keeps completing its
	// ordered shutdown; a later Close joins the same run.
	shortCtx, cancelShort := context.WithTimeout(context.Background(), 100*time.Millisecond)
	shortErr := rt.Close(shortCtx)
	cancelShort()
	if !errors.Is(shortErr, context.DeadlineExceeded) {
		t.Fatalf("short-deadline Close err=%v want deadline while the handler is held", shortErr)
	}
	if got := host.closes.Load(); got != 0 {
		t.Fatalf("Host.Close calls=%d want 0 after an expired Close wait", got)
	}

	closeErrCh := make(chan error, 1)
	go func() { closeErrCh <- rt.Close(context.Background()) }()
	closeErr := awaitScheduleError(t, closeErrCh)
	if !errors.Is(closeErr, context.DeadlineExceeded) {
		t.Fatalf("Close err=%v want the drain deadline while the handler is held", closeErr)
	}
	if got := host.closes.Load(); got != 0 {
		t.Fatalf("Host.Close calls=%d want 0 while an accepted handler is held", got)
	}
	if host.begins.Load() < 1 {
		t.Fatal("BeginShutdown must run before the HTTP drain")
	}
	serveErr := awaitScheduleError(t, serveErrCh)
	if !errors.Is(serveErr, context.DeadlineExceeded) {
		t.Fatalf("Serve err=%v want the drain deadline while the handler is held", serveErr)
	}
	select {
	case <-requestDone:
		t.Fatal("held request must still be in flight while the host stays open")
	default:
	}

	close(handler.release)
	select {
	case <-requestDone:
	case <-time.After(5 * time.Second):
		t.Fatal("held request never completed after release")
	}

	closeCtx, cancelClose := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelClose()
	resumedErr := rt.Close(closeCtx)
	if !errors.Is(resumedErr, serveErr) {
		t.Fatalf("resumed Close err=%v want the recorded serve result %v", resumedErr, serveErr)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want exactly 1 after the resumed drain", got)
	}
	if again := rt.Close(closeCtx); !errors.Is(again, serveErr) {
		t.Fatalf("repeated Close err=%v want the recorded serve result %v", again, serveErr)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want exactly 1 after repeated Close", got)
	}
}

// TestServe_SecondServeRejectedWithoutTouchingFirstHost proves repeat and
// sequential Serve admissions fail before any host work while the first run
// keeps serving.
func TestServe_SecondServeRejectedWithoutTouchingFirstHost(t *testing.T) {
	t.Parallel()
	host := &serveScheduleHost{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	cfg := serveScheduleConfig(t, "2s")
	rt := newServeScheduleRuntime(t, host, cfg)
	client := newScheduleClient(t)
	addr := cfg.Server.Address

	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- rt.Serve(serveCtx) }()
	awaitScheduleServeReady(t, client, addr)

	err := rt.Serve(context.Background())
	if !errors.Is(err, ErrRuntimeServed) {
		t.Fatalf("second Serve err=%v want typed already-served rejection", err)
	}
	if got := host.begins.Load(); got != 0 {
		t.Fatalf("BeginShutdown calls=%d want 0 before shutdown begins", got)
	}
	if got := host.closes.Load(); got != 0 {
		t.Fatalf("Host.Close calls=%d want 0 while the first serve is live", got)
	}
	if resp := scheduleGet(t, client, addr); resp.StatusCode != http.StatusOK {
		t.Fatalf("first host status=%d want 200 after rejected second Serve", resp.StatusCode)
	}

	cancelServe()
	if err := awaitScheduleError(t, serveErrCh); err != nil {
		t.Fatalf("first Serve: %v", err)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want exactly 1 after the first run converges", got)
	}

	err = rt.Serve(context.Background())
	if !errors.Is(err, ErrRuntimeServed) {
		t.Fatalf("sequential Serve after completion err=%v want rejection", err)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want 1 after rejected sequential Serve", got)
	}
}

// TestServe_ConcurrentAdmissionSingleWinner proves concurrent Serve calls admit
// exactly one run: the loser is rejected while the winner keeps serving.
func TestServe_ConcurrentAdmissionSingleWinner(t *testing.T) {
	t.Parallel()
	host := &serveScheduleHost{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	cfg := serveScheduleConfig(t, "2s")
	rt := newServeScheduleRuntime(t, host, cfg)
	client := newScheduleClient(t)
	addr := cfg.Server.Address

	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			results <- rt.Serve(serveCtx)
		}()
	}
	close(start)

	first := awaitScheduleError(t, results)
	if !errors.Is(first, ErrRuntimeServed) {
		t.Fatalf("racing Serve err=%v want exactly one admission", first)
	}
	awaitScheduleServeReady(t, client, addr)
	if got := host.closes.Load(); got != 0 {
		t.Fatalf("Host.Close calls=%d want 0 while the winner serves", got)
	}

	cancelServe()
	second := awaitScheduleError(t, results)
	if second != nil {
		t.Fatalf("winning Serve err=%v want nil after cancellation", second)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want exactly 1", got)
	}
}

// TestServe_RejectsNilContextZeroRuntimeAndAfterClose proves every pre-host
// rejection path: nil receiver, zero runtime, nil context, and Serve after a
// direct Close; none of them touches the host, and a rejected nil-context call
// consumes no admission.
func TestServe_RejectsNilContextZeroRuntimeAndAfterClose(t *testing.T) {
	t.Parallel()
	var nilRuntime *Runtime
	if err := nilRuntime.Serve(context.Background()); !errors.Is(err, ErrRuntimeUnbound) {
		t.Fatalf("nil runtime err=%v want ErrRuntimeUnbound", err)
	}
	if err := (&Runtime{}).Serve(context.Background()); !errors.Is(err, ErrRuntimeUnbound) {
		t.Fatalf("zero runtime err=%v want ErrRuntimeUnbound", err)
	}

	host := &serveScheduleHost{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	cfg := serveScheduleConfig(t, "2s")
	rt := newServeScheduleRuntime(t, host, cfg)
	if err := rt.Serve(nil); !errors.Is(err, ErrServeNilContext) { //nolint:staticcheck // SA1012: intentional nil context contract
		t.Fatalf("nil context err=%v want ErrServeNilContext", err)
	}
	if got := host.begins.Load(); got != 0 || host.closes.Load() != 0 {
		t.Fatalf("nil context must not touch the host (begins=%d closes=%d)", got, host.closes.Load())
	}

	// The rejected call consumed no admission: the next valid Serve still runs.
	client := newScheduleClient(t)
	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- rt.Serve(serveCtx) }()
	awaitScheduleServeReady(t, client, cfg.Server.Address)
	cancelServe()
	if err := awaitScheduleError(t, serveErrCh); err != nil {
		t.Fatalf("admitted Serve after rejection: %v", err)
	}

	closedHost := &serveScheduleHost{handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	closedRT := newServeScheduleRuntime(t, closedHost, serveScheduleConfig(t, "2s"))
	if err := closedRT.Close(context.Background()); err != nil {
		t.Fatalf("direct Close: %v", err)
	}
	if got := closedHost.closes.Load(); got != 1 {
		t.Fatalf("direct Close calls=%d want 1", got)
	}
	if err := closedRT.Serve(context.Background()); !errors.Is(err, ErrRuntimeClosed) {
		t.Fatalf("Serve after Close err=%v want ErrRuntimeClosed", err)
	}
	if got := closedHost.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want 1: Serve after Close must not touch the host", got)
	}
}

// TestServe_ListenerFailureClosesHostOnce proves a listener bind failure
// converges through the same ordered stdhttp shutdown and that Close then
// reports the recorded serve result idempotently.
func TestServe_ListenerFailureClosesHostOnce(t *testing.T) {
	t.Parallel()
	occupier, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy address: %v", err)
	}
	defer func() { _ = occupier.Close() }()

	host := &serveScheduleHost{handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	cfg := &config.Config{Server: config.ServerConfig{Address: occupier.Addr().String(), ShutdownTimeout: "500ms"}}
	rt := newServeScheduleRuntime(t, host, cfg)

	serveErr := rt.Serve(context.Background())
	if serveErr == nil {
		t.Fatal("Serve must fail when the listener address is in use")
	}
	if !strings.Contains(serveErr.Error(), "listen tcp") {
		t.Fatalf("Serve err=%v want the listener failure", serveErr)
	}
	if got := host.begins.Load(); got != 1 {
		t.Fatalf("BeginShutdown calls=%d want 1 on listener failure", got)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want exactly 1 on listener failure", got)
	}

	if err := rt.Close(context.Background()); !errors.Is(err, serveErr) {
		t.Fatalf("Close err=%v want the recorded serve result", err)
	}
	if err := rt.Close(context.Background()); !errors.Is(err, serveErr) {
		t.Fatalf("repeated Close err=%v want the recorded serve result", err)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want 1 after Close reports the recorded result", got)
	}
}
