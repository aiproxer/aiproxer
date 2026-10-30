package usagelimit

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// QueryState classifies the bounded live authority query posture on the wire.
// Canonical here: the open-source host and the query implementation share one
// vocabulary; the usage-authority application and the stdhttp admin handler
// alias it.
type QueryState string

const (
	QueryStateDisabled     QueryState = "disabled"
	QueryStateEmpty        QueryState = "empty"
	QueryStateUnsupported  QueryState = "unsupported"
	QueryStateTooBroad     QueryState = "too_broad"
	QueryStateAdvisoryOnly QueryState = "advisory_only"
	QueryStateDegraded     QueryState = "degraded"
	QueryStateUnavailable  QueryState = "unavailable"
	QueryStateReady        QueryState = "ready"
)

// LimitStatusResult and DecisionHistoryResult are the bounded wire DTOs of the
// live authority query surface. They keep live authority state distinct from
// historical rows.
type LimitStatusResult struct {
	State QueryState
	Page  controlplane.Page[controlplane.AccountingLimitStatusRow]
}

type DecisionHistoryResult struct {
	State QueryState
	Page  controlplane.Page[controlplane.AccountingDecisionRow]
}

// QueryReader is the optional bounded live-query surface beside the four-method
// Authority port. The host consumes it only to feed the protected admin routes;
// a port that does not implement it leaves those routes disabled.
type QueryReader interface {
	Limits(context.Context, controlplane.AccountingLimitStatusQuery) (LimitStatusResult, error)
	Decisions(context.Context, controlplane.AccountingDecisionQuery) (DecisionHistoryResult, error)
}
