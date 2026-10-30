package stdhttp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp/selfdefense"
)

// prepareStandardHandler mounts metrics, diagnostics, admin, secure-session diagnostics,
// model-catalog diagnostics, control-plane query, routing-override admin, and bundled
// frontends, then stacks the outer HTTP middleware. Mount order is load-bearing and
// preserved exactly.
//
// Protected operator handlers arrive as registered [AdminMount] values; the HTTP
// layer owns their path validation and diagnostics-secret mounting.
//
// The focused composer accepts only [StandardHTTPInput]: it owns neither app start/shutdown nor
// resource closers. Callers ([ComposeStandardHTTP]) project focused groups and own lifecycle
// above this seam.
func prepareStandardHandler(
	ctx context.Context,
	cfg *config.Config,
	log *slog.Logger,
	in StandardHTTPInput,
) (http.Handler, error) {
	// Validate what will actually be mounted, including custom composers whose
	// focused frontend list differs from cfg.Plugins.Frontends.
	hybridCfg := *cfg
	hybridCfg.Plugins.Frontends = in.Frontends.Plugins
	if err := standardplugins.ValidateFrontendDeployment(&hybridCfg); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()

	httpProm, err := mountMetrics(mountMetricsInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Operations: in.Operations,
	})
	if err != nil {
		return nil, err
	}

	if err := mountDiagnostics(mountDiagnosticsInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log,
		Operations: in.Operations, Reg: in.Frontends.Registry,
	}); err != nil {
		return nil, err
	}
	installTerminalDecisionPolicy(mux, cfg, in.Operations.TerminalDecisionPolicy)
	mountProxyMemory(mux, in.Operations.ProxyMemory)

	mountKeepwarmAdmin(mountAccountingAdminInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log,
		Operations: in.Operations,
	})
	mountAccountingAdmin(mountAccountingAdminInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log,
		Operations: in.Operations,
	})
	if err := mountAdminMounts(adminMountsInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log,
		Mounts: in.Operations.AdminMounts,
	}); err != nil {
		return nil, err
	}

	if err := mountSecureSessionDiagnostics(mountSecureSessionDiagnosticsInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Security: in.Security, Metrics: in.Operations.Metrics,
	}); err != nil {
		return nil, err
	}

	mountModelCatalogDiagnostics(ctx, diagnosticsMount{
		Mux: mux, Cfg: cfg, Log: log, Models: in.Models,
	})
	mountModelInventoryDiagnostics(ctx, diagnosticsMount{
		Mux: mux, Cfg: cfg, Log: log, Models: in.Models,
	})
	mountAccountingAuthorityQuery(accountingAuthorityQueryMount{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log,
		Security: in.Security, Operations: in.Operations,
	})
	mountControlPlaneQuery(controlPlaneQueryMount{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Operations: in.Operations,
	})
	mountRouteOverrideAdmin(routeOverrideAdminMount{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Operations: in.Operations,
	})

	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux:       mux,
		Frontends: in.Frontends,
	}); err != nil {
		return nil, fmt.Errorf("stdhttp: mount frontends: %w", err)
	}

	if err := callMount(func() error {
		mux.Handle(openAIModelsPath, NewModelRegistryHandler(in.Models.ModelRegistryRuntime))
		return nil
	}); err != nil {
		return nil, err
	}

	traceGen := diag.NewTraceIDGenerator()
	// Resolve the ingress self-defense owned-route inventory against the router that
	// was just built. This runs after every mount and before the stack, so ownership
	// is the router's own answer rather than a reading of the configuration: a
	// feature that is disabled mounted nothing and therefore owns nothing, an exact
	// registration owns one method/path pair, and a trailing-slash registration owns
	// its subtree. Mount order above is what makes this complete.
	if sd := in.Security.SelfDefense; sd.SelfDefenseEnabled() {
		in.Security.SelfDefense.OwnedRoutes = selfdefense.OwnedRoutesFromMux(mux, sd.OwnedRouteCandidates)
	}
	return stackHTTPHandler(stackHTTPInput{
		Cfg: cfg, Log: log, Security: in.Security, TraceGen: traceGen, Inner: mux, HTTPProm: httpProm,
	}), nil
}
