package authoritycoord

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authorityattribution"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

// RequestSlot is one classified logical-request authority provider.
type RequestSlot struct {
	ID              string
	Class           PriorityClass
	Provider        authority.RequestProvider
	Strength        authority.Strength
	FailureBehavior authority.FailureBehavior
	Descriptor      *authority.ProviderDescriptor
	Stage           authority.Stage
}

// RequestCoordinator evaluates concurrency (optional) then classified request
// providers in deterministic priority-class order (requirements 4.5, 8.1, 15.1–15.4).
type RequestCoordinator struct {
	Concurrency           authority.ConcurrencyProvider
	ConcurrencyDescriptor *authority.ProviderDescriptor
	Slots                 []RequestSlot
	CleanupTimeout        time.Duration
	Now                   func() time.Time
}

func (c *RequestCoordinator) nowUTC() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *RequestCoordinator) Admit(ctx context.Context, in authority.RequestAdmission) (CompositeDecision, error) {
	if err := in.Validate(); err != nil {
		return CompositeDecision{}, err
	}
	if c == nil {
		return CompositeDecision{Kind: authority.DecisionAllow, Readiness: authority.ReadinessDisabled}, nil
	}
	slots := requestStageSlots(c.Slots)
	if err := validateStageSlots(slots, func(p authority.RequestProvider) bool { return p == nil }); err != nil {
		return CompositeDecision{}, err
	}
	out := CompositeDecision{Kind: authority.DecisionAllow, Readiness: authority.ReadinessReady}
	timeout := c.CleanupTimeout
	if timeout <= 0 {
		timeout = defaultCleanupTimeout
	}
	now := c.nowUTC()

	if c.Concurrency != nil {
		leaseIn := authority.LeaseAdmission{
			RequestID: in.RequestID, Scope: in.Scope, IdempotencyKey: in.IdempotencyKey,
			Lifecycle: in.Lifecycle, ParentLeaseID: in.ParentLeaseID, AuxPolicy: in.AuxPolicy,
		}
		reg, regErr := c.concurrencyRegistration()
		if regErr != nil {
			return CompositeDecision{}, regErr
		}
		ld, err := invokeAdmitLease(ctx, c.Concurrency, leaseIn, now, reg)
		if err != nil {
			var claimed CompensationStack
			pushLeaseDecisionHolds(&claimed, c.Concurrency, in.RequestID, ld)
			out.CompensateFailures = claimed.ReverseCompensate(ctx, timeout)
			out.Kind = authority.DecisionDeny
			out.DeniedBy = "concurrency"
			return out, &UnavailableError{ProviderID: "concurrency", Err: err}
		}
		out.Lease = ld
		out.Readiness = authorityattribution.AggregateReadiness(out.Readiness, ld.Readiness)
		if ld.BoundVersion.Version != "" {
			out.BoundVersions = append(out.BoundVersions, ld.BoundVersion)
		}
		switch ld.Kind {
		case authority.LeaseDeny:
			out.Kind = authority.DecisionDeny
			out.DeniedBy = "concurrency"
			return out, &DeniedError{ProviderID: "concurrency"}
		case authority.LeaseAllow, authority.LeaseAdvisory, "":
			pushLeaseDecisionHolds(&out.Stack, c.Concurrency, in.RequestID, ld)
		}
	}

	stageOut, err := runStageAdmit(ctx, &out.Stack, slots, in, timeout, requestAdmitConfig())
	out.Kind = stageOut.Kind
	out.Clamps = stageOut.Clamps
	out.Stack = stageOut.Stack
	out.ProviderDecisions = stageOut.ProviderDecisions
	out.Readiness = authorityattribution.AggregateReadiness(out.Readiness, stageOut.Readiness)
	out.CompensateFailures = append(out.CompensateFailures, stageOut.CompensateFailures...)
	out.Evidence = stageOut.Evidence
	out.DeniedBy = stageOut.DeniedBy
	out.BoundVersions = append(out.BoundVersions, stageOut.BoundVersions...)
	return out, err
}

func (c *RequestCoordinator) Settle(parent context.Context, stack CompensationStack, in authority.RequestSettlement) error {
	if c == nil {
		return nil
	}
	if err := validateStageSlots(requestStageSlots(c.Slots), func(p authority.RequestProvider) bool { return p == nil }); err != nil {
		return err
	}
	timeout := defaultCleanupTimeout
	if c.CleanupTimeout > 0 {
		timeout = c.CleanupTimeout
	}
	return runStageSettle(parent, requestStageSlotsUnsorted(c.Slots), stack, in, timeout, stageSettleConfig[authority.RequestSettlement, authority.RequestProvider]{
		observability: StageSettleRecordAllFailures,
		providerNil:   func(p authority.RequestProvider) bool { return p == nil },
		skipProvider:  func(id string) bool { return id == "concurrency" },
		settle:        invokeSettleRequest,
		validate:      validateSettlement,
		withHandles: func(in authority.RequestSettlement, handles []string) authority.RequestSettlement {
			out := in
			out.Handles = append([]string(nil), handles...)
			return out
		},
		resolvePosture: resolveStagePosture[authority.RequestProvider],
	})
}

