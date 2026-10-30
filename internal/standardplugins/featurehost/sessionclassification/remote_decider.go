package sessionclassification

import (
	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
)

// NewRemoteDecider returns the decider one generation may hold, or nil for a
// generation that must never reach the network.
//
// The mode gate lives here, next to the adapter it protects, rather than in the
// generic composition path: a heuristic generation returns nil without
// validating a remote posture, referencing a credential, or constructing a
// client, so requirement 6.2 holds by construction and not by caller discipline
// (requirements 6.1, 6.2).
//
// A jev or hybrid generation returns the Jev adapter built from its own validated
// posture. Because the posture is validated before construction, a remote mode
// with missing or unsafe settings fails the candidate generation here instead of
// publishing a classifier that would silently degrade to local-only
// classification (requirements 6.3, 6.4, 6.10, 8.4).
func NewRemoteDecider(cfg featurestate.Config) (featurestate.RemoteDecider, error) {
	if cfg.Mode != featurestate.ModeJev && cfg.Mode != featurestate.ModeHybrid {
		return nil, nil
	}
	policy, err := featurestate.NewRemotePolicy(cfg)
	if err != nil {
		return nil, err
	}
	return NewJevDecider(policy)
}
