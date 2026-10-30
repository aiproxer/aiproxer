package auxreq_test

import (
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
)

func TestAuxiliary_Stream_PinsGenerationUntilEOF(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManagerWithInstanceID(4, nil, "aux-inst")
	closer := &auxCloser{}
	g := m.PrepareOwned("aux", closer)
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	ctx := genpin.WithRetainer(context.Background(), leaseBinding{lease: lease})

	blocked := make(chan struct{})
	releaseRecv := make(chan struct{})
	client := auxreq.NewClient(func() auxreq.ExecutorRunner {
		return execFunc(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
			return &blockingStream{blocked: blocked, release: releaseRecv}, nil
		})
	})
	stream, err := client.Stream(ctx, auxiliary.Request{Call: &aipapi.Call{ID: "c1"}})
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if err := m.Publish(m.Prepare("next")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-g.Drained():
		t.Fatal("auxiliary stream must block generation close")
	case <-time.After(20 * time.Millisecond):
	}
	if closer.n.Load() != 0 {
		t.Fatal("closed under aux pin")
	}

	close(releaseRecv)
	_, err = stream.Recv(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("recv=%v", err)
	}
	<-blocked
	<-g.Drained()
	if err := g.BeginClose(); err != nil {
		t.Fatal(err)
	}
	_ = g.Close()
	if closer.n.Load() != 1 {
		t.Fatalf("closes=%d", closer.n.Load())
	}
}

func TestAuxiliary_Stream_ReleaseOnceOnCloseAndRace(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManagerWithInstanceID(2, nil, "aux-race")
	g := m.Prepare("aux-race")
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	ctx := genpin.WithRetainer(context.Background(), leaseBinding{lease: lease})
	client := auxreq.NewClient(func() auxreq.ExecutorRunner {
		return execFunc(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
			return &syncEOFStream{}, nil
		})
	})
	stream, err := client.Stream(ctx, auxiliary.Request{Call: &aipapi.Call{ID: "c2"}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = stream.Recv(context.Background())
		}()
		go func() {
			defer wg.Done()
			_ = stream.Close()
		}()
	}
	wg.Wait()
	lease.Release()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if g.Refs() == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if g.Refs() != 0 {
		t.Fatalf("refs=%d; pin leak or underflow", g.Refs())
	}
}

func TestAuxiliary_Stream_RetainFailClosed(t *testing.T) {
	t.Parallel()
	client := auxreq.NewClient(func() auxreq.ExecutorRunner {
		return execFunc(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
			t.Fatal("must not execute")
			return nil, nil
		})
	})
	ctx := genpin.WithRetainer(context.Background(), failingRetainer{})
	_, err := client.Stream(ctx, auxiliary.Request{Call: &aipapi.Call{ID: "c3"}})
	if err == nil {
		t.Fatal("expected retain failure")
	}
}

type failingRetainer struct{}

func (failingRetainer) RuntimeInstanceID() string             { return "i" }
func (failingRetainer) RuntimeGenerationID() string           { return "1" }
func (failingRetainer) Retain(genpin.Kind) (genpin.Pin, bool) { return nil, false }

type execFunc func(context.Context, *aipapi.Call) (aipapi.EventStream, error)

func (f execFunc) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	return f(ctx, call)
}

type blockingStream struct {
	blocked chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (s *blockingStream) Recv(context.Context) (aipapi.Event, error) {
	<-s.release
	s.once.Do(func() { close(s.blocked) })
	return aipapi.Event{}, io.EOF
}
func (s *blockingStream) Close() error { return nil }

type syncEOFStream struct {
	mu     sync.Mutex
	closed bool
}

func (s *syncEOFStream) Recv(context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return aipapi.Event{}, io.EOF
}

func (s *syncEOFStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

type auxCloser struct{ n atomic.Int32 }

func (c *auxCloser) Close() error { c.n.Add(1); return nil }

type leaseBinding struct{ lease *runtimehost.Lease }

func (b leaseBinding) RuntimeInstanceID() string { return b.lease.Meta().InstanceID }
func (b leaseBinding) RuntimeGenerationID() string {
	id := b.lease.Meta().ID
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func (b leaseBinding) Retain(kind genpin.Kind) (genpin.Pin, bool) {
	var pk runtimehost.PinKind
	switch kind {
	case genpin.KindAsync:
		pk = runtimehost.PinAsync
	case genpin.KindProvider:
		pk = runtimehost.PinProvider
	case genpin.KindSSE:
		pk = runtimehost.PinSSE
	default:
		return nil, false
	}
	p, ok := b.lease.RetainPin(pk)
	if !ok {
		return nil, false
	}
	return pinWrap{p: p}, true
}

type pinWrap struct{ p *runtimehost.Pin }

func (w pinWrap) Kind() genpin.Kind {
	switch w.p.Kind() {
	case runtimehost.PinAsync:
		return genpin.KindAsync
	case runtimehost.PinProvider:
		return genpin.KindProvider
	case runtimehost.PinSSE:
		return genpin.KindSSE
	default:
		return 0
	}
}
func (w pinWrap) Release() { w.p.Release() }
