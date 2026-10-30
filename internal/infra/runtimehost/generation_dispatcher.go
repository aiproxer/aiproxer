package runtimehost

import (
	"net/http"
)

// GenerationDispatcher is the stable data-plane http.Handler that binds each
// request to exactly one active generation lease (req 4.5, 5.1-5.4, 15.1).
//
// It does not buffer or wrap http.ResponseWriter, replace connections, recover
// panics, or perform config/model lookups. Direct delegation preserves optional
// ResponseWriter interfaces and the generation handler's middleware stack.
type GenerationDispatcher struct {
	manager *Manager
}

// NewGenerationDispatcher returns a production dispatcher backed by m.
func NewGenerationDispatcher(m *Manager) *GenerationDispatcher {
	return &GenerationDispatcher{manager: m}
}

// ServeHTTP acquires one generation lease, attaches a safe request binding, and
// delegates to the generation handler. The lease is released exactly once when
// the handler returns unless ownership was transferred to a pin.
func (d *GenerationDispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if d == nil || d.manager == nil {
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}
	lease, ok := d.manager.Acquire()
	if !ok {
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}
	defer lease.Release()

	h := lease.Handler()
	if h == nil {
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}

	binding := newRequestBinding(lease)
	ctx := withRequestBinding(r.Context(), binding)
	// Bind immutable model/catalog views once after lease acquisition and
	// before entering the generation handler (req 9.4). Optional for test planes.
	ctx = BindModelViewsIfPresent(ctx, lease.RequestPlane())
	h.ServeHTTP(w, r.WithContext(ctx))
}
