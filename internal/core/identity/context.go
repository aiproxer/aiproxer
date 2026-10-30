package identity

import "context"

type callUserAgentKey struct{}

type callUserAgent struct {
	value string
}

type backgroundIdentityKey struct{}

// WithClientUserAgent marks ctx as a B-leg Open call path and carries the
// captured A-leg User-Agent for passthrough resolution. An empty ua is valid
// and means "call path with no usable client identity" (omit under passthrough).
// A nil ctx is treated as context.Background().
func WithClientUserAgent(ctx context.Context, ua string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, callUserAgentKey{}, callUserAgent{value: ua})
}

// WithBackgroundIdentity forces background/proxy identity resolution even when
// the parent context carries a call-path User-Agent. Use for inventory, token
// count, and other non-Open HTTP. A nil ctx is treated as context.Background().
func WithBackgroundIdentity(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, backgroundIdentityKey{}, true)
}

// CallClientUserAgent reports whether ctx is an Open call path and the carried UA.
// ok is false for unmarked contexts (default background) and for contexts wrapped
// with WithBackgroundIdentity.
func CallClientUserAgent(ctx context.Context) (ua string, ok bool) {
	if ctx == nil {
		return "", false
	}
	if bg, _ := ctx.Value(backgroundIdentityKey{}).(bool); bg {
		return "", false
	}
	v, ok := ctx.Value(callUserAgentKey{}).(callUserAgent)
	if !ok {
		return "", false
	}
	return v.value, true
}
