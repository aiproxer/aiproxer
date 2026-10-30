package runtime

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// sessionClassificationEvidence builds the bounded classification evidence for one
// canonical client turn.
//
// Evidence comes only from protocol-neutral invocation metadata that the
// frontend already accepted: the canonical operation, the accepted canonical
// client User-Agent, and tool-name category bits accumulated through the shared
// sessionclassification helper. Request content (messages, instructions, items,
// tool descriptions, tool parameters) is never traversed, so the value carries
// no transcript, argument or path material (requirements 4.6, 7.2, 7.3, 11.1, 11.2).
func sessionClassificationEvidence(call *aipapi.Call) sessionclassification.Evidence {
	if call == nil {
		return sessionclassification.Evidence{}
	}
	categories := sessionclassification.ToolCategorySet(0)
	for _, tool := range call.Tools {
		categories = categories.AddToolName(tool.Name)
	}
	userAgent, ok := identity.AcceptClientUserAgent(call.Invocation.ClientUserAgent)
	if !ok {
		userAgent = ""
	}
	return sessionclassification.Evidence{
		Operation:       call.Invocation.Operation,
		ClientUserAgent: userAgent,
		ToolCategories:  categories,
	}
}

// runSessionClassificationStage evaluates the optional exclusive classifier plane
// after secret guard and before frontend ingress/request authority/submit, then
// projects a validated result onto ibt.preSession so every later same-turn view
// observes one immutable classification.
//
// The stage is feature-neutral: it knows only the SDK Classifier contract. It
// mutates no message, instruction, tool, route selector, model option or backend
// choice, and it cannot fail the user request — it returns nothing on purpose, so
// an absent plane is a no-op and a classifier/store/remote failure can only leave
// the session conservatively unknown while an already-positive classification is
// preserved (requirements 4.3, 4.4, 4.6, 4.7).
func (e *Executor) runSessionClassificationStage(ctx context.Context, call *aipapi.Call, ibt *identityBoundTurn) {
	if e == nil || e.RuntimeSnapshot == nil || ibt == nil {
		return
	}
	classifier := e.RuntimeSnapshot.SessionClassifier()
	if classifier == nil {
		return
	}
	result := extensions.RunSessionClassificationStage(ctx, e.Log, classifier, sessionclassification.Input{
		TraceID:   ibt.traceID,
		Session:   ibt.preSession,
		Workspace: ibt.workspace,
		Evidence:  sessionClassificationEvidence(call),
	})
	if err := result.Validate(); err != nil {
		return
	}
	ibt.preSession.Classification = result
}
