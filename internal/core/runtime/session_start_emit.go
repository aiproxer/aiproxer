package runtime

import (
	"context"
	"fmt"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

// emitSessionStartIfNeeded emits a session-start audit event for new secure-session turns only
// (requirement 9.1 / 9.4). Resume turns do not emit a duplicate session start.
func (e *Executor) emitSessionStartIfNeeded(
	ctx context.Context,
	traceID string,
	principal coreauth.PrincipalSnapshot,
	br app.BeginResult,
	work aipapi.Call,
	aLeg b2bua.ALegRecord,
) error {
	if e == nil || e.AuthEvents == nil {
		return nil
	}
	if !br.IsNew {
		return nil
	}
	frontend := ""
	if id, ok := execview.FrontendIDFromContext(ctx); ok {
		frontend = id
	}
	ev := coreauth.BuildSessionStartEvent(coreauth.SessionStartBuildInput{
		Now:                     e.now(),
		TraceID:                 traceID,
		Policy:                  e.SessionAuditPolicy,
		Frontend:                frontend,
		PrincipalID:             principal.ID,
		PrincipalDisplayName:    principal.DisplayName,
		AuthoritativeSessionID:  work.Session.AuthoritativeSessionID,
		ClientSessionIDRaw:      work.Session.ClientSessionID,
		ALegID:                  aLeg.ALegID,
		IsNew:                   true,
		SyntheticLocalPrincipal: e.SyntheticLocalPrincipal,
	})
	if err := e.AuthEvents.DispatchSessionStart(ctx, ev); err != nil {
		return fmt.Errorf("executor: session-start event: %w", err)
	}
	return nil
}
