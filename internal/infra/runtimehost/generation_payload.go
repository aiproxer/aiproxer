package runtimehost

import "net/http"

// AttachOwned binds generation-owned resources once while preparing/prepared.
// Lifecycle validation and payload mutation share payloadMu with
// assignPublish/Discard so a binding cannot commit after publication or after
// discard has claimed an empty payload.
func (g *Generation) AttachOwned(owned OwnedCloser) error {
	if g == nil {
		return ErrNotPrepared
	}
	g.payloadMu.Lock()
	defer g.payloadMu.Unlock()
	st := g.Lifecycle()
	if st != GenPreparing && st != GenPrepared {
		return ErrIllegalTransition
	}
	if g.owned != nil || g.requestPlane != nil {
		return ErrOwnedAlreadyBound
	}
	g.owned = owned
	return nil
}

// AttachRequestPlane atomically binds the immutable request-plane publisher as
// both the served plane and the generation-owned closer while preparing.
func (g *Generation) AttachRequestPlane(plane PublishedRequestPlane) error {
	if g == nil {
		return ErrNotPrepared
	}
	g.payloadMu.Lock()
	defer g.payloadMu.Unlock()
	st := g.Lifecycle()
	if st != GenPreparing && st != GenPrepared {
		return ErrIllegalTransition
	}
	if g.requestPlane != nil {
		return ErrRequestPlaneAlreadyBound
	}
	if g.owned != nil {
		return ErrOwnedAlreadyBound
	}
	g.requestPlane = plane
	g.owned = plane
	return nil
}

// RequestPlane returns the bound immutable request-plane publisher, or nil.
func (g *Generation) RequestPlane() PublishedRequestPlane {
	if g == nil {
		return nil
	}
	g.payloadMu.Lock()
	defer g.payloadMu.Unlock()
	return g.requestPlane
}

// Handler returns the bound request-plane handler, or nil when unbound.
func (g *Generation) Handler() http.Handler {
	plane := g.RequestPlane()
	if plane == nil {
		return nil
	}
	return plane.Handler()
}
