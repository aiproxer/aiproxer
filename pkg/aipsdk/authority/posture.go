package authority

import (
	"fmt"
	"strings"
)

// ProviderKind distinguishes enforceable authority providers from fail-open
// usage/traffic observers (requirement 12.7). Observers must not be wired as
// RequestProvider or AttemptProvider for strict admission.
type ProviderKind string

const (
	// ProviderKindAuthority may participate in Admit/Settle/Release. Strength
	// and FailureBehavior on StagePosture further classify required vs advisory
	// and fail-open vs fail-closed infrastructure posture (requirement 15.1).
	ProviderKindAuthority ProviderKind = "authority"
	// ProviderKindObserver is diagnostic/observation only. It must never be
	// treated as a strict admission authority, even when FailureBehavior is
	// fail_open.
	ProviderKindObserver ProviderKind = "observer"
)

// IsKnown reports whether k is a documented provider kind.
func (k ProviderKind) IsKnown() bool {
	switch k {
	case ProviderKindAuthority, ProviderKindObserver:
		return true
	}
	return false
}

// Validate returns an error when k is not a known provider kind.
func (k ProviderKind) Validate() error {
	if !k.IsKnown() {
		return fmt.Errorf("authority: unknown provider kind %q", k)
	}
	return nil
}

// Strength classifies whether an authority provider is required or advisory
// for a lifecycle stage (requirement 15.1).
type Strength string

const (
	StrengthRequired Strength = "required"
	StrengthAdvisory Strength = "advisory"
)

// IsKnown reports whether s is a documented strength.
func (s Strength) IsKnown() bool {
	switch s {
	case StrengthRequired, StrengthAdvisory:
		return true
	}
	return false
}

// Validate returns an error when s is not a known strength.
func (s Strength) Validate() error {
	if !s.IsKnown() {
		return fmt.Errorf("authority: unknown strength %q", s)
	}
	return nil
}

// FailureBehavior classifies fail-open vs fail-closed infrastructure posture
// for a provider at a lifecycle stage (requirement 15.1). Distinct from
// deterministic capacity denials (requirement 15.4). Fail-open observers use
// ProviderKindObserver and must not be represented as strict authorities
// (requirement 12.7).
type FailureBehavior string

const (
	FailureFailClosed FailureBehavior = "fail_closed"
	FailureFailOpen   FailureBehavior = "fail_open"
)

// IsKnown reports whether b is a documented failure behavior.
func (b FailureBehavior) IsKnown() bool {
	switch b {
	case FailureFailClosed, FailureFailOpen:
		return true
	}
	return false
}

// Validate returns an error when b is not a known failure behavior.
func (b FailureBehavior) Validate() error {
	if !b.IsKnown() {
		return fmt.Errorf("authority: unknown failure behavior %q", b)
	}
	return nil
}

// StagePosture is one provider's declared posture for one lifecycle stage.
type StagePosture struct {
	Stage           Stage           `json:"stage"`
	Strength        Strength        `json:"strength"`
	FailureBehavior FailureBehavior `json:"failure_behavior"`
}

// Validate checks stage, strength, and failure behavior are known.
func (p StagePosture) Validate() error {
	if !p.Stage.IsKnown() {
		return fmt.Errorf("authority: unknown stage %q", p.Stage)
	}
	if err := p.Strength.Validate(); err != nil {
		return err
	}
	return p.FailureBehavior.Validate()
}

// ProviderDescriptor is the public registration/describe surface for request,
// attempt, concurrency, or observer providers (requirements 12.1, 12.7, 15.1).
// Coordinators consume descriptors; they are not store or executor types.
//
// Kind is optional for additive compatibility (requirement 12.8): omitted Kind
// defaults to ProviderKindAuthority via EffectiveKind.
type ProviderDescriptor struct {
	ID       string         `json:"id"`
	Kind     ProviderKind   `json:"kind,omitempty"`
	Postures []StagePosture `json:"postures"`
}

// EffectiveKind returns Kind, defaulting omitted values to authority.
func (d ProviderDescriptor) EffectiveKind() ProviderKind {
	if d.Kind == "" {
		return ProviderKindAuthority
	}
	return d.Kind
}

// Validate requires a non-empty trimmed ID and validates kind and each posture row.
// Duplicate stage postures are rejected (requirement 3.2). Observers cannot
// declare StrengthRequired (requirement 12.7).
func (d ProviderDescriptor) Validate() error {
	if strings.TrimSpace(d.ID) == "" {
		return fmt.Errorf("authority: provider id required")
	}
	kind := d.EffectiveKind()
	if d.Kind != "" {
		if err := d.Kind.Validate(); err != nil {
			return err
		}
	}
	if len(d.Postures) == 0 {
		return fmt.Errorf("authority: at least one stage posture required")
	}
	seenStages := make(map[Stage]struct{}, len(d.Postures))
	for i, p := range d.Postures {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("authority: postures[%d]: %w", i, err)
		}
		if _, dup := seenStages[p.Stage]; dup {
			return fmt.Errorf("authority: postures[%d]: duplicate stage %q", i, p.Stage)
		}
		seenStages[p.Stage] = struct{}{}
		if kind == ProviderKindObserver && p.Strength == StrengthRequired {
			return fmt.Errorf("authority: postures[%d]: observer cannot declare required strength (requirement 12.7)", i)
		}
	}
	return nil
}

// Describer exposes provider posture independently of Admit/Settle/Release so
// the design provider method sets stay focused while still satisfying 15.1.
type Describer interface {
	Describe() ProviderDescriptor
}
