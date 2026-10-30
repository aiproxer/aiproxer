package runtime

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/modelview"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/submission"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type preparedRequest struct {
	recvTurnFacts
	bus                *hooks.Bus
	identity           *identityBoundTurn
	call               *aipapi.Call
	guard              *preStreamGuard
	aScope             *leglifecycle.ALeg
	billingCallID      usageevidence.BillingCallID
	billingCallState   *billingCallState
	submission         submission.BindingResult
	billingExposure    billingport.CallExposure
	execSpan           trace.Span
	compactionOpenMeta compaction.PreservationMeta
	// Task 3.2: frozen snapshot taken once after authoritative A-leg resolution,
	// carried through every B-leg attempt; projection evidence is bounded
	// content-free (counts/revisions/placement, no plaintext).
	conversationSnapshot         conversationprojection.Snapshot
	conversationEvidence         *conversationprojection.ProjectionEvidence
	conversationSummary          conversationProjectionSummary
	conversationFilteredBaseline *aipapi.Call
	// Task 3.3: generic two-phase local-turn stage. When isLocal true,
	// localStream is the finite canonical response, merged snapshot already
	// contains source+reply tags, and no billing/route/B-leg must run.
	isLocal         bool
	localStream     aipapi.EventStream
	localHandlerID  string
	interleavedTurn InterleavedTurn
}

func (prep *preparedRequest) ensureRecvTurnFacts(ctx context.Context) {
	if prep == nil {
		return
	}
	if prep.recvTurnFacts.billingCallID == "" && prep.billingCallID != "" {
		prep.recvTurnFacts.billingCallID = prep.billingCallID
		prep.recvTurnFacts.billingCallState = prep.billingCallState
	}
	if prep.aLegID == "" && prep.identity != nil {
		var prov []conversationprojection.OverlayProvenance
		if prep.conversationEvidence != nil {
			prov = prep.conversationEvidence.Provenance
		} else if prep.identity != nil && prep.identity.conversationEvidence != nil {
			prov = prep.identity.conversationEvidence.Provenance
		}
		snap := prep.conversationSnapshot
		if snap.StateRevision == 0 && prep.identity != nil && prep.identity.convSnapshotSet {
			snap = prep.identity.conversationSnapshot
		}
		var filtered aipapi.Call
		if prep.conversationFilteredBaseline != nil {
			filtered = aipapi.CloneCall(*prep.conversationFilteredBaseline)
		} else if prep.identity != nil && prep.identity.conversationFilteredBaseline != nil {
			filtered = aipapi.CloneCall(*prep.identity.conversationFilteredBaseline)
		}
		var ingress aipapi.Call
		if prep.identity != nil && prep.identity.ingressCall != nil {
			ingress = aipapi.CloneCall(*prep.identity.ingressCall)
		} else if prep.call != nil {
			ingress = aipapi.CloneCall(*prep.call)
		}
		savedWP := prep.recvTurnFacts.wirePayload //nolint:staticcheck // explicit recvTurnFacts qualification keeps request-attempt AST ratchet counts stable
		prep.recvTurnFacts = newRecvTurnFacts(ctx, withBillingStoreID(recvTurnFactsInput{
			baseline:                     *prep.call,
			traceID:                      prep.identity.traceID,
			aLegID:                       prep.identity.aLeg.ALegID,
			secureTurn:                   prep.identity.secureTurn,
			secureTurnOK:                 prep.identity.secureTurnOK,
			billingCallID:                prep.billingCallID,
			billingCallState:             prep.billingCallState,
			submissionID:                 submissionIDForBilling(prep.submission),
			conversationSnapshot:         cloneSnapshot(snap),
			conversationProvenance:       slices.Clone(prov),
			conversationFilteredBaseline: aipapi.CloneCall(filtered),
			ingressCall:                  ingress,
		}, prep.billingStoreID, prep.billingStoreIDStamped))
		prep.recvTurnFacts.wirePayload = savedWP //nolint:staticcheck // explicit recvTurnFacts qualification keeps request-attempt AST ratchet counts stable
	}
}

