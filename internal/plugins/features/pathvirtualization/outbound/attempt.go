package outbound

// This file implements the candidate attempt transform itself: the feature's first
// outbound pass at design.md "Existing Architecture and Placement" step 2.
//
// The design of the pass is one sentence: derive the mapping from the authoritative
// workspace projection, run the one pure canonical rewriter in the generation's
// rollout mode, publish what it published, and fail open to the real path if it did
// not finish. Everything a reader might expect to find here and does not is a
// deliberate absence:
//
//   - no routing decision. The pass reads no route field, no candidate key, no
//     backend or model identity, and no retry ordinal, and it always answers
//     request.AttemptContinue with no reason code. requirements.md 5.5 puts route
//     identity, model and backend selection, B-leg sequencing, output commitment,
//     retry/failover authority, billing authority, and secure-session authority
//     outside this feature's reach, and an attempt transform that returned anything
//     other than AttemptContinue would be exercising candidate choice, which is
//     exactly what it must not do;
//   - no state. The mapping is re-derived from the workspace projection on every
//     attempt rather than cached, which is what makes every retry, race participant,
//     and failover candidate of one logical A-leg turn derive the same V1 alias
//     (requirement 5.6) with no synchronization, and what makes the alias identical
//     after a process restart (requirement 6.2);
//   - no second rewriter. This pass calls the same rewrite.Rewriter every other
//     outbound pass calls, with the same mapping, so the audit numbers it reports
//     are the rewrite's own numbers (requirement 7.3).
//
// On failure the pass publishes NOTHING. That is stronger than the rewriter's own
// contract, which returns the input call: this pass does not write back at all, so
// even a hypothetical rewriter that handed back a half-rewritten call alongside its
// error could not get one published. A candidate is either the value the runtime
// built or the fully published rewrite of it.

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

const (
	// TransformID is the stable identity this pass reports to the candidate attempt
	// stage. It is a fixed string rather than a composed value so that a plugin
	// suppression rule, an extension-stage failure log, and a diagnostics inventory
	// all name the same participant across builds and configuration changes. It
	// carries no version, workspace, or mode, so it stays low-cardinality.
	TransformID = "path-virtualization-attempt-transform"

	// OrderAttemptTransform is this pass's position within the candidate attempt
	// stage, which sorts ascending by order, then identity, then registration index.
	//
	// It sits at the default mid-stage position on purpose. Candidate sizing,
	// context eligibility, and token-accounting preflight all run immediately after
	// the whole stage (steps 3 and 5), so any position inside the stage already lets
	// them observe the savings requirement 5.3 asks for. Ordering this pass after
	// anything that reconstructs a call's item or message authority - which is what
	// a reasoning-restoration pass does - is what keeps a restored history virtualized
	// rather than reintroducing real paths.
	OrderAttemptTransform = 0
)

// virtualizer is the one operation this pass needs from the canonical outbound
// rewriter.
//
// It is a consumer-declared port, satisfied by *rewrite.Rewriter, and it exists for
// one reason: the rewriter's error path is documented as unreachable from untrusted
// input, so requirements.md 8.2's fail-open branch could otherwise never be exercised
// by a test and would ship as an unproven claim in a safety-relevant position. The
// port narrows the seam to exactly the call this pass makes - there is no wider
// injection point, no global, and no way to substitute a different rewriter
// implementation in production.
type virtualizer interface {
	RewriteCall(*aipapi.Call) (*aipapi.Call, rewrite.Stats, error)
}

// AttemptTransform is the feature's candidate attempt transform: the early outbound
// pass that virtualizes eligible path-bearing tool history on a backend-bound
// candidate.
//
// One instance is safe to share across every request of a generation and across
// every retry, race participant, and failover candidate of a logical A-leg turn. It
// holds no mutable state and derives everything it needs per attempt, so it needs no
// synchronization and cannot leak one candidate's state into another's
// (requirements.md 5.6, 5.7). The nil pointer is also safe and publishes the call it
// was given unchanged, matching the shared rewriter's own nil-receiver behaviour.
type AttemptTransform struct {
	// mode is the rollout mode this generation was compiled with (requirement 7.2).
	// It is decided at configuration time and is immutable here, so no request can
	// observe a different mode from another.
	mode rewrite.Mode
	// resolver is the compiled exact-name profile policy, or nil when the generation
	// published no policy at all. A nil resolver resolves no selector for any tool,
	// which is the required answer for an unproved surface (requirements.md 3.5).
	resolver *pathvirtualization.Resolver
	// report receives this pass's bounded, content-free outcome. It may be nil, in
	// which case the pass records nothing and still performs the whole rewrite.
	report Reporter
	// bind builds the per-attempt virtualizer from the freshly derived mapping. It
	// is a field rather than a direct call so the fail-open branch is reachable by a
	// white-box test; in production it always binds the shared canonical rewriter in
	// this pass's rollout mode.
	bind func(pathvirtualization.Mapping) virtualizer
}

