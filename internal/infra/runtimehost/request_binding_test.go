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
)

func TestRequestBinding_TransferPinRetainsAfterHandler(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	d := runtimehost.NewGenerationDispatcher(m)

	var pin atomic.Pointer[runtimehost.Pin]
	g := publishPlane(t, m, "pin-src", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := runtimehost.BindingFromContext(r.Context())
		if !ok {
			t.Fatal("missing binding")
		}
		p, ok := b.TransferPin(runtimehost.PinSSE)
		if !ok {
			t.Fatal("TransferPin failed")
		}
		pin.Store(p)
		if _, ok := b.TransferPin(runtimehost.PinAsync); ok {
			t.Fatal("double TransferPin must fail")
		}
		_, _ = io.WriteString(w, "ok")
	}))

	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	if g.Refs() != 1 {
		t.Fatalf("refs after handler with pin=%d want 1", g.Refs())
	}
	p := pin.Load()
	if p == nil || p.Kind() != runtimehost.PinSSE {
		t.Fatalf("pin=%v", p)
	}
	p.Release()
	if g.Refs() != 0 {
		t.Fatalf("refs after pin release=%d", g.Refs())
	}
	p.Release() // exact-once no-op
	if g.Refs() != 0 {
		t.Fatalf("refs after double pin release=%d", g.Refs())
	}
}

func TestRequestBinding_FailedTransferWithoutBinding(t *testing.T) {
	t.Parallel()
	var nilCtx context.Context
	if _, ok := runtimehost.BindingFromContext(nilCtx); ok {
		t.Fatal("nil context must not yield binding")
	}
}

func TestRequestBinding_StatusSnapshotSafe(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	d := runtimehost.NewGenerationDispatcher(m)
	g := m.PrepareRequestPlane("meta", &testRequestPlane{h: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := runtimehost.BindingFromContext(r.Context())
		if !ok {
			t.Fatal("missing binding")
		}
		st := b.Status()
		if st.Meta.ID != 1 || st.Meta.Label != "meta" || st.Meta.PreviousID != 0 {
			t.Fatalf("status=%+v", st)
		}
		if st.Lifecycle != runtimehost.GenActive {
			t.Fatalf("lifecycle=%v", st.Lifecycle)
		}
		_, _ = io.WriteString(w, "ok")
	})})
	g.SetMetaHints(runtimehost.MetaHints{PublicFingerprint: "fp-safe", TriggerKind: "startup"})
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
}

func TestRequestBinding_MetaFrozenAcrossPublication(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	d := runtimehost.NewGenerationDispatcher(m)

	entered := make(chan struct{})
	release := make(chan struct{})
	var frozenID int64
	var frozenFP string
	var frozenLife runtimehost.GenLifecycle

	old := m.PrepareRequestPlane("old", &testRequestPlane{h: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := runtimehost.BindingFromContext(r.Context())
		if !ok {
			t.Fatal("missing binding")
		}
		st := b.Status()
		frozenID = st.Meta.ID
		frozenFP = st.Meta.PublicFingerprint
		frozenLife = st.Lifecycle
		close(entered)
		<-release
		st2 := b.Status()
		meta2 := b.Meta()
		if st2.Meta.ID != frozenID || meta2.ID != frozenID {
			t.Fatalf("bound meta mutated: before=%d after=%d meta=%d", frozenID, st2.Meta.ID, meta2.ID)
		}
		if st2.Meta.PublicFingerprint != frozenFP || meta2.PublicFingerprint != frozenFP {
			t.Fatalf("fingerprint mutated: %q -> %q / %q", frozenFP, st2.Meta.PublicFingerprint, meta2.PublicFingerprint)
		}
		if st2.Lifecycle != frozenLife {
			t.Fatalf("lifecycle mutated: %v -> %v", frozenLife, st2.Lifecycle)
		}
		_, _ = io.WriteString(w, "ok")
	})})
	old.SetMetaHints(runtimehost.MetaHints{PublicFingerprint: "fp-old", TriggerKind: "startup"})
	if err := m.Publish(old); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		rr := httptest.NewRecorder()
		d.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if rr.Code != http.StatusOK {
			t.Errorf("status=%d", rr.Code)
		}
	}()
	<-entered

	neu := m.PrepareRequestPlane("new", &testRequestPlane{h: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "new")
	})})
	neu.SetMetaHints(runtimehost.MetaHints{PublicFingerprint: "fp-new", TriggerKind: "reload"})
	if err := m.Publish(neu); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = m.RetireGeneration(ctx, old)

	close(release)
	<-done
	if frozenID != 1 || frozenFP != "fp-old" || frozenLife != runtimehost.GenActive {
		t.Fatalf("unexpected freeze snapshot id=%d fp=%q life=%v", frozenID, frozenFP, frozenLife)
	}
}

func TestRequestBinding_TransferPinRaceExactOnce(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	d := runtimehost.NewGenerationDispatcher(m)

	var successes atomic.Int64
	var pin atomic.Pointer[runtimehost.Pin]
	g := publishPlane(t, m, "race-pin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := runtimehost.BindingFromContext(r.Context())
		if !ok {
			t.Fatal("missing binding")
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(8)
		for range 8 {
			go func() {
				defer wg.Done()
				<-start
				if p, ok := b.TransferPin(runtimehost.PinProvider); ok {
					successes.Add(1)
					pin.Store(p)
				}
			}()
		}
		close(start)
		wg.Wait()
		_, _ = io.WriteString(w, "ok")
	}))

	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if successes.Load() != 1 {
		t.Fatalf("successes=%d want 1", successes.Load())
	}
	if g.Refs() != 1 {
		t.Fatalf("refs=%d want 1", g.Refs())
	}
	pin.Load().Release()
	if g.Refs() != 0 {
		t.Fatalf("refs after release=%d", g.Refs())
	}
}
