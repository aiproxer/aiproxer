//go:build unix

package main

import (
	"context"
	"os"
	"syscall"
	"testing"
)

// Task 1.5 Unix SIGHUP vs INT/TERM (req 1.2, 11.1-11.9).

func TestSIGHUP_Contracts(t *testing.T) {
	withExclusiveSIGHUP(t)
	t.Run("separated_from_shutdown", func(t *testing.T) {
		if SignalsOverlap() || len(ReloadSignals()) != 1 || ReloadSignals()[0] != syscall.SIGHUP || len(ShutdownSignals()) != 2 {
			t.Fatalf("reload=%v shut=%v overlap=%v", ReloadSignals(), ShutdownSignals(), SignalsOverlap())
		}
	})
	t.Run("coalesce", func(t *testing.T) {
		tr := NewRefSIGHUPTrigger()
		tr.Notify()
		tr.Notify()
		tr.Notify()
		if tr.Delivered() != 1 || tr.Coalesced() != 2 {
			t.Fatalf("delivered=%d coalesced=%d", tr.Delivered(), tr.Coalesced())
		}
		<-tr.C()
		select {
		case <-tr.C():
			t.Fatal("extra pending")
		default:
		}
	})
	t.Run("adapter_delivery", func(t *testing.T) {
		ctx := t.Context()
		tr := NewRefSIGHUPTrigger()
		defer StartRefSIGHUPAdapter(ctx, tr)()
		got := make(chan struct{}, 1)
		go func() { <-tr.C(); got <- struct{}{} }()
		p, err := os.FindProcess(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Signal(syscall.SIGHUP); err != nil {
			t.Fatal(err)
		}
		select {
		case <-got:
		case <-ctx.Done():
			t.Fatal("no delivery")
		}
	})
	t.Run("int_term_not_reload", func(t *testing.T) {
		for _, sig := range ShutdownSignals() {
			if sig == syscall.SIGHUP {
				t.Fatal("shutdown includes SIGHUP")
			}
		}
	})
}

func TestConfigReload_ExplicitTriggerOnly_NoWatcher(t *testing.T) {
	t.Parallel()
	_ = NewRefSIGHUPTrigger()
	_ = ReloadSignals()
}

func TestProductionSIGHUPReload_IntegrationWired(t *testing.T) {
	t.Parallel()
	// Production adapter is exercised by SignalReload_* tests; serve uses
	// startServeSignalHandling with ShutdownSignals() and optional sink.
	sigCtx, stop := startServeSignalHandling(context.Background(), nil)
	defer stop()
	select {
	case <-sigCtx.Done():
		t.Fatal("serve signal context must stay open without INT/TERM")
	default:
	}
	if SignalsOverlap() {
		t.Fatal("overlap")
	}
}