// NewAttemptTransform builds the feature's candidate attempt transform.
//
// The rollout mode is a construction parameter rather than a value read per request,
// because it is a generation-scoped configuration choice: Task 9.1 owns decoding the
// operator's `audit|rewrite` spelling and the strict validation that keeps an
// unrecognized value from widening rewriting, and this pass only consumes the
// resulting mode. An out-of-set mode is not this pass's problem to detect - the
// rewriter already fails closed on it by measuring rather than mutating.
//
// A nil resolver is accepted and means "no policy published". It is not an error
// because the answer it produces, no selector for any tool, is the same answer a
// tool no layer claims produces, and requirements.md 3.5 requires exactly that
// rather than a failure.
func NewAttemptTransform(mode rewrite.Mode, resolver *pathvirtualization.Resolver, opts ...Option) *AttemptTransform {
	transform := &AttemptTransform{mode: mode, resolver: resolver}
	for _, opt := range opts {
		if opt != nil {
			opt(transform)
		}
	}
	// The binder is installed after the options so an absent or partial construction
	// can never leave the pass without the rewriter it is defined in terms of.
	transform.bind = func(mapping pathvirtualization.Mapping) virtualizer {
		return rewrite.NewWithMode(mapping, transform.resolver, transform.mode)
	}
	return transform
}

// ID implements request.AttemptTransform.
func (t *AttemptTransform) ID() string {
	if t == nil {
		return TransformID
	}
	return TransformID
}

// Order implements request.AttemptTransform. See [OrderAttemptTransform].
func (t *AttemptTransform) Order() int { return OrderAttemptTransform }

// FailureMode implements request.AttemptTransform.
//
// FailOpen is the only mode consistent with requirements.md 8.2 for this direction.
// The stage uses it for a panic and for an expired bounded deadline, and in both
// cases it rolls the call back to the value the runtime owned and continues the
// candidate. Failing closed here would turn an internal error in an optional
// optimization into a failed request, which is the opposite of what the requirement
// asks: the real path is a perfectly valid request, and losing it helps nobody.
func (t *AttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

// HandleAttempt implements request.AttemptTransform: the one early outbound pass.
//
// It performs no I/O, consults no stored mapping dictionary, and returns no error of
// its own. Every condition it can meet is either a bounded report or a
// request.AttemptContinue, so the candidate always proceeds:
//
//   - no canonical call: the stage never supplies one, and an absent one leaves
//     nothing to publish, so the pass records the attempt and continues;
//   - an unusable project root: nothing is published and the mapper's own bounded
//     refusal code is recorded (requirement 1.8);
//   - an unusable alias - a root whose alias is not strictly shorter, so outbound
//     virtualization is inactive for the whole candidate: the shared rewriter
//     records its own bounded reason and publishes nothing (requirement 1.4);
//   - an unexpected transformation failure: nothing is published, the real path
//     survives, and a bounded reason is recorded (requirement 8.2).
func (t *AttemptTransform) HandleAttempt(
	_ context.Context,
	call *aipapi.Call,
	meta request.AttemptMeta,
	_ request.Services,
) (request.AttemptDecision, error) {
	if t == nil {
		return request.AttemptDecision{Kind: request.AttemptContinue}, nil
	}
	// The workspace projection is the only authority read here. Deriving on every
	// attempt rather than caching is what keeps the alias identical across retries,
	// race participants, failover candidates, and process restarts
	// (requirements.md 5.6, 6.2), and it is why no identity field can influence the
	// result (requirement 5.7).
	//
	// The derivation is a PURE FUNCTION of that one pinned root, and saying so is the
	// point rather than a detail. What makes requirement 5.6 hold is the existence of a
	// single authority, NOT the absence of recomputation: a cached alias is what could
	// disagree with the turn's root, because a cache can outlive the value it was built
	// from. Re-deriving cannot disagree with the pin it reads, and every participant of
	// one logical turn reads the same pinned view.
	t.record(apply(call, meta.Workspace.ProjectRoot, PassAttempt, t.bind))
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

// record hands one bounded report to the configured reporter.
//
// The call is skipped entirely for an absent reporter, so a deployment that wants no
// observability pays nothing and a nil reporter can never be a nil-pointer hazard on
// the hot path.
func (t *AttemptTransform) record(report Report) {
	if t == nil || t.report == nil {
		return
	}
	t.report(report)
}
