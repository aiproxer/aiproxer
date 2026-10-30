package http

import (
	"bufio"
	"errors"
	"io"
	"net"
	nethttp "net/http"
)

// ResponseStatusRecorder wraps [net/http.ResponseWriter] and records the final
// status. Informational responses (except switching protocols) do not commit it.
// It forwards optional [net/http.ResponseWriter] behaviors (Flusher, Hijacker, Pusher, [io.ReaderFrom])
// so streaming middleware preserves flush and HTTP/2 push support.
type ResponseStatusRecorder struct {
	nethttp.ResponseWriter
	Status int
}

func (rr *ResponseStatusRecorder) WriteHeader(code int) {
	if rr.Status == 0 && (code >= 200 || code == nethttp.StatusSwitchingProtocols) {
		rr.Status = code
	}
	rr.ResponseWriter.WriteHeader(code)
}

// Write implements [net/http.ResponseWriter]. If WriteHeader has not been called yet,
// the first Write implicitly sets status 200 (matching [net/http.ResponseWriter] rules)
// so [ResponseStatusRecorder.Status] reflects a committed response for middleware such
// as panic recovery that must not write a second body after bytes have been sent.
func (rr *ResponseStatusRecorder) Write(b []byte) (int, error) {
	if rr.Status == 0 {
		rr.Status = nethttp.StatusOK
	}
	return rr.ResponseWriter.Write(b)
}

// Flush forwards to the underlying [net/http.Flusher] when supported; otherwise it is a no-op.
func (rr *ResponseStatusRecorder) Flush() {
	if f, ok := rr.ResponseWriter.(nethttp.Flusher); ok {
		if rr.Status == 0 {
			rr.Status = nethttp.StatusOK
		}
		f.Flush()
	}
}

// Hijack forwards to the underlying [net/http.Hijacker] when supported.
func (rr *ResponseStatusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := rr.ResponseWriter.(nethttp.Hijacker)
	if !ok {
		return nil, nil, errors.New("http: Hijacker not supported by underlying ResponseWriter")
	}
	return h.Hijack()
}

// Push forwards to the underlying [net/http.Pusher] when supported.
func (rr *ResponseStatusRecorder) Push(target string, opts *nethttp.PushOptions) error {
	p, ok := rr.ResponseWriter.(nethttp.Pusher)
	if !ok {
		return nethttp.ErrNotSupported
	}
	return p.Push(target, opts)
}

// ReadFrom forwards to the underlying [io.ReaderFrom] when supported.
// Like [ResponseStatusRecorder.Write], the first body bytes imply status 200 if WriteHeader was not called.
func (rr *ResponseStatusRecorder) ReadFrom(r io.Reader) (int64, error) {
	if rr.Status == 0 {
		rr.Status = nethttp.StatusOK
	}
	if rf, ok := rr.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(rr.ResponseWriter, r)
}
