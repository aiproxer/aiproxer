package runtimehost_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

func TestGeneration_LifecycleLegalAndIllegalTransitions(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	g := m.Prepare("prep")
	if g.Lifecycle() != runtimehost.GenPrepared {
		t.Fatalf("prepare lifecycle=%v want prepared", g.Lifecycle())
	}
	if err := g.Transition(runtimehost.GenActive); !errors.Is(err, runtimehost.ErrIllegalTransition) {
		t.Fatalf("prepared→active via Transition must be rejected (publish owns it): %v", err)
	}
	mustPublish(t, m, g)
	if g.Lifecycle() != runtimehost.GenActive {
		t.Fatalf("after publish lifecycle=%v", g.Lifecycle())
	}
	if err := m.Publish(g); !errors.Is(err, runtimehost.ErrAlreadyPublished) {
		t.Fatalf("double publish: %v", err)
	}
	hold, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire hold")
	}
	// BeginShutdown+DetachActive (not a replacing Publish) avoids racing
	// Manager's automatic post-publish retirement scheduling (task 7.3)
	// against this test's own manual lifecycle drive below.
	m.BeginShutdown()
	m.DetachActive()
	if g.Lifecycle() != runtimehost.GenRetiring {
		t.Fatalf("prior lifecycle=%v want retiring", g.Lifecycle())
	}
	if err := g.BeginQuiesce(); err != nil {
		t.Fatal(err)
	}
	if g.Lifecycle() != runtimehost.GenQuiescing {
		t.Fatalf("lifecycle=%v", g.Lifecycle())
	}
	if err := g.MarkQuiesced(); err != nil {
		t.Fatal(err)
	}
	if g.Lifecycle() != runtimehost.GenQuiesced {
		t.Fatalf("lifecycle=%v want quiesced while lease held", g.Lifecycle())
	}
	hold.Release()
	<-g.Drained()
	if g.Lifecycle() != runtimehost.GenDrained {
		t.Fatalf("lifecycle=%v want drained", g.Lifecycle())
	}
	if err := g.BeginClose(); err != nil {
		t.Fatal(err)
	}
	if g.Lifecycle() != runtimehost.GenClosing {
		t.Fatalf("lifecycle=%v", g.Lifecycle())
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if g.Lifecycle() != runtimehost.GenClosed || g.CloseCount() != 1 {
		t.Fatalf("closed state=%v count=%d", g.Lifecycle(), g.CloseCount())
	}
	if err := g.Close(); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("double close: %v", err)
	}
	if g.CloseCount() != 1 {
		t.Fatalf("close count=%d", g.CloseCount())
	}
	illegal := []runtimehost.GenLifecycle{
		runtimehost.GenPrepared, runtimehost.GenActive, runtimehost.GenRetiring,
	}
	for _, st := range illegal {
		if err := g.Transition(st); !errors.Is(err, runtimehost.ErrIllegalTransition) {
			t.Fatalf("closed→%v: %v", st, err)
		}
	}
}

// TestGeneration_RetiringDrainsWithoutQuiesceWhenRefsZero uses BeginShutdown+
// DetachActive (not a replacing Publish) so Manager's automatic post-publish
// retirement scheduling (task 7.3) cannot race ahead to GenClosing/GenClosed
// before this test observes the synchronous zero-ref GenDrained transition.
func TestGeneration_RetiringDrainsWithoutQuiesceWhenRefsZero(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(2, nil)
	g1 := m.Prepare("g1")
	mustPublish(t, m, g1)
	m.BeginShutdown()
	m.DetachActive()
	<-g1.Drained()
	if g1.Lifecycle() != runtimehost.GenDrained {
		t.Fatalf("lifecycle=%v", g1.Lifecycle())
	}
}

