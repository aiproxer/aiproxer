package openresponses

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	httpauth "github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// ExecutorView is the canonical event-stream executor used by create.
type ExecutorView interface {
	Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error)
}

// HandlerConfig configures the HTTP handler for OpenResponses.
type HandlerConfig struct {
	Authorizer            Authorizer
	RequireAuthentication bool
	AllowUnauthenticated  bool
	Executor              ExecutorView
	// ContinuationResolver is the narrow injected seam for resolving parent continuation state.
	ContinuationResolver ContinuationResolver
	// ContinuationStore is the injected protocol-neutral store port for continuation
	// state. When nil, continuation features degrade gracefully.
	ContinuationStore       aipcont.Store
	DefaultRouteSelector    string
	RoutePrefixes           []string
	MaxRequestBodyBytes     int64
	ProtocolLimits          proto.Limits
	DecodeAdmission         aipsdk.DecodeAdmission
	TrafficPorts            traffic.PortBundle
	PreRequestKeepalive     aipsdk.FrontendKeepaliveConfig
	Config                  Config
	HTTPHeaders             aipsdk.HTTPHeaders
	StreamKeepaliveInterval time.Duration
	ResponseIDSource        ResponseIDSource
	CompactResourceIDSource CompactResourceIDSource
	ResponseClock           ResponseClock
	// RecorderFactory is the narrow seam for incremental terminal recording.
	// A nil factory uses the standard core recorder.
	RecorderFactory ContinuationRecorderFactory
	// Profile optionally enables large-payload fast-path candidate evaluation (Task 17).
	Profile frontendpipe.FrontendProfile
	// LargePayload configures the large-payload fast-path candidate limits (Task 17).
	LargePayload frontendpipe.LargePayloadConfig
	// Log is the logger the frontend reports its internal failures through.
	// A nil value leaves the frontend on the standard library default logger.
	Log *slog.Logger
}

// Handler wires OpenResponses HTTP requests to auth → decode → executor. Direct
// handlers require an authenticated transport context by default; callers must
// explicitly opt into anonymous access with AllowUnauthenticated.
type Handler struct {
	cfg      HandlerConfig
	pipeOnce sync.Once
	pipe     frontendpipe.Spec[createEncodeState]
}

// Spec returns the configured frontendpipe.Spec for this handler.
func (h *Handler) Spec() *frontendpipe.Spec[createEncodeState] {
	return h.spec()
}

// NewHandler creates a new OpenResponses HTTP handler.
func NewHandler(cfg HandlerConfig) *Handler {
	if !cfg.AllowUnauthenticated {
		cfg.RequireAuthentication = true
	}
	return &Handler{cfg: cfg}
}

var _ http.Handler = (*Handler)(nil)

func (h *Handler) getStore() aipcont.Store {
	return h.cfg.ContinuationStore
}

// ServeHTTP handles HTTP requests for OpenResponses endpoints.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Method first so non-POST requests stay 405 even without application/json.
	// Auth and JSON Content-Type stay on the handler: frontendpipe does not
	// enforce application/json. Keepalive and body admission live in the pipe.
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dec, ok := h.authorizeCreate(w, r)
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(r.Header.Get("Content-Type")))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeWireError(w, http.StatusUnsupportedMediaType, "invalid_request_error", "unsupported_media_type", "Request Content-Type must be application/json")
		return
	}
	r = r.WithContext(contextWithAuthDecision(r.Context(), dec))
	frontendpipe.ServeHTTP(h.spec(), w, r)
}

func (h *Handler) authorizeCreate(w http.ResponseWriter, r *http.Request) (sdkauth.Decision, bool) {
	ctx := r.Context()
	if h.cfg.Authorizer != nil {
		opts := DecodeCreateOptions{
			Auth:        h.cfg.Authorizer,
			Headers:     r.Header,
			Method:      r.Method,
			Path:        r.URL.Path,
			RemoteAddr:  r.RemoteAddr,
			HTTPHeaders: h.cfg.HTTPHeaders,
		}
		dec, err := opts.Auth.Authenticate(ctx, opts.authMeta(r))
		if err != nil || dec.Outcome != sdkauth.OutcomeAllow {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(proto.WireErrorEnvelope{
				Error: proto.WireErrorDetails{
					Message: "Authentication required",
					Type:    "authentication_error",
					Code:    "unauthorized",
				},
			})
			return sdkauth.Decision{}, false
		}
		return dec, true
	}
	if h.cfg.RequireAuthentication {
		if _, ok := httpauth.PrincipalFromContext(ctx); !ok {
			writeWireError(w, http.StatusUnauthorized, "authentication_error", "unauthorized", "Authentication required")
			return sdkauth.Decision{}, false
		}
	}
	return sdkauth.Decision{}, true
}

