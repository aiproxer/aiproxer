package contract

import (
	"context"
	"net/netip"
)

// sourceAddrContextKey is the unexported key for the request-context source
// address snapshot. It is deliberately package-private so no other package can
// read or forge the snapshot the ingress gate published.
type sourceAddrContextKey struct{}

// WithSourceAddr returns ctx carrying the already-resolved, normalized source
// address for this request.
//
// The value stored is only the resolved netip.Addr: the ingress gate publishes
// the identity it snapshotted so inner observers (the transport-auth adaptive
// observer) never reparse mutable forwarding headers and can never disagree with
// the gate. No header, path, query, credential or principal is retained.
//
// An invalid address is not a snapshot, so the parent context is returned
// unchanged and an outer gate decision is never silently overwritten.
func WithSourceAddr(ctx context.Context, addr netip.Addr) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if !addr.IsValid() {
		return ctx
	}
	return context.WithValue(ctx, sourceAddrContextKey{}, addr.Unmap())
}

// SourceAddr returns the normalized source address attached by
// [WithSourceAddr]. The second result reports whether a snapshot is present, so
// an observer outside a gated handler can fall back without inventing an
// identity.
func SourceAddr(ctx context.Context) (netip.Addr, bool) {
	if ctx == nil {
		return netip.Addr{}, false
	}
	addr, ok := ctx.Value(sourceAddrContextKey{}).(netip.Addr)
	if !ok || !addr.IsValid() {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}
