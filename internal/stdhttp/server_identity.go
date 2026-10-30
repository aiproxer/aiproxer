package stdhttp

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/identity"
)

const headerServer = "Server"

// resolveDownstreamServerPolicy returns the effective A-leg Server field policy.
// Nil cfg yields proxy-mode product identity. The caller's config is not mutated.
func resolveDownstreamServerPolicy(cfg *config.Config) identity.FieldPolicy {
	if cfg == nil {
		return identity.EffectiveDownstreamOf(identity.Config{}).Server
	}
	return identity.EffectiveDownstreamOf(cfg.Identity).Server
}

// applyDownstreamServerHeader sets or removes the HTTP Server response header
// according to policy. Unknown/unsupported modes omit the header (fail-safe)
// rather than silently proxying when validation was skipped.
func applyDownstreamServerHeader(w http.ResponseWriter, policy identity.FieldPolicy) {
	if w == nil {
		return
	}
	switch policy.Mode {
	case identity.ModeDrop:
		w.Header().Del(headerServer)
	case identity.ModeCustom:
		w.Header().Set(headerServer, policy.Value)
	case identity.ModeProxy:
		w.Header().Set(headerServer, identity.DefaultProductName)
	default:
		// Empty is normalized to proxy by EffectiveDownstreamOf; any other
		// runtime mode (passthrough, typos) omits Server.
		w.Header().Del(headerServer)
	}
}

// DownstreamServerMiddleware presents proxy identity to A-leg HTTP clients via the
// standard Server response header. It wraps ResponseWriter with a thin policy writer
// that re-applies proxy|custom|drop immediately before every WriteHeader, implicit
// Write, and Flush so inner handlers cannot leak a conflicting Server after commit.
//
// The wrapper preserves http.Flusher (required by streaming frontend encoders),
// Unwrap() for http.ResponseController, and optional Hijacker/Pusher/ReaderFrom
// forwarding matching [internal/core/http.ResponseStatusRecorder].
//
// Scope: standard HTTP stack-wide (outermost around [stackHTTPHandler]) so auth
// denials, frontend protocol errors, recovery 500s, not-found, diagnostics, and
// successful frontend responses share the same Server identity.
func DownstreamServerMiddleware(cfg *config.Config, next http.Handler) http.Handler {
	policy := resolveDownstreamServerPolicy(cfg)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pw := newServerIdentityWriter(w, policy)
		applyDownstreamServerHeader(pw, policy)
		if next != nil {
			next.ServeHTTP(pw, r)
		}
		// Re-apply when the handler never committed (headers still mutable).
		applyDownstreamServerHeader(pw, policy)
	})
}

// serverIdentityWriter enforces Server header policy at commit time.
type serverIdentityWriter struct {
	http.ResponseWriter
	policy identity.FieldPolicy
}

var (
	_ http.ResponseWriter = (*serverIdentityWriter)(nil)
	_ http.Flusher        = (*serverIdentityWriter)(nil)
	_ io.ReaderFrom       = (*serverIdentityWriter)(nil)
)

func newServerIdentityWriter(w http.ResponseWriter, policy identity.FieldPolicy) *serverIdentityWriter {
	return &serverIdentityWriter{ResponseWriter: w, policy: policy}
}

func (w *serverIdentityWriter) apply() {
	applyDownstreamServerHeader(w.ResponseWriter, w.policy)
}

func (w *serverIdentityWriter) WriteHeader(code int) {
	w.apply()
	w.ResponseWriter.WriteHeader(code)
}

func (w *serverIdentityWriter) Write(b []byte) (int, error) {
	w.apply()
	return w.ResponseWriter.Write(b)
}

// Flush forwards to the underlying Flusher when supported (same pattern as
// ResponseStatusRecorder). Streaming encoders type-assert http.Flusher.
func (w *serverIdentityWriter) Flush() {
	w.apply()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer for http.ResponseController.
func (w *serverIdentityWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Hijack forwards when the underlying writer supports it.
func (w *serverIdentityWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("http: Hijacker not supported by underlying ResponseWriter")
	}
	return h.Hijack()
}

// Push forwards when the underlying writer supports it.
func (w *serverIdentityWriter) Push(target string, opts *http.PushOptions) error {
	p, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return p.Push(target, opts)
}

// ReadFrom applies Server policy then forwards to io.ReaderFrom when available.
func (w *serverIdentityWriter) ReadFrom(r io.Reader) (int64, error) {
	w.apply()
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(w.ResponseWriter, r)
}
