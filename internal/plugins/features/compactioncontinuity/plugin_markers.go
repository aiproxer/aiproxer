package compactioncontinuity

import (
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/injection"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

func preparedMarkerKey(meta compaction.PreservationMeta) string {
	trace, leg := strings.TrimSpace(meta.TraceID), strings.TrimSpace(meta.ALegID)
	if trace == "" || leg == "" {
		return ""
	}
	return trace + "\x00" + leg
}

func (p *Plugin) recordPreparedMarker(meta compaction.PreservationMeta, target InjectionTarget) {
	key := preparedMarkerKey(meta)
	if p == nil || key == "" {
		return
	}
	p.markerMu.Lock()
	defer p.markerMu.Unlock()
	now := time.Now()
	for oldKey, marker := range p.markers {
		if !marker.ExpiresAt.After(now) {
			delete(p.markers, oldKey)
		}
	}
	if len(p.markers) >= 1024 {
		var oldestKey string
		var oldest time.Time
		for candidate, marker := range p.markers {
			if oldestKey == "" || marker.ExpiresAt.Before(oldest) {
				oldestKey, oldest = candidate, marker.ExpiresAt
			}
		}
		delete(p.markers, oldestKey)
	}
	p.markers[key] = preparedInjectionMarker{Target: target, ExpiresAt: now.Add(2 * time.Hour)}
}

func (p *Plugin) rebindPreparedMarker(meta compaction.PreservationMeta, boundary string, revision uint64) {
	key := preparedMarkerKey(meta)
	if p == nil || key == "" || strings.TrimSpace(boundary) == "" || revision == 0 {
		return
	}
	p.markerMu.Lock()
	defer p.markerMu.Unlock()
	marker, ok := p.markers[key]
	if ok && marker.ExpiresAt.After(time.Now()) {
		marker.Target = InjectionTarget{BoundaryKey: boundary, CapsuleRevision: revision}
		p.markers[key] = marker
	}
}

func (p *Plugin) preparedMarker(meta compaction.PreservationMeta, binding string, target InjectionTarget) injection.Marker {
	key := preparedMarkerKey(meta)
	if p == nil || key == "" {
		return injection.Marker{}
	}
	p.markerMu.Lock()
	defer p.markerMu.Unlock()
	marker, ok := p.markers[key]
	if !ok || !marker.ExpiresAt.After(time.Now()) || marker.Target != target {
		return injection.Marker{}
	}
	return injection.Marker{BranchBinding: binding, BoundaryKey: target.BoundaryKey, CapsuleRevision: target.CapsuleRevision}
}

func (p *Plugin) takePreparedMarker(meta compaction.PreservationMeta, target InjectionTarget) bool {
	key := preparedMarkerKey(meta)
	if p == nil || key == "" {
		return false
	}
	p.markerMu.Lock()
	defer p.markerMu.Unlock()
	marker, ok := p.markers[key]
	if !ok || !marker.ExpiresAt.After(time.Now()) || marker.Target != target {
		if ok && !marker.ExpiresAt.After(time.Now()) {
			delete(p.markers, key)
		}
		return false
	}
	delete(p.markers, key)
	return true
}

func (p *Plugin) clearPreparedMarker(meta compaction.PreservationMeta) {
	key := preparedMarkerKey(meta)
	if p == nil || key == "" {
		return
	}
	p.markerMu.Lock()
	delete(p.markers, key)
	p.markerMu.Unlock()
}