func (e *Executor) prepareRequest(ctx context.Context, call *aipapi.Call) (*preparedRequest, context.Context, func(), error) {
	noop := func() {}
	if e == nil || e.Store == nil || call == nil {
		return nil, nil, noop, fmt.Errorf("executor: invalid arguments")
	}
	if e.Bus == nil {
		return nil, nil, noop, fmt.Errorf("executor: nil hook bus")
	}
	bus := e.Bus
	if err := call.Validate(); err != nil {
		return nil, nil, noop, fmt.Errorf("executor: validate call: %w", err)
	}
	if ctx == nil {
		return nil, nil, noop, aipapi.ErrNilContext
	}
	if e.RuntimeSnapshot != nil {
		ctx = extensions.WithRequestRuntimeSnapshot(ctx, e.RuntimeSnapshot)
	}
	detached := false
	if mode, marked := execctx.SessionModeFromContext(ctx); marked && mode == execctx.SessionModeDetached {
		detached = true
	}
	if !detached {
		e.secureSessionMu.Lock()
		if e.SecureSession == nil {
			secureSessionTestPrepare(e)
		}
		secureSessionReady := e.SecureSession != nil
		e.secureSessionMu.Unlock()
		if !secureSessionReady {
			return nil, nil, noop, fmt.Errorf("executor: secure session manager is required")
		}
	}
	pr := &preparedRequest{bus: bus}
	// The operator-declared funding decision is applied before the operation
	// is admitted: a refused background operation never becomes a financial
	// operation, because no billing call identity is minted, no exposure is
	// admitted and no provider is opened.
	if err := e.admitBackgroundSponsorship(ctx); err != nil {
		// Nothing is bound yet: no trace span, no A-leg, no billing call
		// identity and no request authority, so there is nothing to release.
		return nil, nil, noop, err
	}
	prepCtx, execSpan := otel.Tracer(otelScopeExecutor).Start(ctx, "aip.executor.execute")
	pr.execSpan = execSpan
	ibt, workingCall, prepCtx, err := e.prepareIdentity(prepCtx, bus, call)
	if err != nil {
		pr.finalize(err)
		return nil, nil, noop, fmt.Errorf("executor: prepare submit: %w", err)
	}
	prepCtx = ibt.projectContext(prepCtx)
	pr.identity = ibt
	pr.call = workingCall
	pr.submission, err = bindSubmissionAuthority(prepCtx, ibt, workingCall)
	if err != nil {
		pr.finalize(err)
		return nil, nil, noop, fmt.Errorf("executor: bind submission identity: %w", err)
	}
	terminalDecisionPolicy, terminalDecisionEnabled, err := e.snapshotTerminalDecisionPolicy(prepCtx, ibt)
	if err != nil {
		pr.finalize(err)
		return nil, nil, noop, fmt.Errorf("executor: terminal decision policy admission: %w", err)
	}
	// Task 3.2: carry frozen snapshot+evidence from 3.1 seam; exactly once
	// per logical turn. Fail closed already handled inside prepareIdentity.
	// PreserveIngress fallback retains isolation.
	if ibt.convSnapshotSet {
		pr.conversationSnapshot = ibt.conversationSnapshot
		pr.conversationEvidence = ibt.conversationEvidence
		pr.conversationSummary = ibt.conversationSummary
		if ibt.conversationFilteredBaseline != nil {
			fb := aipapi.CloneCall(*ibt.conversationFilteredBaseline)
			pr.conversationFilteredBaseline = &fb
		}
	}
	switch pr.identity.ingressCall {
	case nil:
		ing := aipapi.CloneCall(*workingCall)
		pr.identity.ingressCall = &ing
		bk := aipapi.CloneCall(*workingCall)
		pr.call = &bk
	case pr.call:
		bk := aipapi.CloneCall(*pr.call)
		pr.call = &bk
	}
	// Ensure preparedRequest also mirrors evidence when fallback path created empty snapshot
	if pr.conversationEvidence == nil && ibt.conversationEvidence != nil {
		pr.conversationEvidence = ibt.conversationEvidence
		pr.conversationSnapshot = ibt.conversationSnapshot
		pr.conversationSummary = ibt.conversationSummary
		if ibt.conversationFilteredBaseline != nil {
			fb := aipapi.CloneCall(*ibt.conversationFilteredBaseline)
			pr.conversationFilteredBaseline = &fb
		}
	}

	// Consume the pre-snapshot selection; do not match or reread the view here.
	if pr.identity.ingressCall != nil {
		if pr.identity.localSelection.handler != nil {
			tagger := e.conversationViewTagger()
			if tagger == nil {
				err := fmt.Errorf("executor: conversation view tagger not available for localturn")
				pr.finalize(err)
				return nil, nil, noop, err
			}
			out, err := e.runLocalTurnStage(prepCtx, *pr.identity.ingressCall, pr.conversationSnapshot, pr.identity.localSelection, tagger, pr.identity.aLeg.ALegID)
			if err != nil {
				_ = e.releaseRequestAuthority(prepCtx)
				pr.finalize(err)
				return nil, nil, noop, err
			}
			if out.claimed {
				// Merge source+reply tags into frozen request-local snapshot without second store read.
				pr.conversationSnapshot = out.mergedSnap
				pr.identity.conversationSnapshot = out.mergedSnap
				pr.localStream = out.stream
				pr.isLocal = true
				pr.localHandlerID = out.handlerID
				pr.submission = localSubmissionResult(ibt.scope, ibt.aLeg.ALegID, workingCall.ID)
				// Release prior concurrency authority deterministically; no billing/route/B-leg.
				guardLocal := &preStreamGuard{executor: e, ctx: prepCtx, requestAuthorityAdmitted: true}
				pr.guard = guardLocal
				_ = e.releaseRequestAuthority(prepCtx)
				guardLocal.requestAuthorityAdmitted = false
				return pr, prepCtx, guardLocal.Close, nil
			}
		}
	}

	guard := &preStreamGuard{
		executor:                 e,
		ctx:                      prepCtx,
		requestAuthorityAdmitted: true,
	}
	pr.guard = guard

	if ctx.Value(testInjectBillingErrorKey{}) != nil {
		pr.billingCallID = "invalid"
	}

	if e.PromptCacheMaintenance != nil {
		e.PromptCacheMaintenance.BeginRealTurn(pr.identity.aLeg.ALegID)
	}
	if err := stampBillingCallID(pr); err != nil {
		guard.Close()
		pr.finalize(err)
		return nil, nil, noop, fmt.Errorf("executor: allocate billing call id: %w", err)
	}
	// Freeze trusted store lineage at the same request identity boundary as the
	// BillingCallID. It is carried by terminal facts and never re-resolved per
	// B-leg or terminal callback. A panicking resolver fails closed here with
	// the guard-owned cleanup instead of leaking request authority.
	if err := e.stampBillingStoreID(prepCtx, pr); err != nil {
		guard.Close()
		pr.finalize(err)
		return nil, nil, noop, fmt.Errorf("%w: trusted billing store identity: %w", ErrBillingAdmissionDenied, err)
	}
	if err := completeSubmissionAuthority(pr, ibt); err != nil {
		guard.Close()
		pr.finalize(err)
		return nil, nil, noop, fmt.Errorf("executor: complete submission identity: %w", err)
	}
	if pr.submission.Trusted() {
		prepCtx = submission.WithAuthority(prepCtx, pr.submission.Authority)
	}
	lifecycle := e.lifecycleCoordinator()
	pr.aScope = lifecycle.StartALeg(pr.identity.aLeg.ALegID)
	guard.aScope = pr.aScope

	var recvViews execctx.Views
	var recvViewsOK bool
	if v, ok := execctx.FromContext(prepCtx); ok {
		recvViews = v
		recvViewsOK = true
	}
	boundReg, boundRegOK := modelregistry.BoundViewFromContext(prepCtx)
	if !boundRegOK && e.BoundRegistry.Active() {
		// A request the generation starts itself — an auxiliary background
		// request — carries no request-bound registry view. The generation's
		// own frozen publication is the deployment fact for it, and it is
		// captured here, at the assembly boundary, like every other fact.
		boundReg = e.BoundRegistry
		boundRegOK = true
	}
	boundCat, boundCatOK := modelcatalog.BoundViewFromContext(prepCtx)
	nativeResolver, _ := routing.NativeModelResolverFromContext(prepCtx)
	// The request-bound registry is authoritative for every route. Only a
	// request whose route origin is the generation's derived default route
	// keeps the generation's frozen model bindings, so an explicit client route
	// always uses the current registry (including its wrong-backend
	// rejection).
	nativeResolver = routing.DefaultRouteModelResolver(prepCtx, nativeResolver)
	modelViewID, modelViewIDOK := modelview.FromContext(prepCtx)
	var prov []conversationprojection.OverlayProvenance
	if pr.conversationEvidence != nil {
		prov = pr.conversationEvidence.Provenance
	} else if ibt.conversationEvidence != nil {
		prov = ibt.conversationEvidence.Provenance
	}
	var filtered aipapi.Call
	if pr.conversationFilteredBaseline != nil {
		filtered = aipapi.CloneCall(*pr.conversationFilteredBaseline)
	} else if ibt.conversationFilteredBaseline != nil {
		filtered = aipapi.CloneCall(*ibt.conversationFilteredBaseline)
	}
	var ingress aipapi.Call
	if ibt.ingressCall != nil {
		ingress = aipapi.CloneCall(*ibt.ingressCall)
	} else {
		ingress = aipapi.CloneCall(*workingCall)
	}
	pr.recvTurnFacts = newRecvTurnFacts(prepCtx, withBillingStoreID(recvTurnFactsInput{
		baseline:                     *workingCall,
		traceID:                      ibt.traceID,
		aLegID:                       ibt.aLeg.ALegID,
		recvViews:                    recvViews,
		recvViewsOK:                  recvViewsOK,
		routePrefs:                   slices.Clone(execctx.RouteCandidatePreferences(prepCtx)),
		secureTurn:                   ibt.secureTurn,
		secureTurnOK:                 ibt.secureTurnOK,
		terminalDecisionPolicy:       terminalDecisionPolicy,
		terminalDecisionEnabled:      terminalDecisionEnabled,
		boundRegistry:                boundReg,
		boundRegistryOK:              boundRegOK,
		boundCatalog:                 boundCat,
		boundCatalogOK:               boundCatOK,
		nativeResolver:               nativeResolver,
		modelViewID:                  modelViewID,
		modelViewIDOK:                modelViewIDOK,
		metering:                     meteringHolderFrom(prepCtx),
		requestAuth:                  requestAuthorityFrom(prepCtx),
		billingAccountID:             pr.billingExposure.AccountID,
		billingCustomerPricing:       pr.billingExposure.PricingRef,
		billingChargePolicy:          pr.billingExposure.ChargePolicyRef,
		billingIdentityStamped:       pr.billingIdentityStamped,
		billingCallID:                pr.billingCallID,
		billingCallState:             pr.billingCallState,
		submissionID:                 submissionIDForBilling(pr.submission),
		conversationSnapshot:         cloneSnapshot(pr.conversationSnapshot),
		conversationProvenance:       slices.Clone(prov),
		conversationFilteredBaseline: aipapi.CloneCall(filtered),
		ingressCall:                  ingress,
	}, pr.billingStoreID, pr.billingStoreIDStamped))
	return pr, prepCtx, guard.Close, nil
}

