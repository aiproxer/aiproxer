package codex

import (
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// applyEarlySessionVerbosityBump forces text.verbosity=high for the first
// cfg.EarlySessionVerbosityBumpTurns turns of a conversation when the request
// carries no explicit per-request verbosity (URI param or body option, both
// already merged into call.Options.Verbosity by the time the backend runs).
// Explicit per-request verbosity always wins, even during the early window.
// The bump is a no-op when the feature is disabled, turnNo is 0, or the
// conversation is past the early window. turnNo must come from an atomic
// reserve on the shared session turn counter.
func applyEarlySessionVerbosityBump(p *Payload, call aipapi.Call, cfg Config, turnNo int) {
	if p == nil || cfg.EarlySessionVerbosityBumpDisabled || turnNo <= 0 {
		return
	}
	n := cfg.EarlySessionVerbosityBumpTurns
	if n <= 0 {
		n = DefaultEarlySessionVerbosityBumpTurns
	}
	if turnNo > n {
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
