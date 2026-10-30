package stdhttp

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

// Task 1.5 no-drop dispatcher reference (req 5.1-5.8). Not production GenerationDispatcher.

type generationCtxKey struct{}

func GenerationFromContext(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(generationCtxKey{}).(int64)
	return v, ok
}

type NoDropHandlerRegistry struct {
	mu      sync.RWMutex
	byLabel map[string]http.Handler
}

func NewNoDropHandlerRegistry() *NoDropHandlerRegistry {
	return &NoDropHandlerRegistry{byLabel: make(map[string]http.Handler)}
}

func (r *NoDropHandlerRegistry) Set(label string, h http.Handler) {
	r.mu.Lock()
	r.byLabel[label] = h
	r.mu.Unlock()
}

func (r *NoDropHandlerRegistry) Get(label string) (http.Handler, bool) {
	r.mu.RLock()
	h, ok := r.byLabel[label]
	r.mu.RUnlock()
	return h, ok
}

type RefGenerationDispatcher struct {
	Manager  *runtimehost.Manager
	Handlers *NoDropHandlerRegistry
}

func NewRefGenerationDispatcher(m *runtimehost.Manager, h *NoDropHandlerRegistry) *RefGenerationDispatcher {
	return &RefGenerationDispatcher{Manager: m, Handlers: h}
}

func (d *RefGenerationDispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	lease, ok := d.Manager.Acquire()
	if !ok {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	defer lease.Release()
	gen := lease.Generation()
	h, ok := d.Handlers.Get(gen.Label())
	if !ok {
		http.Error(w, "missing generation handler", http.StatusInternalServerError)
		return
	}
	h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), generationCtxKey{}, gen.ID())))
}

func (d *RefGenerationDispatcher) PublishWithHandler(label string, h http.Handler) (*runtimehost.Generation, error) {
	d.Handlers.Set(label, h)
	cand := d.Manager.Prepare(label)
	if err := d.Manager.Publish(cand); err != nil {
		return nil, err
	}
	return cand, nil
}

type ListenerAliveCounter struct{ publishes atomic.Int64 }

func (c *ListenerAliveCounter) MarkPublish()     { c.publishes.Add(1) }
func (c *ListenerAliveCounter) Publishes() int64 { return c.publishes.Load() }
