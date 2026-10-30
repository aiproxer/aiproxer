package standardplugins

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
)

// ValidateFrontendDeployment runs before any standard handler is published. V1 deliberately
// refuses canonical policy combinations instead of pretending opaque native
// traffic ran their guards, accounting, or audit. Ingress middleware stays on.
// Concrete frontend configuration belongs to standard plugin composition, not
// the generic HTTP router. Monetary compatibility belongs to runtime composition.
func ValidateFrontendDeployment(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("stdhttp: nil config")
	}
	for _, p := range cfg.Plugins.Frontends {
		if !p.Enabled || p.FactoryID() != anthropic.ID {
			continue
		}
		fe, err := anthropic.DecodeConfig(p.Config)
		if err != nil {
			return err
		}
		if !fe.ClaudeCodeHybrid.Enabled {
			continue
		}
		mode, err := cfg.EffectiveAccessMode()
		handler, level := cfg.EffectiveAuthForAudit()
		if err != nil || mode != "single_user" || handler != "local_noop" || level != "none" || !config.IsExplicitLoopbackListenAddress(cfg.Server.Address) {
			return fmt.Errorf("stdhttp: Claude Code hybrid requires single_user, local_noop and an explicit loopback listener")
		}
		if cfg.Server.EffectiveWriteTimeout() < anthropic.ClaudeCodeHybridMinimumWriteTimeout {
			return fmt.Errorf("stdhttp: Claude Code hybrid requires server.write_timeout of at least %s", anthropic.ClaudeCodeHybridMinimumWriteTimeout)
		}
		// A durable continuity store can retain an override even when its admin
		// endpoint is disabled. V1 admits only fresh process-local routing state.
		if len(cfg.ModelAliases) != 0 || cfg.Routing.OverrideAdmin.Enabled || (cfg.Continuity.Store != "" && cfg.Continuity.Store != "memory") {
			return fmt.Errorf("stdhttp: Claude Code hybrid forbids global model aliases, route overrides and durable continuity")
		}
		ss := cfg.SecureSession
		if cfg.Accounting.Enabled || cfg.Accounting.Authority.Enabled || cfg.Accounting.Concurrency.Enabled || cfg.Accounting.StrictAuthoritative || cfg.Metering.Enabled || cfg.ControlPlane.Enabled || ss.AuditDurability == "durable" || ss.RequireWorkspaceID || ss.WorkspaceResolveOnError == "fail_closed" || cfg.Routing.AttemptPolicy == "reject" || cfg.Hooks.ToolReactorErrorPolicy == "fail_closed" {
			return fmt.Errorf("stdhttp: Claude Code hybrid cannot honor configured canonical enforcement/accounting policies")
		}
		for _, f := range cfg.Plugins.Features {
			if !f.Enabled {
				continue
			}
			switch f.FactoryID() {
			case "submit-noop", "parts-noop", "tool-reactor-noop":
				continue
			}
			return fmt.Errorf("stdhttp: Claude Code hybrid V1 requires canonical feature %q disabled (except standard no-ops)", f.FactoryID())
		}
		for _, m := range fe.ClaudeCodeHybrid.ExternalModels {
			backend, _, _ := strings.Cut(m.Route, ":")
			found := false
			for _, b := range cfg.Plugins.Backends {
				if b.Enabled && b.InstanceID() == backend {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("stdhttp: Claude Code hybrid route requires an enabled configured backend")
			}
		}
	}
	return nil
}

// ValidateBilledFrontendDeployment rejects opaque native passthrough when the
// runtime's authoritative monetary composition is active. HTTP mounts and
// capability declarations cannot establish whether that composition exists.
func ValidateBilledFrontendDeployment(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("stdhttp: nil config")
	}
	for _, p := range cfg.Plugins.Frontends {
		if !p.Enabled || p.FactoryID() != anthropic.ID {
			continue
		}
		fe, err := anthropic.DecodeConfig(p.Config)
		if err != nil {
			return err
		}
		if fe.ClaudeCodeHybrid.Enabled {
			return fmt.Errorf("standardplugins: Claude Code hybrid cannot bypass an injected billing composition")
		}
	}
	return nil
}
