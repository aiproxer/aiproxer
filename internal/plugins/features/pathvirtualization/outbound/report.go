package outbound

// This file owns everything a caller may learn about what one outbound pass did.
//
// The whole reporting surface is two values: a closed outcome enum and the shared
// rewriter's content-free statistics. Both are needed and neither may grow a
// content-bearing field, because this is a candidate-level record of a pass that ran
// against untrusted client payloads.
//
// The vocabulary is closed on purpose, and it is closed differently from the
// rewriter's. The rewriter's twelve SkipReason members describe PER-SURFACE
// refusals inside one successful walk: which selector was unresolved, which payload
// was absent. This file's Outcome describes what happened to the PASS as a whole,
// which is a different axis and cannot be expressed as one of those: a call whose
// every surface was refused is still a pass that ran, and a pass that failed before
// it walked anything skipped no surface at all.
//
// The one member that has no counterpart in either existing vocabulary is
// OutcomeTransformationFailed. It is required rather than convenient, and it is
// required by requirements.md 8.2, which asks for a bounded reason specifically when
// outbound virtualization meets an internal error before an alias becomes
// model-visible. DeriveMapping's SkipReason already covers every unusable-root case
// (requirement 1.8's enumeration), so this file adds no new root code: it projects
// that closed set as Report.RootReason rather than widening it.
//
// OutcomeWorkspaceUnresolved is the second such member, and it exists for the same
// reason with a narrower reach. Only the late pass resolves the workspace view at all:
// the early pass reads an already-pinned projection, so a resolution failure is a
// condition the early pass cannot observe and the late pass must account for. Reporting
// it as OutcomeProjectRootUnusable would be a false label - the root is not unusable,
// the authority that would have supplied it failed - and reporting it as
// OutcomeTransformationFailed would borrow a label the transformer's own doc binds to
// the shared rewriter, which never ran. Requirement 8.2 asks for a bounded reason, and
// the honest way to provide one is one more member of a still-closed vocabulary rather
// than a mislabelled existing one.
//
// Nothing here is ever formatted from a payload. Every label is a compile-time
// literal, and every counter comes from the shared rewriter, whose accounting is
// bounded by the closed reason vocabulary and by canonical payload limits
// (requirements.md 7.6, 7.7).

import (
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
)

// Outcome is the bounded, content-free reason one outbound pass ended the way it
// did.
//
// It is an enum because the value only ever reaches fixed-count observability
// dimensions. The set is closed: every member is a condition this pass can actually
// observe, and an unobserved condition cannot invent a new label.
type Outcome uint8

const (
	// OutcomeRewriterRan marks a pass that ran the shared canonical rewriter over
	// the candidate. It is mode-neutral: audit and rewrite both reach it, because
	// audit is the same detection pass with publication switched off
	// (requirements.md 7.3). Whether anything was published is answered by
	// Report.Stats.Rewritten and by the bound rollout mode, never by this value.
	OutcomeRewriterRan Outcome = iota
	// OutcomeProjectRootUnusable marks a pass that published nothing because the
	// authoritative project root is not one of the five supported absolute forms,
	// or is spelled inside the fixed V1 reserved alias namespace. Rewriting is
	// disabled for that mapping rather than guessed at, and the specific bounded
	// code is carried in Report.RootReason (requirement 1.8).
	OutcomeProjectRootUnusable
	// OutcomeTransformationFailed marks a pass that hit an unexpected internal
	// transformation failure before any alias could have become model-visible.
	//
	// This is the fail-open condition of requirements.md 8.2. The candidate is left
	// exactly as the runtime built it, so the real path reaches the backend, and the
	// reason is recorded here instead of being turned into an exclusion: an internal
	// error in an optional optimization must never cost a client its request.
	OutcomeTransformationFailed
	// OutcomeWorkspaceUnresolved marks a pass that published nothing because the
	// authoritative workspace view could not be resolved at all, so there was no
	// project root to derive a mapping from.
	//
	// Only the late pass can reach it: the early pass reads an already-projected
	// workspace view out of its attempt metadata, while this pass resolves the view
	// itself at the request-part stage. Requirement 8.2 still applies - the real path
	// is preserved and a bounded reason is recorded - but the reason is not a root
	// shape refusal, so Report.RootReason stays at its no-refusal value here.
	OutcomeWorkspaceUnresolved
)

