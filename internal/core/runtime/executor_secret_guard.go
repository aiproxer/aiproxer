package runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type secretGuardStageInput struct {
	TraceID   string
	Principal execview.PrincipalView
	Scope     scope.PrincipalScopeView
	Session   session.SessionView
	Workspace aipworkspace.WorkspaceView
	SessionID domain.SessionID
	TurnID    domain.TurnID
}

type secretGuardBlockResult struct {
	quarantineResult string
	storageFailure   bool
	committed        bool
}

func (e *Executor) runSecretGuardStage(ctx context.Context, call *aipapi.Call, in secretGuardStageInput) error {
	if e == nil || e.RuntimeSnapshot == nil {
		return nil
	}
	plane := e.RuntimeSnapshot.SecretGuardExecutionPlane()
	guards := plane.Guards
	if len(guards) == 0 {
		return nil
	}
	if call != nil && call.Decision != nil {
		return aipapi.NewPolicyDeniedError("secret_guard", "secret_guard_chain", "decision_unsupported", "policy_denied",
			"decision requests are unavailable while a secret guard is active", nil)
	}
	attr, _ := secretguard.IngressAttributionFromContext(ctx)
	meta := secretguard.Meta{
		TraceID:             in.TraceID,
		Principal:           in.Principal,
		Scope:               in.Scope,
		Session:             in.Session,
		Workspace:           in.Workspace,
		PeerIP:              attr.PeerIP,
		FrontendID:          attr.FrontendID,
		Operation:           attr.Operation,
		UserAgentDigest:     attr.UserAgentDigest,
		AgentIdentityDigest: attr.AgentIdentityDigest,
		DeviceID:            attr.DeviceID,
		KeyID:               attr.KeyID,
		Fingerprint:         attr.Fingerprint,
	}
	if meta.FrontendID == "" {
		if fe, ok := execview.FrontendIDFromContext(ctx); ok {
			meta.FrontendID = fe
		}
	}
	resolver := plane.MatcherResolver
	if resolver == nil {
		resolver = secretguard.ContextMatcherResolver{}
	}
	svc := secretguard.Services{MatcherResolver: resolver, Capability: plane.Capability}
	audit := e.secretGuardAuditFromPlane(plane, in.TurnID)
	block, err := extensions.RunSecretGuardStage(ctx, e.Log, e.ExtensionMetrics, guards, call, meta, svc, audit, e.SecretGuardDecisionMetrics)
	if err != nil {
		if errors.Is(err, extensions.ErrSecretAuditDelivery) {
			if mapped := e.mapSecretAuditDeliveryError(err); mapped != nil {
				return mapped
			}
			return nil
		}
		return err
	}
	if block == nil {
		return nil
	}
	return e.applySecretGuardBlock(ctx, audit, meta, call, block, in)
}

func (e *Executor) secretGuardAudit(turnID domain.TurnID) *extensions.SecretGuardAudit {
	if e == nil || e.RuntimeSnapshot == nil {
		return nil
	}
	return e.secretGuardAuditFromPlane(e.RuntimeSnapshot.SecretGuardExecutionPlane(), turnID)
}

func (e *Executor) secretGuardAuditFromPlane(plane extensions.SecretGuardPlane, turnID domain.TurnID) *extensions.SecretGuardAudit {
	if secretguard.IsNilObserver(plane.DecisionObserver) {
		return nil
	}
	return &extensions.SecretGuardAudit{
		Observer:      plane.DecisionObserver,
		AccessMode:    plane.AccessMode,
		ConfigVersion: plane.ConfigVersion,
		TurnID:        string(turnID),
		Now:           e.now,
	}
}

func (e *Executor) applySecretGuardBlock(ctx context.Context, audit *extensions.SecretGuardAudit, meta secretguard.Meta, call *aipapi.Call, block *extensions.SecretGuardBlockInfo, in secretGuardStageInput) error {
	if block == nil {
		return nil
	}
	res := e.attemptSecretGuardBlockQuarantine(ctx, in)
	if e.SecretGuardDecisionMetrics != nil {
		action, outcome, cat := secretguard.DecisionMetricLabels(block.Decision)
		e.SecretGuardDecisionMetrics.IncQuarantine(action, outcome, cat)
	}
	if res.committed {
		e.cancelALegAfterQuarantine(ctx, in.Session.ALegID)
	}
	if emitErr := extensions.EmitSecretGuardAudit(ctx, audit, meta, call, block.GuardID, block.Decision, res.quarantineResult, false); emitErr != nil {
		if mapped := e.mapSecretAuditDeliveryError(emitErr); mapped != nil {
			return mapped
		}
	}
	if res.storageFailure {
		if e.SessionDenialMapper != nil {
			return e.SessionDenialMapper(domain.ErrStorageUnavailable)
		}
		return domain.ErrStorageUnavailable
	}
	return block.DenialError()
}

