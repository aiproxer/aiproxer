package compatible

import (
	"context"
	"strings"

	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// AttemptProvider enforces per-compatible-instance limits at the backend-attempt seam.
type AttemptProvider struct {
	rt *Runtime
}

// NewAttemptProvider returns a provider backed by rt. Nil rt is a no-op provider.
func NewAttemptProvider(rt *Runtime) *AttemptProvider {
	if rt == nil {
		return nil
	}
	return &AttemptProvider{rt: rt}
}

func (p *AttemptProvider) AdmitAttempt(ctx context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	if p == nil || p.rt == nil || p.rt.service == nil {
		return authority.Decision{Kind: authority.DecisionAllow}, nil
	}
	backendID := strings.TrimSpace(in.BackendID)
	if _, ok := p.rt.limitFor(backendID); !ok {
		return authority.Decision{Kind: authority.DecisionAllow}, nil
	}
	reqID := strings.TrimSpace(in.BLegID)
	if reqID == "" {
		reqID = strings.TrimSpace(in.AttemptID)
	}
	if reqID == "" {
		reqID = strings.TrimSpace(in.RequestID)
	}
	res, err := p.rt.service.Admit(ctx, concurrencyapp.AdmitInput{
		RequestID: reqID,
		Scope: scope.PrincipalScopeView{
			PolicyLabels: map[string]string{
				"compatible_backend": backendID,
			},
		},
		Namespace:      namespace,
		Lifecycle:      metering.LifecycleBackendAttempt,
		IdempotencyKey: strings.TrimSpace(in.IdempotencyKey),
	})
	if err != nil {
		return authority.Decision{}, err
	}
	switch res.Kind {
	case domain.DecisionDeny:
		return authority.Decision{
			Kind:       authority.DecisionDeny,
			ProviderID: ProviderID,
			Evidence: authority.SafeEvidence{
				Category: "concurrency_limit",
				Code:     "capacity_exceeded",
				Message:  "compatible backend concurrent request limit reached",
			},
		}, nil
	case domain.DecisionAllow:
		if res.LeaseID == "" && len(res.Leases) == 0 {
			return authority.Decision{Kind: authority.DecisionAllow}, nil
		}
		leases := res.Leases
		if len(leases) == 0 && res.LeaseID != "" {
			leases = []concurrencyapp.AdmittedLease{{
				LeaseID: res.LeaseID,
				RuleID:  res.RuleID,
			}}
		}
		out := authority.Decision{
			Kind:       authority.DecisionAllow,
			ProviderID: ProviderID,
		}
		for _, lease := range leases {
			handle := strings.TrimSpace(lease.LeaseID)
			if handle == "" {
				continue
			}
			out.Reservations = append(out.Reservations, authority.Reservation{
				Handle: handle,
				Kind:   authority.ReservationQuota,
				RuleID: lease.RuleID,
				Quantity: &metering.Quantity{
					Component: metering.ComponentRequest,
					Unit:      metering.UnitCount,
					Value:     1,
					Present:   true,
				},
			})
		}
		if len(out.Reservations) > 0 {
			out.CompensationHandle = out.Reservations[0].Handle
		}
		return out, nil
	default:
		return authority.Decision{}, concurrencyapp.WrapError("admit", concurrencyapp.ErrUnavailable)
	}
}

func (p *AttemptProvider) SettleAttempt(ctx context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	if p == nil || p.rt == nil || p.rt.service == nil {
		return authority.OwnedFinalSettlement(in.Handles), nil
	}
	for _, h := range in.Handles {
		if err := p.releaseLease(ctx, in.RequestID, h); err != nil {
			return authority.Settlement{}, err
		}
	}
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (p *AttemptProvider) ReleaseAttempt(ctx context.Context, in authority.AttemptRelease) error {
	if p == nil || p.rt == nil || p.rt.service == nil {
		return nil
	}
	handles := in.Handles
	if len(handles) == 0 && strings.TrimSpace(in.CompensationHandle) != "" {
		handles = []string{in.CompensationHandle}
	}
	for _, h := range handles {
		if err := p.releaseLease(ctx, in.RequestID, h); err != nil {
			return err
		}
	}
	return nil
}

// releaseLease releases one lease under the caller's bounded cleanup context.
func (p *AttemptProvider) releaseLease(ctx context.Context, requestID, leaseID string) error {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return nil
	}
	return p.rt.service.Release(ctx, concurrencyapp.ReleaseInput{
		LeaseID:   leaseID,
		RequestID: strings.TrimSpace(requestID),
		Reason:    "compatible_admission_terminal",
	})
}
