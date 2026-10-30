package openresponses

import (
	"strings"

	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

// Diagnostics holds sanitized operator-visible diagnostic metadata for OpenResponses frontend.
type Diagnostics struct {
	Profile                  string                    `json:"profile"`
	BasePath                 string                    `json:"base_path"`
	WebSocketEnabled         bool                      `json:"websocket_enabled"`
	WebSocketMaxQueuedTurns  int                       `json:"websocket_max_queued_turns"`
	WebSocketMaxQueuedBytes  int64                     `json:"websocket_max_queued_bytes"`
	WebSocketDevelopmentMode bool                      `json:"websocket_development_mode"`
	WebSocketAllowAnyOrigin  bool                      `json:"websocket_allow_any_origin"`
	AllowedOrigins           []string                  `json:"allowed_origins"`
	ContinuationStore        string                    `json:"continuation_store"`
	ContinuationTTL          string                    `json:"continuation_ttl"`
	RouteClaims              []httpcontract.RouteClaim `json:"route_claims"`
}

// SanitizedDiagnostics produces sanitized diagnostic information for a Config and owner ID.
// It never fails: even a malformed config still yields sanitized values for display.
// Route claims are best-effort — a claims error (e.g. invalid config) yields nil claims
// rather than hiding the sanitized diagnostic view.
func SanitizedDiagnostics(cfg Config, ownerID string) Diagnostics {
	claims, err := RouteClaimsForOwner(cfg, sanitizeString(ownerID))
	if err != nil {
		claims = nil
	}
	origins := make([]string, 0, len(cfg.WebSocket.AllowedOrigins))
	for _, o := range cfg.WebSocket.AllowedOrigins {
		origins = append(origins, sanitizeString(o))
	}

	return Diagnostics{
		Profile:                  sanitizeString(cfg.Profile),
		BasePath:                 sanitizeString(cfg.BasePath),
		WebSocketEnabled:         cfg.WebSocket.IsEnabled(),
		WebSocketMaxQueuedTurns:  cfg.WebSocket.MaxQueuedTurns,
		WebSocketMaxQueuedBytes:  cfg.WebSocket.MaxQueuedBytes,
		WebSocketDevelopmentMode: cfg.WebSocket.DevelopmentMode,
		WebSocketAllowAnyOrigin:  cfg.WebSocket.AllowAnyOrigin,
		AllowedOrigins:           origins,
		ContinuationStore:        sanitizeString(cfg.Continuation.PersistentStore),
		ContinuationTTL:          sanitizeString(cfg.Continuation.TTL),
		RouteClaims:              claims,
	}
}

func sanitizeString(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\t", "")
	s = strings.TrimSpace(s)
	if len(s) > 256 {
		s = s[:256]
	}
	return s
}
