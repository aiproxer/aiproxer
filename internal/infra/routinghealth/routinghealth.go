// Package routinghealth supplies composition-root implementations of policy.CandidateHealth
// for the standard bundle without embedding core routing policy types at call sites.
package routinghealth

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/policy"
)

// Empty returns a candidate health source that marks no backends unhealthy (standard default).
func Empty() policy.CandidateHealth {
	return policy.StaticUnhealthy(nil)
}

// StaticKeys returns unhealthy routing keys (same string form as routing.Primary.String()).
func StaticKeys(keys ...string) policy.CandidateHealth {
	if len(keys) == 0 {
		return Empty()
	}
	m := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		m[k] = struct{}{}
	}
	if len(m) == 0 {
		return Empty()
	}
	return policy.StaticUnhealthy(m)
}
