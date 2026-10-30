package outbound

// This file implements the feature's second outbound pass: the idempotent
// request-part hook of design.md "Existing Architecture and Placement" step 4, which
// reapplies the SAME pure canonical rewriter after the later request shaping that sits
// between the candidate attempt stage and the backend-bound request.
//
// The pass exists because the early pass alone cannot satisfy requirements.md 5.2 and
// 5.4. It virtualizes eligible history before candidate sizing, context eligibility, and
// token-accounting preflight observe it, which is requirement 5.3. But the runtime
// then does more work on the same candidate before Backend.Open: post-hook rederivation
// (step 5), the final conversation-view reassertion (step 6), clamps and backend-ingress
// accounting (step 7), and candidate adaptation (step 8). Any of those can put a
// real-root path back onto a path-bearing tool surface, and the per-turn buffer (step
// 9) plus Backend.Open (step 10) would then carry the real path to the backend.
// requirements.md 5.4 forbids exactly that, and this pass is the last writer that can
// prevent it.
//
// Its whole design is one sentence, and it is the same sentence the early pass is
// built from: read the authoritative workspace view the runtime already pinned for the
// turn, derive the workspace-bound mapping from it with the core's pure DeriveMapping,
// run the one pure rewriter in the generation's rollout mode, publish what it
// published, and fail open to the real path if it did not finish. Everything a reader
// might expect to find here and does not is a deliberate absence:
//
//   - no second rewriter, and no second mapping rule. Both passes call the same
//     rewrite.Rewriter with the same mapping, so a surface one pass claims is a surface
//     the other claims (design.md 233). That identity is why reapplication is
//     idempotent: the rewriter publishes a byte splice that only fires on a
//     segment-boundary real-root prefix, so an already virtualized call produces no
//     match and therefore no bytes;
//   - no workspace authority. The workspace view is READ from the projection the
//     runtime pinned for the turn rather than resolved here, so this pass holds no
//     resolver at all and cannot mint a second, competing one;
//   - no state, and specifically no CACHE. The mapping is re-derived from the pinned
//     view on every invocation, and the direction of that argument matters: the
//     authority is the PIN both passes read (requirement 5.6), and re-derivation is a
//     pure function of it, so "same alias for every retry, race participant, and
//     failover candidate of one logical A-leg turn" holds with no synchronization
//     because there is one value rather than many re-resolutions. Requirement 6.2's
//     restart-equivalence follows from the same purity. Stating it the other way round
//     - "never cached, so nothing can go stale" - inverts the risk: a stored alias is
//     precisely what CAN disagree with the turn's root, because it outlives the root
//     that produced it; re-derivation cannot disagree with the pin it reads;
//   - no routing decision and no candidate influence. The pass reads no route field, no
//     candidate identity, and no backend identity, and requirements.md 5.5 puts all of
//     those outside this feature's reach. It also cannot exclude a candidate: the
//     request-part interface has no way to say so;
//   - no returned error. Every condition it can meet is either a bounded report or a
//     silent no-op, so the request-part chain always continues.
//
// On failure the pass publishes NOTHING, exactly like the early pass. That is stronger
// than the rewriter's own contract, which returns the input call on error: this pass
// does not write back at all, so even a hypothetical rewriter that handed back a
// half-rewritten call alongside its error could not get one published. A request is
// either the value the runtime built or the fully published rewrite of it.

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

const (
	// PartHookID is the stable identity this pass reports to the request-part stage.
	// It is a fixed string rather than a composed value so that a plugin suppression
	// rule, an extension-stage failure log, and a diagnostics inventory all name the
	// same participant across builds and configuration changes. It carries no version,
	// workspace, or mode, so it stays low-cardinality, and it differs from
	// [TransformID] so a runtime log names which of the two outbound passes failed.
	PartHookID = "path-virtualization-request-part-hook"

	// OrderRequestPartHook is this pass's position within the request-part chain,
	// which sorts ascending by order, then identity, then registration index.
	//
	// It is the LAST position in that chain, and that is the requirement rather than a
	// preference. design.md "Existing Architecture and Placement" step 4 places this
	// pass after later request shaping; a pass that ran earlier in the chain could not
	// observe - let alone undo - a real path an earlier participant had just restored.
	// Every request-part hook shipped in this repository sits at or below order 100, so
	// a fixed value above that makes "last" a property of this pass rather than a
	// property of whatever else a deployment happens to install.
	//
	// It does not need to be positioned against the stages on either side of it. Every
	// request-part participant runs before the final conversation-view reassertion
	// (step 6) and after the whole candidate attempt stage (step 2), which is the
	// ordering requirement 5.2 needs and the property Task 1.3's runtime
	// characterization pins.
	OrderRequestPartHook = 1000
)

