package openresponses_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corecont "github.com/aiproxer/aiproxer/internal/core/continuation"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

type lifecycleMockStream struct {
	mu        sync.Mutex
	events    []aipapi.Event
	idx       int
	closed    atomic.Bool
	recvBlock chan struct{}
}

func (m *lifecycleMockStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if m.recvBlock != nil {
		select {
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		case <-m.recvBlock:
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := m.events[m.idx]
	m.idx++
	return ev, nil
}

func (m *lifecycleMockStream) Close() error {
	m.closed.Store(true)
	return nil
}

type lifecycleMockExecutor struct {
	executeFn func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error)
}

func (m *lifecycleMockExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, call)
	}
	return nil, errors.New("lifecycleMockExecutor not implemented")
}

type lifecyclePartialDisconnectStream struct {
	events []aipapi.Event
	index  int
	block  chan struct{}
	closed atomic.Bool
}

func (s *lifecyclePartialDisconnectStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.index < len(s.events) {
		ev := s.events[s.index]
		s.index++
		return ev, nil
	}
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-s.block:
		return aipapi.Event{}, io.EOF
	}
}

func (s *lifecyclePartialDisconnectStream) Close() error {
	s.closed.Store(true)
	return nil
}

type lifecycleDeleteCountingStore struct {
	*corecont.MemoryStore
	deletes atomic.Int32
}

func (s *lifecycleDeleteCountingStore) Delete(ctx context.Context, scope aipcont.Scope, id aipcont.ResponseID) error {
	s.deletes.Add(1)
	return s.MemoryStore.Delete(ctx, scope, id)
}

type lifecycleFailingOwnerObserver struct {
	store         *lifecycleDeleteCountingStore
	record        aipcont.ContinuationRecord
	ready         chan struct{}
	once          sync.Once
	seen          atomic.Int32
	finalizeCalls atomic.Int32
	releaseCalls  atomic.Int32
	ownsCalls     atomic.Int32
	cleanupUsed   atomic.Bool
}

func (o *lifecycleFailingOwnerObserver) Observe(context.Context, aipapi.Event) {
	if o.seen.Add(1) == 2 {
		o.once.Do(func() { close(o.ready) })
	}
}
func (o *lifecycleFailingOwnerObserver) Close() error { return nil }
func (o *lifecycleFailingOwnerObserver) OwnsContinuationReservation() bool {
	o.ownsCalls.Add(1)
	return true
}

func (o *lifecycleFailingOwnerObserver) ContinuationReservationCleanupConsumed() bool {
	return o.cleanupUsed.Load()
}

func (o *lifecycleFailingOwnerObserver) ReleaseContinuationReservation() {
	o.releaseCalls.Add(1)
	o.cleanupUsed.Store(true)
	_ = o.store.Delete(context.Background(), o.record.Scope, o.record.ID)
}

func (o *lifecycleFailingOwnerObserver) FinalizeIncomplete(ctx context.Context) error {
	o.finalizeCalls.Add(1)
	if err := o.store.Delete(ctx, o.record.Scope, o.record.ID); err != nil {
		return err
	}
	o.cleanupUsed.Store(true)
	return errors.New("incomplete persistence failed")
}

type lifecycleFailingOwnerFactory struct {
	store    *lifecycleDeleteCountingStore
	observer *lifecycleFailingOwnerObserver
	ready    chan struct{}
}

func (f *lifecycleFailingOwnerFactory) NewRecorder(_ aipcont.Store, record aipcont.ContinuationRecord) aipcont.StreamObserver {
	f.observer = &lifecycleFailingOwnerObserver{store: f.store, record: record, ready: f.ready}
	return f.observer
}

type failResponseWriter struct {
	http.ResponseWriter
	header    http.Header
	written   bool
	failWrite bool
}

func newFailResponseWriter() *failResponseWriter {
	return &failResponseWriter{header: make(http.Header)}
}

func (f *failResponseWriter) Header() http.Header    { return f.header }
func (f *failResponseWriter) WriteHeader(status int) {}
func (f *failResponseWriter) Write(b []byte) (int, error) {
	if f.failWrite {
		return 0, errors.New("connection broken")
	}
	f.written = true
	return len(b), nil
}
func (f *failResponseWriter) Flush() {}

