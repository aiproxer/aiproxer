package sessionclassification

import (
	"slices"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

// MaxRemoteObservationLatency bounds one recorded remote latency sample. A
// sample outside the bound is not exported at all rather than truncated, so a
// bounded observation surface can never report a nonsense duration.
const MaxRemoteObservationLatency = MaxRemoteTimeout + time.Minute

// MaxObservedRevision bounds the classification revision one observation may
// describe. The V1 store assigns the first positive revision 1, so this bound is
// far above anything a monotonic transition can produce while still rejecting a
// nonsensical value instead of reporting it.
const MaxObservedRevision = uint64(1) << 32

// EvaluationOutcome is the closed vocabulary for the result of one bounded
// classification evaluation. Every value is a static identifier: no value is
// derived from a session, a client, or a remote response.
type EvaluationOutcome string

const (
	// EvaluationPromoted marks the single first accepted positive transition.
	EvaluationPromoted EvaluationOutcome = "promoted"
	// EvaluationRestored marks a positive read back from shared process state.
	EvaluationRestored EvaluationOutcome = "restored"
	// EvaluationPreserved marks a positive already present on the session view.
	EvaluationPreserved EvaluationOutcome = "preserved"
	// EvaluationUnknown marks an evaluation without decisive positive evidence.
	EvaluationUnknown EvaluationOutcome = "unknown"
	// EvaluationExcluded marks a configured exclusion that suppressed a
	// prospective local match without revoking an existing positive.
	EvaluationExcluded EvaluationOutcome = "excluded"
	// EvaluationNoAuthority marks a turn with no proxy-owned session/A-leg scope.
	EvaluationNoAuthority EvaluationOutcome = "no_authority"
	// EvaluationStateUnavailable marks the bounded fail-open outcome when
	// process-owned state could not be read or written.
	EvaluationStateUnavailable EvaluationOutcome = "state_unavailable"
	// EvaluationRemoteSkipped marks a remote-required mode whose remote decision
	// was not attempted for this turn.
	EvaluationRemoteSkipped EvaluationOutcome = "remote_skipped"
	// EvaluationRemoteTimeout marks a bounded remote timeout diagnostic. The
	// remote decider owns its emission; the local classifier never reports it.
	EvaluationRemoteTimeout EvaluationOutcome = "remote_timeout"
	// EvaluationRemoteError marks a bounded remote failure diagnostic. The remote
	// decider owns its emission; the local classifier never reports it.
	EvaluationRemoteError EvaluationOutcome = "remote_error"
)

// RemoteOutcome is the closed vocabulary for one attempted or skipped remote
// classification decision. Vendor result strings are deliberately absent: an
// arbitrary service response is never a label (requirements 9.3, 9.4).
type RemoteOutcome string

const (
	// RemotePositive marks a remote decision above the configured threshold.
	RemotePositive RemoteOutcome = "positive"
	// RemoteBelowThreshold marks a valid remote result that did not promote.
	RemoteBelowThreshold RemoteOutcome = "below_threshold"
	// RemoteTimeout marks a hard remote timeout.
	RemoteTimeout RemoteOutcome = "timeout"
	// RemoteRateLimited marks a rejected attempt caused by remote rate limiting.
	RemoteRateLimited RemoteOutcome = "rate_limited"
	// RemoteServerError marks a remote 5xx response.
	RemoteServerError RemoteOutcome = "server_error"
	// RemoteNetworkError marks a transport-level remote failure.
	RemoteNetworkError RemoteOutcome = "network_error"
	// RemoteMalformed marks an unparseable or oversized remote response.
	RemoteMalformed RemoteOutcome = "malformed"
	// RemoteLeaseBusy marks an attempt skipped because another lease is active.
	RemoteLeaseBusy RemoteOutcome = "lease_busy"
	// RemoteBudgetExhausted marks an attempt skipped by the finite attempt budget.
	RemoteBudgetExhausted RemoteOutcome = "budget_exhausted"
	// RemoteSkipped marks a turn that was never eligible for a remote decision.
	RemoteSkipped RemoteOutcome = "skipped"
)

// StoreOperation is the closed vocabulary of durable classification-state
// operations whose bounded outcomes are exported.
type StoreOperation string

const (
	// StoreOperationLoad reads the authoritative record for one authority key.
	StoreOperationLoad StoreOperation = "load"
	// StoreOperationPromote attempts the first accepted positive transition.
	StoreOperationPromote StoreOperation = "promote"
	// StoreOperationRemoteClaim claims the shared remote decision lease.
	StoreOperationRemoteClaim StoreOperation = "remote_claim"
	// StoreOperationRemoteComplete completes a claimed remote decision.
	StoreOperationRemoteComplete StoreOperation = "remote_complete"
)

// StoreOutcome is the closed vocabulary for one bounded store operation result.
type StoreOutcome string

const (
	// StoreOutcomeHit satisfied a read with a positive classification.
	StoreOutcomeHit StoreOutcome = "hit"
	// StoreOutcomeMiss satisfied a read without a positive classification.
	StoreOutcomeMiss StoreOutcome = "miss"
	// StoreOutcomeApplied accepted a durable mutation.
	StoreOutcomeApplied StoreOutcome = "applied"
	// StoreOutcomeUnchanged left state identical because first-positive-wins held.
	StoreOutcomeUnchanged StoreOutcome = "unchanged"
	// StoreOutcomeDenied refused a remote claim.
	StoreOutcomeDenied StoreOutcome = "denied"
	// StoreOutcomeError reports a bounded durable failure.
	StoreOutcomeError StoreOutcome = "error"
)

// EvaluationObservation describes one bounded classification evaluation. The
// struct has no field that could hold a session ID, an A-leg, a raw User-Agent,
// a filename, a path, a prompt, client metadata, or a remote response body.
type EvaluationObservation struct {
	// Mode is the bounded generation mode that produced the evaluation.
	Mode Mode
	// Outcome is the bounded result of the evaluation.
	Outcome EvaluationOutcome
}

// TransitionObservation describes the single first accepted positive
// transition for one logical session. It carries the bounded classification
// snapshot an operator-visible explanation surface can project directly,
// without reaching into feature-private classifier state (requirement 9.5).
type TransitionObservation struct {
	// Source is the bounded source of the accepted evidence.
	Source session.ClassificationSource
	// Confidence is the bounded confidence band of the accepted evidence.
	Confidence session.ConfidenceBand
	// Evidence is one code from this feature's closed evidence vocabulary.
	Evidence session.EvidenceCode
	// Revision is the monotonic snapshot revision assigned by the store. It is
	// deliberately part of the observation and not a metric label: a revision
	// is unbounded and would create unbounded cardinality (requirement 9.4).
	Revision uint64
}

// Snapshot projects the observation as the bounded classification snapshot
// shared with SDK consumers. It is a pure projection of the observation's
// bounded fields; an observer must call ValidTransitionObservation before
// trusting or exporting one.
func (o TransitionObservation) Snapshot() session.Classification {
	return session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     o.Source,
		Confidence: o.Confidence,
		Evidence:   o.Evidence,
		Revision:   o.Revision,
	}
}

