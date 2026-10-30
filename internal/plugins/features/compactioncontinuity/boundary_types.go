package compactioncontinuity

import "time"

// PreviewIntent is a non-billable completion-only boundary prepared before
// the primary request opens.
type PreviewIntent struct {
	Key                  string
	TargetSourceRevision uint64
}

// InjectionTarget identifies one boundary/revision that still needs a
// canonical continuity projection on a later eligible request.
type InjectionTarget struct {
	BoundaryKey     string
	CapsuleRevision uint64
}

// InjectionWatermark records one successfully released projection.
type InjectionWatermark struct {
	BranchBinding   string
	BoundaryKey     string
	CapsuleRevision uint64
}

type preparedInjectionMarker struct {
	Target    InjectionTarget
	ExpiresAt time.Time
}
