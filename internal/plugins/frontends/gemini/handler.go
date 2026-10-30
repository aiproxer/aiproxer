package gemini

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

const HeaderRouteSelector = routeselect.HeaderRouteSelector

// Handler wires HTTP POST …/models/{model}:generateContent (and stream variant) to decode → executor → encode.
type Handler struct {
	Exec aipsdk.ExecutorView
	// DefaultRouteSelector is used when HeaderRouteSelector is absent.
	DefaultRouteSelector string
	// RoutePrefixes are backend route-selector prefixes accepted from URL model.
	RoutePrefixes           routeselect.PrefixSet
	MaxRequestBodyBytes     int64
	Log                     *slog.Logger
	TrafficPorts            traffic.PortBundle
	DecodeAdmission         aipsdk.DecodeAdmission
	PreRequestKeepalive     aipsdk.FrontendKeepaliveConfig
	Config                  Config
	HTTPHeaders             aipsdk.HTTPHeaders
	StreamKeepaliveInterval time.Duration

	// pipeOnce serializes the first spec() build; handlers serve concurrent requests.
	pipeOnce sync.Once
	pipe     frontendpipe.Spec[EncodeOptions]
}

func (h *Handler) spec() *frontendpipe.Spec[EncodeOptions] {
	h.pipeOnce.Do(func() {
		h.buildPipe()
	})
	return &h.pipe
}

func (h *Handler) buildPipe() {
	h.pipe = frontendpipe.Spec[EncodeOptions]{
		Config: frontendpipe.Config{
			Exec:                    h.Exec,
			DefaultRouteSelector:    h.DefaultRouteSelector,
			RoutePrefixes:           h.RoutePrefixes,
			MaxRequestBodyBytes:     h.MaxRequestBodyBytes,
			Log:                     diag.LoggerOrDefault(h.Log),
			TrafficPorts:            h.TrafficPorts,
			DecodeAdmission:         h.DecodeAdmission,
			PreRequestKeepalive:     h.PreRequestKeepalive,
			FrontendID:              ID,
			HTTPHeaders:             h.HTTPHeaders,
			StreamKeepaliveInterval: h.StreamKeepaliveInterval,
		},
		Wire: WireErrors{},
		ResolvePreAdmissionRoute: func(headers http.Header, pm frontendpipe.PathMatch) routing.ResolvedRoute {
			if sel := h.HTTPHeaders.RouteSelector(headers); sel != "" {
				return routing.ExplicitRoute(sel)
			}
			return h.RoutePrefixes.ResolveInlineOrDefault(pm.Model, h.DefaultRouteSelector)
		},
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			model, stream, ok := ParseGenerateContentPath(path)
			if !ok {
				return frontendpipe.PathMatch{}, false
			}
			return frontendpipe.PathMatch{Model: model, Stream: stream}, true
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			decoded, err := DecodeGenerateContentRequest(dctx.Body, DecodeOptions{
				RouteSelector: dctx.RouteSelector,
				Model:         dctx.Path.Model,
				Stream:        dctx.Path.Stream,
				Headers:       dctx.Headers,
				HTTPHeaders:   h.HTTPHeaders,
			})
			if err != nil {
				return nil, err
			}
			return &frontendpipe.Decoded{Call: decoded.Call, Stream: dctx.Path.Stream, RouteSelector: dctx.RouteSelector}, nil
		},
		BuildEncodeOpts: func(_ *frontendpipe.Decoded) EncodeOptions {
			return EncodeOptions{ExposeAipUsageExtensions: h.Config.ExposeAipUsageExtensions}
		},
		WriteStream:    WriteStreamSSE,
		WriteNonStream: WriteNonStreamJSON,
	}
}

// ServeHTTP implements generateContent / streamGenerateContent for the Google AI (ML dev) layout.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	frontendpipe.ServeHTTP(h.spec(), w, r)
}
