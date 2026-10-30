package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	coretraffic "github.com/aiproxer/aiproxer/internal/core/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

const (
	syntheticLocalPrincipalID     = "local-dev"
	syntheticLocalPrincipalIssuer = "aip-localhost"
)

// SecureSessionPrepInput carries fact-based inputs required to prepare a secure-session turn
// for either canonical or wire execution without requiring a canonical aipapi.Call.
type SecureSessionPrepInput struct {
	TraceID       string
	Session       largebody.SessionInput
	ContinuityKey string
}

// PreparedSecureSession holds the pre-turn resolution state before BeginTurn commit.
// Preparation performs scope/principal resolution, context diagnostic binding, session openers,
// and workspace resolution, but strictly DOES NOT invoke BeginTurn or mutate session/store state
// (Requirements 6.2, 14.1, 19).
type PreparedSecureSession struct {
	executor     *Executor
	outCtx       context.Context
	traceID      string
	sessionInput largebody.SessionInput
	principal    execview.PrincipalView
	hasPrincipal bool
	scope        scope.PrincipalScopeView
	wsView       aipworkspace.WorkspaceView
	preSession   session.SessionView
	beginIn      app.BeginInput
}

func (p *PreparedSecureSession) Context() context.Context              { return p.outCtx }
func (p *PreparedSecureSession) TraceID() string                       { return p.traceID }
func (p *PreparedSecureSession) SessionInput() largebody.SessionInput  { return p.sessionInput }
func (p *PreparedSecureSession) Principal() execview.PrincipalView     { return p.principal }
func (p *PreparedSecureSession) HasPrincipal() bool                    { return p.hasPrincipal }
func (p *PreparedSecureSession) Scope() scope.PrincipalScopeView       { return p.scope }
func (p *PreparedSecureSession) Workspace() aipworkspace.WorkspaceView { return p.wsView }
func (p *PreparedSecureSession) PreSession() session.SessionView       { return p.preSession }
func (p *PreparedSecureSession) BeginInput() app.BeginInput            { return p.beginIn }

func (p *PreparedSecureSession) ExecuteBeginTurn(ctx context.Context) (app.BeginResult, error) {
	if p == nil || p.executor == nil || p.executor.SecureSession == nil {
		return app.BeginResult{}, fmt.Errorf("executor: secure session manager is required")
	}
	e := p.executor
	execCtx := p.outCtx
	if ctx != nil {
		execCtx = ctx
	}
	br, err := e.SecureSession.BeginTurn(execCtx, p.beginIn)
	if err != nil {
		mapped := err
		if e != nil && e.SessionDenialMapper != nil {
			mapped = e.SessionDenialMapper(err)
		}
		if e != nil && e.SecureSessionMetrics != nil {
			if errors.Is(err, domain.ErrStorageUnavailable) {
				e.SecureSessionMetrics.ObserveStorageUnavailable()
			}
			code := aipapi.SessionDenialPublicCode(mapped)
			if code == "" {
				code = "unknown"
			}
			e.SecureSessionMetrics.ObserveBeginTurnDenied(code)
		}
		if e != nil && e.Log != nil {
			logCode := aipapi.SessionDenialPublicCode(mapped)
			if logCode == "" {
				logCode = "unknown"
			}
			e.Log.InfoContext(execCtx, "secure_session: begin turn denied", "code", logCode, "trace_id", strings.TrimSpace(p.traceID), "client_session_id", HashOpaqueIDForLog(p.sessionInput.ClientSessionID))
		}
		return app.BeginResult{}, fmt.Errorf("executor: secure session: %w", mapped)
	}
	if e != nil && e.SecureSessionMetrics != nil {
		if br.IsNew {
			e.SecureSessionMetrics.ObserveBeginTurnNew()
		} else {
			e.SecureSessionMetrics.ObserveBeginTurnResume()
		}
	}
	return br, nil
}

func (p *PreparedSecureSession) ResolveALeg(ctx context.Context, alegID string) (b2bua.ALegRecord, routeAuthoritySnapshot, error) {
	if p == nil || p.executor == nil || p.executor.Store == nil {
		return b2bua.ALegRecord{}, routeAuthoritySnapshot{}, fmt.Errorf("executor: store is required to fetch a-leg")
	}
	e := p.executor
	execCtx := p.outCtx
	if ctx != nil {
		execCtx = ctx
	}
	aLeg, err := e.Store.FetchALeg(execCtx, alegID)
	if err != nil {
		return b2bua.ALegRecord{}, routeAuthoritySnapshot{}, fmt.Errorf("executor: fetch a-leg after secure session: %w", err)
	}
	routeAuth, err := e.snapshotRouteOverride(execCtx, aLeg.ALegID)
	if err != nil {
		return b2bua.ALegRecord{}, routeAuthoritySnapshot{}, err
	}
	if err := waitRouteAuthoritySnapshotBarrier(execCtx, aLeg.ALegID); err != nil {
		return b2bua.ALegRecord{}, routeAuthoritySnapshot{}, fmt.Errorf("executor: route authority snapshot barrier: %w", err)
	}
	return aLeg, routeAuth, nil
}

