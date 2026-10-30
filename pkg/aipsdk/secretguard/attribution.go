package secretguard

import "context"

type attrCtxKey int

const keyIngressAttribution attrCtxKey = iota + 18600

// IngressAttribution is sanitized HTTP/auth attribution for Meta and audit.
// It must never carry bearer tokens, Authorization headers, or raw secret material.
// Zero value means absent.
type IngressAttribution struct {
	PeerIP              string
	FrontendID          string
	Operation           string
	UserAgentDigest     string
	AgentIdentityDigest string
	DeviceID            string
	KeyID               string
	Fingerprint         string
}

// WithIngressAttribution returns a child context carrying sanitized ingress attribution.
// A nil parent ctx is tolerated and substituted with [context.TODO] so the result is
// always non-nil.
func WithIngressAttribution(ctx context.Context, a IngressAttribution) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	return context.WithValue(ctx, keyIngressAttribution, a)
}

// IngressAttributionFromContext returns attribution attached with [WithIngressAttribution], if any.
// A nil ctx is tolerated and returns (IngressAttribution{}, false).
func IngressAttributionFromContext(ctx context.Context) (IngressAttribution, bool) {
	if ctx == nil {
		return IngressAttribution{}, false
	}
	a, ok := ctx.Value(keyIngressAttribution).(IngressAttribution)
	return a, ok
}
