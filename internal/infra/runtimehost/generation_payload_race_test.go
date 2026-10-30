package runtimehost

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Task 7.3 repair: payload binding must share the same synchronization
// boundary as publish/discard lifecycle transition + payload claim. Tests use
// channel/lock barriers (no sleeps) and may deliberately hold payloadMu.

type raceCloser struct {
	closed atomic.Int32
	err    error
}

func (c *raceCloser) Close() error {
	c.closed.Add(1)
	return c.err
}

type failPlane struct {
	err      error
	closed   atomic.Int32
	quiesced atomic.Int32
}

func (*failPlane) Handler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}

func (p *failPlane) Quiesce(context.Context) error {
	p.quiesced.Add(1)
	return nil
}

func (p *failPlane) Close() error {
	if p.err != nil {
		return p.err
	}
	p.closed.Add(1)
	return nil
}

func TestAttachOwned_VersusAssignPublish_NoLateBinding(t *testing.T) {
	t.Parallel()

	t.Run("attach_wins_before_publish", func(t *testing.T) {
		t.Parallel()
		g := NewManager(1, nil).BeginPrepare("cand", nil)
		if err := g.Transition(GenPrepared); err != nil {
			t.Fatal(err)
		}
		held := make(chan struct{})
		release := make(chan struct{})
		var attachErr, pubErr error
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			g.payloadMu.Lock()
			close(held)
			<-release
			st := g.Lifecycle()
			if st != GenPrepared {
				attachErr = errors.New("lifecycle changed under payloadMu before bind")
				g.payloadMu.Unlock()
				return
			}
			g.owned = &raceCloser{}
			g.payloadMu.Unlock()
		}()

		go func() {
			defer wg.Done()
			<-held
			done := make(chan error, 1)
			go func() { done <- g.assignPublish(1, 0, time.Unix(1, 0).UTC()) }()
			select {
			case pubErr = <-done:
				attachErr = errors.New("assignPublish completed while payloadMu held")
			default:
			}
			close(release)
			pubErr = <-done
		}()

		wg.Wait()
		if attachErr != nil {
			t.Fatal(attachErr)
		}
		if pubErr != nil {
			t.Fatalf("publish after attach: %v", pubErr)
		}
		if g.Lifecycle() != GenActive {
			t.Fatalf("lifecycle=%v want active", g.Lifecycle())
		}
		g.payloadMu.Lock()
		owned := g.owned
		g.payloadMu.Unlock()
		if owned == nil {
			t.Fatal("attach before publish must leave owned bound")
		}
	})

	t.Run("publish_wins_attach_loses", func(t *testing.T) {
		t.Parallel()
		g := NewManager(1, nil).BeginPrepare("cand", nil)
		if err := g.Transition(GenPrepared); err != nil {
			t.Fatal(err)
		}
		if err := g.assignPublish(1, 0, time.Unix(1, 0).UTC()); err != nil {
			t.Fatal(err)
		}
		if err := g.AttachOwned(&raceCloser{}); !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("late AttachOwned: %v", err)
		}
		g.payloadMu.Lock()
		defer g.payloadMu.Unlock()
		if g.owned != nil || g.requestPlane != nil {
			t.Fatal("late attach must not commit payload after publish")
		}
	})

	t.Run("concurrent_attach_or_publish_never_late", func(t *testing.T) {
		t.Parallel()
		const rounds = 64
		for range rounds {
			g := NewManager(1, nil).BeginPrepare("cand", nil)
			if err := g.Transition(GenPrepared); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			var attachErr, pubErr error
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				attachErr = g.AttachOwned(&raceCloser{})
			}()
			go func() {
				defer wg.Done()
				<-start
				pubErr = g.assignPublish(1, 0, time.Unix(1, 0).UTC())
			}()
			close(start)
			wg.Wait()

			g.payloadMu.Lock()
			owned := g.owned
			plane := g.requestPlane
			g.payloadMu.Unlock()
			st := g.Lifecycle()

			switch {
			case pubErr == nil && st == GenActive:
				if attachErr == nil {
					if owned == nil {
						t.Fatal("successful attach before publish left owned nil")
					}
				} else if !errors.Is(attachErr, ErrIllegalTransition) {
					t.Fatalf("attach err=%v", attachErr)
				} else if owned != nil || plane != nil {
					t.Fatal("losing attach committed payload after publish")
				}
			default:
				if pubErr != nil {
					t.Fatalf("publish err=%v attach=%v st=%v", pubErr, attachErr, st)
				}
			}
		}
	})
}

