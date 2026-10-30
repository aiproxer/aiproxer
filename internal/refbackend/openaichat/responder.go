package openaichat

import "net/http"

// Request is a bounded, defensive view of one authorized chat/completions call.
// Body is a clone owned by the handler; responders must not assume it is retained
// after return, and the handler never retains responder-owned mutable buffers.
type Request struct {
	Sequence int64
	Body     []byte
	Stream   bool
}

// Response is a scripted HTTP response for one Request.
// For stream requests, SSE is written; otherwise JSON is written.
// Status zero means 200. Headers are optional extras (Content-Type is set by the handler).
//
// Headers must not be mutated by the Responder (or any other goroutine) after Response
// is returned; the handler may snapshot them. Prefer a freshly allocated http.Header
// per call rather than a shared map reused across concurrent requests.
type Response struct {
	Status  int
	Headers http.Header
	JSON    string
	SSE     string
}

// Responder builds a per-request Response. It must be safe for concurrent use.
type Responder func(Request) Response
