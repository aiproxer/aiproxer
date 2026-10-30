package aipruntime_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipruntime"
)

// TestServe_ProductionBuildServesAndConverges proves the public external
// Runtime.Serve callability on a real Build: one admitted serve mounts the
// host dispatcher on the configured loopback address, reloads stay owned until
// shutdown begins, cancellation converges through the ordered teardown, Close
// reports the recorded successful result, and later Serve calls are rejected.
func TestServe_ProductionBuildServesAndConverges(t *testing.T) {
	t.Parallel()
	cfgPath, addr := writeServeTestConfig(t)

	rt, err := aipruntime.Build(context.Background(), aipruntime.Options{ConfigPath: cfgPath, LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !rt.Ready() {
		t.Fatal("runtime must be ready before Serve")
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

	// Reloads stay owned by the runtime until shutdown begins.
	_ = rt.ReloadStatus()
	mid := rt.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI, SafeActor: "serve-public-test"})
	if mid.Category == aipruntime.ResultCanceled {
		t.Fatalf("reload before shutdown must not be rejected, got %q", mid.Category)
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

	late := rt.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI, SafeActor: "after-serve"})
	if late.Category != aipruntime.ResultCanceled {
		t.Fatalf("reload after a completed serve = %q want canceled", late.Category)
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close after successful serve must report the recorded result: %v", err)
	}
	if rt.Ready() {
		t.Fatal("host must be closed after the serve run converges")
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("repeated Close: %v", err)
	}
	if err := rt.Serve(context.Background()); !errors.Is(err, aipruntime.ErrRuntimeServed) {
		t.Fatalf("Serve after completion err=%v want ErrRuntimeServed", err)
	}
	if _, err := client.Get("http://" + addr + "/after-serve"); err == nil {
		t.Fatal("rejected Serve after completion must not listen again")
	}
}

// awaitPublicServeReady polls the real loopback listener with a bounded
// deadline (a real HTTP client needs a real bind to accept connections).
func awaitPublicServeReady(t *testing.T, client *http.Client, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + addr + "/any")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusServiceUnavailable {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("generation dispatcher never became ready through Runtime.Serve")
}

// writeServeTestConfig materializes the dogfood example with a free loopback
// address and a short drain timeout, returning the config path and address.
func writeServeTestConfig(t *testing.T) (cfgPath string, addr string) {
	t.Helper()
	raw, err := os.ReadFile(bpkit.MaterializeExampleConfig(t, filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml")))
	if err != nil {
		t.Fatalf("read materialized config: %v", err)
	}
	addr = reserveServeTestAddress(t)
	body := strings.Replace(string(raw), `address: "127.0.0.1:18080"`,
		fmt.Sprintf("address: %q\n  shutdown_timeout: \"2s\"", addr), 1)
	if !strings.Contains(body, addr) {
		t.Fatal("failed to rewrite the server address marker")
	}
	path := filepath.Join(t.TempDir(), "dogfood-serve.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write serve config: %v", err)
	}
	return path, addr
}

func reserveServeTestAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("release reserved address: %v", err)
	}
	return addr
}
