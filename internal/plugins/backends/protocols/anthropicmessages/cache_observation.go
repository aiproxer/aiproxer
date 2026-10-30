package anthropicmessages

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// CacheObservation is the provider-neutral stream-side data handed to the
// owning plugin's observation hook on a committed successful turn. Renewal is
// the exact cacheable prefix (including the cache breakpoint) that created/read
// the provider cache entry, so the renewal request can reproduce it byte-for-byte.
type CacheObservation struct {
	Lineage      promptcache.ObservationLineage
	Model        string
	TTL          string
	Renewal      RenewalSnapshot
	Evidence     promptcache.CacheEvidence
	ObservedAt   time.Time
	CredentialID string
}

// CacheObservationHook, when non-nil, issues a renewable target from committed
// foreground cache evidence. It is supplied by the owning plugin only when
// automatic enrollment is enabled; nil keeps observation emission off.
type CacheObservationHook func(context.Context, CacheObservation) (promptcache.Observation, error)
