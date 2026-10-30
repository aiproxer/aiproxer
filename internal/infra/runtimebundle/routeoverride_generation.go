package runtimebundle

import (
	"fmt"
	"net/http"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	adminov "github.com/aiproxer/aiproxer/internal/stdhttp/admin/routeoverride"
)

func knownBackendsOf(exec *runtime.Executor) map[string]struct{} {
	if exec == nil {
		return nil
	}
	out := make(map[string]struct{}, len(exec.Backends))
	for id := range exec.Backends {
		out[id] = struct{}{}
	}
	return out
}

func bindGenerationRouteOverride(ps *ProcessServices, cfg *config.Config, exec *runtime.Executor, nowFn func() time.Time) (http.Handler, error) {
	if cfg == nil || !cfg.Routing.OverrideAdmin.Enabled {
		return nil, nil
	}
	if ps == nil || ps.RouteOverrideStore == nil || exec == nil {
		return nil, fmt.Errorf("runtimebundle: routing.override_admin.enabled requires a continuity store that implements routeoverride.Store")
	}
	svc, err := routeoverride.NewService(ps.RouteOverrideStore, routing.NewGenerationSelectorValidator(
		exec.SelectorAliases,
		exec.DefaultBackend,
		knownBackendsOf(exec),
		exec.BackendExecutionResolver,
		exec.ExecutionCompositionPolicy,
	), nowFn)
	if err != nil {
		return nil, err
	}
	return adminov.NewHandler(adminov.Options{
		Service:      svc,
		MaxBodyBytes: cfg.Routing.OverrideAdmin.MaxBodyBytes,
		Log:          ps.Logger,
	})
}
