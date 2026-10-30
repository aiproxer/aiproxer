package systemone

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Mount registers System One behind the host's shared authentication boundary.
func Mount(mux *http.ServeMux, opts aipsdk.FrontendMountOptions) error {
	cfg, err := DecodeConfig(opts.PluginCfg)
	if err != nil {
		return err
	}
	claims, err := RouteClaims(ID)
	if err != nil {
		return err
	}
	spec := handlerSpec(opts, cfg)
	for _, claim := range claims {
		mux.Handle(claim.Path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			frontendpipe.ServeHTTP(spec, w, r)
		}))
	}
	return nil
}
