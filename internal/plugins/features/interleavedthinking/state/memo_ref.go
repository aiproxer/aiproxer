package state

// MemoRef locates a stored memo under an authoritative session or A-leg scope.
// Version is a monotonic counter bumped by the memo store on each mutation so
// callers can detect stale references.
type MemoRef struct {
	Key     string `json:"key"`
	Version int64  `json:"version"`
}

// IsEmpty reports whether a memo reference points to no memo.
func (r MemoRef) IsEmpty() bool {
	return r.Key == "" && r.Version == 0
}

// Equal reports whether two memo references identify the same store entry revision.
func (r MemoRef) Equal(other MemoRef) bool {
	return r.Key == other.Key && r.Version == other.Version
}
