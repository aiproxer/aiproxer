package runtimehost_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

type testRequestPlane struct {
	h http.Handler
}

func (p *testRequestPlane) Handler() http.Handler         { return p.h }
func (p *testRequestPlane) Close() error                  { return nil }
func (p *testRequestPlane) Quiesce(context.Context) error { return nil }

func publishPlane(t *testing.T, m *runtimehost.Manager, label string, h http.Handler) *runtimehost.Generation {
	t.Helper()
	g := m.PrepareRequestPlane(label, &testRequestPlane{h: h})
	if err := m.Publish(g); err != nil {
		t.Fatalf("Publish(%s): %v", label, err)
	}
	return g
}

func TestGenerationDispatcher_NoActiveReturns503(t *testing.T) {
	t.Parallel()
	d := runtimehost.NewGenerationDispatcher(runtimehost.NewManager(4, nil))
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rr.Code)
	}
}

func TestGenerationDispatcher_NoBoundHandlerReturns503(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	g := m.Prepare("unbound")
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
	d := runtimehost.NewGenerationDispatcher(m)
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rr.Code)
	}
}

func TestGenerationDispatcher_BeforeAfterPublicationExactHandlers(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	d := runtimehost.NewGenerationDispatcher(m)

	var calls atomic.Int64
	old := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, "old")
	})
	publishPlane(t, m, "old", old)

	rr1 := httptest.NewRecorder()
	d.ServeHTTP(rr1, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr1.Body.String() != "old" {
		t.Fatalf("before body=%q", rr1.Body.String())
	}

	newH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(10)
		_, _ = io.WriteString(w, "new")
	})
	publishPlane(t, m, "new", newH)

	rr2 := httptest.NewRecorder()
	d.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr2.Body.String() != "new" {
		t.Fatalf("after body=%q", rr2.Body.String())
	}
	if calls.Load() != 11 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestGenerationDispatcher_BlockedRequestKeepsOldAfterPublish(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	d := runtimehost.NewGenerationDispatcher(m)

	entered := make(chan struct{})
	release := make(chan struct{})
	old := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		b, ok := runtimehost.BindingFromContext(r.Context())
		if !ok || b.Meta().ID != 1 {
			t.Errorf("old binding meta=%v ok=%v", b, ok)
		}
		_, _ = io.WriteString(w, "old-done")
	})
	oldGen := publishPlane(t, m, "old", old)

	done := make(chan string, 1)
	go func() {
		rr := httptest.NewRecorder()
		d.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/blocked", nil))
		done <- rr.Body.String()
	}()
	<-entered

	newH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := runtimehost.BindingFromContext(r.Context())
		if !ok || b.Meta().ID != 2 {
			t.Errorf("new binding meta=%v ok=%v", b, ok)
		}
		_, _ = io.WriteString(w, "new-done")
	})
	publishPlane(t, m, "new", newH)

	rrNew := httptest.NewRecorder()
	d.ServeHTTP(rrNew, httptest.NewRequest(http.MethodGet, "/new", nil))
	if rrNew.Body.String() != "new-done" {
		t.Fatalf("new body=%q", rrNew.Body.String())
	}

	if oldGen.Refs() < 1 {
		t.Fatalf("old refs=%d want retained lease", oldGen.Refs())
	}
	close(release)
	if body := <-done; body != "old-done" {
		t.Fatalf("blocked body=%q", body)
	}
	if oldGen.Refs() != 0 {
		t.Fatalf("old refs after completion=%d", oldGen.Refs())
	}
}

func TestGenerationDispatcher_ResponseWriterIdentityPreserved(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	d := runtimehost.NewGenerationDispatcher(m)

	var saw http.ResponseWriter
	var flushed atomic.Bool
	publishPlane(t, m, "rw", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saw = w
		if _, ok := w.(http.Flusher); !ok {
			t.Error("missing Flusher")
		}
		if _, ok := w.(http.Hijacker); !ok {
			t.Error("missing Hijacker")
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
			flushed.Store(true)
		}
		_, _ = io.WriteString(w, "ok")
	}))

	rec := &fullRecorder{ResponseRecorder: httptest.NewRecorder()}
	d.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if saw != http.ResponseWriter(rec) {
		t.Fatalf("ResponseWriter identity changed: got %T", saw)
	}
	if !flushed.Load() {
		t.Fatal("expected Flush through preserved writer")
	}
}

type fullRecorder struct {
	*httptest.ResponseRecorder
}

func (f *fullRecorder) Flush() { f.ResponseRecorder.Flush() }
func (f *fullRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, http.ErrNotSupported
}
func (f *fullRecorder) Push(string, *http.PushOptions) error { return http.ErrNotSupported }
func (f *fullRecorder) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(f.ResponseRecorder, r)
}

func TestGenerationDispatcher_RacePublishDuringServe(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(8, nil)
	d := runtimehost.NewGenerationDispatcher(m)
	publishPlane(t, m, "g1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := runtimehost.BindingFromContext(r.Context())
		_, _ = io.WriteString(w, b.Meta().Label)
	}))

	const workers = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)
	labels := make([]string, workers)
	for i := range workers {
		go func() {
			defer wg.Done()
			<-start
			rr := httptest.NewRecorder()
			d.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
			labels[i] = rr.Body.String()
		}()
	}
	pubDone := make(chan error, 1)
	go func() {
		<-start
		pubDone <- m.Publish(m.PrepareRequestPlane("g2", &testRequestPlane{h: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := runtimehost.BindingFromContext(r.Context())
			_, _ = io.WriteString(w, b.Meta().Label)
		})}))
	}()
	close(start)
	wg.Wait()
	if err := <-pubDone; err != nil {
		t.Fatal(err)
	}
	for _, label := range labels {
		if label != "g1" && label != "g2" {
			t.Fatalf("unexpected label %q", label)
		}
	}
}
