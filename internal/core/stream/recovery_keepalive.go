package stream

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type keepaliveIntervalKey struct{}

// DefaultRecoveryKeepaliveInterval is used while waiting on upstream streams or
// executor recv-phase failover (Req 5.5).
const DefaultRecoveryKeepaliveInterval = 12 * time.Second

// ContextWithKeepaliveInterval attaches a recovery keepalive interval for downstream SSE pumping.
func ContextWithKeepaliveInterval(ctx context.Context, d time.Duration) context.Context {
	if ctx == nil || d <= 0 {
		return ctx
	}
	return context.WithValue(ctx, keepaliveIntervalKey{}, d)
}

// KeepaliveIntervalFromContext returns the attached interval or [DefaultRecoveryKeepaliveInterval].
func KeepaliveIntervalFromContext(ctx context.Context) time.Duration {
	if ctx != nil {
		if d, ok := ctx.Value(keepaliveIntervalKey{}).(time.Duration); ok && d > 0 {
			return d
		}
	}
	return DefaultRecoveryKeepaliveInterval
}

// WrapRecoveryKeepalive wraps the canonical event stream so idle reads emit
// protocol-neutral Warning keepalives (see DefaultKeepaliveEvent).
func WrapRecoveryKeepalive(s aipapi.EventStream) (aipapi.EventStream, error) {
	return WrapRecoveryKeepaliveInterval(s, DefaultRecoveryKeepaliveInterval)
}

// WrapRecoveryKeepaliveInterval wraps s with the given idle keepalive interval.
func WrapRecoveryKeepaliveInterval(s aipapi.EventStream, d time.Duration) (aipapi.EventStream, error) {
	if d <= 0 {
		d = DefaultRecoveryKeepaliveInterval
	}
	return NewKeepalive(s, KeepaliveConfig{
		Interval:     d,
		NewKeepalive: DefaultKeepaliveEvent,
	})
}
