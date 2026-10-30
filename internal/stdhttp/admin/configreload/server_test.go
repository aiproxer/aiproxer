package configreload_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	mgmtreload "github.com/aiproxer/aiproxer/internal/stdhttp/admin/configreload"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"github.com/stretchr/testify/assert"
)

func TestManagement_ServerLifecycle(t *testing.T) {
	t.Parallel()
	coord := newFakeCoordinator("/fixed/startup/config.yaml", func(context.Context, sdkreload.Trigger) sdkreload.Result {
		return sdkreload.Result{Category: sdkreload.ResultNoop, ActiveGeneration: 1}
	})
	srv, err := mgmtreload.New(mgmtreload.Options{
		Address:     "127.0.0.1:0",
		AuthMode:    mgmtreload.AuthModeBearer,
		BearerToken: "test-management-secret",
	}, coord)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	addr := srv.Addr()
	if addr == "" || addr == "127.0.0.1:0" {
		t.Fatalf("expected bound addr, got %q", addr)
	}
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+mgmtreload.StatusPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-management-secret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	assert.NoError(t, res.Body.Close())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", res.StatusCode)
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		t.Fatal(err)
	}
}

func TestManagement_ServerShutdownAppliesConfiguredTimeout(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	coord := newFakeCoordinator("/fixed/startup/config.yaml", func(context.Context, sdkreload.Trigger) sdkreload.Result {
		close(entered)
		<-release
		return sdkreload.Result{Category: sdkreload.ResultNoop, ActiveGeneration: 1}
	})
	srv, err := mgmtreload.New(mgmtreload.Options{
		Address:         "127.0.0.1:0",
		AuthMode:        mgmtreload.AuthModeBearer,
		BearerToken:     "test-management-secret",
		ShutdownTimeout: 25 * time.Millisecond,
	}, coord)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	addr := srv.Addr()

	reqErr := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, "http://"+addr+mgmtreload.ReloadPath, http.NoBody)
		if err != nil {
			reqErr <- err
			return
		}
		req.Header.Set("Authorization", "Bearer test-management-secret")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			reqErr <- err
			return
		}
		assert.NoError(t, res.Body.Close())
		reqErr <- nil
	}()

	select {
	case <-entered:
	case err := <-reqErr:
		t.Fatalf("reload request failed before coordinator entry: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for reload handler entry")
	}

	start := time.Now()
	err = srv.Shutdown(context.Background())
	elapsed := time.Since(start)
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error=%v want context deadline exceeded", err)
	}
	if elapsed > time.Second {
		t.Fatalf("shutdown ignored configured timeout: elapsed=%s", elapsed)
	}

	select {
	case err := <-reqErr:
		if err != nil {
			t.Fatalf("reload request did not complete cleanly after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for reload request cleanup")
	}
}

func TestManagement_ServerShutdownTimeoutCanBeRetried(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	coord := newFakeCoordinator("/fixed/startup/config.yaml", func(context.Context, sdkreload.Trigger) sdkreload.Result {
		close(entered)
		<-release
		return sdkreload.Result{Category: sdkreload.ResultNoop, ActiveGeneration: 1}
	})
	srv, err := mgmtreload.New(mgmtreload.Options{
		Address:         "127.0.0.1:0",
		AuthMode:        mgmtreload.AuthModeBearer,
		BearerToken:     "test-management-secret",
		ShutdownTimeout: 500 * time.Millisecond,
	}, coord)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	reqErr := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, "http://"+srv.Addr()+mgmtreload.ReloadPath, http.NoBody)
		if err != nil {
			reqErr <- err
			return
		}
		req.Header.Set("Authorization", "Bearer test-management-secret")
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			err = res.Body.Close()
		}
		reqErr <- err
	}()

	select {
	case <-entered:
	case err := <-reqErr:
		t.Fatalf("reload request failed before coordinator entry: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for reload handler entry")
	}

	firstCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	err = srv.Shutdown(firstCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first shutdown error=%v want context deadline exceeded", err)
	}

	retryErr := make(chan error, 1)
	go func() { retryErr <- srv.Shutdown(context.Background()) }()
	select {
	case err := <-retryErr:
		close(release)
		t.Fatalf("retry returned before active request drained: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-retryErr:
		if err != nil {
			t.Fatalf("retry shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for retry shutdown")
	}
	select {
	case err := <-reqErr:
		if err != nil {
			t.Fatalf("reload request cleanup: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for reload request cleanup")
	}
}

func TestManagement_RejectsNilCoordinator(t *testing.T) {
	t.Parallel()
	_, err := mgmtreload.New(mgmtreload.Options{Address: "127.0.0.1:0"}, nil)
	if err == nil {
		t.Fatal("want nil coordinator error")
	}
}