// RemoteObservation describes one attempted or skipped remote decision. It
// carries a bounded outcome and a bounded latency; it has no field for the
// remote request or response body, so no credential-bearing request detail and
// no raw remote body can cross into a label or a log (requirements 7.5, 9.3).
type RemoteObservation struct {
	// Outcome is the bounded remote result vocabulary.
	Outcome RemoteOutcome
	// Latency is the bounded wall-clock duration of the remote attempt.
	Latency time.Duration
}

// StoreObservation describes one bounded durable classification-state
// operation result.
type StoreObservation struct {
	// Operation is the bounded durable operation.
	Operation StoreOperation
	// Outcome is the bounded operation result.
	Outcome StoreOutcome
}

// Observer receives bounded classification observations. Implementations must
// treat every value as untrusted input: an implementation that exports labels
// validates each field against the closed vocabularies exposed by this package
// and drops anything outside them. An observer never receives request content.
type Observer interface {
	// ObserveEvaluation records one bounded evaluation outcome.
	ObserveEvaluation(EvaluationObservation)
	// ObserveTransition records one accepted positive transition.
	ObserveTransition(TransitionObservation)
	// ObserveRemote records one bounded remote outcome and latency.
	ObserveRemote(RemoteObservation)
	// ObserveStore records one bounded durable operation result.
	ObserveStore(StoreObservation)
}