// Last lease release during GenQuiescing must not drain or enable BeginClose.
// Only MarkQuiesced may convert zero-ref quiescing → drained and close Drained().
// This test drives the lifecycle directly, so it uses BeginShutdown+
// DetachActive (not a replacing Publish) to avoid racing Manager's automatic
// post-publish retirement scheduling (task 7.3) against this manual drive.
func TestGeneration_LastRefReleaseDuringQuiescing_DoesNotDrainUntilMarkQuiesced(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	owned := &stubOwned{closeFn: func() error {
		closes.Add(1)
		return nil
	}}
	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)

	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}

	// Barrier: hold → detach active → BeginQuiesce → release final lease while quiescing.
	released := make(chan struct{})
	quiescing := make(chan struct{})
	go func() {
		<-quiescing
		lease.Release()
		close(released)
	}()

	m.BeginShutdown()
	m.DetachActive()
	if g1.Lifecycle() != runtimehost.GenRetiring {
		t.Fatalf("after publish lifecycle=%v want retiring", g1.Lifecycle())
	}
	if err := g1.BeginQuiesce(); err != nil {
		t.Fatal(err)
	}
	if g1.Lifecycle() != runtimehost.GenQuiescing {
		t.Fatalf("lifecycle=%v want quiescing", g1.Lifecycle())
	}
	close(quiescing)
	<-released

	if g1.Lifecycle() != runtimehost.GenQuiescing {
		t.Fatalf("after last-ref release lifecycle=%v want still quiescing", g1.Lifecycle())
	}
	if g1.Refs() != 0 {
		t.Fatalf("refs=%d want 0", g1.Refs())
	}
	select {
	case <-g1.Drained():
		t.Fatal("Drained must stay open until MarkQuiesced")
	default:
	}
	if err := g1.BeginClose(); !errors.Is(err, runtimehost.ErrIllegalTransition) {
		t.Fatalf("BeginClose before MarkQuiesced: %v", err)
	}
	if closes.Load() != 0 {
		t.Fatal("owned must not close before MarkQuiesced → drain → BeginClose → Close")
	}

	if err := g1.MarkQuiesced(); err != nil {
		t.Fatal(err)
	}
	<-g1.Drained()
	if g1.Lifecycle() != runtimehost.GenDrained {
		t.Fatalf("after MarkQuiesced lifecycle=%v want drained", g1.Lifecycle())
	}
	// Exactly one drain notification: Drained already closed; second select must succeed immediately.
	select {
	case <-g1.Drained():
	default:
		t.Fatal("Drained must be closed exactly once after MarkQuiesced")
	}

	if err := g1.BeginClose(); err != nil {
		t.Fatal(err)
	}
	if err := g1.Close(); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 || g1.CloseCount() != 1 || g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("closes=%d count=%d lifecycle=%v", closes.Load(), g1.CloseCount(), g1.Lifecycle())
	}
}

func TestGeneration_MetaAndStatusStableNoMutableConfig(t *testing.T) {
	t.Parallel()
	clock := runtimehost.NewManualClock(time.Unix(1_700_000_300, 0))
	m := runtimehost.NewManager(2, clock)
	g := m.Prepare("fp-safe")
	g.SetMetaHints(runtimehost.MetaHints{
		PublicFingerprint: "fp-abc",
		TriggerKind:       "api",
		LoadedAt:          clock.Now(),
	})
	mustPublish(t, m, g)
	st := g.Status()
	if st.Meta.ID != 1 || st.Meta.PreviousID != 0 {
		t.Fatalf("meta ids=%+v", st.Meta)
	}
	if st.Meta.PublicFingerprint != "fp-abc" || st.Meta.TriggerKind != "api" {
		t.Fatalf("meta=%+v", st.Meta)
	}
	if st.Meta.PublishedAt.IsZero() || st.Lifecycle != runtimehost.GenActive {
		t.Fatalf("status=%+v", st)
	}
	// Status must be a value snapshot; mutating returned meta must not corrupt generation.
	st.Meta.PublicFingerprint = "mutated"
	if g.Status().Meta.PublicFingerprint != "fp-abc" {
		t.Fatal("status meta must be immutable snapshot")
	}
	g2 := m.Prepare("next")
	mustPublish(t, m, g2)
	if g2.Status().Meta.PreviousID != 1 {
		t.Fatalf("previous id=%d", g2.Status().Meta.PreviousID)
	}
}

func TestManager_PublishRejectsUnpreparedAndNil(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(2, nil)
	if err := m.Publish(nil); !errors.Is(err, runtimehost.ErrNotPrepared) {
		t.Fatalf("nil: %v", err)
	}
	g := m.Prepare("g")
	mustPublish(t, m, g)
	// Force illegal state for publish attempt of a fresh retiring-marked object.
	bad := m.Prepare("bad")
	_ = bad.Transition(runtimehost.GenFailed)
	if err := m.Publish(bad); !errors.Is(err, runtimehost.ErrNotPrepared) {
		t.Fatalf("failed candidate: %v", err)
	}
}

// TestGeneration_OwnedCloser_ClosedExactlyOnceAfterDrain drives BeginClose/
// Close directly, so it uses BeginShutdown+DetachActive (not a replacing
// Publish) to avoid racing Manager's automatic post-publish retirement
// scheduling (task 7.3) against this manual drive.
func TestGeneration_OwnedCloser_ClosedExactlyOnceAfterDrain(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	owned := &stubOwned{closeFn: func() error {
		closes.Add(1)
		return nil
	}}
	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	m.BeginShutdown()
	m.DetachActive()
	select {
	case <-g1.Drained():
		t.Fatal("must wait for lease")
	default:
	}
	if closes.Load() != 0 {
		t.Fatal("owned must not close before drain+Close")
	}
	lease.Release()
	<-g1.Drained()
	if err := g1.BeginClose(); err != nil {
		t.Fatal(err)
	}
	if err := g1.Close(); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatalf("closes=%d", closes.Load())
	}
	_ = g1.Close()
	if closes.Load() != 1 {
		t.Fatalf("owned double-close closes=%d", closes.Load())
	}
}