func (p *preparedRequest) finalize(err error) {
	if err != nil {
		p.execSpan.RecordError(err)
		p.execSpan.SetStatus(codes.Error, err.Error())
	}
	p.execSpan.End()
}

type identityBoundTurn struct {
	traceID      string
	call         *aipapi.Call
	ingressCall  *aipapi.Call
	principal    execview.PrincipalView
	scope        scope.PrincipalScopeView
	hasPrincipal bool
	workspace    aipworkspace.WorkspaceView
	aLeg         b2bua.ALegRecord
	routeAuth    routeAuthoritySnapshot
	secureTurn   execctx.SecureSessionTurn
	secureTurnOK bool
	preSession   session.SessionView
	// Task 3.2 frozen view carried from 3.1 seam.
	conversationSnapshot         conversationprojection.Snapshot
	conversationEvidence         *conversationprojection.ProjectionEvidence
	conversationSummary          conversationProjectionSummary
	conversationFilteredBaseline *aipapi.Call
	convSnapshotSet              bool
	localSelection               localTurnSelection
}

func newIdentityBoundTurn(traceID string, call *aipapi.Call, principal execview.PrincipalView, scope scope.PrincipalScopeView, hasPrincipal bool, workspace aipworkspace.WorkspaceView, aLeg b2bua.ALegRecord, routeAuth routeAuthoritySnapshot, secureTurn execctx.SecureSessionTurn, secureTurnOK bool, preSession session.SessionView) (*identityBoundTurn, error) {
	if traceID == "" || call == nil || aLeg.ALegID == "" || call.Session.ALegID != aLeg.ALegID || preSession.ALegID != aLeg.ALegID {
		return nil, fmt.Errorf("invalid ibt args or A-leg ID mismatch")
	}
	if secureTurnOK {
		if secureTurn.SessionID == "" || secureTurn.TurnID == "" ||
			call.Session.AuthoritativeSessionID != string(secureTurn.SessionID) ||
			preSession.AuthoritativeSessionID != string(secureTurn.SessionID) ||
			preSession.TurnID != string(secureTurn.TurnID) ||
			preSession.WorkspaceID != workspace.ID {
			return nil, fmt.Errorf("invalid ibt args or secure turn mismatch")
		}
	} else {
		if secureTurn.SessionID != "" || secureTurn.TurnID != "" ||
			call.Session.AuthoritativeSessionID != "" ||
			preSession.AuthoritativeSessionID != "" ||
			preSession.TurnID != "" ||
			preSession.WorkspaceID != "" ||
			workspace.ID != "" {
			return nil, fmt.Errorf("invalid ibt args or non-empty detached session")
		}
	}
	cloned := aipapi.CloneCall(*call)
	return &identityBoundTurn{traceID: traceID, call: &cloned, principal: principal, scope: scope, hasPrincipal: hasPrincipal, workspace: workspace, aLeg: aLeg, routeAuth: routeAuth, secureTurn: secureTurn, secureTurnOK: secureTurnOK, preSession: preSession}, nil
}

