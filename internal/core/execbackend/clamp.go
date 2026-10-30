package execbackend

import (
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// CanEnforceAuthorityMaxOutputTokens reports whether the backend can represent a
// MaxOutputTokens authority clamp on the wire for this call.
//
// The seam is provider-neutral: an adapter that drops the clamp for a
// client-compatibility flag installs its own decoder as
// IgnoresAuthorityMaxOutputTokensClamp. Core never interprets the flag itself.
func (be Backend) CanEnforceAuthorityMaxOutputTokens(call *aipapi.Call) bool {
	if call == nil || call.Options.MaxOutputTokens == nil {
		return true
	}
	if !be.EnforcesMaxOutputTokens {
		return false
	}
	if be.IgnoresAuthorityMaxOutputTokensClamp != nil && be.IgnoresAuthorityMaxOutputTokensClamp(*call) {
		return false
	}
	return true
}
