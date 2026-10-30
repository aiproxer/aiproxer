package runtime

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
)

const (
	routeSelectorSourceClient = diag.RouteSelectorSourceClient
	routeSelectorSourceAdmin  = diag.RouteSelectorSourceAdmin
)

// routeAuthoritySnapshot is the request-local copy of override state taken once
// after authoritative A-leg fetch (design D3).
type routeAuthoritySnapshot struct {
	State  routeoverride.State
	Source string
}

func (s routeAuthoritySnapshot) active() bool {
	return s.State.Active
}

func (e *Executor) snapshotRouteOverride(ctx context.Context, aLegID string) (routeAuthoritySnapshot, error) {
	out := routeAuthoritySnapshot{Source: routeSelectorSourceClient}
	if e == nil || e.RouteOverrideReader == nil {
		return out, nil
	}
	st, err := e.RouteOverrideReader.Snapshot(ctx, aLegID)
	if err != nil {
		return routeAuthoritySnapshot{}, fmt.Errorf("executor: snapshot route override: %w", err)
	}
	out.State = st.Clone()
	if st.Active {
		out.Source = routeSelectorSourceAdmin
	}
	return out, nil
}