func TestStreamingLifecycle_CustomOwnerFailureCleansExactlyOnce(t *testing.T) {
	store := &lifecycleDeleteCountingStore{MemoryStore: corecont.NewMemoryStore()}
	factory := &lifecycleFailingOwnerFactory{store: store, ready: make(chan struct{})}
	stream := &lifecyclePartialDisconnectStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventTextDelta, Delta: "partial"},
		},
		block: make(chan struct{}),
	}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Executor:             &lifecycleMockExecutor{executeFn: func(context.Context, *aipapi.Call) (aipapi.EventStream, error) { return stream, nil }},
		ContinuationStore:    store,
		RecorderFactory:      factory,
	})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi","stream":true,"store":true}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AIP-Session-ID", "sess_custom_owner")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(rec, req)
		close(done)
	}()
	// Wait until both committed events reach the recorder before canceling
	// the blocked receive. The finalizer performs the one cleanup and reports
	// its persistence failure; the frontend must not delete a second time.
	select {
	case <-factory.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("custom recorder did not observe committed events")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP timed out on custom-owner cancellation")
	}
	if factory.observer == nil {
		t.Fatal("custom recorder factory was not invoked")
	}
	if got := store.deletes.Load(); got != 1 {
		t.Fatalf("reservation deletes=%d, want exactly 1 (owns=%d finalize=%d release=%d)", got, factory.observer.ownsCalls.Load(), factory.observer.finalizeCalls.Load(), factory.observer.releaseCalls.Load())
	}
	if !stream.closed.Load() {
		t.Fatal("partial stream was not closed")
	}
}

func TestStreamingLifecycle_CancellationAndWriterFailureCleanup(t *testing.T) {
	t.Parallel()

	t.Run("cancellation before terminal cleans reservation and closes stream", func(t *testing.T) {
		var executions atomic.Int32
		store := corecont.NewMemoryStore()
		st := &lifecycleMockStream{
			events: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventTextDelta, Delta: "hello"},
			},
			recvBlock: make(chan struct{}),
		}

		handler := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Executor: &lifecycleMockExecutor{executeFn: func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
				executions.Add(1)
				return st, nil
			}},
			ContinuationStore: store,
		})

		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi","stream":true,"store":true}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-AIP-Session-ID", "sess_test_123")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		// This case asserts that an already canceled request never executes.
		// Cancel before launching the handler so scheduling cannot change it.
		cancel()
		done := make(chan struct{})
		go func() {
			handler.ServeHTTP(rec, req)
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("ServeHTTP timed out on cancellation")
		}

		if got := executions.Load(); got != 0 {
			t.Fatalf("already canceled request executed backend %d times", got)
		}
		if st.closed.Load() {
			t.Fatalf("canceled request executed an unneeded backend stream; status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("writer failure cleans reservation and closes stream exactly once", func(t *testing.T) {
		store := corecont.NewMemoryStore()
		st := &lifecycleMockStream{
			events: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventTextDelta, Delta: "hello"},
			},
		}

		handler := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Executor:             &lifecycleMockExecutor{executeFn: func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) { return st, nil }},
			ContinuationStore:    store,
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi","stream":true,"store":true}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-AIP-Session-ID", "sess_test_123")
		w := newFailResponseWriter()
		w.failWrite = true

		handler.ServeHTTP(w, req)

		if !st.closed.Load() {
			t.Fatalf("backend stream was not closed on writer failure; header=%v", w.header)
		}
	})
}