// The closed evidence vocabulary this feature can produce. The heuristic and
// the (future) remote decider both name their decisive code here so the metrics
// surface has one reusable source of truth (requirements 3.10, 9.4).
const (
	// EvidenceCodeCodex identifies a decisive Codex client identity match.
	EvidenceCodeCodex session.EvidenceCode = "client_family.codex"
	// EvidenceCodeRoo identifies a decisive Roo client identity match.
	EvidenceCodeRoo session.EvidenceCode = "client_family.roo"
	// EvidenceCodeOpenCode identifies a decisive OpenCode client identity match.
	EvidenceCodeOpenCode session.EvidenceCode = "client_family.opencode"
	// EvidenceCodePi identifies a decisive Pi client identity match.
	EvidenceCodePi session.EvidenceCode = "client_family.pi"
	// EvidenceCodeDroid identifies a decisive Droid client identity match.
	EvidenceCodeDroid session.EvidenceCode = "client_family.droid"
	// EvidenceCodeHermes identifies a decisive Hermes client identity match.
	EvidenceCodeHermes session.EvidenceCode = "client_family.hermes"
	// EvidenceCodeDistinctCluster identifies a read/search, edit/remove, and
	// OS-command tool cluster on one turn.
	EvidenceCodeDistinctCluster session.EvidenceCode = "tooling.distinct_coding_cluster"
	// EvidenceCodeProjectMarkerCluster identifies a read/search plus one local
	// mutation category corroborated by a recognized project marker.
	EvidenceCodeProjectMarkerCluster session.EvidenceCode = "tooling.project_marker_cluster"
	// EvidenceCodeRemoteAboveThreshold identifies a remote decision that met the
	// configured positive threshold.
	EvidenceCodeRemoteAboveThreshold session.EvidenceCode = "remote.above_threshold"
)

var (
	observationModes = []string{string(ModeHeuristic), string(ModeHybrid), string(ModeJev)}

	evaluationOutcomes = []string{
		string(EvaluationExcluded),
		string(EvaluationNoAuthority),
		string(EvaluationPreserved),
		string(EvaluationPromoted),
		string(EvaluationRemoteError),
		string(EvaluationRemoteSkipped),
		string(EvaluationRemoteTimeout),
		string(EvaluationRestored),
		string(EvaluationStateUnavailable),
		string(EvaluationUnknown),
	}

	remoteOutcomes = []string{
		string(RemoteBelowThreshold),
		string(RemoteBudgetExhausted),
		string(RemoteLeaseBusy),
		string(RemoteMalformed),
		string(RemoteNetworkError),
		string(RemotePositive),
		string(RemoteRateLimited),
		string(RemoteServerError),
		string(RemoteSkipped),
		string(RemoteTimeout),
	}

	storeOperations = []string{
		string(StoreOperationLoad),
		string(StoreOperationPromote),
		string(StoreOperationRemoteClaim),
		string(StoreOperationRemoteComplete),
	}

	storeOutcomes = []string{
		string(StoreOutcomeApplied),
		string(StoreOutcomeDenied),
		string(StoreOutcomeError),
		string(StoreOutcomeHit),
		string(StoreOutcomeMiss),
		string(StoreOutcomeUnchanged),
	}

	boundedEvidenceCodes = []string{
		string(EvidenceCodeCodex),
		string(EvidenceCodeDroid),
		string(EvidenceCodeHermes),
		string(EvidenceCodeOpenCode),
		string(EvidenceCodePi),
		string(EvidenceCodeRoo),
		string(EvidenceCodeRemoteAboveThreshold),
		string(EvidenceCodeDistinctCluster),
		string(EvidenceCodeProjectMarkerCluster),
	}
)

