package openairesponses

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Mount registers the OpenAI Responses API handler on mux.
func Mount(mux *http.ServeMux, opts aipsdk.FrontendMountOptions) error {
	cfg, err := DecodeConfig(opts.PluginCfg)
	if err != nil {
		return err
	}
	var lpCfg frontendpipe.LargePayloadConfig
	if c, ok := opts.LargePayload.(frontendpipe.LargePayloadConfig); ok {
		lpCfg = c
	}
	mux.Handle("/v1/responses", &Handler{
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
		LargePayload:            lpCfg,
	})
	return nil
}
