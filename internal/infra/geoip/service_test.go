package geoip

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"
)

type fakeReader struct {
	mu        sync.Mutex
	closed    bool
	started   chan struct{}
	continueC chan struct{}
}

func (r *fakeReader) LookupCountry(netip.Addr) (string, bool, error) {
	if r.started != nil {
		r.mu.Lock()
		closed := r.closed
		r.mu.Unlock()
		if closed {
			panic("lookup used closed reader")
		}
		select {
		case <-r.started:
		default:
			close(r.started)
		}
		<-r.continueC
	}
	return "US", true, nil
}

func (r *fakeReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

func (r *fakeReader) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func TestServiceDoesNotCloseRetiredReaderDuringLookup(t *testing.T) {
	t.Parallel()

	old := &fakeReader{started: make(chan struct{}), continueC: make(chan struct{})}
	next := &fakeReader{}
	svc := New(old)
	defer func() { _ = svc.Close() }()

	lookupDone := make(chan struct{})
	go func() {
		_, _, _ = svc.LookupCountry(netip.MustParseAddr("192.0.2.1"))
		close(lookupDone)
	}()
	<-old.started
	publishDone := make(chan error, 1)
	go func() { publishDone <- svc.Publish(context.Background(), next) }()
	select {
	case err := <-publishDone:
		t.Fatalf("Publish returned before lookup drained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if old.isClosed() {
		t.Fatal("retired reader closed while lookup was active")
	}
	close(old.continueC)
	<-lookupDone
	if err := <-publishDone; err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !old.isClosed() {
		t.Fatal("retired reader was not closed after publication")
	}
}

func TestServicePublicationFenceCompletesDurableCommitBeforeClose(t *testing.T) {
	t.Parallel()

	svc := New(nil)
	next := &fakeReader{}
	commitStarted := make(chan struct{})
	releaseCommit := make(chan struct{})
	publishDone := make(chan error, 1)
	go func() {
		publishDone <- svc.PublishVersionWithCommit(context.Background(), next, "version", func() error {
			close(commitStarted)
			<-releaseCommit
			return nil
		})
	}()
	<-commitStarted
	closeDone := make(chan error, 1)
	go func() { closeDone <- svc.Close() }()
	select {
	case <-closeDone:
		t.Fatal("Close returned before durable publication commit completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseCommit)
	if err := <-publishDone; err != nil {
		t.Fatalf("PublishVersionWithCommit: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if status := svc.Status(); status.Version != "version" || status.Ready {
		t.Fatalf("closed service status = %+v", status)
	}
}

func TestServicePublicationCannotCompleteAfterCloseBegins(t *testing.T) {
	t.Parallel()

	svc := New(nil)
	next := &fakeReader{}
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- svc.RunOwnedUpdate(context.Background(), func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return svc.PublishVersion(ctx, next, "late")
		})
	}()
	<-started
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := <-finished; err == nil {
		t.Fatal("update published after Close began")
	}
	if !next.isClosed() {
		t.Fatal("rejected reader was not closed")
	}
	if status := svc.Status(); status.Version != "" || status.Ready {
		t.Fatalf("closed service status = %+v", status)
	}
}

func TestServiceCloseCancelsAndWaitsOwnedUpdate(t *testing.T) {
	t.Parallel()

	svc := New(nil)
	started := make(chan struct{})
	finished := make(chan struct{})
	updateDone := make(chan error, 1)
	go func() {
		updateDone <- svc.RunOwnedUpdate(context.Background(), func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(finished)
			return ctx.Err()
		})
	}()
	<-started
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Close returned before owned update finished")
	}
	if err := <-updateDone; err == nil {
		t.Fatal("owned update returned nil after shutdown cancellation")
	}
	if svc.Ready() {
		t.Fatal("closed service reported ready")
	}
}