// String returns the fixed, low-cardinality label of an outcome. It is safe for
// content-free observability dimensions: it never contains path, tool-name, pointer,
// payload, or workspace-identity bytes.
func (o Outcome) String() string {
	switch o {
	case OutcomeRewriterRan:
		return "rewriter_ran"
	case OutcomeProjectRootUnusable:
		return "project_root_unusable"
	case OutcomeTransformationFailed:
		return "transform_failed"
	case OutcomeWorkspaceUnresolved:
		return "workspace_unresolved"
	default:
		return "unknown"
	}
}

// MarshalText implements [encoding.TextMarshaler] so an outcome reaches any exporter
// as the bounded label rather than as its ordinal.
//
// It exists because requirements.md 7.7 constrains OBSERVABLE output, and the most
// likely exporter of a report is a JSON or log encoder rather than this package's own
// String method. An integer-coded enum encodes as a bare number, which is unbounded in
// the sense that matters here: a value outside the closed vocabulary leaves the process
// as whatever number it is, carrying no label a reader can act on, while Report's
// sibling RootReason already renders as text and made the difference visible. The
// default branch of String answers that case, so marshalling delegates to it rather
// than restating the vocabulary a second time.
func (o Outcome) MarshalText() ([]byte, error) { return []byte(o.String()), nil }

// Pass names which outbound pass produced one report.
//
// It exists because a generation-wide saving cannot answer requirements.md 9.5 on its own.
// In production both outbound passes observe ONE candidate, and the two modes differ on it:
// a REWRITE publishes the alias, so the late pass finds no real-root prefix and measures
// nothing, while an AUDIT publishes nothing, so the late pass finds exactly what the early
// pass found and measures the same figure again. Measured over the shipped composition,
// that made an audit deployment's total exactly twice the rewrite deployment's for identical
// traffic with nothing in the projection saying so, so an operator could not recover the
// per-candidate figure the measurement was about.
//
// A fixed pass index is the right dimension for it. The alternative - keying a measurement
// by something about the candidate - would be a per-candidate de-duplication key whose
// cardinality an input chooses, which requirements.md 7.7 forbids and which would make the
// series unbounded for reasons that have nothing to do with this feature.
//
// The vocabulary is closed, and its zero value is deliberately NOT a shipped pass. A pass
// that forgets to stamp its report then lands in the unattributed row, where the omission
// is visible, rather than being silently attributed to one of the two real passes - which
// for this dimension would be the difference between a recoverable figure and a wrong one.
type Pass uint8

const (
	// PassUnattributed marks a report whose originating pass was not stated. It is the
	// zero value and no shipped pass produces it; see the type comment.
	PassUnattributed Pass = iota
	// PassAttempt marks the EARLY outbound pass: the candidate attempt transform, which
	// runs before candidate sizing, context eligibility, and token-accounting preflight
	// (requirement 5.3).
	PassAttempt
	// PassRequestPart marks the LATE outbound pass: the request-part hook, which
	// reapplies the same rewriter after the later shaping that sits between the candidate
	// attempt stage and the backend-bound request (requirements.md 5.2, 5.4).
	PassRequestPart
	// passCount is the size of the closed pass vocabulary. It is the bound on any
	// projection over this dimension: a value outside the vocabulary folds into one
	// bounded slot rather than indexing out of range or vanishing.
	passCount
)

// String returns the fixed, low-cardinality label of a pass. It is safe for content-free
// observability dimensions: a pass identity is a stage name and nothing else, so it never
// contains path, alias, workspace-tag, tool-name, or payload bytes.
func (p Pass) String() string {
	switch p {
	case PassUnattributed:
		return "unattributed"
	case PassAttempt:
		return "attempt"
	case PassRequestPart:
		return "request_part"
	default:
		return "unknown"
	}
}

