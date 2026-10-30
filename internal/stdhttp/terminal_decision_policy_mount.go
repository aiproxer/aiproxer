package stdhttp

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	policyhttp "github.com/aiproxer/aiproxer/internal/stdhttp/terminalpolicy"
)

const (
	terminalDecisionClientPath   = "/v1/aip/session/features/{feature_id}"
	terminalDecisionOperatorPath = "/admin/session-features/{session_id}/{feature_id}"
)

// installTerminalDecisionPolicy mounts the provider-neutral policy resources
// into the standard generation mux. A missing process store stays mounted as
// a closed 503 surface; it never turns into a silently absent or writable
// route. Operator resources retain the existing diagnostics shared-secret
// boundary and are not exposed when that boundary is disabled.
func installTerminalDecisionPolicy(mux *http.ServeMux, cfg *config.Config, in TerminalDecisionPolicyInput) {
	if mux == nil {
		return
	}
	h := terminalDecisionPolicyUnavailable()
	if in.Store != nil && in.FeatureStatus != nil && in.ResolveClientScope != nil && in.AuthorizeOperatorTarget != nil {
		if candidate, err := policyhttp.NewHandler(policyhttp.Options{
			Store:                   in.Store,
			FeatureStatus:           in.FeatureStatus,
			ResolveClientScope:      in.ResolveClientScope,
			AuthorizeOperatorTarget: in.AuthorizeOperatorTarget,
			GenerationDefault:       in.GenerationDefault,
			MaxBodyBytes:            in.MaxBodyBytes,
		}); err == nil {
			h = candidate
		}
	}
	mux.Handle(terminalDecisionClientPath, h)
	if cfg != nil && strings.TrimSpace(cfg.Diagnostics.SharedSecret) != "" {
		mux.Handle(terminalDecisionOperatorPath, wrapDiagnostics(cfg, h))
	}
}

func terminalDecisionPolicyUnavailable() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "policy_unavailable"})
	})
}
