package state

import (
	"context"
	"strings"
	"time"
)

// BindPlugin returns a [Store] that prefixes the namespace segment with pluginID so keys from
// different plugins cannot collide when they share logical namespace names (R6, tasks 6–6.1).
// pluginID must be non-empty after trimming; otherwise returns [DisabledStore].
func BindPlugin(store Store, pluginID string) Store {
	if store == nil {
		return DisabledStore{}
	}
	pid := strings.TrimSpace(pluginID)
	if pid == "" {
		return DisabledStore{}
	}
	return pluginBound{inner: store, pluginID: pid}
}

type pluginBound struct {
	inner    Store
	pluginID string
}

func (b pluginBound) ns(ns string) string {
	ns = strings.TrimSpace(ns)
	if ns == "" {
		return b.pluginID
	}
	return b.pluginID + "/" + ns
}

func (b pluginBound) Get(ctx context.Context, scope Scope, ns, key string, out any) (bool, error) {
	return b.inner.Get(ctx, scope, b.ns(ns), key, out)
}

func (b pluginBound) Put(ctx context.Context, scope Scope, ns, key string, value any, ttl time.Duration) error {
	return b.inner.Put(ctx, scope, b.ns(ns), key, value, ttl)
}

func (b pluginBound) Delete(ctx context.Context, scope Scope, ns, key string) error {
	return b.inner.Delete(ctx, scope, b.ns(ns), key)
}

func (b pluginBound) InspectTTL(ctx context.Context, scope Scope, ns, key string) (time.Duration, bool, error) {
	return b.inner.InspectTTL(ctx, scope, b.ns(ns), key)
}
