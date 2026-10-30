package stdhttp

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/modelview"
)

// NewCatalogStatusHandler serves GET JSON from [modelcatalog.BuildCatalogDiagnosticsJSON] for the request time.
// log is the process HTTP logger (same as [Run]); if nil, [slog.Default] is used.
// When the request carries a bound catalog view, diagnostics use that snapshot
// rather than rereading the live runtime (req 9.6).
func NewCatalogStatusHandler(log *slog.Logger, cfg modelcatalog.CatalogStatusHandlerConfig) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			if r.Method == http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(true)
		reqCfg := cfg
		if r != nil {
			if bv, ok := modelcatalog.BoundViewFromContext(r.Context()); ok {
				reqCfg.BoundSnapshot = bv
				reqCfg.PreferBound = true
			}
			if id, ok := modelview.FromContext(r.Context()); ok {
				reqCfg.ModelViewIdentity = id.SafeFields()
			}
		}
		v := modelcatalog.BuildCatalogDiagnosticsJSON(reqCfg)
		if err := enc.Encode(v); err != nil {
			log.ErrorContext(r.Context(), "modelcatalog: catalog status encode", "err", err)
		}
	})
}
