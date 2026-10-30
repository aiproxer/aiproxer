package reasoningpreservation

import "context"

// routeBoundEgressPolicy is a narrow SOURCE-port wrapper that enforces an
// explicit allowlist of compressor routes. A mismatch is treated as a
// missing-policy-equivalent deny (R7 AC8), so callers observe the same
// fail-closed outcome as a missing policy. This prevents route-alone approval
// and keeps the decision behind the trusted EgressPolicy seam.
type routeBoundEgressPolicy struct {
	allowed  map[string]struct{}
	delegate EgressPolicy
}

// NewRouteBoundEgressPolicy returns an EgressPolicy that denies when in.Route
// is not in allowed, otherwise delegates. An empty allowed map means no route
// restriction (delegate decides). A nil delegate on allowed match is treated as
// missing-policy deny.
func NewRouteBoundEgressPolicy(allowed map[string]struct{}, delegate EgressPolicy) EgressPolicy {
	cp := make(map[string]struct{}, len(allowed))
	for k := range allowed {
		cp[k] = struct{}{}
	}
	return &routeBoundEgressPolicy{allowed: cp, delegate: delegate}
}

func (r *routeBoundEgressPolicy) Decide(ctx context.Context, in CompressionEgressInput) (CompressionEgressDecision, error) {
	if len(r.allowed) > 0 {
		if _, ok := r.allowed[in.Route]; !ok {
			return CompressionEgressDecision{Action: EgressDeny, PolicyVersion: "missing-policy"}, nil
		}
	}
	if r.delegate == nil {
		return CompressionEgressDecision{Action: EgressDeny, PolicyVersion: "missing-policy"}, nil
	}
	return r.delegate.Decide(ctx, in)
}

var _ EgressPolicy = (*routeBoundEgressPolicy)(nil)
