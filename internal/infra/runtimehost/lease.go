package runtimehost

import (
	"net/http"
	"sync/atomic"
)

// Lease is a hot-path request lease (req 5.3-5.4, 15.1).
type Lease struct {
	gen         *Generation
	released    atomic.Bool
	transferred atomic.Bool
}

// Generation returns the bound generation.
func (l *Lease) Generation() *Generation {
	if l == nil {
		return nil
	}
	return l.gen
}

// Handler returns the bound request-plane handler for this lease, or nil.
func (l *Lease) Handler() http.Handler {
	if l == nil || l.gen == nil {
		return nil
	}
	return l.gen.Handler()
}

// RequestPlane returns the bound immutable request-plane publisher, or nil.
func (l *Lease) RequestPlane() PublishedRequestPlane {
	if l == nil || l.gen == nil {
		return nil
	}
	return l.gen.RequestPlane()
}

// Release drops the lease retain exactly once; double-release is a no-op (req 10.4).
func (l *Lease) Release() {
	if l == nil || !l.released.CompareAndSwap(false, true) || l.transferred.Load() {
		return
	}
	l.gen.releaseRef()
}

// TransferPin converts the lease retain into an async/SSE/provider pin (req 5.7, 10.3).
// Only PinSSE, PinAsync, and PinProvider are accepted; invalid kinds fail without
// consuming the lease so a subsequent valid transfer can still succeed.
func (l *Lease) TransferPin(kind PinKind) (*Pin, bool) {
	if l == nil || !validTransferPinKind(kind) {
		return nil, false
	}
	if !l.released.CompareAndSwap(false, true) {
		return nil, false
	}
	l.transferred.Store(true)
	return &Pin{gen: l.gen, kind: kind}, true
}

// RetainPin acquires an additional independent generation pin while this lease
// still holds spawn rights (req 5.3, 5.7, 10.3). Unlike TransferPin, the lease
// retain is preserved so multiple terminal/async dependents can each hold a pin.
// Invalid kinds and post-release attempts fail closed without consuming ownership.
func (l *Lease) RetainPin(kind PinKind) (*Pin, bool) {
	if l == nil || l.gen == nil || !validTransferPinKind(kind) {
		return nil, false
	}
	if l.released.Load() {
		return nil, false
	}
	if !l.gen.tryRetainWhileBound() {
		return nil, false
	}
	// Lease may have released between the checks and retain; roll back.
	if l.released.Load() {
		l.gen.releaseRef()
		return nil, false
	}
	return &Pin{gen: l.gen, kind: kind}, true
}

func validTransferPinKind(kind PinKind) bool {
	switch kind {
	case PinSSE, PinAsync, PinProvider:
		return true
	default:
		return false
	}
}

// Pin retains a generation across SSE/async/provider work.
type Pin struct {
	gen      *Generation
	kind     PinKind
	released atomic.Bool
}

// Kind returns the pin classification.
func (p *Pin) Kind() PinKind {
	if p == nil {
		return 0
	}
	return p.kind
}

// Generation returns the pinned generation.
func (p *Pin) Generation() *Generation {
	if p == nil {
		return nil
	}
	return p.gen
}

// Release drops the pin retain exactly once.
func (p *Pin) Release() {
	if p == nil || !p.released.CompareAndSwap(false, true) {
		return
	}
	p.gen.releaseRef()
}
