package metering

import (
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// Checkpoint is the public, journal-safe metering checkpoint DTO.
// It carries boundary identity, correlation, scope, and quantities only —
// never raw prompts, responses, headers, credentials, or resume tokens
// (requirement 2.7).
type Checkpoint struct {
	CheckpointID string                   `json:"checkpoint_id"`
	StreamID     string                   `json:"stream_id"`
	Boundary     Boundary                 `json:"boundary"`
	Lifecycle    LifecycleScope           `json:"lifecycle"`
	Perspective  EconomicPerspective      `json:"perspective"`
	Correlation  Correlation              `json:"correlation"`
	Scope        scope.PrincipalScopeView `json:"scope"`
	FrontendID   string                   `json:"frontend_id,omitempty"`
	BackendID    string                   `json:"backend_id,omitempty"`
	Model        string                   `json:"model,omitempty"`
	Quantities   []Quantity               `json:"quantities,omitempty"`
	Presence     Presence                 `json:"presence"`
	Source       Source                   `json:"source,omitempty"`
	Authority    Authority                `json:"authority,omitempty"`
	CapturedAt   time.Time                `json:"captured_at"`
}

// Validate checks required identity and enums for a checkpoint record.
// Boundary/lifecycle pairing is enforced for the four legal capture sites.
func (c Checkpoint) Validate() error {
	if strings.TrimSpace(c.CheckpointID) == "" {
		return fmt.Errorf("metering: checkpoint_id required")
	}
	if strings.TrimSpace(c.StreamID) == "" {
		return fmt.Errorf("metering: stream_id required")
	}
	if err := c.Boundary.Validate(); err != nil {
		return err
	}
	if err := c.Lifecycle.Validate(); err != nil {
		return err
	}
	if err := c.Perspective.Validate(); err != nil {
		return err
	}
	if err := c.Presence.Validate(); err != nil {
		return err
	}
	if c.Source != "" {
		if err := c.Source.Validate(); err != nil {
			return err
		}
	}
	if c.Authority != "" {
		if err := c.Authority.Validate(); err != nil {
			return err
		}
	}
	for i, q := range c.Quantities {
		if err := q.Validate(); err != nil {
			return fmt.Errorf("metering: quantities[%d]: %w", i, err)
		}
	}
	switch c.Boundary {
	case BoundaryFrontendIngress:
		if c.Lifecycle != LifecycleLogicalRequest && c.Lifecycle != LifecycleAuxiliaryRequest {
			return fmt.Errorf("metering: frontend_ingress requires logical_request or auxiliary_request lifecycle")
		}
	case BoundaryBackendIngress, BoundaryBackendEgress:
		if c.Lifecycle != LifecycleBackendAttempt {
			return fmt.Errorf("metering: %s requires backend_attempt lifecycle", c.Boundary)
		}
	case BoundaryFrontendEgress:
		if c.Lifecycle != LifecycleLogicalRequest && c.Lifecycle != LifecycleAuxiliaryRequest {
			return fmt.Errorf("metering: frontend_egress requires logical_request or auxiliary_request lifecycle")
		}
	}
	return nil
}