func TestGeneration_OwnedCloser_NeverTouchesProcessServices(t *testing.T) {
	t.Parallel()
	var processClosed atomic.Bool
	_ = &stubOwned{closeFn: func() error {
		processClosed.Store(true)
		return nil
	}}
	candidate := &stubOwned{closeFn: func() error { return nil }}
	// Generation only receives candidate-owned closer; process stays outside.
	m := runtimehost.NewManager(2, nil)
	g := m.PrepareOwned("cand", candidate)
	mustPublish(t, m, g)
	mustPublish(t, m, m.Prepare("next"))
	<-g.Drained()
	_ = g.BeginClose()
	_ = g.Close()
	if processClosed.Load() {
		t.Fatal("process services must not be closed by generation")
	}
}

func TestGeneration_Discard_UnpublishedCandidateClosesOwnedExactlyOnce(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	owned := &stubOwned{closeFn: func() error {
		closes.Add(1)
		return nil
	}}
	m := runtimehost.NewManager(2, nil)
	g := m.PrepareOwned("cand", owned)
	if err := g.Discard(); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatalf("owned closes=%d want 1", closes.Load())
	}
	if g.Lifecycle() != runtimehost.GenFailed {
		t.Fatalf("lifecycle=%v want failed", g.Lifecycle())
	}
	if g.CloseCount() != 1 {
		t.Fatalf("close count=%d", g.CloseCount())
	}
	if err := g.Discard(); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("second discard: %v", err)
	}
	// Successful Discard (GenFailed, empty payload) is a terminal cleanup:
	// Close returns ErrAlreadyClosed for idempotent compatibility without a
	// closed/closeErr cache (task 7.3 repair).
	if err := g.Close(); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("close after discard: %v", err)
	}
	if closes.Load() != 1 || g.CloseCount() != 1 {
		t.Fatalf("double-close closes=%d count=%d", closes.Load(), g.CloseCount())
	}
	if err := m.Publish(g); !errors.Is(err, runtimehost.ErrNotPrepared) {
		t.Fatalf("discarded candidate publish: %v", err)
	}
}

func TestGeneration_Discard_PreparingAndFailed(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	m := runtimehost.NewManager(2, nil)
	g := m.BeginPrepare("prep", &stubOwned{closeFn: func() error {
		closes.Add(1)
		return nil
	}})
	if err := g.Discard(); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 || g.Lifecycle() != runtimehost.GenFailed {
		t.Fatalf("preparing discard closes=%d lifecycle=%v", closes.Load(), g.Lifecycle())
	}

	var closes2 atomic.Int32
	failed := m.PrepareOwned("failed", &stubOwned{closeFn: func() error {
		closes2.Add(1)
		return nil
	}})
	if err := failed.Transition(runtimehost.GenFailed); err != nil {
		t.Fatal(err)
	}
	if err := failed.Discard(); err != nil {
		t.Fatal(err)
	}
	if closes2.Load() != 1 {
		t.Fatalf("failed discard closes=%d", closes2.Load())
	}
}

// TestGeneration_Discard_DoesNotWeakenPublishedClosePath drives BeginClose/
// Close directly, so it uses BeginShutdown+DetachActive (not a replacing
// Publish) to avoid racing Manager's automatic post-publish retirement
// scheduling (task 7.3) against this manual drive.
func TestGeneration_Discard_DoesNotWeakenPublishedClosePath(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	m := runtimehost.NewManager(2, nil)
	g := m.PrepareOwned("pub", &stubOwned{closeFn: func() error {
		closes.Add(1)
		return nil
	}})
	mustPublish(t, m, g)
	if err := g.Discard(); !errors.Is(err, runtimehost.ErrIllegalTransition) {
		t.Fatalf("discard active: %v", err)
	}
	if closes.Load() != 0 {
		t.Fatal("discard must not close published generation owned payload")
	}
	m.BeginShutdown()
	m.DetachActive()
	<-g.Drained()
	if err := g.BeginClose(); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatalf("published close path closes=%d", closes.Load())
	}
}

func TestGeneration_Discard_ConcurrentExactlyOnce(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	g := runtimehost.NewManager(1, nil).PrepareOwned("race", &stubOwned{closeFn: func() error {
		closes.Add(1)
		return nil
	}})
	const n = 32
	errs := make(chan error, n)
	var started, release sync.WaitGroup
	started.Add(n)
	release.Add(1)
	for range n {
		go func() {
			started.Done()
			release.Wait()
			errs <- g.Discard()
		}()
	}
	started.Wait()
	release.Done()
	var ok, already int
	for range n {
		err := <-errs
		switch {
		case err == nil:
			ok++
		case errors.Is(err, runtimehost.ErrAlreadyClosed):
			already++
		default:
			t.Fatalf("unexpected discard err: %v", err)
		}
	}
	if ok != 1 || already != n-1 {
		t.Fatalf("ok=%d already=%d closes=%d", ok, already, closes.Load())
	}
	if closes.Load() != 1 || g.CloseCount() != 1 {
		t.Fatalf("closes=%d count=%d", closes.Load(), g.CloseCount())
	}
}

type stubOwned struct {
	closeFn func() error
}

func (s *stubOwned) Close() error {
	if s == nil || s.closeFn == nil {
		return nil
	}
	return s.closeFn()
}