// RequestPartHook is the feature's late outbound pass: the idempotent hook that
// reapplies the one pure canonical rewriter after later request shaping, so the
// backend-bound request carries virtualized path-bearing tool history through PTB and
// Backend.Open (requirements.md 5.2, 5.4).
//
// One instance is safe to share across every request of a generation and across every
// retry, race participant, and failover candidate of a logical A-leg turn. It holds no
// mutable state and derives everything it needs per invocation, so it needs no
// synchronization and cannot leak one request's state into another's
// (requirements.md 5.6, 5.7). The nil pointer is also safe and publishes the call it
// was given unchanged.
type RequestPartHook struct {
	// mode is the rollout mode this generation was compiled with (requirement 7.2).
	// It is decided at configuration time and is immutable here, so no request can
	// observe a different mode from another, and both outbound passes of one generation
	// necessarily run the same detection code (requirement 7.3).
	mode rewrite.Mode
	// resolver is the compiled exact-name profile policy, or nil when the generation
	// published no policy at all. A nil resolver resolves no selector for any tool,
	// which is the required answer for an unproved surface (requirements.md 3.5).
	resolver *pathvirtualization.Resolver
	// report receives this pass's bounded, content-free outcome. It may be nil, in
	// which case the pass records nothing and still performs the whole rewrite.
	report Reporter
	// bind builds the per-invocation virtualizer from the freshly derived mapping. It
	// is a field rather than a direct call so the fail-open branch is reachable by a
	// white-box test; in production it always binds the shared canonical rewriter in
	// this pass's rollout mode.
	bind func(pathvirtualization.Mapping) virtualizer
}

// NewRequestPartHook builds the feature's late outbound pass.
//
// The rollout mode and the compiled policy are the same two values the early pass
// takes, and there is deliberately no third authority argument. sdkhooks.PartMeta
// carries TraceID, ALegID, BLegID, AttemptSeq, and BackendID and no workspace view, so
// the workspace has to arrive by some other route; the runtime pins one per logical turn
// and projects it onto the public SDK context seams alongside session, scope, and
// principal (internal/core/execctx.WithViews), and this pass READS that projection
// through [workspace.WorkspaceViewFromContext].
//
// Reading the pinned snapshot rather than resolving is what makes requirement 5.6 hold
// by construction rather than by luck. The early pass reads the same pinned view out of
// its request.AttemptMeta.Workspace, so the two passes cannot disagree about which
// workspace a request belongs to no matter what the resolver chain would have answered
// later - the shape a live re-resolution produces is two workspace tags in one
// backend-bound request. Because the projection is a plain context value, nothing is
// resolved per attempt here, and requirement 6.2's restart-equivalence follows from the
// derivation being a pure function of the pin.
//
// An absent projection is accepted and means "no workspace authority was published". It
// is not an error, and it is reported as the unresolved-workspace condition rather than
// as a root-shape refusal: what is missing is the authority, not the spelling of the root
// it would have supplied, and requirements.md 8.1 makes an unconfigured feature
// unobservable while 5.7 forbids substituting any other root source. The pass publishes
// nothing.
func NewRequestPartHook(
	mode rewrite.Mode,
	resolver *pathvirtualization.Resolver,
	opts ...HookOption,
) *RequestPartHook {
	hook := &RequestPartHook{mode: mode, resolver: resolver}
	for _, opt := range opts {
		if opt != nil {
			opt(hook)
		}
	}
	// The binder is installed after the options so an absent or partial construction
	// can never leave the pass without the rewriter it is defined in terms of.
	hook.bind = func(mapping pathvirtualization.Mapping) virtualizer {
		return rewrite.NewWithMode(mapping, hook.resolver, hook.mode)
	}
	return hook
}

