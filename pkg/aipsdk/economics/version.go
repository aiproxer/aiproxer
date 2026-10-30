package economics

import "time"

// VersionRef is an immutable snapshot identity with optional timestamps
// (requirements 6.2, 7.6, 11.x deferred binding).
type VersionRef struct {
	ID          string    `json:"id"`
	Version     string    `json:"version"`
	EffectiveAt time.Time `json:"effective_at,omitzero"`
	FetchedAt   time.Time `json:"fetched_at,omitzero"`
}

// RatingSnapshotRef binds a rating/pricebook snapshot used for an admission or settlement.
type RatingSnapshotRef struct {
	VersionRef
	RaterID string `json:"rater_id,omitempty"`
}

// PolicySnapshotRef binds an authority/policy snapshot used for an admission or settlement.
type PolicySnapshotRef struct {
	VersionRef
	PolicyID string `json:"policy_id,omitempty"`
}
