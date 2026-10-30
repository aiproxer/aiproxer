package runtime

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func (e *Executor) prepareSubmitAndALeg(ctx context.Context, bus *hooks.Bus, call *aipapi.Call) (traceID string, baseline aipapi.Call, aLeg b2bua.ALegRecord, routeAuth routeAuthoritySnapshot, outCtx context.Context, err error) {
	ibt, workingCall, outCtx, err := e.prepareIdentity(ctx, bus, call)
	if err == nil {
		traceID, baseline, aLeg, routeAuth = ibt.traceID, *workingCall, ibt.aLeg, ibt.routeAuth
	}
	return
}

func (e *Executor) prepareIdentity(ctx context.Context, bus *hooks.Bus, call *aipapi.Call) (ibt *identityBoundTurn, workingCall *aipapi.Call, outCtx context.Context, err error) {
	if e != nil {
		if mode, marked := execctx.SessionModeFromContext(ctx); marked && mode == execctx.SessionModeDetached {
			return e.prepareSubmitAndALegDetached(ctx, bus, call)
		}
	}
	if e == nil || e.SecureSession == nil {
		return nil, nil, ctx, fmt.Errorf("executor: secure session manager is required")
	}
	return e.prepareSubmitAndALegSecure(ctx, bus, call)
}
