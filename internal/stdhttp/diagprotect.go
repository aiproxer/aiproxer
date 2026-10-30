package stdhttp

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
)

func wrapDiagnostics(cfg *config.Config, next http.Handler) http.Handler {
	if cfg == nil {
		return diag.WrapDiagnosticsProtect("", next)
	}
	return diag.WrapDiagnosticsProtectHeaders(cfg.Diagnostics.SharedSecret, cfg.HTTPHeaders.Effective().DiagnosticsSecret, next)
}
