package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
)

// lifecycleSnapshot is an immutable copy of tracker counters for assertions.
type lifecycleSnapshot struct {
	starts map[string]int
	closes map[string]int
	order  []string
}

// lifecycleTracker records owned-resource start/close order for exactly-once
// and reverse-order certification. It is safe for concurrent use.
type lifecycleTracker struct {
	mu       sync.Mutex
	starts   map[string]int
	closes   map[string]int
	order    []string
	worker   *workerResource
	borrowed int
}

func (t *lifecycleTracker) recordStart(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.starts == nil {
		t.starts = map[string]int{}
	}
	t.starts[id]++
	t.order = append(t.order, "start-"+id)
}

func (t *lifecycleTracker) recordClose(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closes == nil {
		t.closes = map[string]int{}
	}
	t.closes[id]++
	t.order = append(t.order, "close-"+id)
}

func (t *lifecycleTracker) snapshot() lifecycleSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := lifecycleSnapshot{
		starts: map[string]int{},
		closes: map[string]int{},
		order:  append([]string(nil), t.order...),
	}
	maps.Copy(out.starts, t.starts)
	maps.Copy(out.closes, t.closes)
	return out
}

func (t *lifecycleTracker) totalStarts() int {
	snap := t.snapshot()
	total := 0
	for _, n := range snap.starts {
		total += n
	}
	return total
}

func (t *lifecycleTracker) totalCloses() int {
	snap := t.snapshot()
	total := 0
	for _, n := range snap.closes {
		total += n
	}
	return total
}

// Worker returns the goroutine worker assembled with the binding, if any.
func (t *lifecycleTracker) Worker() *workerResource {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.worker
}

// borrowedCount returns the count of declared borrowed handles. Borrowed
// handles are declaration-only values with no close entry point.
func (t *lifecycleTracker) borrowedCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.borrowed
}

// failingOwned builds an owned resource whose Start always fails, for unwind
// certification. It records through the tracker like any owned resource.
func (t *lifecycleTracker) failingOwned(id string) billing.OwnedResource {
	return billing.OwnedResource{
		ID:    id,
		Start: func(context.Context) error { t.recordStart(id); return errors.New("external test: owned start failed") },
		Close: func(context.Context) error { t.recordClose(id); return nil },
	}
}

// workerResource is a binding-owned background worker. Start launches exactly
// one goroutine that drains the job queue; Close shuts the queue and joins
// the goroutine, so shutdown is observed deterministically without sleeps.
type workerResource struct {
	id      string
	tracker *lifecycleTracker

	mu        sync.Mutex
	jobs      chan string
	results   chan string
	exited    chan struct{}
	processed []string
	closed    bool
}

func (w *workerResource) Start(context.Context) error {
	w.tracker.recordStart(w.id)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.jobs = make(chan string, 16)
	w.results = make(chan string, 16)
	w.exited = make(chan struct{})
	go w.loop()
	return nil
}

func (w *workerResource) loop() {
	defer close(w.exited)
	for job := range w.jobs {
		w.mu.Lock()
		w.processed = append(w.processed, job)
		w.mu.Unlock()
		w.results <- job
	}
}

func (w *workerResource) Close(context.Context) error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.jobs)
	}
	exited := w.exited
	w.mu.Unlock()
	<-exited
	w.tracker.recordClose(w.id)
	return nil
}

// Submit enqueues one unit of work. It must be called after Start.
func (w *workerResource) Submit(job string) {
	w.mu.Lock()
	jobs := w.jobs
	w.mu.Unlock()
	jobs <- job
}

// WaitProcessed blocks until n results arrive and returns the count.
func (w *workerResource) WaitProcessed(t interface{ Fatalf(string, ...any) }, n int) int {
	got := 0
	for range n {
		w.mu.Lock()
		results := w.results
		w.mu.Unlock()
		<-results
		got++
	}
	return got
}

// Processed returns the count of drained jobs.
func (w *workerResource) Processed() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.processed)
}

// Exited is closed when the worker goroutine has terminated.
func (w *workerResource) Exited() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.exited
}

// OfferFixture is one assembled external offer: the validated binding plus
// its dedicated port implementations and lifecycle tracker. Ports are fresh
// per fixture so call counters stay deterministic under parallel tests.
type OfferFixture struct {
	Binding   billing.Binding
	Screen    *offerScreen
	Quoter    *offerQuoter
	Rater     *offerRater
	Admission *offerAdmission
	Terminal  *terminalLog
	Tracker   *lifecycleTracker
	Worker    *workerResource
}

// AssembleBinding builds the certified external offer binding: a custom
// per-submission/credit customer offer with synthetic non-token passthrough,
// a goroutine worker plus a plain owned resource, and one borrowed handle.
// The binding is validated before return; the tracker exposes the worker.
func AssembleBinding(tracker *lifecycleTracker) (*OfferFixture, error) {
	if tracker == nil {
		tracker = &lifecycleTracker{}
	}
	worker := &workerResource{id: "worker-test", tracker: tracker}
	plain := billing.OwnedResource{
		ID:    "worker-plain",
		Start: func(context.Context) error { tracker.recordStart("worker-plain"); return nil },
		Close: func(context.Context) error { tracker.recordClose("worker-plain"); return nil },
	}
	fix := &OfferFixture{
		Screen:    &offerScreen{allowedAccount: testAccount},
		Quoter:    &offerQuoter{},
		Rater:     &offerRater{},
		Admission: &offerAdmission{},
		Terminal:  &terminalLog{},
		Tracker:   tracker,
		Worker:    worker,
	}
	bound := billing.Binding{
		ID:           "external-offer-test",
		Version:      billing.BindingVersionV1,
		Scope:        offerScope(),
		CreditScreen: fix.Screen,
		Quoter:       fix.Quoter,
		Admission:    fix.Admission,
		Terminal:     fix.Terminal,
		Lifecycle: billing.Lifecycle{
			Owned: []billing.OwnedResource{
				{ID: worker.id, Start: worker.Start, Close: worker.Close},
				plain,
			},
			Borrowed: []billing.BorrowedRef{{ID: "store-shared-journal", Kind: "journal"}},
		},
	}
	if err := bound.Validate(); err != nil {
		return nil, fmt.Errorf("assembled binding invalid: %w", err)
	}
	fix.Binding = bound
	tracker.mu.Lock()
	tracker.worker = worker
	tracker.borrowed = len(bound.Lifecycle.Borrowed)
	tracker.mu.Unlock()
	return fix, nil
}
