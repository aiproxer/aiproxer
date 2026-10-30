package systemone

import (
	"context"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func handlerSpec(opts aipsdk.FrontendMountOptions, cfg Config) *frontendpipe.Spec[struct{}] {
	prefixes := routeselect.NewPrefixSet(opts.RoutePrefixes)
	return &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: opts.Exec, DefaultRouteSelector: opts.DefaultRoute, RoutePrefixes: prefixes,
			MaxRequestBodyBytes: opts.MaxRequestBodyBytes, DecodeAdmission: opts.DecodeAdmission,
			Log:          diag.LoggerOrDefault(opts.Log),
			TrafficPorts: opts.TrafficPorts, HTTPHeaders: opts.HTTPHeaders, FrontendID: ID,
		},
		Wire: WireErrors{}, ClassifyExecute: classifyExecute,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, path == "/v1/systemone"
		},
		ResolveBodyRoute: func(body []byte, _ frontendpipe.PathMatch) routing.ResolvedRoute {
			// Route only from the body model/operator default, not client headers.
			// Runs under decode admission; authoritative even when empty.
			return prefixes.ResolveFromModelOrDefault(body, opts.DefaultRoute)
		},
		Decode: func(ctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			call, err := decodeRequest(ctx.Body, cfg.MaxQuestions)
			if err != nil {
				return nil, err
			}
			if ctx.RouteSelector != "" {
				call.Route.Selector = ctx.RouteSelector
			}
			return &frontendpipe.Decoded{Call: call, RouteSelector: call.Route.Selector}, nil
		},
		BuildEncodeOpts: func(*frontendpipe.Decoded) struct{} { return struct{}{} },
		WriteNonStream: func(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, stream aipapi.EventStream, _ struct{}) error {
			return writeResponse(ctx, w, call, stream)
		},
	}
}