// MarshalText implements [encoding.TextMarshaler] so a pass reaches any exporter as the
// bounded label rather than as its ordinal, for the reason [Outcome.MarshalText] gives.
func (p Pass) MarshalText() ([]byte, error) { return []byte(p.String()), nil }

// Report is the content-free record of one outbound pass.
//
// Every field is a closed code, a count, or a byte total, so the whole value is safe
// to log, export, or use as a metric dimension. There is deliberately no error
// field: the pass records a bounded REASON for a failure rather than the failure
// itself, so a rewriter error text can never reach a log line, and so the caller
// cannot accidentally surface an internal detail it never inspected
// (requirements.md 7.7).
type Report struct {
	// Pass names which outbound pass produced this report. It is what makes a
	// generation-wide saving decomposable into per-pass contributions; see [Pass].
	Pass Pass
	// Outcome is the pass-level verdict. It is always set.
	Outcome Outcome
	// RootReason is the mapper's own bounded refusal code, set only when Outcome is
	// OutcomeProjectRootUnusable. It reuses the lexical core's closed SkipReason
	// vocabulary unchanged, so requirement 1.8's enumeration stays the single
	// authority on why a root is unusable and no second root code exists.
	RootReason pathvirtualization.SkipReason
	// Stats is the shared rewriter's content-free outcome: eligible and rewritten
	// counts, decoded-value byte totals, and per-reason skip tallies over the
	// rewriter's own closed vocabulary. It is zero for every outcome that did not
	// reach the rewriter, and identical between audit and rewrite for the same input.
	Stats rewrite.Stats
}

// Reporter receives one content-free report per outbound pass.
//
// It is declared here, by the consumer, so this package never depends on the
// feature's telemetry, metrics, or diagnostics machinery: requirements.md 9.3 wires
// the real bounded counters, and this task only requires that the outcome and the
// reason are reachable and content-free. An absent reporter is legal and simply
// discards the record.
//
// A reporter may be invoked concurrently. One pass instance is shared by every
// retry, race participant, and failover candidate of a logical A-leg turn, so an
// implementation that keeps counters must be safe for concurrent use.
type Reporter func(Report)

// Option configures one outbound pass at construction.
type Option func(*AttemptTransform)

// WithReporter installs the sink that receives this pass's bounded reports.
//
// It is the only option, and it is optional: a pass built without one still performs
// the whole rewrite and simply records nothing. Making the sink an explicit
// construction parameter rather than package state is what keeps one instance safe to
// share across concurrent candidates, and it keeps this package free of any
// observable mutable state (no init() registration, no globals).
func WithReporter(reporter Reporter) Option {
	return func(t *AttemptTransform) { t.report = reporter }
}

// HookOption configures the late request-part pass at construction.
//
// It is a separate type from Option rather than a second option for the same pass
// because the two passes are separate types with separate fields: one option type per
// pass is what keeps a reporter for one pass from being silently applied to the other,
// and it leaves a future option for either pass additive.
type HookOption func(*RequestPartHook)

// WithHookReporter installs the sink that receives the late pass's bounded reports.
//
// It is the late pass's counterpart of [WithReporter] and the same argument applies:
// the sink is an explicit construction parameter rather than package state, so one
// instance is safe to share across every request of a generation.
//
// The two passes deliberately take SEPARATE reporters rather than sharing one. The
// report type itself carries no pass identity - it is the same closed outcome
// vocabulary and the same content-free statistics for both - so a deployment that
// wants to account for the two passes separately does it by wiring two sinks, which is
// the composition root's decision rather than a field this package would have to widen
// later. A deployment that wants one combined sink installs the same function twice.
func WithHookReporter(reporter Reporter) HookOption {
	return func(h *RequestPartHook) { h.report = reporter }
}
