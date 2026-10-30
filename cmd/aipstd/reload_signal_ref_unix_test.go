//go:build unix

package main

import (
	"context"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
)

// Task 1.5 Unix SIGHUP trigger reference (req 1.2, 11.x). Not production serve wiring.

type RefSIGHUPTrigger struct {
	ch        chan struct{}
	coalesced atomic.Int64
	delivered atomic.Int64
}

func NewRefSIGHUPTrigger() *RefSIGHUPTrigger {
	return &RefSIGHUPTrigger{ch: make(chan struct{}, 1)}
}
func (t *RefSIGHUPTrigger) Coalesced() int64   { return t.coalesced.Load() }
func (t *RefSIGHUPTrigger) Delivered() int64   { return t.delivered.Load() }
func (t *RefSIGHUPTrigger) C() <-chan struct{} { return t.ch }

func (t *RefSIGHUPTrigger) Notify() {
	select {
	case t.ch <- struct{}{}:
		t.delivered.Add(1)
	default:
		t.coalesced.Add(1)
	}
}

func StartRefSIGHUPAdapter(ctx context.Context, trigger *RefSIGHUPTrigger) (stop func()) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, ReloadSignals()...)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case sig, ok := <-sigCh:
				if !ok {
					return
				}
				if sig == syscall.SIGHUP {
					trigger.Notify()
				}
			}
		}
	}()
	return func() { signal.Stop(sigCh) }
}
