package authority

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// RequestAdmission is logical-request admit input. It intentionally has no
// AttemptID or BLegID fields (requirements 9.2, 9.3).
type RequestAdmission struct {
	RequestID      string                        `json:"request_id"`
	ALegID         string                        `json:"a_leg_id,omitempty"`
	TraceID        string                        `json:"trace_id,omitempty"`
	Perspective    metering.EconomicPerspective  `json:"perspective"`
	Lifecycle      metering.LifecycleScope       `json:"lifecycle"`
	Scope          scope.PrincipalScopeView      `json:"scope"`
	Exposure       economics.ExposureBasis       `json:"exposure"`
	BoundVersions  []economics.PolicySnapshotRef `json:"bound_versions,omitempty"`
	RatingVersions []economics.RatingSnapshotRef `json:"rating_versions,omitempty"`
	IdempotencyKey string                        `json:"idempotency_key,omitempty"`
	// ParentLeaseID is set for auxiliary requests that inherit occupancy (10.10).
	ParentLeaseID string `json:"parent_lease_id,omitempty"`
	// AuxPolicy overrides default parent-lease inheritance for auxiliary admits.
	AuxPolicy string `json:"aux_policy,omitempty"`
}

// Validate checks required request identity and enums.
func (a RequestAdmission) Validate() error {
	if strings.TrimSpace(a.RequestID) == "" {
		return fmt.Errorf("authority: request_id required")
	}
	if a.Perspective != "" {
		if err := a.Perspective.Validate(); err != nil {
			return err
		}
	}
	if a.Lifecycle != "" {
		if err := a.Lifecycle.Validate(); err != nil {
			return err
		}
	}
	return a.Exposure.Validate()
}

// RequestSettlement settles customer/logical-request reservations once.
type RequestSettlement struct {
	RequestID      string                        `json:"request_id"`
	Handles        []string                      `json:"handles,omitempty"`
	Facts          []metering.Fact               `json:"facts,omitempty"`
	BoundVersions  []economics.PolicySnapshotRef `json:"bound_versions,omitempty"`
	IdempotencyKey string                        `json:"idempotency_key,omitempty"`
}

// RequestRelease compensates or releases prior request reservations.
type RequestRelease struct {
	RequestID          string   `json:"request_id"`
	Handles            []string `json:"handles,omitempty"`
	CompensationHandle string   `json:"compensation_handle,omitempty"`
	Reason             string   `json:"reason,omitempty"`
}

// AttemptAdmission is backend-attempt admit input and requires attempt/B-leg
// identity (requirements 9.2, 9.3).
type AttemptAdmission struct {
	RequestID      string                        `json:"request_id"`
	AttemptID      string                        `json:"attempt_id"`
	BLegID         string                        `json:"b_leg_id"`
	ALegID         string                        `json:"a_leg_id,omitempty"`
	BackendID      string                        `json:"backend_id,omitempty"`
	Model          string                        `json:"model,omitempty"`
	Perspective    metering.EconomicPerspective  `json:"perspective"`
	Lifecycle      metering.LifecycleScope       `json:"lifecycle"`
	Scope          scope.PrincipalScopeView      `json:"scope"`
	Exposure       economics.ExposureBasis       `json:"exposure"`
	BoundVersions  []economics.PolicySnapshotRef `json:"bound_versions,omitempty"`
	RatingVersions []economics.RatingSnapshotRef `json:"rating_versions,omitempty"`
	IdempotencyKey string                        `json:"idempotency_key,omitempty"`
}

// Validate requires request, attempt, and B-leg identity.
func (a AttemptAdmission) Validate() error {
	if strings.TrimSpace(a.RequestID) == "" {
		return fmt.Errorf("authority: request_id required")
	}
	if strings.TrimSpace(a.AttemptID) == "" {
		return fmt.Errorf("authority: attempt_id required")
	}
	if strings.TrimSpace(a.BLegID) == "" {
		return fmt.Errorf("authority: b_leg_id required")
	}
	if a.Perspective != "" {
		if err := a.Perspective.Validate(); err != nil {
			return err
		}
	}
	if a.Lifecycle != "" {
		if err := a.Lifecycle.Validate(); err != nil {
			return err
		}
	}
	return a.Exposure.Validate()
}

// AttemptSettlement settles operator/attempt reservations for one B-leg.
type AttemptSettlement struct {
	RequestID        string                        `json:"request_id"`
	AttemptID        string                        `json:"attempt_id"`
	BLegID           string                        `json:"b_leg_id"`
	Handles          []string                      `json:"handles,omitempty"`
	Reservations     []Reservation                 `json:"reservations,omitempty"`
	Facts            []metering.Fact               `json:"facts,omitempty"`
	Outcome          metering.AttemptOutcome       `json:"outcome,omitempty"`
	Surfaced         metering.SurfacedState        `json:"surfaced,omitempty"`
	Kind             string                        `json:"kind,omitempty"`
	ALegID           string                        `json:"a_leg_id,omitempty"`
	BackendID        string                        `json:"backend_id,omitempty"`
	Model            string                        `json:"model,omitempty"`
	Scope            scope.PrincipalScopeView      `json:"scope,omitzero"`
	BackendAttempted bool                          `json:"backend_attempted,omitempty"`
	OutputCommitted  bool                          `json:"output_committed,omitempty"`
	ClientCanceled   bool                          `json:"client_canceled,omitempty"`
	BoundVersions    []economics.PolicySnapshotRef `json:"bound_versions,omitempty"`
	IdempotencyKey   string                        `json:"idempotency_key,omitempty"`
}

// AttemptRelease compensates or releases prior attempt reservations.
type AttemptRelease struct {
	RequestID          string   `json:"request_id"`
	AttemptID          string   `json:"attempt_id"`
	BLegID             string   `json:"b_leg_id"`
	Handles            []string `json:"handles,omitempty"`
	CompensationHandle string   `json:"compensation_handle,omitempty"`
	Reason             string   `json:"reason,omitempty"`
}

// RequestProvider evaluates logical-request customer authority.
type RequestProvider interface {
	AdmitRequest(ctx context.Context, in RequestAdmission) (Decision, error)
	SettleRequest(ctx context.Context, in RequestSettlement) (Settlement, error)
	ReleaseRequest(ctx context.Context, in RequestRelease) error
}

// AttemptProvider evaluates backend-attempt operator authority.
type AttemptProvider interface {
	AdmitAttempt(ctx context.Context, in AttemptAdmission) (Decision, error)
	SettleAttempt(ctx context.Context, in AttemptSettlement) (Settlement, error)
	ReleaseAttempt(ctx context.Context, in AttemptRelease) error
}

// AttemptClampPreviewer optionally previews non-widening clamps without holds
// (design Clamp Preview; requirement 2.1). PreviewAttempt must not reserve or
// record durable admission evidence.
type AttemptClampPreviewer interface {
	PreviewAttempt(ctx context.Context, in AttemptAdmission) (Decision, error)
}
