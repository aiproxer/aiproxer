//go:build !unix

package main

import (
	"context"
	"testing"
)

// Task 1.5: non-Unix API-only compile/contract (req 1.8, 11.8).

func TestSIGHUP_APIOnlyPlatform(t *testing.T) {
	t.Parallel()
	if PlatformReloadMode() != "api-only" {
		t.Fatalf("mode=%s", PlatformReloadMode())
	}
	if len(ReloadSignals()) != 0 {
		t.Fatal("non-Unix must not register SIGHUP")
	}
	if SignalsOverlap() {
		t.Fatal("unexpected overlap")
	}
	tr := NewRefSIGHUPTrigger()
	stop := StartRefSIGHUPAdapter(context.TODO(), tr)
	stop()
	if tr.Delivered() != 0 {
		t.Fatal("API-only stub must not deliver signal triggers")
	}
}

func TestProductionSIGHUPReload_IntegrationWired(t *testing.T) {
	t.Parallel()
	sigCtx, stop := startServeSignalHandling(context.Background(), nil)
	defer stop()
	select {
	case <-sigCtx.Done():
		t.Fatal("serve signal context must stay open without INT/TERM")
	default:
	}
	if PlatformReloadMode() != "api-only" {
		t.Fatalf("mode=%s", PlatformReloadMode())
	}
}
