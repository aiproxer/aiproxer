package b2bua

import (
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
)

func (s *MemoryStore) SeedRouteOverrideForTest(aLegID string, st routeoverride.State) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	leg, ok := s.legs[aLegID]
	if !ok {
		return false
	}
	leg.override = st
	return true
}

func (s *MemoryStore) PeekLastSeenAtForTest(aLegID string) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	leg, ok := s.legs[aLegID]
	if !ok {
		return time.Time{}, false
	}
	return leg.record.LastSeenAt, true
}
