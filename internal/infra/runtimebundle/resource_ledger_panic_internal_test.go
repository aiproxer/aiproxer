package runtimebundle

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// breakLedgerPhaseEntry breaks an internal ResourceLedger invariant so the phase
// machinery itself unwinds. safeLedgerStart already converts lifecycle hook
// panics into bounded start errors, so a hook panic cannot exercise the
// machinery path these regressions cover.
func breakLedgerPhaseEntry(t *testing.T, l *ResourceLedger) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ensureCond()
	l.entries = append(l.entries, nil)
}

// dropBrokenLedgerEntry restores the invariant broken by breakLedgerPhaseEntry
// while keeping the real entries, so later lifecycle calls still exercise
// cleanup of resources whose starts were already attempted.
func dropBrokenLedgerEntry(t *testing.T, l *ResourceLedger) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.entries[:0]
	for _, e := range l.entries {
		if e != nil {
			kept = append(kept, e)
		}
	}
	l.entries = kept
}

func ledgerPhaseFlags(l *ResourceLedger) (preparing, activating, publishing, prepareDone, activateDone, publishDone bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.preparing, l.activating, l.publishing, l.prepareDone, l.activateDone, l.publishDone
}

// runLedgerPhaseExpectingPanic fails when an unexpected internal panic surfaces
// as an ordinary result. The precommit result is reported so a swallowed panic
// that returns nil is distinguishable from a real bounded error.
func runLedgerPhaseExpectingPanic(t *testing.T, name string, phase func() error) {
	t.Helper()
	var (
		recovered any
		result    error
	)
	func() {
		defer func() { recovered = recover() }()
		result = phase()
	}()
	if recovered == nil {
		t.Fatalf("%s must propagate its unexpected internal panic, got result %v", name, result)
	}
}

// runLedgerPhaseWithin bounds a call that would otherwise hang the binary when a
// phase flag stays set after an unexpected internal panic.
func runLedgerPhaseWithin(t *testing.T, name string, phase func(context.Context) error) error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- phase(context.Background()) }()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second): // deadlock guard only; correctness comes from the phase flags below.
		t.Fatalf("%s blocked after an unexpected internal panic", name)
		return nil
	}
}

func TestResourceLedger_UnexpectedPreparePanicStaysObservableAndReleasesPhase(t *testing.T) {
	l := NewResourceLedger()
	breakLedgerPhaseEntry(t, l)

	runLedgerPhaseExpectingPanic(t, "Prepare", func() error { return l.Prepare(context.Background()) })

	preparing, _, _, prepareDone, _, _ := ledgerPhaseFlags(l)
	if preparing {
		t.Fatal("a recovered Prepare panic must clear preparing; a stranded flag blocks every later lifecycle call")
	}
	if prepareDone {
		t.Fatal("an unexpected internal panic must not be cached as a completed Prepare result")
	}
	if l.prepared.Load() {
		t.Fatal("an unexpected internal panic must not mark the ledger prepared")
	}

	dropBrokenLedgerEntry(t, l)
	if err := runLedgerPhaseWithin(t, "Prepare retry", l.Prepare); err != nil {
		t.Fatalf("Prepare after a recovered internal panic: %v", err)
	}
	if err := runLedgerPhaseWithin(t, "Quiesce", l.Quiesce); err != nil {
		t.Fatalf("Quiesce after a recovered Prepare panic: %v", err)
	}
	if err := runLedgerPhaseWithin(t, "Close", l.Close); err != nil {
		t.Fatalf("Close after a recovered Prepare panic: %v", err)
	}
}

func TestResourceLedger_UnexpectedActivatePanicStaysObservableAndReleasesPhase(t *testing.T) {
	l := NewResourceLedger()
	breakLedgerPhaseEntry(t, l)

	runLedgerPhaseExpectingPanic(t, "Activate", func() error { return l.Activate(context.Background()) })

	_, activating, _, _, activateDone, _ := ledgerPhaseFlags(l)
	if activating {
		t.Fatal("a recovered Activate panic must clear activating; a stranded flag blocks every later lifecycle call")
	}
	if activateDone {
		t.Fatal("an unexpected internal panic must not be cached as a completed Activate result")
	}

	dropBrokenLedgerEntry(t, l)
	if err := runLedgerPhaseWithin(t, "Activate retry", l.Activate); err != nil {
		t.Fatalf("Activate after a recovered internal panic: %v", err)
	}
	if err := runLedgerPhaseWithin(t, "Quiesce", l.Quiesce); err != nil {
		t.Fatalf("Quiesce after a recovered Activate panic: %v", err)
	}
	if err := runLedgerPhaseWithin(t, "Close", l.Close); err != nil {
		t.Fatalf("Close after a recovered Activate panic: %v", err)
	}
}

// TestResourceLedger_UnexpectedPublishPanicBecomesBoundedCachedError covers the
// post-commit phase: the generation is already the active request plane, so an
// unexpected internal panic must become the bounded publish-start error, cache
// completion, never retry an already attempted start, and leave Quiesce/Close
// usable.
func TestResourceLedger_UnexpectedPublishPanicBecomesBoundedCachedError(t *testing.T) {
	l := NewResourceLedger()
	var starts, stops atomic.Int32
	l.AddAction("publish-worker", PhasePublish,
		func(context.Context) error {
			starts.Add(1)
			return nil
		},
		func(context.Context) error {
			stops.Add(1)
			return nil
		})
	breakLedgerPhaseEntry(t, l)

	first := l.Publish(context.Background())
	if first == nil || !strings.Contains(first.Error(), "start failed") {
		t.Fatalf("unexpected internal Publish panic must become the bounded publish-start error: %v", first)
	}
	_, _, publishing, _, _, publishDone := ledgerPhaseFlags(l)
	if publishing || !publishDone {
		t.Fatalf("Publish panic must cache completion and clear publishing: publishing=%v publishDone=%v", publishing, publishDone)
	}
	second := l.Publish(context.Background())
	if second == nil || second.Error() != first.Error() {
		t.Fatalf("Publish must not retry or change the cached outcome: first=%v second=%v", first, second)
	}
	if starts.Load() != 1 {
		t.Fatalf("partially attempted publish starts=%d want no retry after the panic", starts.Load())
	}

	dropBrokenLedgerEntry(t, l)
	if err := runLedgerPhaseWithin(t, "Quiesce", l.Quiesce); err != nil {
		t.Fatalf("Quiesce after a failed publication: %v", err)
	}
	if err := runLedgerPhaseWithin(t, "Close", l.Close); err != nil {
		t.Fatalf("Close after a failed publication: %v", err)
	}
	if stops.Load() != 1 {
		t.Fatalf("attempted publish resource cleanup calls=%d want 1", stops.Load())
	}
}