// ObservationModes returns the sorted closed mode vocabulary.
func ObservationModes() []string { return slices.Clone(observationModes) }

// EvaluationOutcomes returns the sorted closed evaluation outcome vocabulary.
func EvaluationOutcomes() []string { return slices.Clone(evaluationOutcomes) }

// RemoteOutcomes returns the sorted closed remote outcome vocabulary.
func RemoteOutcomes() []string { return slices.Clone(remoteOutcomes) }

// StoreOperations returns the sorted closed store operation vocabulary.
func StoreOperations() []string { return slices.Clone(storeOperations) }

// StoreOutcomes returns the sorted closed store outcome vocabulary.
func StoreOutcomes() []string { return slices.Clone(storeOutcomes) }

// BoundedEvidenceCodes returns the sorted closed decisive evidence vocabulary.
func BoundedEvidenceCodes() []string { return slices.Clone(boundedEvidenceCodes) }

// ModeAllowed reports whether mode is a member of the closed mode vocabulary.
func ModeAllowed(mode Mode) bool { return containsValue(observationModes, string(mode)) }

// EvaluationOutcomeAllowed reports whether outcome is in the closed vocabulary.
func EvaluationOutcomeAllowed(outcome EvaluationOutcome) bool {
	return containsValue(evaluationOutcomes, string(outcome))
}

// RemoteOutcomeAllowed reports whether outcome is in the closed vocabulary.
func RemoteOutcomeAllowed(outcome RemoteOutcome) bool {
	return containsValue(remoteOutcomes, string(outcome))
}

// StoreOperationAllowed reports whether operation is in the closed vocabulary.
func StoreOperationAllowed(operation StoreOperation) bool {
	return containsValue(storeOperations, string(operation))
}

// StoreOutcomeAllowed reports whether outcome is in the closed vocabulary.
func StoreOutcomeAllowed(outcome StoreOutcome) bool {
	return containsValue(storeOutcomes, string(outcome))
}

// EvidenceCodeAllowed reports whether code is one of this feature's decisive
// evidence codes. A syntactically valid but unknown code is still rejected, so
// an arbitrary vendor or client-derived string cannot become a label.
func EvidenceCodeAllowed(code session.EvidenceCode) bool {
	return containsValue(boundedEvidenceCodes, string(code))
}

// ValidEvaluationObservation reports whether every label value in observation is
// inside the closed vocabularies.
func ValidEvaluationObservation(observation EvaluationObservation) bool {
	return ModeAllowed(observation.Mode) && EvaluationOutcomeAllowed(observation.Outcome)
}

// ValidTransitionObservation reports whether observation describes a bounded
// accepted positive transition.
func ValidTransitionObservation(observation TransitionObservation) bool {
	switch observation.Source {
	case session.SourceLocalIdentity, session.SourceLocalTooling, session.SourceRemote:
	default:
		return false
	}
	if observation.Confidence != session.ConfidenceHigh {
		return false
	}
	if !EvidenceCodeAllowed(observation.Evidence) || observation.Revision == 0 || observation.Revision > MaxObservedRevision {
		return false
	}
	return observation.Snapshot().Validate() == nil
}

// ValidRemoteObservation reports whether observation describes a bounded remote
// outcome with a non-negative, bounded latency. Latency is an integer duration,
// so a negative or over-bound sample is the only way it can be unusable.
func ValidRemoteObservation(observation RemoteObservation) bool {
	if !RemoteOutcomeAllowed(observation.Outcome) {
		return false
	}
	return observation.Latency >= 0 && observation.Latency <= MaxRemoteObservationLatency
}

// ValidStoreObservation reports whether every label value in observation is
// inside the closed vocabularies.
func ValidStoreObservation(observation StoreObservation) bool {
	return StoreOperationAllowed(observation.Operation) && StoreOutcomeAllowed(observation.Outcome)
}

func containsValue(values []string, want string) bool {
	return want != "" && slices.Contains(values, want)
}
