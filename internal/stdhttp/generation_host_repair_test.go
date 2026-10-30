package stdhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// These cases run the serve adapter against a real BuildHost so the
// host-owned shutdown ordering is exercised end to end. Generation/process
// retirement ordering itself is owned and tested by runtimebundle Host.Close.

func newServeIntegrationHost(t *testing.T) *runtimebundle.Host {
	t.Helper()
	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:      bpkit.WriteDogfoodLocalStubConfig(t),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	return host
}

//nolint:paralleltest // mutates package-level httpServerShutdown / listenAndServe
func TestRunWithGenerationHost_HTTPShutdownFailureDoesNotRetire(t *testing.T) {
	origListen := listenAndServe
	origShutdown := httpServerShutdown
	t.Cleanup(func() {
		listenAndServe = origListen
		httpServerShutdown = origShutdown
	})

	started := make(chan struct{})
	stopListen := make(chan struct{})
	var stopListenOnce sync.Once
	stopListenFn := func() { stopListenOnce.Do(func() { close(stopListen) }) }
	t.Cleanup(stopListenFn)
	listenAndServe = func(*http.Server) error {
		close(started)
		<-stopListen
		return http.ErrServerClosed
	}
	httpServerShutdown = func(context.Context, *http.Server) error {
		return context.DeadlineExceeded
	}

	host := newServeIntegrationHost(t)
	host.Config().Server.Address = "127.0.0.1:0"

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWithGenerationHost(ctx, GenerationHostInput{
			Config:          host.Config(),
			Log:             host.Logger(),
			Host:            host,
			ShutdownTimeout: time.Second,
		})
	}()
	<-started
	cancel()
	got := <-errCh
	stopListenFn()
	if got == nil {
		t.Fatal("expected http shutdown failure")
	}
	if !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("err=%v", got)
	}
	if host.ProcessClosed() {
		t.Fatal("process must not close under failed HTTP drain")
	}
	if !host.Ready() {
		t.Fatal("active generation must remain after failed HTTP drain")
	}
}

//nolint:paralleltest // mutates package-level httpServerShutdown / listenAndServe
func TestRunWithGenerationHost_CancellationPreservesConcurrentListenerFailure(t *testing.T) {
	origListen := listenAndServe
	origShutdown := httpServerShutdown
	t.Cleanup(func() {
		listenAndServe = origListen
		httpServerShutdown = origShutdown
	})

	listenerFailure := errors.New("listener failed during cancellation")
	listenStarted := make(chan struct{})
	releaseListener := make(chan struct{})
	listenerReturned := make(chan struct{})
	listenAndServe = func(*http.Server) error {
		close(listenStarted)
		<-releaseListener
		close(listenerReturned)
		return listenerFailure
	}
	shutdownEntered := make(chan struct{})
	releaseShutdown := make(chan struct{})
	httpServerShutdown = func(context.Context, *http.Server) error {
		close(shutdownEntered)
		<-releaseShutdown
		return nil
	}

	host := newServeIntegrationHost(t)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWithGenerationHost(ctx, GenerationHostInput{
			Config:          host.Config(),
			Log:             host.Logger(),
			Host:            host,
			ShutdownTimeout: 5 * time.Second,
		})
	}()
	<-listenStarted
	cancel()
	<-shutdownEntered
	close(releaseListener)
	<-listenerReturned
	close(releaseShutdown)

	got := <-errCh
	if !errors.Is(got, listenerFailure) {
		t.Fatalf("got %v, want listener failure", got)
	}
	if !host.ProcessClosed() {
		t.Fatal("process services must close after the listener exits and HTTP drain succeeds")
	}
}

//nolint:paralleltest // mutates package-level httpServerShutdown / listenAndServe
func TestRunWithGenerationHost_ShutdownListenerErrorStillDrainsHTTP(t *testing.T) {
	origListen := listenAndServe
	origShutdown := httpServerShutdown
	t.Cleanup(func() {
		listenAndServe = origListen
		httpServerShutdown = origShutdown
	})

	listenAndServe = func(*http.Server) error {
		return errors.New("listener boom")
	}
	var shutdownCalls atomic.Int32
	httpServerShutdown = func(context.Context, *http.Server) error {
		shutdownCalls.Add(1)
		return nil
	}

	host := newServeIntegrationHost(t)
	host.Config().Server.Address = "127.0.0.1:0"

	err := RunWithGenerationHost(context.Background(), GenerationHostInput{
		Config:          host.Config(),
		Log:             host.Logger(),
		Host:            host,
		ShutdownTimeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected serve error")
	}
	if shutdownCalls.Load() != 1 {
		t.Fatalf("http shutdown calls=%d want 1", shutdownCalls.Load())
	}
	if !host.ProcessClosed() {
		t.Fatal("process must close after successful HTTP drain on listener error")
	}
	if host.CanAcquireActive() {
		t.Fatal("manager must reject acquire after shutdown")
	}
}