func (c *RequestCoordinator) RenewLease(ctx context.Context, in authority.LeaseRenew) (authority.LeaseDecision, error) {
	if c == nil || c.Concurrency == nil {
		return authority.LeaseDecision{}, fmt.Errorf("authoritycoord: concurrency provider not configured")
	}
	reg, err := c.concurrencyRegistration()
	if err != nil {
		return authority.LeaseDecision{}, err
	}
	return invokeRenewLease(ctx, c.Concurrency, in, c.nowUTC(), reg)
}

func (c *RequestCoordinator) concurrencyRegistration() (authority.ProviderDescriptor, error) {
	if c == nil {
		return authority.ProviderDescriptor{}, fmt.Errorf("authoritycoord: nil coordinator")
	}
	if c.ConcurrencyDescriptor != nil {
		if strings.TrimSpace(c.ConcurrencyDescriptor.ID) == "" {
			return authority.ProviderDescriptor{}, fmt.Errorf("authoritycoord: concurrency descriptor id required")
		}
		return *c.ConcurrencyDescriptor, nil
	}
	if d, ok := c.Concurrency.(authority.Describer); ok {
		desc := d.Describe()
		if strings.TrimSpace(desc.ID) == "" {
			return authority.ProviderDescriptor{}, fmt.Errorf("authoritycoord: concurrency describer id required")
		}
		if _, ok := authority.AdmitPosture(desc, authority.StageLeaseAdmit); !ok {
			desc.Postures = append(append([]authority.StagePosture(nil), desc.Postures...), authority.StagePosture{
				Stage: authority.StageLeaseAdmit, Strength: authority.StrengthRequired, FailureBehavior: authority.FailureFailClosed,
			})
		}
		return desc, nil
	}
	return authority.ProviderDescriptor{
		ID: "concurrency",
		Postures: []authority.StagePosture{{
			Stage: authority.StageLeaseAdmit, Strength: authority.StrengthRequired, FailureBehavior: authority.FailureFailClosed,
		}},
	}, nil
}

func (c *RequestCoordinator) ReleaseLease(parent context.Context, leaseID, requestID, reason string) error {
	if c == nil || c.Concurrency == nil || strings.TrimSpace(leaseID) == "" {
		return nil
	}
	timeout := defaultCleanupTimeout
	if c.CleanupTimeout > 0 {
		timeout = c.CleanupTimeout
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()
	return c.Concurrency.ReleaseLease(ctx, authority.LeaseRelease{LeaseID: leaseID, RequestID: requestID, Reason: reason})
}

func (c *RequestCoordinator) ReleaseLeaseSet(parent context.Context, setID, leaseID, requestID, reason string) error {
	if c == nil || c.Concurrency == nil || strings.TrimSpace(setID) == "" {
		return nil
	}
	timeout := defaultCleanupTimeout
	if c.CleanupTimeout > 0 {
		timeout = c.CleanupTimeout
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()
	return c.Concurrency.ReleaseLease(ctx, authority.LeaseRelease{SetID: setID, LeaseID: leaseID, RequestID: requestID, Reason: reason})
}

func (c *RequestCoordinator) ReleaseLeases(parent context.Context, leaseIDs []string, requestID, reason string) error {
	var first error
	for _, id := range leaseIDs {
		if err := c.ReleaseLease(parent, id, requestID, reason); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (c *RequestCoordinator) Release(ctx context.Context, stack CompensationStack) []CompensateFailed {
	timeout := defaultCleanupTimeout
	if c != nil && c.CleanupTimeout > 0 {
		timeout = c.CleanupTimeout
	}
	return stack.ReverseCompensate(ctx, timeout)
}

func requestStageSlot(slot RequestSlot) stageProviderSlot[authority.RequestProvider] {
	return stageProviderSlot[authority.RequestProvider]{
		id: strings.TrimSpace(slot.ID), classOrd: int(slot.Class), provider: slot.Provider,
		strength: slot.Strength, failBeh: slot.FailureBehavior, descriptor: slot.Descriptor,
		stage: slot.Stage, advisoryClassOrd: int(PriorityAdvisory),
	}
}

func requestStageSlots(slots []RequestSlot) []stageProviderSlot[authority.RequestProvider] {
	out := make([]stageProviderSlot[authority.RequestProvider], len(slots))
	for i, slot := range slots {
		out[i] = requestStageSlot(slot)
	}
	return out
}

func requestStageSlotsUnsorted(slots []RequestSlot) []stageProviderSlot[authority.RequestProvider] {
	return requestStageSlots(slots)
}

func pushRequestHoldsFromAdmit(stack *CompensationStack, id string, prov authority.RequestProvider, in authority.RequestAdmission, d authority.Decision) {
	pushRequestDecisionHolds(stack, id, prov, in.RequestID, d)
}

func requestAdmitConfig() stageAdmitConfig[authority.RequestAdmission, authority.RequestProvider] {
	return stageAdmitConfig[authority.RequestAdmission, authority.RequestProvider]{
		label:       "request",
		providerNil: func(p authority.RequestProvider) bool { return p == nil },
		admit:       invokeAdmitRequest,
		pushHolds:   pushRequestHoldsFromAdmit,
		registration: func(slot stageProviderSlot[authority.RequestProvider]) (authority.ProviderDescriptor, authority.Stage) {
			return stageSlotRegistration(slot, authority.StageRequestAdmit)
		},
		resolvePosture: resolveStagePosture[authority.RequestProvider],
	}
}