func (p *PreparedSecureSession) BindSession(br app.BeginResult, aLeg b2bua.ALegRecord) session.SessionView {
	s := p.preSession
	s.ALegID = aLeg.ALegID
	s.AuthoritativeSessionID = string(br.Record.SessionID)
	s.IsNew = br.IsNew
	s.AuthoritativeFirstTurn = br.IsNew
	s.ResumeEligible = br.Record.ResumeEligible
	s.TurnID = string(br.TurnID)
	s.WorkspaceID = strings.TrimSpace(p.wsView.ID)
	return s
}

func (p *PreparedSecureSession) SecureTurn(br app.BeginResult) execctx.SecureSessionTurn {
	return execctx.SecureSessionTurn{
		SessionID: br.Record.SessionID,
		TurnID:    br.TurnID,
		Policy:    br.EffectivePolicy,
	}
}

func (p *PreparedSecureSession) ResponseCarrier(br app.BeginResult) largebody.SessionResponseCarrier {
	if p == nil {
		return largebody.SessionResponseCarrier{}
	}
	var token string
	if br.IsNew && len(br.Response.ResumeToken) > 0 {
		token = string(br.Response.ResumeToken)
	}
	aLegID := strings.TrimSpace(br.Record.ALegID)
	if aLegID == "" {
		aLegID = strings.TrimSpace(p.sessionInput.ALegID)
	}
	return largebody.SessionResponseCarrier{
		AuthoritativeSessionID: string(br.Record.SessionID),
		ALegID:                 aLegID,
		ResumeToken:            largebody.NewSensitiveString(token),
	}
}

// RecordClientTurnWithShape records accepted client turn facts directly from
// a bounded ClientTurnShape without prompt text materialization (Requirements 14.3, 14.5).
// Semantic-fact budget overflow returns an error wrapping largebody.ErrSemanticFactBudgetExceeded
// so the caller can trigger pre-commit canonical fallback (Requirement 14.4).
func (p *PreparedSecureSession) RecordClientTurnWithShape(
	ctx context.Context,
	br app.BeginResult,
	shape largebody.ClientTurnShape,
	maxFactBytes int64,
) error {
	if p == nil || p.executor == nil {
		return fmt.Errorf("executor: executor is required")
	}
	in, err := BuildClientTurnRecordInputFromShape(p.executor.now(), p.traceID, br, shape, maxFactBytes)
	if err != nil {
		return err
	}
	if p.executor.SecureSessionRecorder == nil {
		return nil
	}
	execCtx := p.outCtx
	if ctx != nil {
		execCtx = ctx
	}
	if err := p.executor.SecureSessionRecorder.RecordClientTurnAfterGate(execCtx, in); err != nil {
		if p.executor.SecureSessionMetrics != nil {
			p.executor.SecureSessionMetrics.ObserveRecorderClientTurnFailed(p.executor.SecureSessionRecordingMandatory)
		}
		if p.executor.SecureSessionRecordingMandatory {
			return fmt.Errorf("executor: secure session recording: %w", err)
		}
		if p.executor.Log != nil {
			p.executor.Log.DebugContext(execCtx, "secure_session recorder client turn", "error", err)
		}
	}
	return nil
}

// BuildClientTurnRecordInput builds an app.ClientTurnRecordInput from this prepared session
// and a ClientTurnShape under maxFactBytes without materializing prompt text (Requirements 14.3, 14.5).
func (p *PreparedSecureSession) BuildClientTurnRecordInput(
	br app.BeginResult,
	shape largebody.ClientTurnShape,
	maxFactBytes int64,
) (app.ClientTurnRecordInput, error) {
	if p == nil || p.executor == nil {
		return app.ClientTurnRecordInput{}, fmt.Errorf("executor: executor is required")
	}
	return BuildClientTurnRecordInputFromShape(p.executor.now(), p.traceID, br, shape, maxFactBytes)
}