func (t *identityBoundTurn) projectContext(ctx context.Context) context.Context {
	if t == nil {
		return ctx
	}
	if t.hasPrincipal {
		ctx = scope.WithScope(execview.WithPrincipal(ctx, t.principal), t.scope)
	}
	if t.secureTurnOK {
		ctx = execctx.WithSecureSessionTurn(ctx, t.secureTurn)
	}
	views, _ := execctx.FromContext(ctx)
	if t.hasPrincipal {
		views.Principal, views.Scope = t.principal, t.scope
	}
	views.Workspace = t.workspace
	labels := views.Session.Labels
	views.Session = t.preSession
	if len(t.preSession.Labels) > 0 {
		views.Session.Labels = maps.Clone(t.preSession.Labels)
	}
	if len(labels) > 0 {
		if views.Session.Labels == nil {
			views.Session.Labels = make(map[string]string)
		}
		maps.Copy(views.Session.Labels, labels)
	}
	return execctx.WithViews(ctx, views)
}

type preStreamGuard struct {
	mu                       sync.Mutex
	executor                 *Executor
	ctx                      context.Context
	aScope                   *leglifecycle.ALeg
	requestAuthorityAdmitted bool
	handedOver               bool
	closed                   bool
}

func (g *preStreamGuard) Handoff() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.handedOver = true
}

func (g *preStreamGuard) Close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.closed || g.handedOver {
		g.mu.Unlock()
		return
	}
	g.closed = true
	g.mu.Unlock()

	if g.requestAuthorityAdmitted {
		_ = g.executor.releaseRequestAuthority(g.ctx)
	}
	if g.aScope != nil {
		cleanupCtx, cleanupCancel := detachedCleanupContext(g.ctx, cancelLosersTimeout)
		defer cleanupCancel()
		_ = g.aScope.Cancel(cleanupCtx, leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone})
		g.aScope.End()
	}
}

type testInjectBillingErrorKey struct{}
