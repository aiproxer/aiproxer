package runtimebundle_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

type terminalDecisionWithdrawalPlane struct {
	mu           sync.Mutex
	events       []string
	quiesced     chan struct{}
	closed       chan struct{}
	quiescedOnce sync.Once
	closedOnce   sync.Once
}

func newTerminalDecisionWithdrawalPlane() *terminalDecisionWithdrawalPlane {
	return &terminalDecisionWithdrawalPlane{
		quiesced: make(chan struct{}),
		closed:   make(chan struct{}),
	}
}

func (p *terminalDecisionWithdrawalPlane) Handler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}

func (p *terminalDecisionWithdrawalPlane) Quiesce(context.Context) error {
	p.mu.Lock()
	p.events = append(p.events, "quiesce")
	p.mu.Unlock()
	p.quiescedOnce.Do(func() { close(p.quiesced) })
	return nil
}

func (p *terminalDecisionWithdrawalPlane) Close() error {
	p.mu.Lock()
	p.events = append(p.events, "close")
	p.mu.Unlock()
	p.closedOnce.Do(func() { close(p.closed) })
	return nil
}

func (p *terminalDecisionWithdrawalPlane) snapshotEvents() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.events...)
}

func TestTerminalDecisionLifecycle_WithdrawalQuiescesDrainsAndClosesBeforeProcessRelease(t *testing.T) {
	t.Parallel()
	process := newProcessForGeneration(t)
	plane := newTerminalDecisionWithdrawalPlane()
	m := runtimehost.NewManager(2, nil)
	g := m.PrepareRequestPlane("terminal-withdrawal", plane)
	if err := m.Publish(g); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	requestLease, ok := m.Acquire()
	if !ok {
		t.Fatal("Acquire request lease")
	}
	continuationPin, ok := requestLease.RetainPin(runtimehost.PinProvider)
	if !ok {
		requestLease.Release()
		t.Fatal("RetainPin continuation lease")
	}

	// Withdrawal first stops admission and detaches the active generation. The
	// retained request and continuation leases keep the generation open while
	// quiesce is allowed to run.
	m.BeginShutdown()
	if _, ok := m.Acquire(); ok {
		t.Fatal("withdrawn manager admitted a new request")
	}
	if detached := m.DetachActive(); detached != g {
		t.Fatal("withdrawal detached the wrong generation")
	}

	retireDone := make(chan error, 1)
	go func() {
		_, err := m.RetireGeneration(context.Background(), g)
		retireDone <- err
	}()

	select {
	case <-plane.quiesced:
	case <-time.After(2 * time.Second):
		t.Fatal("generation was not quiesced")
	}
	select {
	case <-plane.closed:
		t.Fatal("generation closed before retained request/continuation leases drained")
	default:
	}
	deadline := time.Now().Add(2 * time.Second)
	for g.Lifecycle() != runtimehost.GenQuiesced && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := g.Lifecycle(); got != runtimehost.GenQuiesced {
		t.Fatalf("lifecycle=%v want GenQuiesced while leases remain", got)
	}
	if process.Closed() {
		t.Fatal("process dependency released during generation quiesce/drain")
	}

	requestLease.Release()
	select {
	case <-plane.closed:
		t.Fatal("generation closed before continuation lease drained")
	default:
	}
	continuationPin.Release()

	select {
	case err := <-retireDone:
		if err != nil {
			t.Fatalf("RetireGeneration: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("generation retirement did not complete after leases drained")
	}
	select {
	case <-plane.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("generation resource was not closed")
	}
	if got := plane.snapshotEvents(); len(got) < 2 || got[0] != "quiesce" || got[1] != "close" {
		t.Fatalf("withdrawal events=%v want quiesce then close", got)
	}
	if process.Closed() {
		t.Fatal("process dependency released by generation retirement")
	}

	if err := m.ShutdownDetached(context.Background()); err != nil {
		t.Fatalf("final ShutdownDetached: %v", err)
	}
	if err := process.Close(); err != nil {
		t.Fatalf("process close: %v", err)
	}
}

var (
	_ runtimehost.PublishedRequestPlane = (*terminalDecisionWithdrawalPlane)(nil)
	_ runtimehost.QuiesceCloser         = (*terminalDecisionWithdrawalPlane)(nil)
	_ runtimehost.OwnedCloser           = (*terminalDecisionWithdrawalPlane)(nil)
)
