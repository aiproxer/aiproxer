package extensions

import (
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// DecisionContextOptions carries the non-view inputs needed to assemble a
// policydecision.Context: lifecycle state and the evaluation timeout budget for the
// target stage/provider (requirements 2.1, 6.3). All fields are optional; zero values
// preserve legacy/local-anonymous semantics.
type DecisionContextOptions struct {
	// OutputCommitted records whether client-visible output has already been committed for
	// this request (completion/stream-stage decisions). False for pre-backend stages.
	OutputCommitted bool
	// EvaluationTimeout is the configured decision-provider evaluation budget for the
	// target stage/provider. Zero means no new timeout is applied (legacy behavior).
	EvaluationTimeout time.Duration
	// EvaluationDeadline is the derived evaluation deadline (now + EvaluationTimeout). Zero
	// means no deadline is applied.
	EvaluationDeadline time.Time
}

// BuildDecisionContext assembles a safe, request-scoped policydecision.Context from the
// trusted execution views attached to an accepted request, plus stage/provider metadata,
// output-committed state, and the evaluation timeout budget (requirements 2.1-2.6, 7.5).
//
// Safety properties:
//   - Authoritative scope comes from views.Scope (safe-by-construction: no raw credentials,
//     headers, resume tokens, or unvetted claims). It is carried separately from the legacy
//     views.Principal projection (requirement 2.6).
//   - Internal/auxiliary origin and parent trace attribution are preserved through
//     views.Scope.Origin and views.Scope.ParentTraceID (requirement 2.4).
//   - Unknown optional scope fields are preserved as unknown rather than inferred from
//     client payloads (requirement 2.2).
//   - The returned context is defensively cloned: mutating its maps, slices, or embedded
//     scope cannot affect the caller's views (requirement 7.7).
//
// The builder does not synthesize unsafe annotation entries or pull in raw request payloads;
// only the views' existing annotations are carried through.
func BuildDecisionContext(views execctx.Views, stage, providerID string, opts DecisionContextOptions) policydecision.Context {
	ctx := policydecision.Context{
		TraceID:            views.Attempt.TraceID,
		ALegID:             views.Session.ALegID,
		BLegID:             views.Attempt.BLegID,
		AttemptSeq:         views.Attempt.AttemptSeq,
		Stage:              stage,
		ProviderID:         providerID,
		Scope:              views.Scope,
		Principal:          views.Principal,
		Session:            views.Session,
		Workspace:          views.Workspace,
		Annotations:        views.Annotations,
		OutputCommitted:    opts.OutputCommitted,
		EvaluationTimeout:  opts.EvaluationTimeout,
		EvaluationDeadline: opts.EvaluationDeadline,
	}
	return ctx.Clone()
}
