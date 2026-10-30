//go:build unix

package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

const sighupSafeActor = "sighup"

// SIGHUPAdapter is the production Unix OS driving adapter for fixed-source reload.
// It registers SIGHUP separately from INT/TERM, delivers into one bounded
// process-owned channel, and invokes a narrow ReloadTriggerSink (req 1.2, 11.x).
type SIGHUPAdapter struct {
	sink ReloadTriggerSink

	mu      sync.Mutex
	started bool
	sigCh   chan os.Signal
	stopCh  chan struct{}
	doneCh  chan struct{}
	stopped atomic.Bool
}

// NewSIGHUPAdapter constructs a production SIGHUP adapter. sink must be non-nil.
func NewSIGHUPAdapter(sink ReloadTriggerSink) *SIGHUPAdapter {
	return &SIGHUPAdapter{sink: sink}
}

// Start registers SIGHUP and runs one owned worker. It is idempotent after the
// first successful start; Stop must be called to release signal registration.
func (a *SIGHUPAdapter) Start(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return nil
	}
	if a.sink == nil {
		return errNilReloadSink
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.stopped.Store(false)
	a.sigCh = make(chan os.Signal, 1)
	a.stopCh = make(chan struct{})
	a.doneCh = make(chan struct{})
	signal.Notify(a.sigCh, ReloadSignals()...)
	a.started = true
	go a.loop(ctx)
	return nil
}

// Stop unregisters SIGHUP, rejects late triggers, and waits for the worker.
// It is safe to call multiple times and never sends on a closed channel.
func (a *SIGHUPAdapter) Stop() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if !a.started {
		a.mu.Unlock()
		return
	}
	if a.stopped.Swap(true) {
		done := a.doneCh
		a.mu.Unlock()
		if done != nil {
			<-done
		}
		return
	}
	sigCh := a.sigCh
	stopCh := a.stopCh
	doneCh := a.doneCh
	a.mu.Unlock()

	if sigCh != nil {
		signal.Stop(sigCh)
	}
	select {
	case <-stopCh:
	default:
		close(stopCh)
	}
	if doneCh != nil {
		<-doneCh
	}

	a.mu.Lock()
	a.started = false
	a.sigCh = nil
	a.stopCh = nil
	a.doneCh = nil
	a.mu.Unlock()
}

func (a *SIGHUPAdapter) loop(ctx context.Context) {
	defer close(a.doneCh)

	// deliverCh is the process-owned bounded trigger channel (cap 1). The single
	// worker both drains OS notifications and invokes the sink so there is never
	// one goroutine per signal (req 11.7). When deliverCh is full, a concurrent
	// Reload call lets the coordinator coalesce (req 11.5).
	deliverCh := make(chan struct{}, 1)

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		case _, ok := <-a.sigCh:
			if !ok || a.stopped.Load() {
				return
			}
			select {
			case deliverCh <- struct{}{}:
			default:
				// Worker already has a pending or in-flight delivery; coalesce
				// through the coordinator instead of queuing unbounded work.
				a.invokeReload(ctx)
			}
		case <-deliverCh:
			if a.stopped.Load() {
				return
			}
			a.invokeReload(ctx)
		}
	}
}

func (a *SIGHUPAdapter) invokeReload(ctx context.Context) {
	if a == nil || a.sink == nil || a.stopped.Load() {
		return
	}
	hostCtx := context.WithoutCancel(ctx)
	if hostCtx == nil {
		hostCtx = context.Background()
	}
	_ = a.sink.Reload(hostCtx, sdkreload.Trigger{
		Kind:       sdkreload.TriggerSIGHUP,
		AcceptedAt: time.Now().UTC(),
		SafeActor:  sighupSafeActor,
	})
}
