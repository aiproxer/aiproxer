package aipruntime_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipruntime"
)

// TestServe_RejectionsAreClassifiableFromExternalPackages proves the documented
// Serve admission classes are stable public identities: an external package
// classifies nil-context, unbound, closed, and already-served rejections with
// errors.Is, including repeat admissions while serving and after completion.
func TestServe_RejectionsAreClassifiableFromExternalPackages(t *testing.T) {
	t.Parallel()

	var nilRuntime *aipruntime.Runtime
	if err := nilRuntime.Serve(context.Background()); !errors.Is(err, aipruntime.ErrRuntimeUnbound) {
		t.Fatalf("nil runtime err=%v want ErrRuntimeUnbound", err)
	}
	zero := &aipruntime.Runtime{}
	if err := zero.Serve(context.Background()); !errors.Is(err, aipruntime.ErrRuntimeUnbound) {
		t.Fatalf("zero runtime err=%v want ErrRuntimeUnbound", err)
	}
	if err := zero.Serve(nil); !errors.Is(err, aipruntime.ErrServeNilContext) { //nolint:staticcheck // SA1012: intentional nil context contract
		t.Fatalf("nil context err=%v want ErrServeNilContext", err)
	}

	closed := aipruntime.NewRuntimeWithHostForTest(&facadeFakeHost{ready: true})
	if err := closed.Close(context.Background()); err != nil {
		t.Fatalf("direct Close: %v", err)
	}
	if err := closed.Serve(context.Background()); !errors.Is(err, aipruntime.ErrRuntimeClosed) {
		t.Fatalf("Serve after Close err=%v want ErrRuntimeClosed", err)
	}

	cfgPath, addr := writeServeTestConfig(t)
	rt, err := aipruntime.Build(context.Background(), aipruntime.Options{ConfigPath: cfgPath, LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	t.Cleanup(client.CloseIdleConnections)
	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- rt.Serve(serveCtx) }()
	awaitPublicServeReady(t, client, addr)

	if err := rt.Serve(context.Background()); !errors.Is(err, aipruntime.ErrRuntimeServed) {
		t.Fatalf("second Serve while live err=%v want ErrRuntimeServed", err)
	}
	cancelServe()
	select {
	case err := <-serveErrCh:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not converge after cancellation")
	}
	if err := rt.Serve(context.Background()); !errors.Is(err, aipruntime.ErrRuntimeServed) {
		t.Fatalf("sequential Serve after completion err=%v want ErrRuntimeServed", err)
	}
}