func (e *Executor) attemptSecretGuardBlockQuarantine(ctx context.Context, in secretGuardStageInput) secretGuardBlockResult {
	res := secretGuardBlockResult{quarantineResult: secretguard.QuarantineResultSkipped}
	if e == nil || e.SecureSession == nil {
		return res
	}
	if in.SessionID == "" {
		e.markQuarantinePersistenceFault()
		if e.Log != nil {
			e.Log.ErrorContext(
				ctx, "secure_session: secret_guard block missing session_id; quarantine invariant failed",
				slog.String("trace_id", in.TraceID),
			)
		}
		res.quarantineResult = secretguard.QuarantineResultFailed
		res.storageFailure = true
		return res
	}
	if err := e.SecureSession.Quarantine(ctx, domain.QuarantineInput{
		SessionID:  in.SessionID,
		TurnID:     in.TurnID,
		ReasonCode: "secret_guard_block",
		EventID:    in.TraceID,
		At:         e.now(),
	}); err != nil {
		e.markQuarantinePersistenceFault()
		if e.Log != nil {
			e.Log.ErrorContext(
				ctx, "secure_session: quarantine persistence failed after secret_guard block",
				slog.String("session_id", string(in.SessionID)),
			)
		}
		res.quarantineResult = secretguard.QuarantineResultFailed
		res.storageFailure = true
		return res
	}
	res.quarantineResult = secretguard.QuarantineResultCommitted
	res.committed = true
	return res
}

func (e *Executor) mapSecretAuditDeliveryError(err error) error {
	if err == nil {
		return nil
	}
	policy := secretguard.AuditFailClosed
	if e != nil && e.RuntimeSnapshot != nil {
		plane := e.RuntimeSnapshot.SecretGuardExecutionPlane()
		if plane.AuditFailurePolicy != "" {
			policy = plane.AuditFailurePolicy
		}
	}
	if policy == secretguard.AuditBestEffort {
		if e != nil && e.Log != nil {
			e.Log.Debug("secret_guard: audit delivery failed (best_effort)")
		}
		return nil
	}
	if e != nil && e.SessionDenialMapper != nil {
		return e.SessionDenialMapper(domain.ErrMandatoryAuditFailure)
	}
	return aipapi.NewSessionDenialMandatoryAuditFailure("secret_guard: decision audit delivery failed")
}

func (e *Executor) cancelALegAfterQuarantine(ctx context.Context, aLegID string) {
	aLegID = strings.TrimSpace(aLegID)
	if e == nil || aLegID == "" {
		return
	}
	lifecycle := e.lifecycleCoordinator()
	if lifecycle == nil {
		return
	}
	cleanupCtx := ctx
	if cleanupCtx == nil {
		// No parent context exists when the caller passes nil; Background is the
		// only root available for the detached post-quarantine cancel.
		cleanupCtx = context.Background()
	} else {
		cleanupCtx = context.WithoutCancel(cleanupCtx)
	}
	cerr := lifecycle.CancelALeg(cleanupCtx, aLegID, leglifecycle.CancelCause{
		Kind:   leglifecycle.CancelExplicit,
		Detail: "secret_guard_quarantine",
	})
	if cerr != nil && e.Log != nil && !errors.Is(cerr, leglifecycle.ErrALegCanceled) {
		e.Log.DebugContext(cleanupCtx, "secure_session: cancel after quarantine", "error", cerr)
	}
}

func (e *Executor) markQuarantinePersistenceFault() {
	if e == nil {
		return
	}
	e.quarantinePersistenceFault.Store(true)
}

func (e *Executor) QuarantinePersistenceFaulted() bool {
	if e == nil {
		return false
	}
	return e.quarantinePersistenceFault.Load()
}

func (e *Executor) assertSecureSessionActiveBeforeOpen(ctx context.Context) error {
	if e == nil {
		return nil
	}
	if e.QuarantinePersistenceFaulted() {
		if e.SessionDenialMapper != nil {
			return e.SessionDenialMapper(domain.ErrStorageUnavailable)
		}
		return domain.ErrStorageUnavailable
	}
	m := e.secureSessionForAttempt()
	if m == nil {
		return nil
	}
	st, ok := execctx.SecureSessionTurnFromContext(ctx)
	if !ok || st.SessionID == "" {
		return nil
	}
	if err := m.AssertActive(ctx, st.SessionID); err != nil {
		if e.SessionDenialMapper != nil {
			return e.SessionDenialMapper(err)
		}
		return err
	}
	return nil
}
