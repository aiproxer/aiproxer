package standardplugins

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

const compatibleOriginClientFacing = "client_facing"

// ProjectOpenResponsesFrontendRows builds secret-safe diagnostics rows from config only.
// No provider requests, plugin activation, or credential resolution occur. Unknown or
// invalid frontend config rows surface a bounded ConfigError with instance identity.
func ProjectOpenResponsesFrontendRows(cfg *config.Config) []diag.InstanceDiagnostic {
	if cfg == nil {
		return nil
	}
	out := make([]diag.InstanceDiagnostic, 0)
	for _, row := range cfg.Plugins.Frontends {
		if strings.TrimSpace(row.FactoryID()) != openresponses.ID {
			continue
		}
		entry := diag.InstanceDiagnostic{
			Origin:      compatibleOriginClientFacing,
			ID:          row.InstanceID(),
			InstanceID:  row.InstanceID(),
			FactoryKind: openresponses.ID,
			Enabled:     row.Enabled,
		}
		decoded, err := openresponses.DecodeConfig(row.Config)
		if err != nil {
			inst := strings.TrimSpace(row.InstanceID())
			if inst == "" {
				inst = "<unknown>"
			}
			entry.ConfigError = "instance " + inst + ": " + err.Error()
			entry.Conformance = "invalid"
			out = append(out, entry)
			continue
		}
		entry.Profile = sanitizedOpenResponsesString(decoded.Profile)
		entry.BasePath = sanitizedOpenResponsesString(decoded.BasePath)
		entry.WebSocketEnabled = decoded.WebSocket.IsEnabled()
		entry.ContinuationStore = sanitizedOpenResponsesString(decoded.Continuation.PersistentStore)
		entry.ContinuationTTL = sanitizedOpenResponsesString(decoded.Continuation.TTL)
		entry.AllowedOrigins = sanitizedOpenResponsesOrigins(decoded.WebSocket.AllowedOrigins)
		entry.Capabilities = openResponsesFrontendCapabilities(decoded)
		entry.Conformance = "profile:" + entry.Profile
		entry.RouteClaims = openResponsesRouteClaims(decoded)
		entry.Details = []diag.SafeField{{Key: "protocol", Value: "openresponses"}}
		out = append(out, entry)
	}
	return out
}

// openResponsesFrontendCapabilities projects the client-facing semantic
// capability surface for the pinned profile. The set is deterministic and
// transport-aware: WebSocket appears only when enabled.
func openResponsesFrontendCapabilities(cfg openresponses.Config) []string {
	caps := []string{"ordered_items", "streaming", "tools", "compaction"}
	if cfg.WebSocket.IsEnabled() {
		caps = append(caps, "websocket")
	}
	return caps
}

func openResponsesRouteClaims(cfg openresponses.Config) []string {
	claims, err := openresponses.RouteClaims(cfg)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(claims))
	for _, c := range claims {
		out = append(out, sanitizedOpenResponsesRouteClaim(c))
	}
	return out
}

func sanitizedOpenResponsesRouteClaim(c httpcontract.RouteClaim) string {
	method := sanitizeOpenResponsesClaimString(c.Method)
	path := sanitizeOpenResponsesClaimString(c.Path)
	if method == "" || path == "" {
		return ""
	}
	return method + " " + path
}

func sanitizeOpenResponsesClaimString(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\t", "")
	return strings.TrimSpace(s)
}

func sanitizedOpenResponsesString(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\t", "")
	return strings.TrimSpace(s)
}

func sanitizedOpenResponsesOrigins(in []string) []string {
	out := make([]string, 0, len(in))
	for _, o := range in {
		if o = sanitizedOpenResponsesString(o); o != "" {
			out = append(out, o)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
