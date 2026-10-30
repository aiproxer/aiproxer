package gemini

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Mount registers the Gemini generateContent-compatible handler on mux.
func Mount(mux *http.ServeMux, opts aipsdk.FrontendMountOptions) error {
	cfg, err := DecodeConfig(opts.PluginCfg)
	if err != nil {
		return err
	}
	descriptors, err := RouteDescriptors(ID)
	if err != nil {
		return err
	}
	h := &Handler{
		Exec: opts.Exec, DefaultRouteSelector: opts.DefaultRoute,
		RoutePrefixes: routeselect.NewPrefixSet(opts.RoutePrefixes), MaxRequestBodyBytes: opts.MaxRequestBodyBytes,
		DecodeAdmission: opts.DecodeAdmission, TrafficPorts: opts.TrafficPorts,
		Log:                 diag.LoggerOrDefault(opts.Log),
		PreRequestKeepalive: opts.PreRequestKeepalive, HTTPHeaders: opts.HTTPHeaders,
		StreamKeepaliveInterval: opts.StreamKeepaliveInterval, Config: cfg,
	}
	for _, descriptor := range descriptors {
		mux.Handle(descriptor.MountPattern, h)
	}
	return nil
}
