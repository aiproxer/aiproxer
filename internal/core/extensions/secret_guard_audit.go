package extensions

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

var ErrSecretAuditDelivery = errors.New("secret_guard: audit delivery failed")

type SecretGuardDecisionMetrics interface {
	IncDecision(action, outcome, sourceCategory string)
	IncMatch(action, outcome, sourceCategory string)
	IncQuarantine(action, outcome, sourceCategory string)
	IncFailure(action, outcome, sourceCategory string)
	IncScanLimit(action, outcome, sourceCategory string)
}

type SecretGuardAudit struct {
	Observer      secretguard.Observer
	AccessMode    string
	ConfigVersion string
	TurnID        string
	Now           func() time.Time
}

type SecretGuardBlockInfo struct {
	GuardID  string
	Decision secretguard.Decision
}

func (b *SecretGuardBlockInfo) DenialError() error {
	if b == nil {
		return nil
	}
	return aipapi.NewPolicyDeniedError(
		feature.StageIDSecretGuard,
		b.GuardID,
		ReasonSecretGuardBlocked,
		CategoryDenied,
		secretGuardBlockedClientMessage,
		nil,
	)
}

func BuildSecretDecisionEvent(
	meta secretguard.Meta,
	call *aipapi.Call,
	guardID string,
	decision secretguard.Decision,
	quarantineResult string,
	backendDispatched bool,
	accessMode string,
	configVersion string,
	turnID string,
	now time.Time,
) secretguard.DecisionEvent {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	route, model := requestedRouteModel(call)
	principalID := strings.TrimSpace(meta.Principal.ID)
	if principalID == "" {
		principalID = meta.Scope.PrincipalID.String()
	}
	workspaceID := strings.TrimSpace(meta.Workspace.ID)
	if workspaceID == "" {
		workspaceID = meta.Scope.WorkspaceID.String()
	}
	source := ""
	if strings.TrimSpace(meta.PeerIP) != "" {
		source = "remote_addr"
	}
	eventID := strings.TrimSpace(meta.TraceID)
	if eventID == "" {
		eventID = guardID
	} else if guardID != "" {
		eventID = eventID + ":" + guardID
	}
	return secretguard.DecisionEvent{
		Timestamp:           now,
		EventID:             eventID,
		TraceID:             meta.TraceID,
		SessionID:           meta.Session.AuthoritativeSessionID,
		ALegID:              meta.Session.ALegID,
		TurnID:              turnID,
		PrincipalID:         principalID,
		TenantID:            meta.Scope.TenantID.String(),
		OrgID:               meta.Scope.OrganizationID.String(),
		WorkspaceID:         workspaceID,
		PeerIP:              meta.PeerIP,
		Source:              source,
		FrontendID:          meta.FrontendID,
		Operation:           meta.Operation,
		AgentIdentityDigest: meta.AgentIdentityDigest,
		RequestedRoute:      route,
		RequestedModel:      model,
		Findings:            cloneSecretGuardFindings(decision.Findings),
		Action:              secretguard.ActionForOutcome(decision.Outcome),
		Outcome:             decision.Outcome,
		AccessMode:          accessMode,
		ConfigVersion:       configVersion,
		QuarantineResult:    quarantineResult,
		BackendDispatched:   backendDispatched,
		GuardID:             guardID,
		ScanLimitHit:        decision.ScanLimitHit,
	}
}

func cloneSecretGuardFindings(in []secretguard.Finding) []secretguard.Finding {
	if len(in) == 0 {
		return nil
	}
	out := make([]secretguard.Finding, len(in))
	for i := range in {
		out[i] = secretguard.Finding{
			SecretRefName:   in[i].SecretRefName,
			Aliases:         append([]string(nil), in[i].Aliases...),
			SourceCategory:  in[i].SourceCategory,
			Location:        in[i].Location,
			OccurrenceCount: in[i].OccurrenceCount,
			DetectorID:      in[i].DetectorID,
			RuleID:          in[i].RuleID,
			Confidence:      in[i].Confidence,
		}
	}
	return out
}

func requestedRouteModel(call *aipapi.Call) (route, model string) {
	if call == nil {
		return "", ""
	}
	route = strings.TrimSpace(call.Route.Selector)
	if i := strings.LastIndex(route, ":"); i >= 0 && i+1 < len(route) {
		model = route[i+1:]
	}
	return route, model
}

func emitSecretGuardAudit(ctx context.Context, audit *SecretGuardAudit, meta secretguard.Meta, call *aipapi.Call, guardID string, decision secretguard.Decision, quarantineResult string, backendDispatched bool) error {
	if audit == nil || audit.Observer == nil {
		return nil
	}
	nowFn := audit.Now
	var now time.Time
	if nowFn != nil {
		now = nowFn()
	}
	ev := BuildSecretDecisionEvent(meta, call, guardID, decision, quarantineResult, backendDispatched, audit.AccessMode, audit.ConfigVersion, audit.TurnID, now)
	return audit.Observer.OnSecretDecision(ctx, ev)
}

func EmitSecretGuardAudit(ctx context.Context, audit *SecretGuardAudit, meta secretguard.Meta, call *aipapi.Call, guardID string, decision secretguard.Decision, quarantineResult string, backendDispatched bool) error {
	return emitSecretGuardAudit(ctx, audit, meta, call, guardID, decision, quarantineResult, backendDispatched)
}