// cleanupContinuationReservation runs independently of a canceled request so
// failed execution cannot strand a reserved proxy ID until its TTL expires.
func cleanupContinuationReservation(store aipcont.Store, scope aipcont.Scope, responseID string) {
	if store == nil || responseID == "" {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = store.Delete(cleanupCtx, scope, aipcont.ResponseID(responseID))
}

func continuationScope(decoded *DecodedCreate) aipcont.Scope {
	scope := aipcont.Scope{SessionID: strings.TrimSpace(decoded.Call.Session.AuthoritativeSessionID)}
	if decoded.AuthDecision.Scope != nil {
		scope.TenantID = decoded.AuthDecision.Scope.TenantID.String()
		scope.PrincipalID = decoded.AuthDecision.Scope.PrincipalID.String()
	}
	if scope.PrincipalID == "" {
		scope.PrincipalID = decoded.AuthDecision.Principal.ID
	}
	if scope.PrincipalID == "" {
		scope.PrincipalID = scope.SessionID
	}
	return scope
}

func (h *Handler) storagePolicy() aipcont.StoragePolicy {
	ttl, err := time.ParseDuration(h.cfg.Config.Continuation.TTL)
	if err != nil || ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return aipcont.StoragePolicy{Mode: aipcont.PersistencePersistent, TTL: ttl, Limits: aipcont.DefaultStorageLimits()}
}

// safeCanonicalErrorMessage applies a strict allowlist to canonical messages
// crossing the wire. Error codes remain authoritative for programmatic
// handling; arbitrary provider text is never exposed by default.
func safeCanonicalErrorMessage(message string) string {
	switch strings.TrimSpace(message) {
	case "Backend execution failed",
		"Rate limit exceeded",
		"request processing timed out",
		"request was canceled by client",
		"upstream reported a compaction failure",
		"upstream reported an error",
		"stream error",
		"bad request",
		"failed":
		return strings.TrimSpace(message)
	default:
		return "Backend execution failed"
	}
}

// classifyExecutionError maps a failed execution or stream-collect error to the
// OpenResponses wire envelope. Wire-specific branches stay here: a canonical
// stream failure carries the upstream error code through
// classifyCanonicalEventError, and client cancel/timeout keep their fixed
// codes. Every other error is classified by the shared execerr.ClassifyExecute
// contract and mapped by mapExecuteOutcome, so executor failures keep the same
// status, message, and kind grouping as every other frontend.
func classifyExecutionError(err error) (status int, typ, code, message string) {
	if streamErr, ok := errors.AsType[*aipapi.StreamError](err); ok {
		return classifyCanonicalEventError(aipapi.Event{
			Kind:         aipapi.EventError,
			ErrorCode:    streamErr.Code,
			ErrorMessage: streamErr.Message,
		})
	}
	if errors.Is(err, context.Canceled) {
		return http.StatusBadRequest, "invalid_request_error", "client_closed_request", "Request canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, "timeout_error", "timeout", "Request timed out"
	}
	return mapExecuteOutcome(execerr.ClassifyExecute(err))
}

func writeWireError(w http.ResponseWriter, status int, typ, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(proto.WireErrorEnvelope{
		Error: proto.WireErrorDetails{Message: message, Type: typ, Code: code},
	})
}

func (opts DecodeCreateOptions) authMeta(r *http.Request) sdkauth.InboundCallMeta {
	return sdkauth.InboundCallMeta{
		Frontend:            ID,
		Method:              r.Method,
		Path:                r.URL.Path,
		ClientAddr:          r.RemoteAddr,
		AuthorizationBearer: opts.HTTPHeaders.APIKeyFrom(r.Header),
	}
}

func (h *Handler) writeCompact(ctx context.Context, w http.ResponseWriter, es aipapi.EventStream, decoded *DecodedCompact) error {
	if decoded == nil || decoded.Call == nil {
		writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
		return nil
	}
	if es == nil {
		writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
		return nil
	}
	clock := h.cfg.ResponseClock
	if clock == nil {
		clock = systemResponseClock{}
	}
	ids := h.cfg.CompactResourceIDSource
	if ids == nil {
		ids = systemCompactResourceIDSource{}
	}
	resource, collectErr := collectCompact(ctx, es, compactEnvelope(decoded, ids.NewCompactResourceID(), clock.Now()), decoded.Call.Options, h.cfg.ProtocolLimits)
	if collectErr != nil {
		if ctx.Err() != nil {
			return nil
		}
		status, typ, code, message := classifyExecutionError(collectErr)
		writeWireError(w, status, typ, code, message)
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resource)
	return nil
}
