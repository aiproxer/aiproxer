package codex

import (
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// applyMidSessionVerbosityBump forces text.verbosity=high on every configured
// periodic turn of a conversation when the request carries no explicit
// per-request verbosity (URI param or body option, both already merged into
// call.Options.Verbosity by the time the backend runs). Explicit per-request
// verbosity always wins, even on frequency turns. The bump is a no-op when the
// feature is disabled, turnNo is 0, or the current turn is not a frequency hit.
// turnNo must come from an atomic reserve on the shared session turn counter.
func applyMidSessionVerbosityBump(p *Payload, call aipapi.Call, cfg Config, turnNo int) {
	if p == nil || cfg.MidSessionVerbosityBumpDisabled || turnNo <= 0 {
		return
	}
	frequency := cfg.MidSessionVerbosityBumpFrequency
	if frequency <= 0 {
		frequency = DefaultMidSessionVerbosityBumpFrequency
	}
	if turnNo%frequency != 0 {
		return
	}
	if strings.TrimSpace(string(call.Options.Verbosity)) != "" {
		return
	}
	if p.Text == nil {
		p.Text = &textSpec{}
	}
	p.Text.Verbosity = aipapi.VerbosityHigh
}
