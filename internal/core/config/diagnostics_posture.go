package config

import (
	"fmt"
	"strings"
)

// ValidateProtectedDiagnosticsPosture rejects exposing protected operator surfaces on a
// non-loopback bind without diagnostics.shared_secret (minimum length enforced separately by
// [Validate] via validateDiagnosticsSecret). Health-only diagnostics never require a secret.
// Loopback binds may use an empty secret with protected surfaces (local trust posture).
func ValidateProtectedDiagnosticsPosture(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	if IsExplicitLoopbackListenAddress(cfg.Server.Address) {
		return nil
	}
	secret := strings.TrimSpace(cfg.Diagnostics.SharedSecret)
	if len(secret) >= 12 {
		return nil
	}
	surfaces := protectedDiagnosticsSurfaces(cfg)
	if len(surfaces) == 0 {
		return nil
	}
	return fmt.Errorf(
		"diagnostics.shared_secret: required (at least 12 characters) when exposing %s on a non-loopback server.address %q",
		strings.Join(surfaces, ", "),
		strings.TrimSpace(cfg.Server.Address),
	)
}

// protectedDiagnosticsSurfaces returns human-readable surface names for diagnostics that use
// [diag.WrapDiagnosticsProtect] in stdhttp when secret is set, and are unauthenticated when the
// secret is empty. Must stay aligned with
// [github.com/aiproxer/aiproxer/internal/stdhttp.ComposeStandardHTTP] mounts.
func protectedDiagnosticsSurfaces(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	d := &cfg.Diagnostics
	if d.Enabled {
		if strings.TrimSpace(d.AttemptsPath) != "" {
			out = append(out, "attempts")
		}
		if strings.TrimSpace(d.InventoryPath) != "" {
			out = append(out, "inventory")
		}
		if strings.TrimSpace(d.RouteTracePath) != "" {
			out = append(out, "route_trace")
		}
		if strings.TrimSpace(d.PprofPath) != "" {
			out = append(out, "pprof")
		}
	}
	if cfg.Observability.Metrics.Enabled {
		out = append(out, "metrics")
	}
	if strings.TrimSpace(cfg.ModelCatalog.DiagnosticsPath) != "" {
		out = append(out, "model_catalog")
	}
	if strings.TrimSpace(cfg.ModelInventory.DiagnosticsPath) != "" {
		out = append(out, "model_inventory")
	}
	if cfg.SecureSessionEffectivelyEnabled() && cfg.SecureSession.DiagnosticsExposeSummaries &&
		strings.TrimSpace(cfg.SecureSession.DiagnosticsPathPrefix) != "" {
		out = append(out, "secure_session_summaries")
	}
	if cfg.Accounting.Admin.Enabled && strings.TrimSpace(cfg.Accounting.Admin.Path) != "" {
		out = append(out, "token_accounting_admin")
	}
	if cfg.Routing.OverrideAdmin.Enabled {
		out = append(out, "routing_override_admin")
	}
	if AuthorityQueryEffectivelyExposed(cfg) {
		out = append(out, "accounting_authority_query")
	}
	if ControlPlaneQueryEffectivelyExposed(cfg) {
		out = append(out, "control_plane_query")
	}
	return out
}