// CaptureFrontendIngressCheckpoint captures an immutable FE-ingress checkpoint
// from bounded wire facts and post-BeginTurn session/a-leg correlation,
// sharing exact canonical checkpoint helpers without cloning or retaining a aipapi.Call
// (Requirements 15.1–15.3, 16.1–16.6, 19).
func (p *PreparedSecureSession) CaptureFrontendIngressCheckpoint(
	ctx context.Context,
	requestID string,
	br app.BeginResult,
	aLeg b2bua.ALegRecord,
	maxOutputTokens *int,
) (context.Context, *checkpoint.RequestHolder, error) {
	if p == nil || p.executor == nil {
		return ctx, nil, fmt.Errorf("executor: executor is required")
	}
	execCtx := p.outCtx
	if ctx != nil {
		execCtx = ctx
	}
	sessionID := strings.TrimSpace(string(br.Record.SessionID))
	if sessionID == "" {
		sessionID = p.sessionInput.CorrelationID()
	}
	aLegID := strings.TrimSpace(aLeg.ALegID)
	if aLegID == "" {
		aLegID = strings.TrimSpace(br.Record.ALegID)
	}
	if aLegID == "" {
		aLegID = strings.TrimSpace(p.sessionInput.ALegID)
	}
	return captureWireFrontendIngress(execCtx, WireFrontendIngressArgs{
		RequestID:       requestID,
		TraceID:         p.traceID,
		Scope:           p.scope,
		ALegID:          aLegID,
		SessionID:       sessionID,
		MaxOutputTokens: maxOutputTokens,
		Now:             p.executor.now(),
	})
}

// PersistFrontendIngressFact appends the customer FE-ingress journal fact
// when a MeteringRecorder is configured and binds its FactID (Requirements 15.1–15.3, 19).
func (p *PreparedSecureSession) PersistFrontendIngressFact(ctx context.Context, holder *checkpoint.RequestHolder) (string, error) {
	if p == nil || p.executor == nil {
		return "", fmt.Errorf("executor: executor is required")
	}
	execCtx := p.outCtx
	if ctx != nil {
		execCtx = ctx
	}
	return p.executor.persistFrontendIngressFact(execCtx, holder)
}

// EnrichWireFrontendIngressQuantities merges exact measured wire token counting results
// into the stored FrontendIngress snapshot for this prepared session (Requirements 15.4, 15.5).
func (p *PreparedSecureSession) EnrichWireFrontendIngressQuantities(holder *checkpoint.RequestHolder, count largebody.WireCountResult) {
	if p == nil || p.executor == nil {
		return
	}
	p.executor.enrichWireFrontendIngressQuantities(holder, count)
}

