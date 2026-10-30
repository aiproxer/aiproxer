package checkpoint

import (
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// EgressFactInput builds a metering fact for backend or frontend egress.
type EgressFactInput struct {
	Checkpoint      metering.Checkpoint // boundary/lifecycle/correlation template
	FactID          string
	Sequence        int64
	Kind            metering.FactKind
	Quantities      []metering.Quantity
	Outcome         metering.AttemptOutcome
	Surfaced        metering.SurfacedState
	Presence        metering.Presence
	Source          metering.Source
	Authority       metering.Authority
	Now             time.Time
	Money           *metering.MoneyObservation
	SourceID        string
	IdentityVersion int
	SourceEventKind string
	SourceRevision  int64
}

// FactFromEgress drafts a Fact from an egress observation (requirements 2.3, 2.4).
func FactFromEgress(in EgressFactInput) (metering.Fact, error) {
	factID := strings.TrimSpace(in.FactID)
	if factID == "" {
		return metering.Fact{}, fmt.Errorf("metering/checkpoint: fact_id required")
	}
	kind := in.Kind
	if kind == "" {
		kind = metering.FactKindCumulative
	}
	presence := in.Presence
	if presence == "" {
		if len(in.Quantities) == 0 {
			presence = metering.PresenceUnknown
		} else {
			presence = metering.PresencePresent
		}
	}
	source := in.Source
	if source == "" {
		source = metering.SourceObserved
	}
	authority := in.Authority
	if authority == "" {
		authority = metering.AuthorityAuthoritative
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cp := in.Checkpoint
	identityVersion := in.IdentityVersion
	if identityVersion == 0 {
		identityVersion = metering.IdentityVersionV1
	}
	sourceID := strings.TrimSpace(in.SourceID)
	if sourceID == "" {
		sourceID = factID
	}
	fact := metering.Fact{
		FactID:          factID,
		StreamID:        cp.StreamID,
		Sequence:        in.Sequence,
		Kind:            kind,
		Perspective:     cp.Perspective,
		Boundary:        cp.Boundary,
		Lifecycle:       cp.Lifecycle,
		Correlation:     cp.Correlation,
		Scope:           cp.Scope,
		FrontendID:      cp.FrontendID,
		BackendID:       cp.BackendID,
		Model:           cp.Model,
		AttemptOutcome:  in.Outcome,
		Surfaced:        in.Surfaced,
		Quantities:      in.Quantities,
		Money:           in.Money,
		Source:          source,
		Authority:       authority,
		Presence:        presence,
		RecordedAt:      now,
		SourceID:        sourceID,
		IdentityVersion: identityVersion,
		SourceEventKind: strings.TrimSpace(in.SourceEventKind),
		SourceRevision:  in.SourceRevision,
	}
	if err := fact.Validate(); err != nil {
		return metering.Fact{}, err
	}
	return fact, nil
}

// IngressFactInput builds a metering fact for a frozen backend-ingress checkpoint.
type IngressFactInput struct {
	Checkpoint      metering.Checkpoint
	FactID          string
	Sequence        int64
	Kind            metering.FactKind
	Quantities      []metering.Quantity
	Presence        metering.Presence
	Source          metering.Source
	Authority       metering.Authority
	Now             time.Time
	SourceID        string
	IdentityVersion int
	SourceEventKind string
	SourceRevision  int64
}

// FactFromIngress drafts a Fact from a backend-ingress freeze (requirements 2.2, 5.2).
func FactFromIngress(in IngressFactInput) (metering.Fact, error) {
	kind := in.Kind
	if kind == "" {
		kind = metering.FactKindReservationEstimate
	}
	authority := in.Authority
	if authority == "" {
		authority = metering.AuthorityEstimated
	}
	cp := in.Checkpoint
	cp.Boundary = metering.BoundaryBackendIngress
	cp.Lifecycle = metering.LifecycleBackendAttempt
	return FactFromEgress(EgressFactInput{
		Checkpoint:      cp,
		FactID:          in.FactID,
		Sequence:        in.Sequence,
		Kind:            kind,
		Quantities:      in.Quantities,
		Presence:        in.Presence,
		Source:          in.Source,
		Authority:       authority,
		Now:             in.Now,
		SourceID:        in.SourceID,
		IdentityVersion: in.IdentityVersion,
		SourceEventKind: in.SourceEventKind,
		SourceRevision:  in.SourceRevision,
	})
}

// FactFromFrontendIngress drafts a Fact from a frontend-ingress checkpoint (reqs 5.1, 5.5).
func FactFromFrontendIngress(in IngressFactInput) (metering.Fact, error) {
	kind := in.Kind
	if kind == "" {
		kind = metering.FactKindReservationEstimate
	}
	authority := in.Authority
	if authority == "" {
		authority = metering.AuthorityEstimated
	}
	cp := in.Checkpoint
	cp.Boundary = metering.BoundaryFrontendIngress
	cp.Lifecycle = metering.LifecycleLogicalRequest
	if cp.Perspective == "" {
		cp.Perspective = metering.PerspectiveCustomer
	}
	return FactFromEgress(EgressFactInput{
		Checkpoint:      cp,
		FactID:          in.FactID,
		Sequence:        in.Sequence,
		Kind:            kind,
		Quantities:      in.Quantities,
		Presence:        in.Presence,
		Source:          in.Source,
		Authority:       authority,
		Now:             in.Now,
		SourceID:        in.SourceID,
		IdentityVersion: in.IdentityVersion,
		SourceEventKind: in.SourceEventKind,
		SourceRevision:  in.SourceRevision,
	})
}

// BackendEgressCheckpoint templates a public checkpoint for one B-leg egress fact.
func BackendEgressCheckpoint(ingress Snapshot, outcome metering.AttemptOutcome, surfaced metering.SurfacedState) metering.Checkpoint {
	_ = outcome
	_ = surfaced
	cp := ingress.Public
	cp.Boundary = metering.BoundaryBackendEgress
	cp.Lifecycle = metering.LifecycleBackendAttempt
	if cp.Perspective == "" {
		cp.Perspective = metering.PerspectiveOperator
	}
	return cp
}

// FrontendEgressCheckpoint templates a public checkpoint for logical-request egress.
func FrontendEgressCheckpoint(feIngress Snapshot) metering.Checkpoint {
	cp := feIngress.Public
	cp.Boundary = metering.BoundaryFrontendEgress
	cp.Lifecycle = metering.LifecycleLogicalRequest
	if cp.Perspective == "" {
		cp.Perspective = metering.PerspectiveCustomer
	}
	return cp
}
