package terminaldecision

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// Provider evaluates one canonical terminal candidate. Decide receives a
// value-copy of Input and a context supplied by the platform; providers have
// no contract capability to mutate the request, claim a terminal, or perform
// platform work.
type Provider interface {
	ID() string
	Decide(context.Context, Input) (Decision, error)
}

// DecisionKind identifies the only outcomes a provider may return.
type DecisionKind string

const (
	// DecisionAllowStop permits the platform to publish the candidate as the
	// terminal outcome.
	DecisionAllowStop DecisionKind = "allow_stop"
	// DecisionContinue asks the platform to validate and execute a bounded
	// continuation intent.
	DecisionContinue DecisionKind = "continue"
	// DecisionSurfaceFailure asks the platform to publish a controlled failure
	// outcome; it is not retry or continuation authority.
	DecisionSurfaceFailure DecisionKind = "surface_failure"
)

// IsKnown reports whether k is one of the three legal decision kinds.
func (k DecisionKind) IsKnown() bool {
	switch k {
	case DecisionAllowStop, DecisionContinue, DecisionSurfaceFailure:
		return true
	default:
		return false
	}
}

// CandidateCause identifies the canonical class of a provisional terminal.
// The four authoritative causes cannot be continued by a provider. Normal,
// transport, limit, and provider-error causes cross the generic seam; this SDK
// does not decide feature policy for them.
type CandidateCause string

const (
	// CandidateCauseNormal is a normal backend completion candidate.
	CandidateCauseNormal CandidateCause = "normal"
	// CandidateCauseTransport is a transport-derived candidate.
	CandidateCauseTransport CandidateCause = "transport"
	// CandidateCauseLimit is an output or resource limit candidate.
	CandidateCauseLimit CandidateCause = "limit"
	// CandidateCauseProviderError is a provider-error candidate.
	CandidateCauseProviderError CandidateCause = "provider_error"
	// CandidateCauseRefusal is an authoritative refusal candidate.
	CandidateCauseRefusal CandidateCause = "refusal"
	// CandidateCauseContentFilter is an authoritative content-filter candidate.
	CandidateCauseContentFilter CandidateCause = "content_filter"
	// CandidateCauseCancellation is an authoritative cancellation candidate.
	CandidateCauseCancellation CandidateCause = "cancellation"
	// CandidateCauseAuthorityDenied is an authoritative authority-denial candidate.
	CandidateCauseAuthorityDenied CandidateCause = "authority_denied"
)

// IsKnown reports whether c is one of the canonical candidate causes.
func (c CandidateCause) IsKnown() bool {
	switch c {
	case CandidateCauseNormal, CandidateCauseTransport, CandidateCauseLimit, CandidateCauseProviderError,
		CandidateCauseRefusal, CandidateCauseContentFilter, CandidateCauseCancellation, CandidateCauseAuthorityDenied:
		return true
	default:
		return false
	}
}

// Authoritative reports whether c is a non-continuable authoritative outcome.
func (c CandidateCause) Authoritative() bool {
	switch c {
	case CandidateCauseRefusal, CandidateCauseContentFilter, CandidateCauseCancellation, CandidateCauseAuthorityDenied:
		return true
	default:
		return false
	}
}

// CanonicalTerminalCandidate is the bounded, provider-neutral terminal fact
// offered for provisional evaluation. Reference is an opaque canonical
// identifier; it is not a transport frame, response body, or provider object.
type CanonicalTerminalCandidate struct {
	Cause           CandidateCause
	Reference       string
	OutputCommitted bool
}

// RequestIdentity identifies the logical request and its canonical legs. All
// identifiers are bounded opaque values; they contain no request payload.
type RequestIdentity struct {
	RequestID string
	TraceID   string
	ALegID    string
	BLegID    string
}

// PolicySnapshot is the frozen policy projection visible to a provider. It is
// a value type so a provider cannot observe live generation configuration.
type PolicySnapshot struct {
	Revision                string
	MaxContinuationAttempts uint8
}

// ContinuationEvidence is bounded canonical evidence about a possible next
// trajectory. A zero Attempt is valid for an initial candidate; the platform
// owns interpretation and budget enforcement.
type ContinuationEvidence struct {
	TrajectoryRef string
	Attempt       uint8
}

// Evidence bounds the canonical semantic facts a provider may inspect. Text
// is a bounded normalized projection, never a raw protocol frame or mutable
// call/item collection. Actions are fixed-capacity value data so copying Input
// cannot share provider-visible state with the platform.
type Evidence struct {
	Objective     string
	RecentText    string
	CandidateText string

	Actions     [MaxEvidenceActions]ActionFact
	ActionCount uint8

	// ExplicitCompletion is an observation: the platform saw one trusted
	// explicit-completion signal for this logical response. It is true either
	// for an ordinary client/harness-owned completion tool call with a matching
	// completed result, or for a valid proxy-owned control completion the
	// platform handled privately. Observation alone never asserts that the
	// proxy asked for that signal.
	ExplicitCompletion bool
	// ExplicitCompletionExpected is an expectation: this response actually ran
	// with a successfully active proxy-owned model control protocol, so an
	// explicit completion signal was requested of the model.
	//
	// It is additive and independent of ExplicitCompletion. All four
	// combinations are legal and no platform rule relates the two: an expected
	// signal may be absent, and a client/harness-owned completion may be
	// observed with no proxy expectation at all. The zero value is the correct
	// projection for every provider and generation that never had a proxy-owned
	// control protocol, so a caller that ignores this field observes exactly the
	// pre-existing contract.
	ExplicitCompletionExpected bool
	Lineage                    EvidenceLineage
}

// ActionFact is a compact canonical summary of one message/tool action. Item
// and call identifiers are opaque references; arguments, results, and raw
// payloads never cross the provider boundary.
type ActionFact struct {
	ItemID string
	CallID string
	Kind   aipapi.ItemKind
	Status aipapi.ItemStatus
	Name   string
}

// EvidenceLineage carries bounded continuation and progress references. The
// platform owns dereferencing and interpretation of these values.
type EvidenceLineage struct {
	TrajectoryRef string
	ParentRef     string
	ProgressRef   string
	Attempt       uint8
}

// Input carries immutable-by-value canonical evidence and policy snapshots,
// plus an optional request-pinned narrow capability. Providers must not retain
// or use Auxiliary beyond Decide. Deadline is the platform's evaluation bound
// and must be non-zero; the provider must honor the context deadline supplied
// with Decide as well.
type Input struct {
	Candidate    CanonicalTerminalCandidate
	Request      RequestIdentity
	Policy       PolicySnapshot
	Continuation ContinuationEvidence
	Evidence     Evidence
	// Auxiliary is the optional request-pinned child client. It has no terminal,
	// backend, steering, or snapshot authority.
	Auxiliary auxiliary.Client
	Deadline  time.Time
}

// ContinuationIntent is a bounded request for core-owned continuation. The
// provider supplies only canonical references, internal-control provenance,
// and optional bounded control text; it cannot append that text or open work.
type ContinuationIntent struct {
	TrajectoryRef string
	ControlRef    string
	Instruction   string
	Provenance    string
	ReasonCode    string
}

// Decision is the bounded provider result. Continue is legal only when Kind is
// DecisionContinue and contains a valid intent. Other kinds must leave it nil.
type Decision struct {
	Kind       DecisionKind
	ReasonCode string
	Continue   *ContinuationIntent
}
