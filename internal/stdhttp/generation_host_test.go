package stdhttp_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/stretchr/testify/assert"
)

func TestInitialGeneration_RunWithGenerationHostShutdown(t *testing.T) {
	t.Parallel()
	cfgPath := bpkit.WriteDogfoodLocalStubConfig(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	host.Config().Server.Address = addr

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- stdhttp.RunWithGenerationHost(ctx, stdhttp.GenerationHostInput{
			Config:          host.Config(),
			Log:             host.Logger(),
			Host:            host,
			ShutdownTimeout: 5 * time.Second,
		})
	}()

	deadline := time.Now().Add(5 * time.Second)
	var ready bool
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			resp, err := http.Get("http://" + addr + "/any")
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				assert.NoError(t, resp.Body.Close())
				if resp.StatusCode != http.StatusServiceUnavailable {
					ready = true
					break
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		cancel()
		<-errCh
		t.Fatal("generation host never became ready")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunWithGenerationHost: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown timed out")
	}
	if !host.ProcessClosed() {
		t.Fatal("process services must close on host shutdown")
	}
	if host.CanAcquireActive() {
		t.Fatal("manager must reject acquire after shutdown")
	}
}
