package runtimehost_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

type recordingExec struct {
	id       string
	calls    atomic.Int64
	cancels  atomic.Int64
	stream   aipapi.EventStream
	execErr  error
	cancelFn func(context.Context, aipapi.ALegCancelRequest) error
}

func (e *recordingExec) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	e.calls.Add(1)
	if e.execErr != nil {
		return nil, e.execErr
	}
	if e.stream != nil {
		return e.stream, nil
	}
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventMessageStarted}}), nil
}

func (e *recordingExec) CancelALeg(ctx context.Context, req aipapi.ALegCancelRequest) error {
	e.cancels.Add(1)
	if e.cancelFn != nil {
		return e.cancelFn(ctx, req)
	}
	return nil
}

func (e *recordingExec) WallClock() func() time.Time { return nil }

type execPlane struct {
	exec    aipsdk.ExecutorView
	closed  atomic.Bool
	handler http.Handler
}

func (p *execPlane) Handler() http.Handler {
	if p.handler != nil {
		return p.handler
	}
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}
func (p *execPlane) Quiesce(context.Context) error { return nil }
func (p *execPlane) Close() error {
	p.closed.Store(true)
	return nil
}
func (p *execPlane) ExecutorView() aipsdk.ExecutorView { return p.exec }

type holdStream struct {
	mu     sync.Mutex
	events []aipapi.Event
	pos    int
	closed atomic.Bool
	block  chan struct{}
}

func (s *holdStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pos >= len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[s.pos]
	s.pos++
	return ev, nil
}

func (s *holdStream) Close() error {
	s.closed.Store(true)
	if s.block != nil {
		select {
		case <-s.block:
		default:
			close(s.block)
		}
	}
	return nil
}

func publishExecGen(t *testing.T, m *runtimehost.Manager, label string, exec aipsdk.ExecutorView) *runtimehost.Generation {
	t.Helper()
	g := m.PrepareRequestPlane(label, &execPlane{exec: exec})
	if err := m.Publish(g); err != nil {
		t.Fatalf("Publish %s: %v", label, err)
	}
	return g
}

func TestGenerationExecutor_PreReloadFacadeRoutesNewCallsAfterPublish(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	oldExec := &recordingExec{id: "old"}
	publishExecGen(t, m, "g1", oldExec)
	facade := runtimehost.NewGenerationExecutor(m)

	stream, err := facade.Execute(context.Background(), &aipapi.Call{})
	if err != nil {
		t.Fatalf("Execute gen1: %v", err)
	}
	if oldExec.calls.Load() != 1 {
		t.Fatalf("old calls=%d", oldExec.calls.Load())
	}
	_, _ = stream.Recv(context.Background())
	_ = stream.Close()

	newExec := &recordingExec{id: "new"}
	publishExecGen(t, m, "g2", newExec)

	stream2, err := facade.Execute(context.Background(), &aipapi.Call{})
	if err != nil {
		t.Fatalf("Execute gen2: %v", err)
	}
	t.Cleanup(func() { _ = stream2.Close() })
	if newExec.calls.Load() != 1 {
		t.Fatalf("new calls=%d", newExec.calls.Load())
	}
	if oldExec.calls.Load() != 1 {
		t.Fatalf("old must not receive post-reload calls: %d", oldExec.calls.Load())
	}
}

func TestGenerationExecutor_OldStreamSurvivesPublication(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	hold := &holdStream{
		events: []aipapi.Event{{Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventTextDelta}},
		block:  make(chan struct{}),
	}
	oldExec := &recordingExec{id: "old", stream: hold}
	publishExecGen(t, m, "g1", oldExec)
	facade := runtimehost.NewGenerationExecutor(m)

	oldStream, err := facade.Execute(context.Background(), &aipapi.Call{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	newExec := &recordingExec{id: "new"}
	publishExecGen(t, m, "g2", newExec)

	close(hold.block)
	ev, err := oldStream.Recv(context.Background())
	if err != nil || ev.Kind != aipapi.EventMessageStarted {
		t.Fatalf("old stream after publish: ev=%v err=%v", ev, err)
	}
	_, err = oldStream.Recv(context.Background())
	if err != nil {
		t.Fatalf("second recv: %v", err)
	}
	_, err = oldStream.Recv(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF got %v", err)
	}
	_ = oldStream.Close()

	if oldExec.calls.Load() != 1 || newExec.calls.Load() != 0 {
		t.Fatalf("old=%d new=%d", oldExec.calls.Load(), newExec.calls.Load())
	}
}

func TestGenerationExecutor_CancelALegReachesAcrossGenerations(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	var gotID string
	var mu sync.Mutex
	oldExec := &recordingExec{
		id: "old",
		cancelFn: func(_ context.Context, req aipapi.ALegCancelRequest) error {
			mu.Lock()
			gotID = req.ALegID
			mu.Unlock()
			return nil
		},
	}
	publishExecGen(t, m, "g1", oldExec)
	facade := runtimehost.NewGenerationExecutor(m)

	// Simulate process-owned cancel path: after publication, cancel still works
	// through the stable facade (active gen executor shares process lifecycle).
	newExec := &recordingExec{
		id: "new",
		cancelFn: func(_ context.Context, req aipapi.ALegCancelRequest) error {
			mu.Lock()
			gotID = "via-new:" + req.ALegID
			mu.Unlock()
			return nil
		},
	}
	publishExecGen(t, m, "g2", newExec)

	err := facade.CancelALeg(context.Background(), aipapi.ALegCancelRequest{ALegID: "aleg-1", Reason: "test"})
	if err != nil {
		t.Fatalf("CancelALeg: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotID != "via-new:aleg-1" {
		t.Fatalf("gotID=%q", gotID)
	}
	if newExec.cancels.Load() != 1 {
		t.Fatalf("cancels=%d", newExec.cancels.Load())
	}
}

func TestGenerationExecutor_AcquireFailure(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(2, nil)
	m.BeginShutdown()
	facade := runtimehost.NewGenerationExecutor(m)
	_, err := facade.Execute(context.Background(), &aipapi.Call{})
	if !errors.Is(err, runtimehost.ErrNoActiveExecutor) {
		t.Fatalf("err=%v", err)
	}
}
