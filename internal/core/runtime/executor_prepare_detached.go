package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	corehooks "github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// prepareSubmitAndALegDetached prepares one trusted internal auxiliary call.
// It shares the normal submit/request-authority seams, but intentionally does
// not enter secure-session BeginTurn: the child owns a private B2BUA A-leg,
// while the parent session and A-leg remain lineage-only metadata.
func (e *Executor) prepareSubmitAndALegDetached(
	ctx context.Context,
	bus *corehooks.Bus,
	call *aipapi.Call,
) (ibt *identityBoundTurn, workingCall *aipapi.Call, outCtx context.Context, err error) {
	if e == nil || e.Store == nil || call == nil {
		return nil, nil, ctx, fmt.Errorf("executor: invalid detached arguments")
	}
	work := aipapi.CloneCall(*call)
	// The frontend's route and origin are the baseline the final authority is
	// measured against: a submit hook that rewrites the selector replaces them.
	frontendOrigin := routing.RouteOriginFromContext(ctx)
	frontendSelector := strings.TrimSpace(call.Route.Selector)
	traceID := strings.TrimSpace(work.ID)
	if traceID == "" {
		traceID = diag.StableCallID(&work)
	}
	work.ID, call.ID, outCtx = traceID, traceID, ctx

	var principal execview.PrincipalView
	var reqScope scope.PrincipalScopeView
	hasPrincipal := false
	if s, p, ok := e.resolveRequestScope(ctx); ok {
		reqScope, principal, hasPrincipal, outCtx = s, p, true, scope.WithScope(execview.WithPrincipal(outCtx, p), s)
	}
	// Route-hint preferences are request-local advisory authority. Do not let
	// a parent primary snapshot reorder the explicitly selected extractor route.
	outCtx = execctx.WithoutRouteCandidatePreferences(outCtx)
	outCtx = diag.WithCallDiag(outCtx, traceID, "")

	// Detached children with captured parent A-leg lineage always receive a
	// private A-leg. An explicitly targeted A-leg without captured parent
	// lineage is genuine external ingress and may reuse that A-leg so ingress
	// cleanup (including stale steering) applies to the intended owner.
	parentMeta, _ := execctx.DetachedSessionFromContext(outCtx)
	var aLeg b2bua.ALegRecord
	if strings.TrimSpace(work.Session.ALegID) != "" && strings.TrimSpace(parentMeta.ParentALegID) == "" {
		aLeg, err = e.Store.FetchALeg(outCtx, work.Session.ALegID)
		if err != nil {
			if !errors.Is(err, b2bua.ErrALegNotFound) {
				return nil, nil, outCtx, fmt.Errorf("executor: fetch detached a-leg %q: %w", work.Session.ALegID, err)
			}
			aLeg, err = e.Store.CreateALeg(outCtx, "")
		}
	} else {
		aLeg, err = e.Store.CreateALeg(outCtx, "")
	}
	if err != nil {
		return nil, nil, outCtx, fmt.Errorf("executor: prepare detached a-leg: %w", err)
	}
	work.Session = aipapi.SessionRef{ALegID: aLeg.ALegID}

	preSession := session.SessionView{
		ALegID:         aLeg.ALegID,
		IsNew:          false,
		ResumeEligible: false,
	}

	ibt, err = newIdentityBoundTurn(traceID, &work, principal, reqScope, hasPrincipal, aipworkspace.WorkspaceView{}, aLeg, routeAuthoritySnapshot{}, execctx.SecureSessionTurn{}, false, preSession)
	if err != nil {
		return nil, nil, outCtx, fmt.Errorf("executor: create identity bound turn: %w", err)
	}
	workingCall = &work
	// Establish private child views before any submit/local selection can read
	// the inherited context. General identity projection preserves view labels,
	// so it must never be given the parent's session/attempt/annotations here.
	outCtx = execctx.WithViews(outCtx, execctx.Views{
		Principal: ibt.principal,
		Scope:     ibt.scope,
		Session:   ibt.preSession,
		Attempt:   execview.AttemptView{TraceID: ibt.traceID},
		Annotations: map[string]string{
			"execution_mode": "detached",
		},
	})

	if e.Log != nil {
		outCtx = corehooks.WithDiagnosticsLogger(outCtx, e.Log)
	}

	// Keep the normal request-ingress and authority boundaries. The detached
	// marker only changes session lifecycle/routing authority; it is not a
	// bypass around billing, usage, hooks, or B-leg execution.
	if outCtx, _, err = captureFrontendIngressBeforeSubmit(outCtx, *workingCall, ibt.scope, e.now()); err != nil {
		return nil, nil, outCtx, err
	}
	if outCtx, err = e.admitRequestAuthorityOnce(outCtx, workingCall.ID, ibt.aLeg.ALegID, ibt.traceID, ibt.scope); err != nil {
		return nil, nil, outCtx, err
	}
	guard := &preStreamGuard{executor: e, ctx: outCtx, requestAuthorityAdmitted: true}
	defer guard.Close()
	submitMeta := &sdkhooks.SubmitMeta{TraceID: ibt.traceID, Annotations: map[string]string{}}
	if err := bus.RunSubmit(outCtx, workingCall, submitMeta); err != nil {
		return nil, nil, outCtx, err
	}
	// --- Task 3.2 seam: snapshot once after A-leg resolution ---
	// Preserve ingress then project exclusion+steering before backend work;
	// fail closed on snapshot/projection errors.
	ingressClone := aipapi.CloneCall(*workingCall)
	ibt.ingressCall = &ingressClone
	if err := e.selectLocalAndBootstrap(outCtx, ibt, workingCall); err != nil {
		return nil, nil, outCtx, err
	}
	backendClone := aipapi.CloneCall(*workingCall)
	originalForFilter := aipapi.CloneCall(backendClone)
	snapView, projEv, projected, perr := e.snapshotAndProject(outCtx, ibt.aLeg.ALegID, backendClone)
	if perr != nil {
		return nil, nil, outCtx, perr
	}
	ibt.conversationSnapshot = snapView
	ibt.conversationEvidence = projEv
	ibt.conversationSummary = newConversationProjectionSummary(snapView, projEv)
	ibt.convSnapshotSet = true
	if filtered, ferr := conversationprojection.FilterNeverBackend(originalForFilter, snapView); ferr == nil {
		ibt.conversationFilteredBaseline = &filtered
	} else {
		return nil, nil, outCtx, ferr
	}
	backendClone = projected
	workingCall = &backendClone
	// Submit hooks may enrich canonical calls, but cannot turn detached
	workingCall.Session.AuthoritativeSessionID, workingCall.Session.ClientSessionID = "", ""
	workingCall.Session.ALegID = ibt.aLeg.ALegID
	workingCall.Session.ContinuityKey, workingCall.Session.ResumeToken, workingCall.Session.Metadata = "", "", nil

	*workingCall = aipapi.CloneCall(*workingCall)
	call.Session = workingCall.Session
	// Ensure backend is distinct from preserved ingress (deep clone isolation).
	if ibt.ingressCall != nil && ibt.ingressCall == workingCall {
		bk := aipapi.CloneCall(*ibt.ingressCall)
		workingCall = &bk
	}
	outCtx = diag.EnsureCallDiag(outCtx, ibt.traceID, ibt.aLeg.ALegID)
	// A submit hook that rewrote the route replaces the frontend's origin: the
	// route belongs to the hook's authority, so it uses the current
	// request-bound registry.
	outCtx = routing.WithRouteOrigin(outCtx, finalRouteOrigin(
		frontendOrigin,
		frontendSelector,
		workingCall.Route.Selector,
		false,
	))
	guard.Handoff()
	return ibt, workingCall, outCtx, nil
}
