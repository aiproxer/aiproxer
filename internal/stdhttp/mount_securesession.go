package stdhttp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	ssessiondiag "github.com/aiproxer/aiproxer/internal/core/securesession/adapters/diag"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
)

// mountSecureSessionDiagnosticsInput carries inputs for [mountSecureSessionDiagnostics].
type mountSecureSessionDiagnosticsInput struct {
	LogCtx   context.Context
	Mux      *http.ServeMux
	Cfg      *config.Config
	Log      *slog.Logger
	Security HTTPSecurityInput
	// Metrics lends only the collapse detector for advisory snapshots; nil keeps the pre-health shape.
	Metrics *metrics.Bundle
}

// mountSecureSessionDiagnostics mounts the secure-session diagnostics summary endpoints when
// secure sessions are effectively enabled, summary exposure is requested, and a store is wired.
// Errors are returned with the same wrapping the inline block previously used so
// ComposeStandardHTTP's error chain stays identical.
func mountSecureSessionDiagnostics(in mountSecureSessionDiagnosticsInput) error {
	mux, cfg, log, sec := in.Mux, in.Cfg, in.Log, in.Security
	if !cfg.SecureSessionEffectivelyEnabled() || !cfg.SecureSession.DiagnosticsExposeSummaries || sec.SecureSessionStore == nil {
		return nil
	}
	p := strings.TrimSpace(cfg.SecureSession.DiagnosticsPathPrefix)
	if p == "" {
		return fmt.Errorf(
			"stdhttp: secure_session diagnostics_expose_summaries requires " +
				"secure_session.diagnostics_path_prefix",
		)
	}
	base := strings.TrimSuffix(p, "/")
	var health ssessiondiag.CacheHealthReader
	if in.Metrics != nil && in.Metrics.PromptCacheCollapse != nil {
		health = in.Metrics.PromptCacheCollapse
	}
	ssh, err := ssessiondiag.NewHandlerWithCacheHealth(
		base,
		sec.SecureSessionStore,
		cfg.SecureSession.RedactionDefault,
		nil,
		log,
		health,
	)
	if err != nil {
		return fmt.Errorf("stdhttp: secure-session diagnostics handler: %w", err)
	}
	dh := wrapDiagnostics(cfg, ssh)
	mux.Handle("GET "+base+"/", dh)
	mux.Handle("GET "+base, dh)
	log.InfoContext(in.LogCtx, "secure-session diagnostics mounted", "path", base)
	return nil
}
