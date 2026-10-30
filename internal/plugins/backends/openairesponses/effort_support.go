package openairesponses

import (
	"context"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Provider-owned graded reasoning-effort facts for the official OpenAI API.
//
// Source: OpenAI model page for gpt-6-astra and the official reasoning guide
// (https://developers.openai.com/api/docs/models/gpt-6-astra,
// https://developers.openai.com/api/docs/guides/reasoning; verified 2026-10-28).
// Only the official endpoint proves these facts: the backend accepts an
// arbitrary BaseURL, and compatible third-party endpoints are not evidence of
// OpenAI support. Unknown model IDs, aliases, altered base URLs, and
// unverified versions answer Unknown — never a refusal, never a prefix
// inference such as all gpt-6*.
var officialEffortValues = map[string]struct {
	supported  []string
	exhaustive bool
}{
	"gpt-6-astra": {supported: []string{"low", "medium", "high", "xhigh", "max"}, exhaustive: true},
}

// isOfficialEndpoint reports whether the configured BaseURL is exactly the
// official OpenAI API. Trailing slashes are tolerated; any other host, path,
// or scheme change is not official evidence.
func isOfficialEndpoint(baseURL string) bool {
	trimmed := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	return trimmed == DefaultBaseURL
}

// resolveEffortSupport answers one reasoning-effort value for one candidate's
// native model. It is metadata only: no network, no credentials.
func resolveEffortSupport(cfg Config) func(context.Context, aipapi.Call, routing.AttemptCandidate, string) execbackend.EffortSupportState {
	official := isOfficialEndpoint(cfg.BaseURL)
	return func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate, effort string) execbackend.EffortSupportState {
		if !official || effort == "" {
			return execbackend.EffortSupportUnknown
		}
		model := resolveModel(cand, call)
		if model == "" {
			return execbackend.EffortSupportUnknown
		}
		facts, ok := officialEffortValues[model]
		if !ok {
			return execbackend.EffortSupportUnknown
		}
		if slices.Contains(facts.supported, effort) {
			return execbackend.EffortSupportSupported
		}
		if facts.exhaustive {
			return execbackend.EffortSupportUnsupported
		}
		return execbackend.EffortSupportUnknown
	}
}
