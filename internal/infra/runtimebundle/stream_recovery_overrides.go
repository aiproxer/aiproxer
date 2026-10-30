package runtimebundle

import "github.com/aiproxer/aiproxer/internal/core/config"

func mergeStreamRecoveryOverrides(env, cli config.StreamRecoveryOverrides) config.StreamRecoveryOverrides {
	out := env
	if cli.CLIEnabled != nil {
		out.CLIEnabled = cli.CLIEnabled
	}
	if cli.CLIIdleTimeout > 0 {
		out.CLIIdleTimeout = cli.CLIIdleTimeout
	}
	if cli.CLIGracePeriod > 0 {
		out.CLIGracePeriod = cli.CLIGracePeriod
	}
	if cli.CLIPostOutputPolicy != "" {
		out.CLIPostOutputPolicy = cli.CLIPostOutputPolicy
	}
	if cli.CLIEmitWarning != nil {
		out.CLIEmitWarning = cli.CLIEmitWarning
	}
	return out
}
