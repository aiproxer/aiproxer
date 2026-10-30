package codex

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const DefaultBaseURL = "https://chatgpt.com/backend-api/codex"

const (
	DefaultOAuthTokenURL = "https://auth.openai.com/oauth/token"
	DefaultOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
)

// Transport mode constants for the Codex backend.
const (
	TransportAuto      = "auto"
	TransportHTTPS     = "https"
	TransportWebSocket = "websocket"
)

// DefaultWebSocketFallbackCooldown is the negative-cache window used when an auto
// transport WebSocket attempt fails before the first canonical event. During the
// cooldown, auto mode skips WebSocket and goes straight to HTTPS to avoid
// repeated dial/handshake latency on known-broken environments.
const DefaultWebSocketFallbackCooldown = 300 * time.Second

// DefaultEarlySessionVerbosityBumpTurns is the default number of leading
// per-conversation turns for which the early-session verbosity bump forces
// text.verbosity=high when no explicit per-request verbosity is set.
const DefaultEarlySessionVerbosityBumpTurns = 5

// DefaultMidSessionVerbosityBumpFrequency is the default periodic turn number
// for the mid-session verbosity bump.
const DefaultMidSessionVerbosityBumpFrequency = 10

type Config struct {
	BaseURL       string
	AccessToken   string
	RefreshToken  string
	AccountID     string
	OAuthTokenURL string
	OAuthClientID string
	HTTPClient    *http.Client
	Models        []string
	// ModelCatalog is the auto-discovered Codex model catalog used for the
	// built-in model inventory when Models is empty. May be nil (e.g. in
	// tests without DI); the connector then loads the shipped fallback
	// snapshot. No model slugs are hardcoded.
	ModelCatalog *catalog.Catalog
	// CatalogLoadOpts re-resolves the model catalog on every inventory load
	// when Enabled, instead of freezing the Configure-time ModelCatalog.
	// Zero value keeps the frozen-catalog inventory.
	CatalogLoadOpts                       catalog.LoadOptions
	DefaultReasoningEffort                string
	DefaultVerbosity                      aipapi.VerbosityLevel
	ManagedOAuthEnabled                   bool
	ManagedOAuthStoragePath               string
	ManagedOAuthAccounts                  []string
	ManagedOAuthSelectionStrategy         string
	ManagedOAuthSessionAffinityTTL        time.Duration
	ManagedOAuthSessionAffinityMaxEntries int
	RateLimitFallback                     time.Duration
	GPT55DowngradeDisabled                bool
	GPT55DowngradeSourceModel             string
	GPT55DowngradeTargetModel             string
	PlanTypeHint                          string
	Transport                             string
	ExperimentalWebSocket                 bool
	WebSocketFallbackCooldown             time.Duration
	// EarlySessionVerbosityBumpDisabled opts out of the early-session verbosity
	// bump. The bump is enabled by default; set this true to disable it.
	EarlySessionVerbosityBumpDisabled bool
	// EarlySessionVerbosityBumpTurns is the number of leading per-conversation
	// turns bumped to high verbosity. Zero/negative falls back to
	// DefaultEarlySessionVerbosityBumpTurns.
	EarlySessionVerbosityBumpTurns int
	// MidSessionVerbosityBumpDisabled opts out of the periodic mid-session
	// verbosity bump. The bump is enabled by default; set this true to disable it.
	MidSessionVerbosityBumpDisabled bool
	// MidSessionVerbosityBumpFrequency is the periodic turn number for the
	// mid-session bump. Zero/negative falls back to
	// DefaultMidSessionVerbosityBumpFrequency. When the mid-session bump is
	// enabled, it must be greater than the early session turn count once defaults
	// are applied. When the mid-session bump is disabled, the value is ignored.
	MidSessionVerbosityBumpFrequency int
	// NativeContext holds experimental native reasoning continuity and
	// Responses Compaction V2 configuration. May be nil (compaction disabled).
	NativeContext                            *NativeContextConfig
	DisableNativeCompactionWithoutAccounting bool
}

// NormalizeTransport returns the effective transport mode for cfg. An empty
// transport defaults to HTTPS. WebSocket and auto probing are experimental and
// must be enabled explicitly so live clients do not hit the WS path by default.
// An unknown value is rejected with an error so it surfaces through the standard
// config-error path.
func NormalizeTransport(raw string, experimentalWebSocket bool) (string, error) {
	t := strings.ToLower(strings.TrimSpace(raw))
	if t == "" {
		return TransportHTTPS, nil
	}
	switch t {
	case TransportAuto, TransportHTTPS, TransportWebSocket:
		if (t == TransportAuto || t == TransportWebSocket) && !experimentalWebSocket {
			return "", fmt.Errorf("%s: transport %q requires experimental_websocket: true", ID, t)
		}
		return t, nil
	default:
		return "", fmt.Errorf("%s: unknown transport %q (want %s, %s, or %s)", ID, raw, TransportAuto, TransportHTTPS, TransportWebSocket)
	}
}

func validateVerbosityBumpConfig(cfg Config) error {
	earlyTurns := cfg.EarlySessionVerbosityBumpTurns
	if earlyTurns <= 0 {
		earlyTurns = DefaultEarlySessionVerbosityBumpTurns
	}
	if cfg.MidSessionVerbosityBumpDisabled {
		return nil
	}
	frequency := cfg.MidSessionVerbosityBumpFrequency
	if frequency <= 0 {
		frequency = DefaultMidSessionVerbosityBumpFrequency
	}
	if frequency <= earlyTurns {
		return fmt.Errorf("%s: mid_session_verbosity_bump_frequency (%d) must be greater than early_session_verbosity_bump_turns (%d)", ID, frequency, earlyTurns)
	}
	return nil
}