// PrepareSecureSession prepares fact-based inputs for secure-session execution.
// It executes scope resolution, session openers, and workspace resolution, but
// strictly DOES NOT call BeginTurn or mutate session/store state (Requirements 6.2, 14.1, 19).
func (e *Executor) PrepareSecureSession(ctx context.Context, in SecureSessionPrepInput) (*PreparedSecureSession, error) {
	if e == nil || e.SecureSession == nil {
		return nil, fmt.Errorf("executor: secure session manager is required")
	}

	traceID := strings.TrimSpace(in.TraceID)
	outCtx := ctx
	var principal execview.PrincipalView
	hasPrincipal := false
	var reqScope scope.PrincipalScopeView
	if s, p, ok := e.resolveRequestScope(ctx); ok {
		reqScope, principal, hasPrincipal, outCtx = s, p, true, scope.WithScope(execview.WithPrincipal(outCtx, p), s)
	}
	outCtx = diag.WithCallDiag(outCtx, traceID, "")

	preSession := session.SessionView{
		AuthoritativeSessionID: strings.TrimSpace(in.Session.AuthoritativeSessionID),
		ClientSessionHint:      strings.TrimSpace(in.Session.ClientSessionID),
		ALegID:                 "",
		IsNew:                  false,
		ResumeEligible:         false,
	}

	snap := e.RuntimeSnapshot
	if snap != nil {
		openIn := session.OpenInput{TraceID: traceID, Principal: principal, Session: preSession}
		openRes := extensions.RunSessionOpenStage(
			outCtx,
			e.Log,
			e.ExtensionMetrics,
			snap.SessionOpeners(),
			openIn,
		)
		if len(openRes.SessionLabelUpserts) > 0 {
			if preSession.Labels == nil {
				preSession.Labels = make(map[string]string, len(openRes.SessionLabelUpserts))
			}
			maps.Copy(preSession.Labels, openRes.SessionLabelUpserts)
		}
	}

	var wsView aipworkspace.WorkspaceView
	if snap != nil {
		wsStart := time.Now()
		wsCtx, wsSpan := otel.Tracer(otelScopeExecutor).Start(outCtx, "aip.executor.workspace_resolve")
		var werr error
		wsView, werr = snap.Workspace().Resolve(wsCtx)
		outcome := "ok"
		if werr != nil {
			if e != nil && e.SecureSessionWorkspaceResolveFailClosed {
				outcome = "fail_closed"
				wsSpan.RecordError(werr)
				wsSpan.SetStatus(codes.Error, "workspace resolve failed")
				wsSpan.End()
				outCtx = wsCtx
				if e.ExtensionMetrics != nil {
					e.ExtensionMetrics.ObserveStage(extensions.MetricsStageWorkspaceResolve, outcome, time.Since(wsStart).Seconds())
				}
				mapped := domain.ErrWorkspaceUnresolved
				if e.SessionDenialMapper != nil {
					mapped = e.SessionDenialMapper(domain.ErrWorkspaceUnresolved)
				}
				if e.SecureSessionMetrics != nil {
					code := aipapi.SessionDenialPublicCode(mapped)
					if code == "" {
						code = "unknown"
					}
					e.SecureSessionMetrics.ObserveBeginTurnDenied(code)
				}
				if e.Log != nil {
					e.Log.InfoContext(outCtx, "secure_session: workspace resolve denied", "code", aipapi.SessionDenialPublicCode(mapped), "trace_id", strings.TrimSpace(traceID), "error", werr)
				}
				return nil, fmt.Errorf("executor: secure session: %w", mapped)
			}
			outcome = "fail_open"
			if e.Log != nil {
				e.Log.DebugContext(wsCtx, "workspace: resolve error (fail-open)", "error", werr)
			}
			wsSpan.RecordError(werr)
			wsSpan.SetStatus(codes.Error, "workspace resolve failed")
		} else {
			wsSpan.SetStatus(codes.Ok, "")
		}
		wsSpan.End()
		outCtx = wsCtx
		if e.ExtensionMetrics != nil {
			e.ExtensionMetrics.ObserveStage(extensions.MetricsStageWorkspaceResolve, outcome, time.Since(wsStart).Seconds())
		}
	}

	beginIn := app.BeginInput{
		Now:     e.now(),
		TraceID: traceID,
		Session: app.SessionWire{
			ClientSessionID: in.Session.ClientSessionID,
			ContinuityKey:   in.ContinuityKey,
			ALegID:          in.Session.ALegID,
			SessionID:       in.Session.AuthoritativeSessionID,
			ResumeToken:     in.Session.ResumeToken.Reveal(),
		},
		Principal:              principalRefFromScope(principal, reqScope),
		Workspace:              domain.WorkspaceRef{ID: strings.TrimSpace(wsView.ID)},
		GlobalPolicy:           app.DefaultGlobalPolicy(),
		ClientHints:            domain.ClientHints{ClientSessionID: strings.TrimSpace(in.Session.ClientSessionID)},
		FirstMessageDigest:     "",
		WorkspaceMatchRequired: e != nil && e.SecureSessionRequireWorkspaceID,
	}

	return &PreparedSecureSession{
		executor:     e,
		outCtx:       outCtx,
		traceID:      traceID,
		sessionInput: in.Session,
		principal:    principal,
		hasPrincipal: hasPrincipal,
		scope:        reqScope,
		wsView:       wsView,
		preSession:   preSession,
		beginIn:      beginIn,
	}, nil
}

