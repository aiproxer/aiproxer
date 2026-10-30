package stdhttp

import (
	"fmt"
	"runtime"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

var runningAsAdmin = detectRunningAsAdmin

func validateStartupSecurity(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("stdhttp: nil config")
	}
	if err := config.ValidateProtectedDiagnosticsPosture(cfg); err != nil {
		return fmt.Errorf("stdhttp: %w", err)
	}
	noAuth := cfg.EffectiveServerAuthMode() == config.AuthModeNoAuth
	loopback := config.IsExplicitLoopbackListenAddress(cfg.Server.Address)
	if noAuth && !loopback {
		return fmt.Errorf(
			"stdhttp: no_auth mode requires explicit loopback server.address, got %q",
			cfg.Server.Address,
		)
	}
	isAdmin, err := runningAsAdmin()
	if err != nil {
		return fmt.Errorf("stdhttp: determine administrative privilege: %w", err)
	}
	if isAdmin {
		return fmt.Errorf("stdhttp: refusing to start as administrative user on %s", runtime.GOOS)
	}
	return nil
}
