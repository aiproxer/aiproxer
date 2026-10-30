package keepwarm

// EvidenceGate is supplied by an adapter/composition root. The scheduler does
// not know provider names or request shapes; active renewal requires every
// independent safety property to be proven by the issuing backend.
type EvidenceGate struct {
	SafeControl               bool
	AffinityPreserved         bool
	CacheEffectProven         bool
	ForegroundIsolationProven bool
}

func (g EvidenceGate) ActiveRenewalSupported() bool {
	return g.SafeControl && g.AffinityPreserved && g.CacheEffectProven && g.ForegroundIsolationProven
}
