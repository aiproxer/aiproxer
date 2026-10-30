package runtimebundle

import (
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	coreconfig "github.com/aiproxer/aiproxer/internal/core/config"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

// quotaRequestRegistration compiles the one optional provider quota policy
// for the candidate generation and binds it to the nonfinancial account-window
// reader. No registration is returned when quota is absent, preserving the
// existing generic authority path.
func quotaRequestRegistration(cfg *coreconfig.Config, store coremetering.AccountWindowStore, now func() time.Time) (*authority.RequestRegistration, error) {
	if cfg == nil || cfg.Accounting.Authority.Quota == nil {
		return nil, nil
	}
	policyConfig, err := cfg.Accounting.Authority.Quota.PolicyConfig()
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: quota policy config: %w", err)
	}
	policy, err := authority.CompileQuotaPolicy(policyConfig)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: quota policy: %w", err)
	}
	provider, err := authoritycoord.NewQuotaRequestProvider(authoritycoord.QuotaRequestProviderConfig{
		ID: "provider-quota:" + policy.ID(), Policy: policy, Store: store, Now: now,
	})
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: quota provider: %w", err)
	}
	descriptor := provider.Describe()
	return &authority.RequestRegistration{
		Descriptor: descriptor,
		Priority:   authority.RequestPriorityQuotaBudgetRate,
		Provider:   provider,
	}, nil
}

// attachAuthorityCoordinators merges descriptor-bound production registrations
// into request/attempt coordinators (requirements 3.1–3.4, 3.7–3.9, 12.1, 12.4).
func attachAuthorityCoordinators(rt *runtime.AccountingRuntime, prod ProductionOptions) error {
	if rt == nil {
		return nil
	}
	var concurrencyDesc *authority.ProviderDescriptor
	if prod.ConcurrencyRegistration != nil {
		if err := prod.ConcurrencyRegistration.Validate(); err != nil {
			return fmt.Errorf("runtimebundle: concurrency_registration: %w", err)
		}
		rt.ConcurrencyProvider = prod.ConcurrencyRegistration.Provider
		desc := prod.ConcurrencyRegistration.Descriptor
		concurrencyDesc = &desc
	}
	if rt.UsageAuthority == nil && rt.ConcurrencyProvider == nil && !prod.HasAuthorityOverrides() {
		return nil
	}
	req, att := runtime.BuildAuthorityCoordinators(rt.UsageAuthority, rt.ConcurrencyProvider)
	if req == nil && (len(prod.RequestRegistrations) > 0 || rt.ConcurrencyProvider != nil) {
		req = &authoritycoord.RequestCoordinator{Concurrency: rt.ConcurrencyProvider}
	}
	if att == nil && len(prod.AttemptRegistrations) > 0 {
		att = &authoritycoord.AttemptCoordinator{}
	}
	seen := map[string]struct{}{}
	for i, reg := range prod.RequestRegistrations {
		if err := reg.Validate(); err != nil {
			return fmt.Errorf("runtimebundle: request_registrations[%d]: %w", i, err)
		}
		id := strings.TrimSpace(reg.Descriptor.ID)
		if _, dup := seen[id]; dup {
			return fmt.Errorf("runtimebundle: duplicate request registration id %q", id)
		}
		seen[id] = struct{}{}
		class, err := mapRequestPriority(reg.Priority)
		if err != nil {
			return fmt.Errorf("runtimebundle: request_registrations[%d]: %w", i, err)
		}
		posture, err := authority.RequireAdmitPosture(reg.Descriptor, authority.StageRequestAdmit)
		if err != nil {
			return fmt.Errorf("runtimebundle: request_registrations[%d]: %w", i, err)
		}
		if req == nil {
			req = &authoritycoord.RequestCoordinator{Concurrency: rt.ConcurrencyProvider}
		}
		desc := reg.Descriptor
		req.Slots = append(req.Slots, authoritycoord.RequestSlot{
			ID: id, Class: class, Provider: reg.Provider, Strength: posture.Strength, FailureBehavior: posture.FailureBehavior,
			Descriptor: &desc, Stage: authority.StageRequestAdmit,
		})
	}
	seen = map[string]struct{}{}
	for i, reg := range prod.AttemptRegistrations {
		if err := reg.Validate(); err != nil {
			return fmt.Errorf("runtimebundle: attempt_registrations[%d]: %w", i, err)
		}
		id := strings.TrimSpace(reg.Descriptor.ID)
		if _, dup := seen[id]; dup {
			return fmt.Errorf("runtimebundle: duplicate attempt registration id %q", id)
		}
		seen[id] = struct{}{}
		class, err := mapAttemptPriority(reg.Priority)
		if err != nil {
			return fmt.Errorf("runtimebundle: attempt_registrations[%d]: %w", i, err)
		}
		posture, err := authority.RequireAdmitPosture(reg.Descriptor, authority.StageAttemptAdmit)
		if err != nil {
			return fmt.Errorf("runtimebundle: attempt_registrations[%d]: %w", i, err)
		}
		if att == nil {
			att = &authoritycoord.AttemptCoordinator{}
		}
		desc := reg.Descriptor
		att.Slots = append(att.Slots, authoritycoord.AttemptSlot{
			ID: id, Class: class, Provider: reg.Provider, Strength: posture.Strength, FailureBehavior: posture.FailureBehavior,
			Descriptor: &desc, Stage: authority.StageAttemptAdmit,
		})
	}
	if err := rejectOverlappingRegistrationIDs(prod); err != nil {
		return err
	}
	if req != nil && rt.ConcurrencyProvider != nil {
		req.Concurrency = rt.ConcurrencyProvider
		req.ConcurrencyDescriptor = concurrencyDesc
	}
	rt.RequestCoordinator, rt.AttemptCoordinator = req, att
	return nil
}

