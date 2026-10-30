package compatible

import (
	"fmt"

	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

// AttemptRegistration returns a descriptor-bound attempt registration for limits.
func AttemptRegistration(limits Limits, store concurrencyapp.LeaseStore) (authority.AttemptRegistration, *Runtime, error) {
	rt, err := NewRuntime(limits, store)
	if err != nil {
		return authority.AttemptRegistration{}, nil, err
	}
	if rt == nil {
		return authority.AttemptRegistration{}, nil, nil
	}
	prov := NewAttemptProvider(rt)
	if prov == nil {
		return authority.AttemptRegistration{}, nil, nil
	}
	reg := authority.AttemptRegistration{
		Descriptor: authority.ProviderDescriptor{
			ID:   ProviderID,
			Kind: authority.ProviderKindAuthority,
			Postures: []authority.StagePosture{{
				Stage:           authority.StageAttemptAdmit,
				Strength:        authority.StrengthRequired,
				FailureBehavior: authority.FailureFailClosed,
			}, {
				Stage:           authority.StageAttemptSettle,
				Strength:        authority.StrengthRequired,
				FailureBehavior: authority.FailureFailClosed,
			}, {
				Stage:           authority.StageAttemptRelease,
				Strength:        authority.StrengthRequired,
				FailureBehavior: authority.FailureFailClosed,
			}},
		},
		Priority: authority.AttemptPriorityQuotaRate,
		Provider: prov,
	}
	if err := reg.Validate(); err != nil {
		return authority.AttemptRegistration{}, nil, fmt.Errorf("compatible admission registration: %w", err)
	}
	return reg, rt, nil
}