// ID implements sdkhooks.RequestPartHook.
func (h *RequestPartHook) ID() string {
	if h == nil {
		return PartHookID
	}
	return PartHookID
}

// Order implements sdkhooks.RequestPartHook. See [OrderRequestPartHook].
func (h *RequestPartHook) Order() int { return OrderRequestPartHook }

// FailureMode implements sdkhooks.RequestPartHook.
//
// FailOpen is the only mode consistent with requirements.md 8.2 for this direction,
// and requirement 8.3's fail-CLOSED rule does not reach here at all: that rule is
// scoped to "reverse expansion failures for path-bearing MODEL tool calls" once a
// virtual alias is model-visible, which is the INBOUND direction Task 8.x owns and the
// only moment an unresolved alias could ever become a client's filesystem target. This
// pass runs in the outbound direction, where an alias travels to the BACKEND and the
// real path is the value the backend must be able to reason about. Nothing this pass can
// do is client-visible, so failing closed here would turn an internal error in an
// optional optimization into a failed request - the opposite of what 8.2 asks.
func (h *RequestPartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

// HandleRequestParts implements sdkhooks.RequestPartHook: the one late outbound pass.
//
// It performs no I/O at all, consults no stored mapping dictionary, and never returns an
// error, so the request-part chain always continues and the runtime's post-hook
// re-validation is the only thing that can reject a publication. Every condition it can
// meet is either a bounded report or a silent no-op:
//
//   - an absent pass or an absent call: there is nothing to publish, so the pass does
//     nothing;
//   - no pinned workspace view on the context: nothing is published, the real path
//     survives, and a bounded reason is recorded (requirements.md 8.2);
//   - an unusable project root: nothing is published and the mapper's own bounded
//     refusal code is recorded (requirement 1.8);
//   - an unusable alias - a root whose alias is not strictly shorter, so outbound
//     virtualization is inactive for the whole request: the shared rewriter records its
//     own bounded reason and publishes nothing (requirement 1.4);
//   - an already virtualized call: the rewriter publishes no bytes, so reapplication is
//     free (requirements.md 2.9);
//   - an unexpected transformation failure: nothing is published, the real path
//     survives, and a bounded reason is recorded (requirement 8.2).
func (h *RequestPartHook) HandleRequestParts(
	ctx context.Context,
	call *aipapi.Call,
	_ sdkhooks.PartMeta,
) error {
	if h == nil {
		return nil
	}
	// The pinned workspace view is the only authority read here. It is the same
	// snapshot the early pass read out of its attempt metadata, and re-deriving the
	// mapping from it per invocation - rather than caching one - keeps the alias
	// identical across retries, race participants, failover candidates, and process
	// restarts (requirements.md 5.6, 6.2). It is also why no identity field can
	// influence the result (requirement 5.7).
	root, resolved := h.projectRoot(ctx)
	if !resolved {
		h.record(Report{Pass: PassRequestPart, Outcome: OutcomeWorkspaceUnresolved})
		return nil
	}
	h.record(apply(call, root, PassRequestPart, h.bind))
	return nil
}

// projectRoot reads the pinned workspace view and returns its project root.
//
// It reports false only when the runtime projected no workspace view at all, which is
// the one condition under which no authority exists. An EMPTY project root inside an
// attached view is NOT one of them: that is a genuine view - the detached auxiliary
// path pins exactly that - and DeriveMapping is the authority that decides an empty
// project root is unusable (requirements.md 1.8).
//
// Reading the projection cannot fail and cannot name a path, so unlike a resolver call
// there is no error here to surface, wrap, or record, and nothing reaches an observable
// dimension of this feature (requirements.md 7.7).
func (h *RequestPartHook) projectRoot(ctx context.Context) (string, bool) {
	view, ok := workspace.WorkspaceViewFromContext(ctx)
	if !ok {
		return "", false
	}
	return view.ProjectRoot, true
}

// record hands one bounded report to the configured reporter.
//
// The call is skipped entirely for an absent reporter, so a deployment that wants no
// observability pays nothing and a nil reporter can never be a nil-pointer hazard on
// the hot path.
func (h *RequestPartHook) record(report Report) {
	if h == nil || h.report == nil {
		return
	}
	h.report(report)
}
