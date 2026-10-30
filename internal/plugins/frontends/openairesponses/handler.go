package openairesponses

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openaiwire"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

const (
	HeaderRouteSelector  = routeselect.HeaderRouteSelector
	responseIDALegPrefix = "resp_aip_"
)

// Handler wires HTTP POST /v1/responses to decode → executor → encode.
type Handler struct {
	Exec aipsdk.ExecutorView
	// DefaultRouteSelector is used when HeaderRouteSelector is absent.
	DefaultRouteSelector string
	// RoutePrefixes are backend route-selector prefixes accepted from body model.
	RoutePrefixes routeselect.PrefixSet
	// MaxRequestBodyBytes caps the request body; zero uses reqbody.DefaultMaxBytes.
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

type aLegCanceler interface {
	CancelALeg(ctx context.Context, req aipapi.ALegCancelRequest) error
}

type responseIDCancelCarrier struct {
	ALegID    string `json:"a"`
	SessionID string `json:"s,omitempty"`
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
			Log:                     h.logger(),
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
			if strings.HasSuffix(path, "/responses") || path == "/responses" {
				return frontendpipe.PathMatch{}, true
			}
			return frontendpipe.PathMatch{}, false
		},
		AltServe: func(ctx context.Context, w http.ResponseWriter, r *http.Request) bool {
			if isCancelPath(r.URL.Path) {
				h.serveCancel(ctx, w, r)
				return true
			}
			return false
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			decoded, err := DecodeCreateRequest(dctx.Body, DecodeOptions{RouteSelector: dctx.RouteSelector, Headers: dctx.Headers, HTTPHeaders: h.HTTPHeaders})
			if err != nil {
				return nil, err
			}
			return &frontendpipe.Decoded{Call: decoded.Call, Stream: decoded.Stream, RouteSelector: dctx.RouteSelector}, nil
		},
		BuildEncodeOpts: func(decoded *frontendpipe.Decoded) EncodeOptions {
			call := decoded.Call
			opts := EncodeOptions{
				ResponseID:               responseIDForCall(call),
				CreatedAt:                diag.StableUnix(call),
				ExposeAipUsageExtensions: h.Config.ExposeAipUsageExtensions,
			}
			opts.MessageID = "msg_" + opts.ResponseID
			if clk := h.Exec.WallClock(); clk != nil {
				opts.CreatedAt = clk().Unix()
			}
			return opts
		},
		WriteStream:    WriteStreamSSE,
		WriteNonStream: WriteNonStreamJSON,
		WireWriteStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
			return wireWriteStreamSSEWithOptions(ctx, w, rc, es, EncodeOptions{
				ResponseID:               rc.OpenAIResponseID(),
				MessageID:                rc.OpenAIMessageID(),
				CreatedAt:                h.selectWireCreatedAt(rc),
				ExposeAipUsageExtensions: h.Config.ExposeAipUsageExtensions,
			})
		},
		WireWriteNonStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
			return wireWriteNonStreamJSONWithOptions(ctx, w, rc, es, EncodeOptions{
				ResponseID:               rc.OpenAIResponseID(),
				MessageID:                rc.OpenAIMessageID(),
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

// ServeHTTP implements OpenAI Responses create on POST …/responses.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	frontendpipe.ServeHTTP(h.spec(), w, r)
}

func isCancelPath(path string) bool {
	path = strings.TrimSpace(path)
	return strings.HasSuffix(path, "/cancel") && responseIDFromCancelPath(path) != ""
}

func (h *Handler) logWriteJSONErr(ctx context.Context, msg string, werr error) {
	if werr == nil {
		return
	}
	diag.LogError(ctx, h.logger(), msg, diag.AttrOpts{}, werr)
}

// logger returns the handler's diagnostics logger. It never returns nil, so a
// handler built without an explicit logger still reports its internal
// failures.
func (h *Handler) logger() *slog.Logger {
	return diag.LoggerOrDefault(h.Log)
}

func (h *Handler) serveCancel(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if h.Exec == nil {
		h.logWriteJSONErr(ctx, "write error json failed", openaiwire.WriteErrorJSON(
			w,
			http.StatusInternalServerError,
			"executor not configured",
			"api_error",
			"",
		))
		return
	}
	canceler, ok := h.Exec.(aLegCanceler)
	if !ok {
		h.logWriteJSONErr(ctx, "write error json failed", openaiwire.WriteErrorJSON(
			w,
			http.StatusNotImplemented,
			"response cancellation is not configured",
			"api_error",
			"",
		))
		return
	}
	aLegID := strings.TrimSpace(r.Header.Get(sessionwire.HeaderALegID))
	responseID := responseIDFromCancelPath(r.URL.Path)
	sessionID := strings.TrimSpace(r.Header.Get(sessionwire.HeaderAuthoritativeSessionID))
	if aLegID == "" {
		decodedALegID, decodedSessionID, ok := cancelCarrierFromResponseID(responseID)
		if !ok {
			h.logWriteJSONErr(ctx, "write error json failed", openaiwire.WriteErrorJSON(
				w,
				http.StatusBadRequest,
				"missing A-leg cancellation carrier",
				"invalid_request_error",
				"",
			))
			return
		}
		aLegID = decodedALegID
		if sessionID == "" {
			sessionID = decodedSessionID
		}
		if sessionID == "" {
			h.logWriteJSONErr(ctx, "write error json failed", openaiwire.WriteErrorJSON(
				w,
				http.StatusBadRequest,
				"missing session cancellation carrier",
				"invalid_request_error",
				"",
			))
			return
		}
	}
	if err := canceler.CancelALeg(ctx, aipapi.ALegCancelRequest{
		ALegID:      aLegID,
		SessionID:   sessionID,
		ResumeToken: r.Header.Get(sessionwire.HeaderResumeToken),
		FrontendID:  ID,
		Reason:      "openai_responses_cancel",
	}); err != nil {
		diag.LogError(ctx, h.logger(), "cancel a-leg failed", diag.AttrOpts{}, err)
		status, msg, typ := cancelErrorWire(err)
		h.logWriteJSONErr(ctx, "write error json failed", openaiwire.WriteErrorJSON(w, status, msg, typ, ""))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		ID     string `json:"id"`
		Object string `json:"object"`
		Status string `json:"status"`
	}{
		ID:     responseID,
		Object: "response",
		Status: "cancelled",
	})
}