func TestAttachRequestPlane_VersusDiscard_NoFailedLeak(t *testing.T) {
	t.Parallel()

	t.Run("discard_claims_or_attach_loses", func(t *testing.T) {
		t.Parallel()
		const rounds = 64
		for range rounds {
			g := NewManager(1, nil).BeginPrepare("cand", nil)
			plane := &attachTestPlane{}
			start := make(chan struct{})
			var attachErr, discardErr error
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				attachErr = g.AttachRequestPlane(plane)
			}()
			go func() {
				defer wg.Done()
				<-start
				discardErr = g.Discard()
			}()
			close(start)
			wg.Wait()

			g.payloadMu.Lock()
			owned := g.owned
			rp := g.requestPlane
			g.payloadMu.Unlock()

			switch {
			case attachErr == nil && discardErr == nil:
				if plane.closed.Load() != 1 {
					t.Fatalf("discard must close attached plane; closed=%d", plane.closed.Load())
				}
				if owned != nil || rp != nil {
					t.Fatal("successful discard must clear requestPlane+owned")
				}
			case errors.Is(attachErr, ErrIllegalTransition) && discardErr == nil:
				if plane.closed.Load() != 0 {
					t.Fatal("losing attach's plane must not be closed (never bound)")
				}
				if owned != nil || rp != nil {
					t.Fatal("discard of empty payload must leave pair nil")
				}
				if g.Lifecycle() != GenFailed {
					t.Fatalf("lifecycle=%v", g.Lifecycle())
				}
			default:
				t.Fatalf("attach=%v discard=%v owned=%v plane=%v closed=%d",
					attachErr, discardErr, owned != nil, rp != nil, plane.closed.Load())
			}
		}
	})

	t.Run("discard_blocked_on_payloadMu_cannot_miss_late_bind", func(t *testing.T) {
		t.Parallel()
		g := NewManager(1, nil).BeginPrepare("cand", nil)
		plane := &attachTestPlane{}

		g.payloadMu.Lock()
		discardDone := make(chan error, 1)
		go func() { discardDone <- g.Discard() }()

		select {
		case err := <-discardDone:
			g.payloadMu.Unlock()
			t.Fatalf("Discard completed while payloadMu held: %v", err)
		default:
		}

		if st := g.Lifecycle(); st != GenPreparing && st != GenPrepared {
			g.payloadMu.Unlock()
			t.Fatalf("lifecycle moved without payloadMu: %v", st)
		}
		g.requestPlane = plane
		g.owned = plane
		g.payloadMu.Unlock()

		if err := <-discardDone; err != nil {
			t.Fatalf("Discard: %v", err)
		}
		if plane.closed.Load() != 1 {
			t.Fatalf("Discard must claim late-bound plane; closed=%d", plane.closed.Load())
		}
		g.payloadMu.Lock()
		defer g.payloadMu.Unlock()
		if g.owned != nil || g.requestPlane != nil {
			t.Fatal("pair must be cleared after discard claim")
		}
	})
}

func TestDiscard_FailedRestorePair_ThenRetryClearsBoth(t *testing.T) {
	t.Parallel()
	g := NewManager(1, nil).BeginPrepare("cand", nil)
	failing := &failPlane{err: errors.New("close failed")}
	if err := g.AttachRequestPlane(failing); err != nil {
		t.Fatal(err)
	}

	if err := g.Discard(); err == nil || err.Error() != "close failed" {
		t.Fatalf("first Discard: %v", err)
	}
	g.payloadMu.Lock()
	if g.owned != failing || g.requestPlane != failing {
		g.payloadMu.Unlock()
		t.Fatal("failed Discard must restore requestPlane+owned exactly")
	}
	g.payloadMu.Unlock()
	if g.Lifecycle() != GenFailed {
		t.Fatalf("lifecycle=%v", g.Lifecycle())
	}

	failing.err = nil
	if err := g.Discard(); err != nil {
		t.Fatalf("retry Discard: %v", err)
	}
	g.payloadMu.Lock()
	defer g.payloadMu.Unlock()
	if g.owned != nil || g.requestPlane != nil {
		t.Fatal("successful retry must clear both")
	}
	if failing.closed.Load() != 1 {
		t.Fatalf("closed=%d want 1 successful close", failing.closed.Load())
	}
}

