package anthropic

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

const (
	HeaderRouteSelector    = routeselect.HeaderRouteSelector
	HeaderAnthropicVersion = "anthropic-version"
)

// Handler wires HTTP POST /v1/messages to decode → executor → encode.
type Handler struct {
	Exec aipsdk.ExecutorView
	// DefaultRouteSelector is used when HeaderRouteSelector is absent.
	DefaultRouteSelector string
	// RoutePrefixes are backend route-selector prefixes accepted from body model.
	RoutePrefixes           routeselect.PrefixSet
	MaxRequestBodyBytes     int64
	Log                     *slog.Logger
	TrafficPorts            traffic.PortBundle
	DecodeAdmission         aipsdk.DecodeAdmission
	PreRequestKeepalive     aipsdk.FrontendKeepaliveConfig
	Config                  Config
	HTTPHeaders             aipsdk.HTTPHeaders
	StreamKeepaliveInterval time.Duration
	// hybridExternal enables inline system and canonical tool-result roles only for
	// the private hybrid external handler; the ordinary frontend is unchanged.
	hybridExternal bool

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
		Wire:               WireErrors{},
		RouteFromBodyModel: true,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			if strings.HasSuffix(path, "/messages") || path == "/messages" {
				return frontendpipe.PathMatch{}, true
			}
			return frontendpipe.PathMatch{}, false
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			decoded, err := DecodeMessageRequest(dctx.Body, DecodeOptions{
				RouteSelector:    dctx.RouteSelector,
				AnthropicVersion: dctx.AnthropicVersion,
				Headers:          dctx.Headers,
				HTTPHeaders:      h.HTTPHeaders,
				hybridExternal:   h.hybridExternal,
			})
			if err != nil {
				return nil, err
			}
			if h.hybridExternal {
				decoded.Call.Messages = hybridToolResultMessages(decoded.Call.Messages)
				// Stock CLI carries the full worker history. Do not accept proxy
				// resume authority from that untrusted body: a fresh A-leg also
				// cannot inherit an override retained by an older generation.
				decoded.Call.Session = aipapi.SessionRef{}
			}
			return &frontendpipe.Decoded{Call: decoded.Call, Stream: decoded.Stream, RouteSelector: dctx.RouteSelector}, nil
		},
		BuildEncodeOpts: func(decoded *frontendpipe.Decoded) EncodeOptions {
			call := decoded.Call
			return EncodeOptions{
				MessageID:                "msg_" + diag.StableCallToken(call),
				ExposeAipUsageExtensions: h.Config.ExposeAipUsageExtensions,
			}
		},
		WriteStream:    WriteStreamSSE,
		WriteNonStream: WriteNonStreamJSON,
	}
}

// ServeHTTP implements Messages create on POST …/messages.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	frontendpipe.ServeHTTP(h.spec(), w, r)
}
