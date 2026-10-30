package runtimehost

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
)

type attachTestPlane struct {
	quiesced atomic.Int32
	closed   atomic.Int32
}

func (*attachTestPlane) Handler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}

func (p *attachTestPlane) Quiesce(context.Context) error {
	p.quiesced.Add(1)
	return nil
}

func (p *attachTestPlane) Close() error {
	p.closed.Add(1)
	return nil
}

type attachTestCloser struct {
	closed atomic.Int32
}

func (c *attachTestCloser) Close() error {
	c.closed.Add(1)
	return nil
}

func TestAttachRequestPlaneAtomicallyOwnsDiscard(t *testing.T) {
	t.Parallel()
	g := NewManager(1, nil).BeginPrepare("candidate", nil)
	plane := &attachTestPlane{}
	if err := g.AttachRequestPlane(plane); err != nil {
		t.Fatalf("AttachRequestPlane: %v", err)
	}
	if g.RequestPlane() != plane || g.Handler() == nil {
		t.Fatal("attached plane is not the served request plane")
	}
	if err := g.AttachOwned(&attachTestCloser{}); !errors.Is(err, ErrOwnedAlreadyBound) {
		t.Fatalf("AttachOwned after request plane: %v", err)
	}
	if err := g.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if got := plane.closed.Load(); got != 1 {
		t.Fatalf("plane Close calls=%d want 1", got)
	}
}

func TestAttachRequestPlaneCannotSplitExistingOwnership(t *testing.T) {
	t.Parallel()
	owned := &attachTestCloser{}
	plane := &attachTestPlane{}
	g := NewManager(1, nil).BeginPrepare("candidate", owned)
	if err := g.AttachRequestPlane(plane); !errors.Is(err, ErrOwnedAlreadyBound) {
		t.Fatalf("AttachRequestPlane after owned closer: %v", err)
	}
	if g.RequestPlane() != nil || g.Handler() != nil {
		t.Fatal("rejected request plane became visible")
	}
	if err := g.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if got := owned.closed.Load(); got != 1 {
		t.Fatalf("owned Close calls=%d want 1", got)
	}
	if got := plane.closed.Load(); got != 0 {
		t.Fatalf("rejected plane Close calls=%d want 0", got)
	}
}

func TestRetireGenerationUsesBoundPlaneAsAuthoritativeOwner(t *testing.T) {
	t.Parallel()
	m := NewManager(2, nil)
	bound := &attachTestPlane{}
	g := m.PrepareRequestPlane("bound", bound)
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
	if err := m.Publish(m.Prepare("next")); err != nil {
		t.Fatal(err)
	}
	// retireGeneration derives the QuiesceCloser solely from the generation's
	// bound RequestPlane; there is no external collaborator argument to
	// mismatch (task 7.3).
	if _, err := m.RetireGeneration(context.Background(), g); err != nil && !errors.Is(err, ErrAlreadyClosed) {
		t.Fatal(err)
	}
	if bound.quiesced.Load() != 1 || bound.closed.Load() != 1 {
		t.Fatalf("bound plane quiesce=%d close=%d want 1/1", bound.quiesced.Load(), bound.closed.Load())
	}
}
