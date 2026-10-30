package policydecision

import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// Outcome is the protocol-neutral result of one policy decision provider at a
// lifecycle position. OutcomeUnknown is the zero value and is never legal in an
// emitted record (requirement 1.1, 1.5).
type Outcome string

const (
	OutcomeUnknown Outcome = "unknown"
	OutcomeAllow   Outcome = "allow"
	OutcomeDeny    Outcome = "deny"
	OutcomeSkip    Outcome = "skip"
	OutcomeError   Outcome = "error"
)

// IsKnown reports whether o is one of the documented non-unknown outcomes.
func (o Outcome) IsKnown() bool {
	switch o {
	case OutcomeAllow, OutcomeDeny, OutcomeSkip, OutcomeError:
		return true
	}
	return false
}

// Effect describes what a decision does to request or response content beyond the
// outcome itself (requirement 1.2). EffectNone is the zero value and is always legal
// when paired with a known outcome that does not require mutation.
type Effect string

const (
	EffectNone     Effect = "none"
	EffectAnnotate Effect = "annotate"
	EffectMutate   Effect = "mutate"
	EffectReplace  Effect = "replace"
	EffectReplay   Effect = "replay"
	EffectSwallow  Effect = "swallow"
)

// IsKnown reports whether e is one of the documented effects.
func (e Effect) IsKnown() bool {
	switch e {
	case EffectNone, EffectAnnotate, EffectMutate, EffectReplace, EffectReplay, EffectSwallow:
		return true
	}
	return false
}

// FailureBehavior records how a stage runner should treat provider failures for the
// decision (requirement 6.1, 6.2). FailureBehaviorUnspecified is the zero value.
type FailureBehavior string

const (
	FailureBehaviorUnspecified FailureBehavior = ""
	FailureBehaviorFailOpen    FailureBehavior = "fail_open"
	FailureBehaviorFailClosed  FailureBehavior = "fail_closed"
)

// IsKnown reports whether b is one of the documented non-unspecified behaviors.
func (b FailureBehavior) IsKnown() bool {
	switch b {
	case FailureBehaviorFailOpen, FailureBehaviorFailClosed:
		return true
	}
	return false
}

// EvidenceVisibility controls whether privileged diagnostic detail may leave the core
// extension runner (requirement 7.4). EvidenceDefault is the zero value and the only
// value emitted unless explicit diagnostics exposure posture is enabled.
type EvidenceVisibility string

const (
	EvidenceDefault    EvidenceVisibility = "default"
	EvidencePrivileged EvidenceVisibility = "privileged"
)

// IsKnown reports whether v is one of the documented visibility values.
func (v EvidenceVisibility) IsKnown() bool {
	switch v {
	case EvidenceDefault, EvidencePrivileged:
		return true
	}
	return false
}

// ProviderRef identifies the decision provider that produced a record and the stage
// it ran at. IDs are stable plugin identifiers; Stage is a legal feature stage ID.
type ProviderRef struct {
	ID    string `json:"id"`
	Stage string `json:"stage"`
}

// LegalStageIDs returns the ordered legal pipeline stage IDs (delegates to
// pkg/aipsdk/feature so callers do not need a second stage taxonomy).
func LegalStageIDs() []string {
	return feature.LegalPipelineStageIDs()
}

// IsLegalStageID reports whether stage is one of the legal pipeline stages.
func IsLegalStageID(stage string) bool {
	return feature.ValidateStageID(stage)
}
