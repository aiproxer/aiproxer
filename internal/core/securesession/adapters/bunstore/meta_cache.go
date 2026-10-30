package bunstore

import (
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/jellydator/ttlcache/v3"
)

type sessionMetaCache struct {
	exists     *ttlcache.Cache[domain.SessionID, bool]
	transcript *ttlcache.Cache[domain.SessionID, bool]
}

func newSessionMetaCache(ttl time.Duration, maxEntries uint64) *sessionMetaCache {
	opts := []ttlcache.Option[domain.SessionID, bool]{
		ttlcache.WithTTL[domain.SessionID, bool](ttl),
		ttlcache.WithCapacity[domain.SessionID, bool](maxEntries),
		ttlcache.WithDisableTouchOnHit[domain.SessionID, bool](),
	}
	return &sessionMetaCache{
		exists:     ttlcache.New(opts...),
		transcript: ttlcache.New(opts...),
	}
}

func (m *sessionMetaCache) invalidate(id domain.SessionID) {
	if m == nil {
		return
	}
	m.exists.Delete(id)
	m.transcript.Delete(id)
}

func (m *sessionMetaCache) seedAfterCreate(id domain.SessionID, transcriptEnabled bool) {
	if m == nil {
		return
	}
	m.exists.Set(id, true, ttlcache.DefaultTTL)
	m.transcript.Set(id, transcriptEnabled, ttlcache.DefaultTTL)
}