func responseIDForCall(call *aipapi.Call) string {
	if call != nil {
		if aLegID := strings.TrimSpace(call.Session.ALegID); aLegID != "" {
			carrier := responseIDCancelCarrier{
				ALegID:    aLegID,
				SessionID: strings.TrimSpace(call.Session.AuthoritativeSessionID),
			}
			raw, err := json.Marshal(carrier)
			if err == nil {
				return responseIDALegPrefix + base64.RawURLEncoding.EncodeToString(raw)
			}
		}
	}
	return "resp_" + diag.StableCallToken(call)
}

func cancelCarrierFromResponseID(responseID string) (string, string, bool) {
	encoded, ok := strings.CutPrefix(strings.TrimSpace(responseID), responseIDALegPrefix)
	if !ok || encoded == "" {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", false
	}
	var carrier responseIDCancelCarrier
	if err := json.Unmarshal(raw, &carrier); err == nil {
		aLegID := strings.TrimSpace(carrier.ALegID)
		if aLegID == "" {
			return "", "", false
		}
		return aLegID, strings.TrimSpace(carrier.SessionID), true
	}
	aLegID := strings.TrimSpace(string(raw))
	if aLegID == "" {
		return "", "", false
	}
	return aLegID, "", true
}

func responseIDFromCancelPath(path string) string {
	path = strings.Trim(strings.TrimSpace(path), "/")
	parts := strings.Split(path, "/")
	for i := len(parts) - 3; i >= 0; i-- {
		if parts[i] == "responses" && parts[i+2] == "cancel" {
			return parts[i+1]
		}
	}
	return ""
}

func cancelErrorWire(err error) (int, string, string) {
	switch {
	case errors.Is(err, domain.ErrMissingPrincipal):
		return http.StatusUnauthorized, "missing cancellation identity", "authentication_error"
	case errors.Is(err, domain.ErrOwnerMismatch):
		return http.StatusForbidden, "cancellation forbidden", "invalid_request_error"
	case errors.Is(err, domain.ErrSessionNotFound):
		return http.StatusNotFound, "cancellation target not found", "invalid_request_error"
	default:
		return http.StatusInternalServerError, "internal error", "api_error"
	}
}
