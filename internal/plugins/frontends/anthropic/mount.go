package anthropic

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Mount registers the Anthropic Messages API handler on mux.
func Mount(mux *http.ServeMux, opts aipsdk.FrontendMountOptions) error {
	cfg, err := DecodeConfig(opts.PluginCfg)
	if err != nil {
		return err
	}
	claims, err := RouteClaims(ID)
	if err != nil {
		return err
	}
	for _, claim := range claims {
		makeHandler := func() *Handler {
			return &Handler{
				Exec:                    opts.Exec,
				DefaultRouteSelector:    opts.DefaultRoute,
				RoutePrefixes:           routeselect.NewPrefixSet(opts.RoutePrefixes),
				MaxRequestBodyBytes:     opts.MaxRequestBodyBytes,
				DecodeAdmission:         opts.DecodeAdmission,
				Log:                     diag.LoggerOrDefault(opts.Log),
				TrafficPorts:            opts.TrafficPorts,
				PreRequestKeepalive:     opts.PreRequestKeepalive,
				HTTPHeaders:             opts.HTTPHeaders,
				StreamKeepaliveInterval: opts.StreamKeepaliveInterval,
				Config:                  cfg,
			}
		}
		var handler http.Handler = makeHandler()
		if cfg.ClaudeCodeHybrid.Enabled {
			hybrid := newHybridHandler(handler, cfg.ClaudeCodeHybrid, opts)
			external := makeHandler()
			external.hybridExternal = true
			// Only the trusted injected default header participates in routing
			// on the fresh external request, even in custom/direct mounts.
			external.HTTPHeaders.Route = []string{aipsdk.HeaderRoute}
			hybrid.external = external
			handler = hybrid
		}
		mux.Handle(claim.Path, handler)
	}
	return nil
}
