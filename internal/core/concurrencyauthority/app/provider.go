package app

import (
	"context"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// Provider adapts Service onto authority.ConcurrencyProvider.
type Provider struct {
	svc *Service
}

// NewProvider returns a ConcurrencyProvider backed by svc.
func NewProvider(svc *Service) *Provider {
	return &Provider{svc: svc}
}

var _ authority.ConcurrencyProvider = (*Provider)(nil)

// AdmitLease implements authority.ConcurrencyProvider.
func (p *Provider) AdmitLease(ctx context.Context, in authority.LeaseAdmission) (authority.LeaseDecision, error) {
	if p == nil || p.svc == nil {
		return authority.LeaseDecision{}, WrapError("admit", ErrUnavailable)
	}
	res, err := p.svc.Admit(ctx, AdmitInput{
		RequestID:      in.RequestID,
		Scope:          in.Scope,
		Namespace:      in.Namespace,
		TTL:            in.TTL,
		BoundVersion:   in.BoundVersion,
		IdempotencyKey: in.IdempotencyKey,
		RuleID:         in.RuleID,
		Lifecycle:      in.Lifecycle,
		ParentLeaseID:  in.ParentLeaseID,
		AuxPolicy:      domain.AuxPolicy(in.AuxPolicy),
	})
	if err != nil {
		return authority.LeaseDecision{}, err
	}
	return mapAdmitDecision(res), nil
}

// MarkLeaseSetUncertain marks a set conservatively occupied after ambiguous renew.
func (p *Provider) MarkLeaseSetUncertain(ctx context.Context, setID string) error {
	if p == nil || p.svc == nil || p.svc.store == nil {
		return WrapError("mark_uncertain", ErrUnavailable)
	}
	setID = strings.TrimSpace(setID)
	if setID == "" {
		return WrapError("mark_uncertain", ErrInvalidInput)
	}
	return p.svc.store.MarkSetUncertain(ctx, setID, p.svc.now())
}

// RenewLease implements authority.ConcurrencyProvider.
func (p *Provider) RenewLease(ctx context.Context, in authority.LeaseRenew) (authority.LeaseDecision, error) {
	if p == nil || p.svc == nil {
		return authority.LeaseDecision{}, WrapError("renew", ErrUnavailable)
	}
	res, err := p.svc.Renew(ctx, RenewInput{
		LeaseID:            in.LeaseID,
		RequestID:          in.RequestID,
		ExpectedGeneration: in.ExpectedGeneration,
		TTL:                in.TTL,
		SetID:              in.SetID,
		RenewBefore:        in.RenewBefore,
	})
	if err != nil {
		return authority.LeaseDecision{}, err
	}
	return mapAdmitDecision(res), nil
}

// ReleaseLease implements authority.ConcurrencyProvider.
func (p *Provider) ReleaseLease(ctx context.Context, in authority.LeaseRelease) error {
	if p == nil || p.svc == nil {
		return WrapError("release", ErrUnavailable)
	}
	return p.svc.Release(ctx, ReleaseInput{
		LeaseID:   in.LeaseID,
		RequestID: in.RequestID,
		Reason:    in.Reason,
		SetID:     in.SetID,
	})
}

// QueryLeases implements authority.ConcurrencyProvider.
func (p *Provider) QueryLeases(ctx context.Context, q authority.LeaseQuery) (authority.LeasePage, error) {
	if p == nil || p.svc == nil {
		return authority.LeasePage{}, WrapError("query", ErrUnavailable)
	}
	res, err := p.svc.Query(ctx, QueryCommand{
		LeaseID:   q.LeaseID,
		RequestID: q.RequestID,
		RuleID:    q.RuleID,
		State:     domain.LeaseState(q.State),
		Limit:     q.Limit,
	})
	if err != nil {
		return authority.LeasePage{}, err
	}
	page := authority.LeasePage{Leases: make([]authority.LeaseRecord, 0, len(res.Leases))}
	for _, lease := range res.Leases {
		page.Leases = append(page.Leases, authority.LeaseRecord{
			LeaseID:    lease.LeaseID,
			RequestID:  lease.LogicalID,
			State:      authority.LeaseState(lease.State),
			Generation: lease.Generation,
			ExpiresAt:  lease.ExpiresAt,
			ReleasedAt: lease.ReleasedAt,
			RuleID:     lease.RuleID,
			Version: economics.PolicySnapshotRef{
				VersionRef: economics.VersionRef{Version: lease.RuleVersion},
			},
			DimensionKey: string(lease.Dimensions.Key()),
		})
	}
	return page, nil
}

func mapAdmitDecision(res AdmitResult) authority.LeaseDecision {
	dec := authority.LeaseDecision{
		Kind:            mapDecisionKind(res.Kind),
		LeaseID:         res.LeaseID,
		Generation:      res.Generation,
		ExpiresAt:       res.ExpiresAt,
		RemainingSlots:  res.RemainingSlots,
		Readiness:       mapReadiness(res.Readiness),
		BoundVersion:    res.BoundVersion,
		RenewBefore:     res.RenewBefore,
		TTL:             res.TTL,
		FailureBehavior: authority.FailureBehavior(res.FailureBehavior),
		SetID:           res.SetID,
		Evidence: authority.SafeEvidence{
			Category: res.Evidence.Category,
			Code:     res.Evidence.Code,
			Message:  res.Evidence.Message,
			RuleID:   res.Evidence.RuleID,
			Attrs:    res.Evidence.Attrs,
		},
	}
	if len(res.Leases) > 0 {
		dec.Leases = make([]authority.LeaseOccupancy, 0, len(res.Leases))
		for _, occ := range res.Leases {
			dec.Leases = append(dec.Leases, authority.LeaseOccupancy{
				LeaseID:         occ.LeaseID,
				Generation:      occ.Generation,
				RuleID:          occ.RuleID,
				ExpiresAt:       occ.ExpiresAt,
				RenewBefore:     occ.RenewBefore,
				TTL:             occ.TTL,
				FailureBehavior: authority.FailureBehavior(occ.FailureBehavior),
			})
		}
	}
	return dec
}

func mapDecisionKind(k domain.DecisionKind) authority.LeaseDecisionKind {
	switch k {
	case domain.DecisionDeny:
		return authority.LeaseDeny
	case domain.DecisionAdvisory:
		return authority.LeaseAdvisory
	default:
		return authority.LeaseAllow
	}
}