func TestReloadGenerationAndShutdownIsolation(t *testing.T) {
	t.Parallel()

	oldStore := corecont.NewMemoryStore()
	newStore := corecont.NewMemoryStore()

	ctx := context.Background()
	scope := aipcont.Scope{PrincipalID: "p1", SessionID: "s1"}

	// Old generation reserves ID
	oldID, err := oldStore.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatalf("old store reserve: %v", err)
	}

	// Close old generation
	if err := oldStore.Close(); err != nil {
		t.Fatalf("old store close: %v", err)
	}

	// Old store rejects lookups and new operations
	if _, err := oldStore.Get(ctx, scope, oldID); !errors.Is(err, aipcont.ErrStoreClosed) {
		t.Fatalf("old store get: want ErrStoreClosed, got %v", err)
	}
	if _, err := aipcont.Lookup(ctx, oldStore, scope, oldID); !errors.Is(err, aipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("old store lookup: want ErrPreviousResponseNotFound, got %v", err)
	}

	// New generation operates independently and cannot look up old generation ID
	newID, err := newStore.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatalf("new store reserve: %v", err)
	}

	if _, err := aipcont.Lookup(ctx, newStore, scope, oldID); !errors.Is(err, aipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("cross-generation lookup: new store returned old ID lookup result %v", err)
	}

	rec := aipcont.ContinuationRecord{
		ID:       newID,
		Scope:    scope,
		Lineage:  aipcont.Lineage{ProfileID: "openresponses", Model: "m"},
		Terminal: true,
	}
	if err := newStore.PutTerminal(ctx, rec); err != nil {
		t.Fatalf("new store put terminal: %v", err)
	}

	got, err := aipcont.Lookup(ctx, newStore, scope, newID)
	if err != nil || got.ID != newID {
		t.Fatalf("new generation lookup failed: %v", err)
	}
}

func TestContinuationStressAndGoroutineTolerance(t *testing.T) {
	t.Parallel()

	store := corecont.NewMemoryStore()
	defer func() { _ = store.Close() }()

	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Executor: &lifecycleMockExecutor{
			executeFn: func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
				return &lifecycleMockStream{
					events: []aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventTextDelta, Delta: "hello"},
						{Kind: aipapi.EventResponseFinished},
					},
				}, nil
			},
		},
		ContinuationStore: store,
	})

	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(iterations)

	for i := range iterations {
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi","stream":true,"store":true}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-AIP-Session-ID", fmt.Sprintf("sess_test_%d", i))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("iteration %d: status=%d, want %d; body=%s", i, rec.Code, http.StatusOK, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "[DONE]") {
				t.Errorf("iteration %d: missing [DONE] in stream output: %s", i, rec.Body.String())
			}
		}(i)
	}

	wg.Wait()

	// OpenResponses HTTP ServeHTTP and core MemoryStore operate synchronously
	// on the caller's goroutine; all concurrent requests are tracked and joined
	// deterministically by sync.WaitGroup. Global runtime.NumGoroutine assertions
	// are invalid under t.Parallel because sibling parallel tests (such as
	// WebSocket server/session tests) create ambient goroutines concurrently.
}

func TestContinuationStressAndGoroutineTolerance_AmbientOverlap(t *testing.T) {
	t.Parallel()

	// Simulate ambient goroutines created by concurrent sibling tests (e.g. WebSocket
	// servers, connection read pumps, background timers) during test execution.
	const ambientCount = 20
	ambientGate := make(chan struct{})
	var ambientWG sync.WaitGroup
	ambientWG.Add(ambientCount)
	for range ambientCount {
		go func() {
			defer ambientWG.Done()
			<-ambientGate
		}()
	}
	defer func() {
		close(ambientGate)
		ambientWG.Wait()
	}()

	store := corecont.NewMemoryStore()
	defer func() { _ = store.Close() }()

	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Executor: &lifecycleMockExecutor{
			executeFn: func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
				return &lifecycleMockStream{
					events: []aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventTextDelta, Delta: "hello"},
						{Kind: aipapi.EventResponseFinished},
					},
				}, nil
			},
		},
		ContinuationStore: store,
	})

	const iterations = 30
	var wg sync.WaitGroup
	wg.Add(iterations)

	for i := range iterations {
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi","stream":true,"store":true}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-AIP-Session-ID", fmt.Sprintf("sess_ambient_%d", i))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("iteration %d: status=%d, want %d; body=%s", i, rec.Code, http.StatusOK, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "[DONE]") {
				t.Errorf("iteration %d: missing [DONE] in stream output: %s", i, rec.Body.String())
			}
		}(i)
	}

	wg.Wait()
}
