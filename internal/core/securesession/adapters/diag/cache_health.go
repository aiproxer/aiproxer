package diag

// CacheHealthStatus is the advisory per-session prompt-cache observation
// carried by the session diagnostics read model. It distinguishes unknown
// (never observed, evicted, or lost to restart), healthy, and collapsed
// observations; Stale qualifies a last-known status past the detector's idle
// horizon. The signal is advisory only: it never feeds routing, billing,
// keep-warm, or stream behavior, and it carries no prompt content,
// credentials, or token counts.
type CacheHealthStatus string

const (
	CacheHealthUnknown   CacheHealthStatus = "unknown"
	CacheHealthHealthy   CacheHealthStatus = "healthy"
	CacheHealthCollapsed CacheHealthStatus = "collapsed"
)

// SessionCacheHealth is one session's advisory prompt-cache observation.
type SessionCacheHealth struct {
	Status CacheHealthStatus
	Stale  bool
}

// CacheHealthReader is the borrowed read-only port over process-owned
// collapse-diagnostic state. The detector owns bounded storage, identity
// keying, expiry, and eviction; the diagnostics handler only borrows
// point-in-time snapshots for already-authorized responses.
type CacheHealthReader interface {
	SessionCacheHealth(sessionID string) SessionCacheHealth
}

type cacheHealthDTO struct {
	Status string `json:"status"`
	Stale  bool   `json:"stale"`
}

// newCacheHealthDTO maps one borrowed observation into the read model. A nil
// reader yields nil so unwired handlers keep their existing response shape
// byte-for-byte. Unrecognized statuses degrade to unknown.
func newCacheHealthDTO(r CacheHealthReader, sessionID string) *cacheHealthDTO {
	if r == nil {
		return nil
	}
	h := r.SessionCacheHealth(sessionID)
	switch h.Status {
	case CacheHealthHealthy, CacheHealthCollapsed:
		return &cacheHealthDTO{Status: string(h.Status), Stale: h.Stale}
	default:
		return &cacheHealthDTO{Status: string(CacheHealthUnknown), Stale: h.Stale}
	}
}
