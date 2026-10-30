package runtime

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// wireSessionClassificationInput carries the already-bound wire facts the
// metadata-only classifier stage needs. Every field is bounded metadata the
// wire lane already resolved after its one-way commit:
//
//   - Session is the view bound by PreparedSecureSession.BindSession, so the
//     authoritative secure session, A-leg and turn IDs are proxy-owned;
//   - Workspace is the view PrepareSecureSession resolved, identical to the one
//     the canonical lane hands its stage;
//   - Evidence is the sessionclassification.Evidence a certified frontend proof
//     compiled, carried through WireSessionFacts.
//
// There is deliberately no canonical request field: this stage exists precisely
// so the large-payload lane can classify without reconstructing one
// (requirements 4.1, 5.2, 5.5, 5.7).
type wireSessionClassificationInput struct {
	TraceID   string
	Session   session.SessionView
	Workspace aipworkspace.WorkspaceView
	Evidence  sessionclassification.Evidence
}

// runWireSessionClassificationStage evaluates the optional exclusive classifier
// plane on the committed large-payload lane.
//
// It reuses the one generic runner, extensions.RunSessionClassificationStage,
// that the canonical lane uses, so monotonicity, prior-positive preservation,
// absent-plane no-op and fail-open-to-unknown behave identically on both paths
// instead of being re-derived here (requirements 4.3, 4.4, 4.6, 4.7).
//
// The returned projection is intentionally discarded. Every consumer requirement
// 4.2 names - submit, tool-catalog, request-shaping, pre-request and route-hint
// stages - is a canonical-required plane, and an occupied canonical-required
// plane statically blocks this lane before it starts, so no same-turn
// SessionView reader exists here by construction. The load-bearing effect is the
// monotonic session-state transition the classifier performs inside Classify,
// which later turns observe. Nothing about the classification crosses the client
// boundary and no post-commit canonical fallback is ever requested: an error,
// invalid output or absent evidence simply leaves this one committed wire
// execution running (requirements 5.1, 5.4, 5.7).
func (e *Executor) runWireSessionClassificationStage(ctx context.Context, in wireSessionClassificationInput) {
	if e == nil || e.RuntimeSnapshot == nil {
		return
	}
	_ = extensions.RunSessionClassificationStage(ctx, e.Log, e.RuntimeSnapshot.SessionClassifier(), sessionclassification.Input{
		TraceID:   in.TraceID,
		Session:   in.Session,
		Workspace: in.Workspace,
		Evidence:  in.Evidence,
	})
}
