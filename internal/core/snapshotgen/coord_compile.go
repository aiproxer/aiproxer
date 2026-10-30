package snapshotgen

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/runtimegen"
)

func buildCoordinators(contrib runtimegen.GenerationContribution) (*authoritycoord.RequestCoordinator, *authoritycoord.AttemptCoordinator, error) {
	var req *authoritycoord.RequestCoordinator
	var att *authoritycoord.AttemptCoordinator
	var conc authority.ConcurrencyProvider
	var concDesc *authority.ProviderDescriptor
	if contrib.ConcurrencyRegistration != nil {
		if err := contrib.ConcurrencyRegistration.Validate(); err != nil {
			return nil, nil, fmt.Errorf("snapshotgen: concurrency registration: %w", err)
		}
		conc = contrib.ConcurrencyRegistration.Provider
		desc := contrib.ConcurrencyRegistration.Descriptor
		concDesc = &desc
	}
	if contrib.MaxActiveRequests > 0 {
		var desc authority.ProviderDescriptor
		if concDesc != nil {
			desc = *concDesc
		}
		limiter := newMaxActiveLimiter(contrib.MaxActiveRequests, conc, desc)
		if limiter != nil {
			conc = limiter
			if concDesc == nil {
				d := limiter.desc
				concDesc = &d
			}
		}
	}
	if conc != nil || len(contrib.RequestRegistrations) > 0 {
		req = &authoritycoord.RequestCoordinator{Concurrency: conc, ConcurrencyDescriptor: concDesc}
	}
	for i, reg := range contrib.RequestRegistrations {
		if err := reg.Validate(); err != nil {
			return nil, nil, fmt.Errorf("snapshotgen: request registration[%d]: %w", i, err)
		}
		class, err := mapRequestPriority(reg.Priority)
		if err != nil {
			return nil, nil, err
		}
		posture, err := authority.RequireAdmitPosture(reg.Descriptor, authority.StageRequestAdmit)
		if err != nil {
			return nil, nil, fmt.Errorf("snapshotgen: request registration[%d]: %w", i, err)
		}
		if req == nil {
			req = &authoritycoord.RequestCoordinator{}
		}
		desc := reg.Descriptor
		req.Slots = append(req.Slots, authoritycoord.RequestSlot{
			ID: strings.TrimSpace(reg.Descriptor.ID), Class: class, Provider: reg.Provider,
			Strength: posture.Strength, FailureBehavior: posture.FailureBehavior,
			Descriptor: &desc, Stage: authority.StageRequestAdmit,
		})
	}
	for i, reg := range contrib.AttemptRegistrations {
		if err := reg.Validate(); err != nil {
			return nil, nil, fmt.Errorf("snapshotgen: attempt registration[%d]: %w", i, err)
		}
		class, err := mapAttemptPriority(reg.Priority)
		if err != nil {
			return nil, nil, err
		}
		posture, err := authority.RequireAdmitPosture(reg.Descriptor, authority.StageAttemptAdmit)
		if err != nil {
			return nil, nil, fmt.Errorf("snapshotgen: attempt registration[%d]: %w", i, err)
		}
		if att == nil {
			att = &authoritycoord.AttemptCoordinator{}
		}
		desc := reg.Descriptor
		att.Slots = append(att.Slots, authoritycoord.AttemptSlot{
			ID: strings.TrimSpace(reg.Descriptor.ID), Class: class, Provider: reg.Provider,
			Strength: posture.Strength, FailureBehavior: posture.FailureBehavior,
			Descriptor: &desc, Stage: authority.StageAttemptAdmit,
		})
	}
	return req, att, nil
}

func mapRequestPriority(p authority.RequestPriority) (authoritycoord.PriorityClass, error) {
	switch p {
	case authority.RequestPriorityConcurrency:
		return authoritycoord.PriorityConcurrency, nil
	case authority.RequestPriorityCreditWallet:
		return authoritycoord.PriorityCreditWallet, nil
	case authority.RequestPriorityQuotaBudgetRate:
		return authoritycoord.PriorityQuotaBudgetRate, nil
	case authority.RequestPriorityAdvisory:
		return authoritycoord.PriorityAdvisory, nil
	default:
		return 0, fmt.Errorf("snapshotgen: unknown request priority %q", p)
	}
}

func mapAttemptPriority(p authority.AttemptPriority) (authoritycoord.AttemptPriorityClass, error) {
	switch p {
	case authority.AttemptPriorityHardSpend:
		return authoritycoord.AttemptPriorityHardSpend, nil
	case authority.AttemptPriorityQuotaRate:
		return authoritycoord.AttemptPriorityQuotaRate, nil
	case authority.AttemptPriorityAdvisory:
		return authoritycoord.AttemptPriorityAdvisory, nil
	default:
		return 0, fmt.Errorf("snapshotgen: unknown attempt priority %q", p)
	}
}
