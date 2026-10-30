package backendplugin

import (
	"context"
	"net"
	"time"
)

// ServeWithForTest runs Serve with an injected environment and pipe dialer.
func ServeWithForTest(ctx context.Context, svc Service, opts ServeOptions, lookupEnv func(string) (string, bool), dialPipe func(string) (net.Conn, error)) error {
	return serveWith(ctx, svc, opts, lookupEnv, dialPipe, channelListener)
}

// ServeWithListenerForTest runs serveWith with an injected channel listener
// factory so a test can force the channel serve to fail.
func ServeWithListenerForTest(ctx context.Context, svc Service, opts ServeOptions, lookupEnv func(string) (string, bool), dialPipe func(string) (net.Conn, error), newListener func(net.Conn) net.Listener) error {
	return serveWith(ctx, svc, opts, lookupEnv, dialPipe, newListener)
}

// SetFallbackCancelGraceForTest overrides the package-level fallbackCancelGrace for tests
// and returns a function to restore the previous value.
func SetFallbackCancelGraceForTest(d time.Duration) func() {
	prev := fallbackCancelGrace
	fallbackCancelGrace = d
	return func() {
		fallbackCancelGrace = prev
	}
}
