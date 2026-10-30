package metrics

import (
	"time"

	ssessiondiag "github.com/aiproxer/aiproxer/internal/core/securesession/adapters/diag"
)

// SessionHealth returns the advisory per-session roll-up across every
// session/backend/model key for sessionID. Worst status wins: collapsed beats
// healthy beats unknown. The snapshot is strictly read-only: it never touches
// LRU order, consumes no B-leg deduplication history, trips no episodes, and
// performs no eviction. An empty session, a nil detector, or a zero value
// reports unknown. It implements the secure-session diagnostics health port,
// so the read model borrows snapshots without owning detector state.
func (d *PromptCacheCollapse) SessionCacheHealth(sessionID string) ssessiondiag.SessionCacheHealth {
	if d == nil || sessionID == "" {
		return ssessiondiag.SessionCacheHealth{Status: ssessiondiag.CacheHealthUnknown}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now
	if now == nil {
		now = time.Now
	}
	at := now()
	var matched, fresh, collapsed, healthy bool
	for key, s := range d.keys {
		if key.session != sessionID || s == nil {
			continue
		}
		matched = true
		// Stale qualifies the aggregate only when no key carries fresh signal:
		// one fresh key dominates idle siblings.
		fresh = fresh || at.Sub(s.lastSeen) < d.cfg.IdleExpiry
		if s.warned {
			collapsed = true
		} else if s.sawHealthy {
			healthy = true
		}
	}
	if !matched {
		return ssessiondiag.SessionCacheHealth{Status: ssessiondiag.CacheHealthUnknown}
	}
	out := ssessiondiag.SessionCacheHealth{Status: ssessiondiag.CacheHealthUnknown, Stale: !fresh}
	switch {
	case collapsed:
		out.Status = ssessiondiag.CacheHealthCollapsed
	case healthy:
		out.Status = ssessiondiag.CacheHealthHealthy
	}
	return out
}