func (e *Executor) prepareSubmitAndALegSecure(
	ctx context.Context,
	bus *hooks.Bus,
	call *aipapi.Call,
) (
	ibt *identityBoundTurn,
	workingCall *aipapi.Call,
	outCtx context.Context,
	err error,
) {
	snap := e.RuntimeSnapshot
	work := *call
	// The frontend's route and origin are the baseline the final authority is
	// measured against: a hook that rewrites the selector, or an administrator's
	// route override, replaces them.
	frontendOrigin := routing.RouteOriginFromContext(ctx)
	frontendSelector := strings.TrimSpace(call.Route.Selector)
	traceID := strings.TrimSpace(work.ID)
	if traceID == "" {
		traceID = diag.StableCallID(&work)
	}
	work.ID, call.ID = traceID, traceID

	prep, err := e.PrepareSecureSession(ctx, SecureSessionPrepInput{
		TraceID:       traceID,
		Session:       largebody.SessionInputFromRef(work.Session),
		ContinuityKey: work.Session.ContinuityKey,
	})
	if err != nil {
		return nil, nil, ctx, err
	}
	outCtx = prep.Context()

	br, err := prep.ExecuteBeginTurn(outCtx)
	if err != nil {
		return nil, nil, outCtx, err
	}

	work.Session.AuthoritativeSessionID = string(br.Record.SessionID)
	work.Session.ALegID = strings.TrimSpace(br.Record.ALegID)
	work.Session.ResumeToken = ""

	aLeg, routeAuth, err := prep.ResolveALeg(outCtx, br.Record.ALegID)
	if err != nil {
		return nil, nil, outCtx, err
	}

	work.Session.ContinuityKey = strings.TrimSpace(aLeg.ContinuityKey)
	work.Session.ALegID = aLeg.ALegID

	// Preserve unaugmented customer request for ingress accounting (P2) and
	// semantic delta preservation (P1). The pre-open observer may append a
	// proxy-generated continuity block; that block must not contaminate the
	// customer-perspective checkpoint nor cause the semantic source watermark
	// to be consumed before RequestOpened extracts user decisions/constraints.
	// Capture the immutable customer view before mutation and keep it for
	// metering and ingress provenance.
	unaugmentedForIngress := aipapi.CloneCall(work)

	// TP-2 pre-open seam: after the per-turn override snapshot and route
	// authority barrier (inside ResolveALeg) and before secret-guard, submit,
	// request stages and route-plan construction. Runs once per logical request
	// (prepare runs once; retries reuse prep), before the primary B-leg opens.
	// Fail-open: preserver errors never propagate; auxiliary turns skipped
	// inside the observer; wire fast-path has no aipapi.Call so stays canonical-only.
	// execctx views attach later, so carry the minimal trusted session view
	// (authoritative IDs plus session-open label upserts) on the observer ctx;
	// pre-open policy then sees the same session overrides as post-open.
	// P1: The real preserver's BeforeRequest would commit the source high
	// watermark based on the current call. That would make source.Prepare in
	// RequestOpened see original items as already processed (injection only
	// appends a developer item) and submit an empty SanitizedDelta while still
	// being extraction-eligible via previewBound. Preserve the staged delta
	// until successful open by not consuming watermark during preparation; the
	// plugin's BeforeRequest is changed to stage without advancing the watermark.
	// P2: Metering must use the unaugmented customer view, not the injected one.
	e.observeCompactionBeforeRequest(
		session.WithSessionView(outCtx, session.SessionView{
			AuthoritativeSessionID: work.Session.AuthoritativeSessionID,
			ALegID:                 aLeg.ALegID,
			Labels:                 prep.PreSession().Labels,
		}),
		traceID, aLeg.ALegID, &work,
	)

	preSession := prep.BindSession(br, aLeg)
	secureTurn := prep.SecureTurn(br)
	secureTurnOK := true

	ibt, err = newIdentityBoundTurn(traceID, &work, prep.Principal(), prep.Scope(), prep.HasPrincipal(), prep.Workspace(), aLeg, routeAuth, secureTurn, secureTurnOK, preSession)
	if err != nil {
		return nil, nil, outCtx, fmt.Errorf("executor: create identity bound turn: %w", err)
	}
	var baseEvidence *extensions.DecisionEvidence
	workingCall = &work
	if snap != nil {
		guardViews := execctx.Views{
			Principal: ibt.principal,
			Scope:     ibt.scope,
			Session:   ibt.preSession,
			Attempt:   execview.AttemptView{TraceID: ibt.traceID},
			Workspace: ibt.workspace,
		}
		baseEvidence = &extensions.DecisionEvidence{
			Emitter:       e.policyEvidenceEmitter(snap),
			Views:         guardViews,
			TimeoutBudget: snap.TimeoutBudgetSource(),
			TimeoutGuard:  snap.ProviderTimeoutGuard(),
		}
		outCtx = extensions.WithDecisionEvidence(outCtx, baseEvidence)
		if err := e.runSecretGuardStage(outCtx, workingCall, secretGuardStageInput{
			TraceID:   ibt.traceID,
			Principal: ibt.principal,
			Scope:     ibt.scope,
			Session:   ibt.preSession,
			Workspace: ibt.workspace,
			SessionID: ibt.secureTurn.SessionID,
			TurnID:    ibt.secureTurn.TurnID,
		}); err != nil {
			return nil, nil, outCtx, err
		}
	}
	// Stage session_classification sits after secret_guard and before submit_request
	// (design "Current extension ordering" / "Canonical Execution Integration"):
	// the authoritative secure session, A-leg and workspace are already bound, and
	// no classification evaluation may observe or precede secret-guard processing.
	// A validated result is projected onto ibt.preSession here so every later
	// same-turn view observes one immutable classification. The stage returns
	// nothing: a missing plane is a no-op and a classifier/store/remote failure
	// never rejects the request.
	e.runSessionClassificationStage(outCtx, workingCall, ibt)
	var meteringHolder *checkpoint.RequestHolder
	// P2: use unaugmented customer view for FE-ingress checkpoint; the
	// current workingCall may already contain the proxy-appended continuity
	// block, which must not be counted as customer input.
	outCtx, meteringHolder, err = captureFrontendIngressBeforeSubmit(outCtx, unaugmentedForIngress, ibt.scope, e.now())
	if err != nil {
		return nil, nil, outCtx, err
	}
	_ = meteringHolder
	outCtx, err = e.admitRequestAuthorityOnce(outCtx, workingCall.ID, ibt.aLeg.ALegID, ibt.traceID, ibt.scope)
	if err != nil {
		return nil, nil, outCtx, err
	}
	failAfterRequestAdmit := func(err error) (*identityBoundTurn, *aipapi.Call, context.Context, error) {
		_ = e.releaseRequestAuthority(outCtx)
		return nil, nil, outCtx, err
	}
	submitMeta := &sdk.SubmitMeta{TraceID: ibt.traceID, Annotations: map[string]string{}}
	if e.Log != nil {
		outCtx = hooks.WithDiagnosticsLogger(outCtx, e.Log)
	}
	if snap != nil {
		submitViews := execctx.Views{
			Principal:   ibt.principal,
			Scope:       ibt.scope,
			Session:     ibt.preSession,
			Attempt:     execview.AttemptView{TraceID: ibt.traceID},
			Workspace:   ibt.workspace,
			Annotations: submitMeta.Annotations,
		}
		baseEvidence = baseEvidence.WithViews(submitViews)
		outCtx = extensions.WithDecisionEvidence(outCtx, baseEvidence)
		outCtx = hooks.WithSubmitEvidence(outCtx, extensions.NewSubmitEvidenceFunc(baseEvidence))
	}
	if err := bus.RunSubmit(outCtx, workingCall, submitMeta); err != nil {
		return failAfterRequestAdmit(err)
	}
	if snap != nil {
		bundle := coretraffic.PortBundleFromSnapshot(snap)
		if !bundle.EmitIsNoop() {
			ctpCall := *workingCall
			ctpSess := workingCall.Session
			ctpSess.ResumeToken = ""
			ctpCall.Session = ctpSess
			if rawPayload, jerr := json.Marshal(&ctpCall); jerr == nil {
				meta := sdktraffic.CaptureMeta{
					TraceID:     ibt.traceID,
					ALegID:      strings.TrimSpace(ibt.aLeg.ALegID),
					SessionID:   ctpCall.Session.CorrelationID(),
					PrincipalID: strings.TrimSpace(ibt.principal.ID),
					Scope:       ibt.scope.Clone(),
				}
				bundle.Emit(
					outCtx,
					sdktraffic.LegCTP,
					meta,
					"aip/canonical+json",
					"application/json",
					rawPayload,
				)
			} else if e.Log != nil {
				e.Log.DebugContext(outCtx, "submit traffic marshal skipped", "leg", sdktraffic.LegCTP, "error", jerr)
			}
		}
	}
	ingressClone := aipapi.CloneCall(unaugmentedForIngress)
	ibt.ingressCall = &ingressClone
	if err := e.selectLocalAndBootstrap(outCtx, ibt, workingCall); err != nil {
		return failAfterRequestAdmit(err)
	}
	if snap != nil {
		// --- Task 3.2 seam: snapshot once after authoritative A-leg resolution ---
		// Preserve ingress before projection; project exclusion+steering ONCE
		// before backend request/pre-request transforms, context estimation,
		// billing, routing/capability/baseline. Fail closed on snapshot/
		// projection errors; evidence stays bounded content-free.
		// P2: ingress is customer perspective (unaugmented); backend is the
		// augmented workingCall that already carries the continuity block.
		backendClone := aipapi.CloneCall(*workingCall)
		// Snapshot coherent view and derive backend-effective call.
		snapView, projEv, projected, perr := e.snapshotAndProject(outCtx, ibt.aLeg.ALegID, backendClone)
		if perr != nil {
			return failAfterRequestAdmit(perr)
		}
		ibt.conversationSnapshot = snapView
		ibt.conversationEvidence = projEv
		ibt.conversationSummary = newConversationProjectionSummary(snapView, projEv)
		ibt.convSnapshotSet = true
		// Projection reads its input and returns a fresh call, so backendClone
		// still carries the unfiltered original for this second pass.
		if filtered, ferr := conversationprojection.FilterNeverBackend(backendClone, snapView); ferr == nil {
			ibt.conversationFilteredBaseline = &filtered
		} else {
			// Filter should not fail if Project succeeded; treat as fail-closed
			return failAfterRequestAdmit(ferr)
		}
		backendClone = projected
		workingCall = &backendClone
		ann := maps.Clone(submitMeta.Annotations)
		if ann == nil {
			ann = make(map[string]string, len(submitMeta.Annotations))
		}
		reqMeta := request.RequestMeta{
			TraceID:     ibt.traceID,
			Annotations: ann,
			Principal:   ibt.principal,
			Scope:       ibt.scope,
			Session:     ibt.preSession,
			Workspace:   ibt.workspace,
		}
		if reqMeta.Annotations == nil {
			reqMeta.Annotations = make(map[string]string, len(submitMeta.Annotations))
		}
		pdViews := decisionViewsFromRequestMeta(reqMeta, execview.AttemptView{TraceID: ibt.traceID})
		outCtx = extensions.WithDecisionEvidence(outCtx, baseEvidence.WithViews(pdViews))
		catalogMeta := toolcatalog.CatalogMeta{
			TraceID:     ibt.traceID,
			Annotations: ann,
			Principal:   ibt.principal,
			Session:     ibt.preSession,
			Workspace:   ibt.workspace,
		}
		catSvc := toolcatalog.Services{State: snap.State(), Aux: snap.Aux()}
		if err := extensions.RunToolCatalogFilterStage(
			outCtx,
			e.Log,
			e.ExtensionMetrics,
			snap.ToolCatalogFilters(),
			workingCall,
			catalogMeta,
			catSvc,
		); err != nil {
			return failAfterRequestAdmit(err)
		}
		reqSvc := request.Services{State: snap.State(), Aux: snap.Aux()}
		if err := extensions.RunRequestTransformStage(
			outCtx,
			e.Log,
			e.ExtensionMetrics,
			snap.RequestTransforms(),
			workingCall,
			reqMeta,
			reqSvc,
		); err != nil {
			return failAfterRequestAdmit(err)
		}
		preMeta := prerequest.Meta{
			TraceID:        ibt.traceID,
			Annotations:    ann,
			Principal:      ibt.principal,
			Scope:          ibt.scope,
			Session:        ibt.preSession,
			Workspace:      ibt.workspace,
			AuxiliaryDepth: execctx.AuxiliaryDepth(outCtx),
		}
		preSvc := prerequest.Services{State: snap.State(), Aux: snap.Aux()}
		if err := extensions.RunPreRequestStage(
			outCtx,
			e.Log,
			e.ExtensionMetrics,
			snap.PreRequestHandlers(),
			workingCall,
			preMeta,
			preSvc,
		); err != nil {
			return failAfterRequestAdmit(err)
		}
	}
	// Ensure ingress is set even when snap == nil (no CTP branch).
	if ibt.ingressCall == nil {
		ingressClone := aipapi.CloneCall(unaugmentedForIngress)
		ibt.ingressCall = &ingressClone
		// Ensure backend workingCall is a distinct clone for isolation.
		backendClone := aipapi.CloneCall(*workingCall)
		workingCall = &backendClone
	}
	effective := aipapi.CloneCall(*workingCall)
	if ibt.routeAuth.active() {
		effective.Route.Selector = ibt.routeAuth.State.Selector
	}
	if snap != nil {
		hintIn := routehint.Input{
			TraceID:   ibt.traceID,
			Call:      &effective,
			Principal: ibt.principal,
			Session:   ibt.preSession,
			Workspace: ibt.workspace,
		}
		prefs, err := extensions.RunRouteHintStage(
			outCtx,
			e.Log,
			snap.RouteHintProviders(),
			&effective,
			hintIn,
		)
		if err != nil {
			return failAfterRequestAdmit(err)
		}
		outCtx = execctx.WithRouteCandidatePreferences(outCtx, prefs)
	}
	if snap != nil {
		// A route-hint provider saw &effective and may still hold it; detach
		// the working call from that retained pointer.
		*workingCall = aipapi.CloneCall(effective)
	} else {
		// No extension saw effective, so its deep copy can become the working
		// call without another full-history clone.
		*workingCall = effective
	}
	call.Session = workingCall.Session
	if br.IsNew && len(br.Response.ResumeToken) > 0 {
		call.Session.ResumeToken = string(br.Response.ResumeToken)
	}
	outCtx = diag.EnsureCallDiag(outCtx, ibt.traceID, ibt.aLeg.ALegID)
	policyLabels := policyLabelsFromMetadata(br.EffectivePolicy)
	views := execctx.ViewsFromSecureSubmit(execctx.SecureSubmitViewsInput{
		TraceID:                ibt.traceID,
		ALeg:                   ibt.aLeg,
		Call:                   *workingCall,
		HookAnnotations:        submitMeta.Annotations,
		AuthoritativeSessionID: string(br.Record.SessionID),
		TurnID:                 string(br.TurnID),
		ResumeEligible:         br.Record.ResumeEligible,
		// The secure-session authority decides the first turn, not a
		// timestamp: only an authoritative first turn may derive a project.
		AuthoritativeFirstTurn: br.IsNew,
		PolicyLabels:           policyLabels,
		// The classification stage already decided this turn's classification;
		// projecting it here keeps every later same-turn view on one immutable
		// snapshot instead of letting a consumer re-derive it (requirements 4.2, 9.5).
		Classification: ibt.preSession.Classification,
	})
	if ibt.hasPrincipal {
		views.Principal = ibt.principal
		views.Scope = ibt.scope
	}
	views.Workspace = ibt.workspace
	views.Session.WorkspaceID = strings.TrimSpace(ibt.workspace.ID)
	if len(ibt.preSession.Labels) > 0 {
		if views.Session.Labels == nil {
			views.Session.Labels = make(map[string]string)
		}
		maps.Copy(views.Session.Labels, ibt.preSession.Labels)
	}
	outCtx = execctx.WithSecureSessionTurn(outCtx, ibt.secureTurn)
	if e.SecureSessionRecorder != nil {
		// Structural client records describe ingress, not backend-only projection.
		in := buildClientTurnRecordInput(e.now(), ibt.traceID, br, ibt.ingressCall)
		if err := e.SecureSessionRecorder.RecordClientTurnAfterGate(outCtx, in); err != nil {
			if e.SecureSessionMetrics != nil {
				e.SecureSessionMetrics.ObserveRecorderClientTurnFailed(e.SecureSessionRecordingMandatory)
			}
			if e.SecureSessionRecordingMandatory {
				return failAfterRequestAdmit(fmt.Errorf("executor: secure session recording: %w", err))
			}
			if e.Log != nil {
				e.Log.DebugContext(outCtx, "secure_session recorder client turn", "error", err)
			}
		}
	}
	outCtx = execctx.WithViews(outCtx, views)
	if err := e.emitSessionStartIfNeeded(
		outCtx,
		ibt.traceID,
		principalSnapshotForSessionAudit(ibt.principal),
		br,
		*workingCall,
		ibt.aLeg,
	); err != nil {
		return failAfterRequestAdmit(err)
	}
	// The route origin is final at the last authority that could replace the
	// route: an overridden or rewritten route is not the proxy's own default
	// route, so it uses the current request-bound registry.
	outCtx = routing.WithRouteOrigin(outCtx, finalRouteOrigin(
		frontendOrigin,
		frontendSelector,
		workingCall.Route.Selector,
		routeAuth.active(),
	))
	return ibt, workingCall, outCtx, nil
}

func policyLabelsFromMetadata(p domain.PolicyMetadata) map[string]string {
	out := make(map[string]string)
	if s := strings.TrimSpace(p.PolicyVersion); s != "" {
		out["policy_version"] = s
	}
	if s := strings.TrimSpace(p.EffectiveTreatment); s != "" {
		out["effective_treatment"] = s
	}
	if s := strings.TrimSpace(p.AuditMode); s != "" {
		out["audit_mode"] = s
	}
	if s := strings.TrimSpace(p.RedactionProfile); s != "" {
		out["redaction_profile"] = s
	}
	out["transcript_enabled"] = "false"
	if p.TranscriptEnabled {
		out["transcript_enabled"] = "true"
	}
	return out
}

func principalSnapshotForSessionAudit(p execview.PrincipalView) coreauth.PrincipalSnapshot {
	return coreauth.NewPrincipalSnapshot(p.ID, p.DisplayName)
}
