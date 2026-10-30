package request

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type AttemptDecisionKind string

const (
	AttemptContinue         AttemptDecisionKind = "continue"
	AttemptExcludeCandidate AttemptDecisionKind = "exclude_candidate"
)

type AttemptDecision struct {
	Kind       AttemptDecisionKind
	ReasonCode string
}

type AttemptMeta struct {
	TraceID         string
	ALegID          string
	CandidateKey    string
	BackendID       string
	BackendPrefixes []string
	Model           string
	ReplaySupport   aipapi.ReasoningReplaySupport
	// AttemptPolicy is the executor-resolved per-attempt policy for
	// unrepresentable client-sent reasoning. When log_skip, attempt
	// transforms withhold unrepresentable restores (continue without
	// restore) instead of excluding the candidate, unifying restored and
	// client-sent reasoning under one per-candidate rule. Empty/reject
	// preserves existing feature-config behaviour.
	AttemptPolicy aipapi.AttemptPolicy
	Scope         scope.PrincipalScopeView
	Session       session.SessionView
	Workspace     workspace.WorkspaceView
}

type AttemptTransform interface {
	ID() string
	Order() int
	FailureMode() sdkhooks.FailureMode
	HandleAttempt(context.Context, *aipapi.Call, AttemptMeta, Services) (AttemptDecision, error)
}
