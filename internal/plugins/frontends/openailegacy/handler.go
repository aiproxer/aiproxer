package openailegacy

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openaiwire"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

const HeaderRouteSelector = routeselect.HeaderRouteSelector

// Handler wires HTTP POST …/chat/completions to decode → executor → encode.
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
	// Profile optionally overrides the large-payload fast-path profile (defaults to NewProfile()).
	Profile frontendpipe.FrontendProfile
	// LargePayload configures the large-payload fast-path candidate limits.
	LargePayload frontendpipe.LargePayloadConfig

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

// Spec returns the configured frontendpipe.Spec for this handler.
func (h *Handler) Spec() *frontendpipe.Spec[EncodeOptions] {
	return h.spec()
}

func (h *Handler) buildPipe() {
	prof := h.Profile
	if prof == nil {
		prof = NewProfile()
	}
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
			LargePayload:            h.LargePayload,
		},
		Wire:               frontendpipe.OpenAIWire{},
		Profile:            prof,
		RouteFromBodyModel: true,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			if strings.HasSuffix(path, "/chat/completions") || path == "/chat/completions" {
				return frontendpipe.PathMatch{}, true
			}
			return frontendpipe.PathMatch{}, false
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			decoded, err := DecodeChatRequest(dctx.Body, DecodeOptions{RouteSelector: dctx.RouteSelector, Headers: dctx.Headers, HTTPHeaders: h.HTTPHeaders})
			if err != nil {
				return nil, err
			}
			return &frontendpipe.Decoded{Call: decoded.Call, Stream: decoded.Stream, RouteSelector: dctx.RouteSelector}, nil
		},
		BuildEncodeOpts: func(decoded *frontendpipe.Decoded) EncodeOptions {
			call := decoded.Call
			// One canonical sum feeds both wire IDs: StableCallToken and
			// StableUnix each re-marshal the whole call, and the executor
			// clock (when present) discards the deterministic timestamp, so
			// the sum is computed lazily and at most once.
			var sum [32]byte
			summed := false
			getSum := func() [32]byte {
				if !summed {
					sum = diag.StableCallSum(call)
					summed = true
				}
				return sum
			}
			opts := EncodeOptions{
				CompletionID:             "chatcmpl_" + diag.StableCallTokenFromSum(getSum()),
				ExposeAipUsageExtensions: h.Config.ExposeAipUsageExtensions,
			}
			if clk := h.Exec.WallClock(); clk != nil {
				opts.CreatedAt = clk().Unix()
			} else {
				opts.CreatedAt = diag.StableUnixFromSum(getSum())
			}
			return opts
		},
		WriteStream:    WriteStreamSSE,
		WriteNonStream: WriteNonStreamJSON,
		WireWriteStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
			return wireWriteStreamSSEWithOptions(ctx, w, rc, es, EncodeOptions{
				CompletionID:             rc.OpenAIChatCompletionID(),
				CreatedAt:                h.selectWireCreatedAt(rc),
				ExposeAipUsageExtensions: h.Config.ExposeAipUsageExtensions,
			})
		},
		WireWriteNonStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
			return wireWriteNonStreamJSONWithOptions(ctx, w, rc, es, EncodeOptions{
				CompletionID:             rc.OpenAIChatCompletionID(),
				CreatedAt:                h.selectWireCreatedAt(rc),
				ExposeAipUsageExtensions: h.Config.ExposeAipUsageExtensions,
			})
		},
	}
}

// selectWireCreatedAt samples one creation timestamp for a wire response,
// mirroring canonical BuildEncodeOpts precedence: the configured executor wall
// clock wins when present, otherwise the deterministic seeds apply. Each
// wire-writer closure calls this once per response and reuses the value across
// its frames; identity seeds, IDs, and carriers are never derived from it.
func (h *Handler) selectWireCreatedAt(rc frontendpipe.ResponseContext) int64 {
	if h.Exec != nil {
		if clk := h.Exec.WallClock(); clk != nil {
			return clk().Unix()
		}
	}
	if ts := rc.DeterministicTimestamp(); ts != 0 {
		return ts
	}
	return time.Now().Unix()
}

// ServeHTTP implements Chat Completions create on POST …/chat/completions.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	frontendpipe.ServeHTTP(h.spec(), w, r)
}

// WriteErrorJSON writes an OpenAI-shaped JSON error (delegates to openaiwire).
func WriteErrorJSON(w http.ResponseWriter, status int, message, errType, code string) error {
	return openaiwire.WriteErrorJSON(w, status, message, errType, code)
}
