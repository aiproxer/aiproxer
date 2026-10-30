package policy

// CandidateHealth supplies routing keys (same string form as routing.AttemptCandidate.Key /
// routing.Primary.String()) that must be treated as temporarily unhealthy and excluded from
// failover planning alongside explicit negotiation exclusions.
type CandidateHealth interface {
	UnhealthyCandidateKeys() map[string]struct{}
}

// StaticUnhealthy is a fixed key set (tests or simple operators).
type StaticUnhealthy map[string]struct{}

// UnhealthyCandidateKeys returns a copy of the underlying map when non-nil.
func (s StaticUnhealthy) UnhealthyCandidateKeys() map[string]struct{} {
	if s == nil {
		return nil
	}
	out := make(map[string]struct{}, len(s))
	for k := range s {
		out[k] = struct{}{}
	}
	return out
}
