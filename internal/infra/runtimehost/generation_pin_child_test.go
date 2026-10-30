package runtimehost_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
)

func TestGenerationPin_ChildRetain_MultipleIndependentExactOnce(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	g := m.Prepare("child-multi")
	mustPublish(t, m, g)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	defer lease.Release()

	pins := make([]*runtimehost.Pin, 0, 3)
	for _, kind := range []runtimehost.PinKind{runtimehost.PinSSE, runtimehost.PinAsync, runtimehost.PinProvider} {
		p, ok := lease.RetainPin(kind)
		if !ok {
			t.Fatalf("RetainPin(%v) failed", kind)
		}
		pins = append(pins, p)
	}
	// Lease ref + 3 child pins.
	if g.Refs() != 4 {
		t.Fatalf("refs=%d want 4", g.Refs())
	}
	if _, ok := lease.RetainPin(0); ok {
		t.Fatal("invalid kind must reject without consuming ownership")
	}
	if _, ok := lease.RetainPin(runtimehost.PinKind(99)); ok {
		t.Fatal("unknown kind must reject")
	}
	if g.Refs() != 4 {
		t.Fatalf("refs after invalid retain=%d", g.Refs())
	}

	lease.Release()
	if g.Refs() != 3 {
		t.Fatalf("refs after lease release=%d want 3", g.Refs())
	}
	if _, ok := lease.RetainPin(runtimehost.PinAsync); ok {
		t.Fatal("RetainPin after lease release must fail closed")
	}

	mustPublish(t, m, m.Prepare("child-multi-next"))
	select {
	case <-g.Drained():
		t.Fatal("child pins must block drain")
	default:
	}

	for i, p := range pins {
		p.Release()
		p.Release() // exact-once
		if i < len(pins)-1 {
			select {
			case <-g.Drained():
				t.Fatal("drained early")
			default:
			}
		}
	}
	<-g.Drained()
	if g.Refs() != 0 {
		t.Fatalf("refs=%d", g.Refs())
	}
}

func TestGenerationPin_ChildRetain_RacesRetirementAndHTTPRelease(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(8, nil)
	g := m.Prepare("child-race")
	mustPublish(t, m, g)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}

	var pins sync.Map
	var retained atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	// Gate HTTP release until at least one child pin succeeds so the race
	// covers retirement + release without an unbounded sleep.
	firstPin := make(chan struct{})
	var firstOnce sync.Once

	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			p, ok := lease.RetainPin(runtimehost.PinProvider)
			if !ok {
				return
			}
			retained.Add(1)
			pins.Store(i, p)
			firstOnce.Do(func() { close(firstPin) })
		}(i)
	}
	wg.Go(func() {
		<-start
		_ = m.Publish(m.Prepare("child-race-next"))
	})
	wg.Go(func() {
		<-start
		select {
		case <-firstPin:
		case <-time.After(2 * time.Second):
			t.Error("timed out waiting for first child pin")
		}
		lease.Release()
	})

	close(start)
	wg.Wait()

	n := int(retained.Load())
	if n == 0 {
		t.Fatal("expected at least one child pin to win the race")
	}
	var held int
	pins.Range(func(_, v any) bool {
		held++
		p, ok := v.(*runtimehost.Pin)
		if !ok {
			t.Fatalf("sync.Map value type %T, want *runtimehost.Pin", v)
		}
		p.Release()
		p.Release()
		return true
	})
	if held != n {
		t.Fatalf("held=%d retained=%d", held, n)
	}
	// After all releases, retired generation must drain (no underflow).
	select {
	case <-g.Drained():
	case <-time.After(2 * time.Second):
		t.Fatalf("generation did not drain; refs=%d", g.Refs())
	}
	if g.Refs() != 0 {
		t.Fatalf("refs underflow/leak=%d", g.Refs())
	}
}

func TestGenerationPin_ChildRetain_TimeoutShutdownNoForceClose(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	closer := &countingCloser{}
	g := m.PrepareOwned("child-shutdown", closer)
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	pin, ok := lease.RetainPin(runtimehost.PinProvider)
	if !ok {
		t.Fatal("RetainPin")
	}
	lease.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := m.ShutdownDetached(ctx)
	if err == nil {
		t.Fatal("expected timeout while child-pinned")
	}
	if closer.closes.Load() != 0 {
		t.Fatalf("force-closed pinned generation closes=%d", closer.closes.Load())
	}
	pin.Release()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if _, err := m.RetireGeneration(ctx2, g); err != nil {
		t.Fatalf("retire after release: %v", err)
	}
	if closer.closes.Load() != 1 {
		t.Fatalf("closes=%d want 1", closer.closes.Load())
	}
}

func TestGenerationPin_RequestBinding_GenpinRetainer(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	d := runtimehost.NewGenerationDispatcher(m)

	var pin genpin.Pin
	g := publishPlane(t, m, "genpin-ctx", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ret, ok := genpin.FromContext(r.Context())
		if !ok {
			t.Fatal("missing genpin retainer")
		}
		if ret.RuntimeGenerationID() == "" {
			t.Fatal("empty runtime generation id")
		}
		p, ok := ret.Retain(genpin.KindProvider)
		if !ok {
			t.Fatal("Retain failed")
		}
		pin = p
		_, _ = io.WriteString(w, "ok")
	}))

	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	if g.Refs() != 1 {
		t.Fatalf("refs after handler=%d want 1 (child pin)", g.Refs())
	}
	if pin == nil || pin.Kind() != genpin.KindProvider {
		t.Fatalf("pin=%v", pin)
	}
	pin.Release()
	if g.Refs() != 0 {
		t.Fatalf("refs=%d", g.Refs())
	}
}

func TestGenerationPin_TransferPinStillCompatible(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	g := m.Prepare("xfer-compat")
	mustPublish(t, m, g)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	child, ok := lease.RetainPin(runtimehost.PinAsync)
	if !ok {
		t.Fatal("child")
	}
	xfer, ok := lease.TransferPin(runtimehost.PinProvider)
	if !ok {
		t.Fatal("transfer")
	}
	lease.Release() // transferred; no-op
	if g.Refs() != 2 {
		t.Fatalf("refs=%d want 2", g.Refs())
	}
	child.Release()
	xfer.Release()
	if g.Refs() != 0 {
		t.Fatalf("refs=%d", g.Refs())
	}
}
