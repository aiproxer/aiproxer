package continuity

import (
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/config"
)

// NewMemoryStoreFromConfig creates an in-memory b2bua.Store from the memory-related
// fields of a ContinuityConfig. It validates TTL and MaxLegs.
//
// The backing is resolved with [config.EffectiveContinuityStore], the same resolver
// the configuration validator and the loader defaults use, so an omitted continuity
// block and an omitted in_memory flag build the same store the configuration
// declares. This is a pure-core helper with no infra/db dependency.
func NewMemoryStoreFromConfig(cfg config.ContinuityConfig) (b2bua.Store, error) {
	if store := config.EffectiveContinuityStore(cfg); store != config.ContinuityStoreMemory {
		return nil, fmt.Errorf("continuity: store %q is not the memory store", store)
	}
	opts := b2bua.MemoryStoreOptions{}
	if s := strings.TrimSpace(cfg.TTL); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, fmt.Errorf("continuity.ttl: %w", err)
		}
		if d < 0 {
			return nil, fmt.Errorf("continuity.ttl must be non-negative")
		}
		opts.TTL = d
	}
	if cfg.MaxLegs < 0 {
		return nil, fmt.Errorf("continuity: max_legs must be >= 0")
	}
	if cfg.MaxLegs != 0 {
		opts.MaxLegs = cfg.MaxLegs
	}
	return b2bua.NewMemoryStore(opts)
}