func TestPublishedClose_ClearsOrRetainsPair(t *testing.T) {
	t.Parallel()

	t.Run("success_clears_requestPlane_and_owned", func(t *testing.T) {
		t.Parallel()
		m := NewManager(2, nil)
		plane := &attachTestPlane{}
		g := m.PrepareRequestPlane("pub", plane)
		if err := m.Publish(g); err != nil {
			t.Fatal(err)
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
		if g.RequestPlane() != nil || g.Handler() != nil {
			t.Fatal("successful Close must not expose request plane")
		}
		g.payloadMu.Lock()
		defer g.payloadMu.Unlock()
		if g.owned != nil || g.requestPlane != nil {
			t.Fatal("successful Close must clear requestPlane+owned")
		}
	})

	t.Run("failed_close_retains_both_for_retry", func(t *testing.T) {
		t.Parallel()
		m := NewManager(2, nil)
		failing := &failPlane{err: errors.New("boom")}
		g := m.PrepareRequestPlane("pub", failing)
		if err := m.Publish(g); err != nil {
			t.Fatal(err)
		}
		m.BeginShutdown()
		m.DetachActive()
		<-g.Drained()
		if err := g.BeginClose(); err != nil {
			t.Fatal(err)
		}
		if err := g.Close(); err == nil || err.Error() != "boom" {
			t.Fatalf("Close: %v", err)
		}
		if g.Lifecycle() != GenClosing {
			t.Fatalf("lifecycle=%v want closing", g.Lifecycle())
		}
		g.payloadMu.Lock()
		if g.owned != failing || g.requestPlane != failing {
			g.payloadMu.Unlock()
			t.Fatal("failed Close must retain requestPlane+owned")
		}
		g.payloadMu.Unlock()

		failing.err = nil
		if err := g.Close(); err != nil {
			t.Fatalf("retry Close: %v", err)
		}
		if g.RequestPlane() != nil {
			t.Fatal("retry success must clear request plane exposure")
		}
	})
}

func TestClose_AfterDiscard_TerminalSentinel(t *testing.T) {
	t.Parallel()

	t.Run("successful_discard_then_close_is_already_closed", func(t *testing.T) {
		t.Parallel()
		g := NewManager(1, nil).PrepareOwned("cand", &raceCloser{})
		if err := g.Discard(); err != nil {
			t.Fatal(err)
		}
		if err := g.Close(); !errors.Is(err, ErrAlreadyClosed) {
			t.Fatalf("Close after successful Discard: %v", err)
		}
		if err := g.Discard(); !errors.Is(err, ErrAlreadyClosed) {
			t.Fatalf("repeated Discard: %v", err)
		}
	})

	t.Run("failed_discard_close_does_not_claim_cleanup", func(t *testing.T) {
		t.Parallel()
		failing := &raceCloser{err: errors.New("nope")}
		g := NewManager(1, nil).PrepareOwned("cand", failing)
		if err := g.Discard(); err == nil {
			t.Fatal("expected discard failure")
		}
		if err := g.Close(); !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("Close after failed Discard: %v want ErrIllegalTransition", err)
		}
		if !errors.Is(g.Close(), ErrIllegalTransition) {
			t.Fatal("Close must not report successful cleanup while payload remains")
		}
		failing.err = nil
		if err := g.Discard(); err != nil {
			t.Fatalf("Discard retry: %v", err)
		}
		if err := g.Close(); !errors.Is(err, ErrAlreadyClosed) {
			t.Fatalf("Close after successful retry Discard: %v", err)
		}
	})

	t.Run("cleanup_join_preserves_real_error", func(t *testing.T) {
		t.Parallel()
		realErr := errors.New("real cleanup")
		joined := errors.Join(realErr, ErrAlreadyClosed)
		if !errors.Is(joined, realErr) {
			t.Fatal("real cleanup errors joined with ErrAlreadyClosed must remain observable")
		}
		if !errors.Is(joined, ErrAlreadyClosed) {
			t.Fatal("sanity: join still matches AlreadyClosed")
		}
	})
}