func rejectOverlappingRegistrationIDs(prod ProductionOptions) error {
	stagesByID := map[string]map[authority.Stage]struct{}{}
	claim := func(id string, postures []authority.StagePosture) error {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil
		}
		seen := stagesByID[id]
		if seen == nil {
			seen = map[authority.Stage]struct{}{}
			stagesByID[id] = seen
		}
		for _, p := range postures {
			if _, dup := seen[p.Stage]; dup {
				return fmt.Errorf("runtimebundle: duplicate provider id %q for overlapping stage %q", id, p.Stage)
			}
			seen[p.Stage] = struct{}{}
		}
		return nil
	}
	for _, reg := range prod.RequestRegistrations {
		if err := claim(reg.Descriptor.ID, reg.Descriptor.Postures); err != nil {
			return err
		}
	}
	for _, reg := range prod.AttemptRegistrations {
		if err := claim(reg.Descriptor.ID, reg.Descriptor.Postures); err != nil {
			return err
		}
	}
	if prod.ConcurrencyRegistration != nil {
		if err := claim(prod.ConcurrencyRegistration.Descriptor.ID, prod.ConcurrencyRegistration.Descriptor.Postures); err != nil {
			return err
		}
	}
	return nil
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
		return 0, fmt.Errorf("unknown request priority %q", p)
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
		return 0, fmt.Errorf("unknown attempt priority %q", p)
	}
}

func slotIDs(regs []string, fallback func() []string) []string {
	if len(regs) > 0 {
		return append([]string(nil), regs...)
	}
	return fallback()
}

func requestRegistrationIDs(prod ProductionOptions, rt *runtime.AccountingRuntime) []string {
	ids := make([]string, 0, len(prod.RequestRegistrations))
	for _, reg := range prod.RequestRegistrations {
		ids = append(ids, strings.TrimSpace(reg.Descriptor.ID))
	}
	return slotIDs(ids, func() []string {
		if rt == nil || rt.RequestCoordinator == nil {
			return nil
		}
		out := make([]string, 0, len(rt.RequestCoordinator.Slots))
		for _, s := range rt.RequestCoordinator.Slots {
			if id := strings.TrimSpace(s.ID); id != "" {
				out = append(out, id)
			}
		}
		return out
	})
}

func attemptRegistrationIDs(prod ProductionOptions, rt *runtime.AccountingRuntime) []string {
	ids := make([]string, 0, len(prod.AttemptRegistrations))
	for _, reg := range prod.AttemptRegistrations {
		ids = append(ids, strings.TrimSpace(reg.Descriptor.ID))
	}
	return slotIDs(ids, func() []string {
		if rt == nil || rt.AttemptCoordinator == nil {
			return nil
		}
		out := make([]string, 0, len(rt.AttemptCoordinator.Slots))
		for _, s := range rt.AttemptCoordinator.Slots {
			if id := strings.TrimSpace(s.ID); id != "" {
				out = append(out, id)
			}
		}
		return out
	})
}
