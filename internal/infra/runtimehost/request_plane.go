package runtimehost

import (
	"context"
	"net/http"
)

// PublishedRequestPlane is the narrow immutable request-plane surface bound to a
// Generation for preparation, publication, and acquire. The future data-plane
// dispatcher only needs Handler(); Close remains on OwnedCloser.
//
// Implementations must not expose mutable config, runtime.App, mixed Built
// handles, ProcessServices ownership, or process closers. runtimehost never
// imports runtimebundle; concrete bundles satisfy this interface from outside.
type PublishedRequestPlane interface {
	QuiesceCloser
	Handler() http.Handler
}

// PublishedWorkStarter is optionally implemented by PublishedRequestPlane values
// that own admission-independent work which must not run until the request plane
// is the active published generation. Manager.Publish invokes it after the
// active-pointer swap. runtimehost never imports runtimebundle.
type PublishedWorkStarter interface {
	StartPublished(ctx context.Context) error
}
