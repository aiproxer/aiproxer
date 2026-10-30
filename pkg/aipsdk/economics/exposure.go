package economics

import (
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// OutputBoundKind classifies how a conservative max-output assumption was chosen
// at admission (requirement 7.6; aligns with Phase 2 unknown-output policies).
type OutputBoundKind string

const (
	OutputBoundRequireClientLimit  OutputBoundKind = "require_client_limit"
	OutputBoundConfiguredDefault   OutputBoundKind = "configured_default"
	OutputBoundModelBackendMaximum OutputBoundKind = "model_backend_maximum"
	OutputBoundClamp               OutputBoundKind = "clamp"
	OutputBoundDeny                OutputBoundKind = "deny"
	OutputBoundClientProvided      OutputBoundKind = "client_provided"
)

// IsKnown reports whether k is a documented output bound kind.
func (k OutputBoundKind) IsKnown() bool {
	switch k {
	case OutputBoundRequireClientLimit, OutputBoundConfiguredDefault, OutputBoundModelBackendMaximum,
		OutputBoundClamp, OutputBoundDeny, OutputBoundClientProvided:
		return true
	}
	return false
}

// ConservativeOutputAssumption is the output token bound used for admission
// exposure when actual output is unknown.
type ConservativeOutputAssumption struct {
	BoundKind  OutputBoundKind `json:"bound_kind"`
	TokenCount int64           `json:"token_count"`
	PolicyID   string          `json:"policy_id,omitempty"`
	Present    bool            `json:"present"`
}

// Validate checks bound kind when Present.
func (c ConservativeOutputAssumption) Validate() error {
	if !c.Present {
		return nil
	}
	if !c.BoundKind.IsKnown() {
		return fmt.Errorf("economics: unknown output bound kind %q", c.BoundKind)
	}
	if c.TokenCount < 0 {
		return fmt.Errorf("economics: token_count must be non-negative")
	}
	return nil
}

// ExposureBasis describes quantities and assumptions used for authority admission.
type ExposureBasis struct {
	Perspective metering.EconomicPerspective `json:"perspective"`
	Boundary    metering.Boundary            `json:"boundary"`
	Lifecycle   metering.LifecycleScope      `json:"lifecycle"`
	Quantities  []metering.Quantity          `json:"quantities,omitempty"`
	Money       Money                        `json:"money,omitzero"`
	Output      ConservativeOutputAssumption `json:"output,omitzero"`
	FactRefs    []metering.FactRef           `json:"fact_refs,omitempty"`
}

// Validate checks perspective/boundary/lifecycle and optional output assumption.
func (e ExposureBasis) Validate() error {
	if err := e.Perspective.Validate(); err != nil {
		return err
	}
	if err := e.Boundary.Validate(); err != nil {
		return err
	}
	if err := e.Lifecycle.Validate(); err != nil {
		return err
	}
	for i, q := range e.Quantities {
		if err := q.Validate(); err != nil {
			return fmt.Errorf("economics: quantities[%d]: %w", i, err)
		}
	}
	for i, ref := range e.FactRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("economics: fact_refs[%d]: %w", i, err)
		}
	}
	return e.Output.Validate()
}
