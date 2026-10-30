package runtime

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
)

type openNextRequest struct {
	reqFacts    requestFacts
	routeFacts  routeFacts
	progress    *recoveryController
	mode        openMode
	interleaved interleavedstate.State
}

func (e *Executor) openInitialAttempt(ctx context.Context, prep *preparedRequest, plan *routePlanState) (openedAttempt, error) {
	return attemptOpenOwner{e}.openInitial(ctx, prep, plan)
}

// openInitial runs the pre-output attempt-open loop until a backend
// stream is opened and its B-leg is registered with the A-leg scope.
func (o attemptOpenOwner) openInitial(ctx context.Context, prep *preparedRequest, plan *routePlanState) (openedAttempt, error) {
	e := o.Executor
	prep.ensureRecvTurnFacts(ctx)
	plan.ensureProgress(e, prep)
	for {
		if err := ctx.Err(); err != nil {
			return openedAttempt{}, err
		}
		if err := prep.aScope.Err(); err != nil {
			return openedAttempt{}, err
		}
		out, err := e.openNext(ctx, openNextRequest{
			reqFacts: requestFacts{
				recvTurnFacts:   prep.recvTurnFacts,
				bus:             prep.bus,
				aScope:          prep.aScope,
				structuralCause: plan.progress.causeClaims(),
			},
			routeFacts:  plan.facts(),
			progress:    plan.progress,
			mode:        openModeInitial,
			interleaved: plan.progress.interleaved,
		})
		if err != nil {
			return out, err
		}
		if out.ready == nil {
			plan.progress.interleaved = out.interleaved
			continue
		}
		return out, nil
	}
}
