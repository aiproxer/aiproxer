package aipruntime

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
)

// normalizedProduction is the defensively copied descriptor-bound registration set.
type normalizedProduction struct {
	RequestRegistrations     []authority.RequestRegistration
	AttemptRegistrations     []authority.AttemptRegistration
	ConcurrencyRegistration  *authority.ConcurrencyRegistration
	FeatureHostRegistrations []featurehost.Registration
	EnterpriseCapabilities   config.CapabilitySet
	AdminMounts              []AdminMount
}

// normalizeCanonicalOptions accepts descriptor-bound registrations only.
// implyBilling declares the billing capability for the monetary entry point.
func normalizeCanonicalOptions(opts Options, implyBilling bool) (normalizedProduction, error) {
	if isNilValue(opts.UsageLimits) && opts.UsageLimits != nil {
		return normalizedProduction{}, fmt.Errorf("aipruntime: Options.UsageLimits is typed-nil")
	}
	out := normalizedProduction{
		RequestRegistrations:     append([]authority.RequestRegistration(nil), opts.RequestRegistrations...),
		AttemptRegistrations:     append([]authority.AttemptRegistration(nil), opts.AttemptRegistrations...),
		ConcurrencyRegistration:  opts.ConcurrencyRegistration,
		FeatureHostRegistrations: append([]featurehost.Registration(nil), opts.FeatureHostRegistrations...),
		EnterpriseCapabilities:   capabilitySet(opts.EnterpriseCapabilities, implyBilling),
		AdminMounts:              append([]AdminMount(nil), opts.AdminMounts...),
	}
	if len(out.FeatureHostRegistrations) > 0 {
		if err := featurehost.Validate(out.FeatureHostRegistrations); err != nil {
			return normalizedProduction{}, fmt.Errorf("aipruntime: %w", err)
		}
	}
	if err := validateRegistrationSets(out); err != nil {
		return normalizedProduction{}, err
	}
	return out, nil
}

// capabilitySet builds the config validation input from the declared
// capabilities; blank names are ignored and unknown names travel as opaque
// keys. The map is built fresh, so callers cannot widen policy after Build.
func capabilitySet(caps []EnterpriseCapability, implyBilling bool) config.CapabilitySet {
	out := make(config.CapabilitySet, len(caps)+1)
	for _, c := range caps {
		if name := strings.TrimSpace(string(c)); name != "" {
			out[name] = true
		}
	}
	if implyBilling {
		out[string(CapabilityBilling)] = true
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func validateRegistrationSets(n normalizedProduction) error {
	seenReq := make(map[string]struct{}, len(n.RequestRegistrations))
	for i, reg := range n.RequestRegistrations {
		if err := reg.Validate(); err != nil {
			return fmt.Errorf("aipruntime: request_registrations[%d]: %w", i, err)
		}
		id := strings.TrimSpace(reg.Descriptor.ID)
		if _, dup := seenReq[id]; dup {
			return fmt.Errorf("aipruntime: duplicate request registration id %q", id)
		}
		seenReq[id] = struct{}{}
	}
	seenAtt := make(map[string]struct{}, len(n.AttemptRegistrations))
	for i, reg := range n.AttemptRegistrations {
		if err := reg.Validate(); err != nil {
			return fmt.Errorf("aipruntime: attempt_registrations[%d]: %w", i, err)
		}
		id := strings.TrimSpace(reg.Descriptor.ID)
		if _, dup := seenAtt[id]; dup {
			return fmt.Errorf("aipruntime: duplicate attempt registration id %q", id)
		}
		seenAtt[id] = struct{}{}
	}
	if n.ConcurrencyRegistration != nil {
		if err := n.ConcurrencyRegistration.Validate(); err != nil {
			return fmt.Errorf("aipruntime: concurrency_registration: %w", err)
		}
	}
	return rejectOverlappingRegistrationStages(n)
}

func rejectOverlappingRegistrationStages(n normalizedProduction) error {
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
				return fmt.Errorf("aipruntime: duplicate provider id %q for overlapping stage %q", id, p.Stage)
			}
			seen[p.Stage] = struct{}{}
		}
		return nil
	}
	for _, reg := range n.RequestRegistrations {
		if err := claim(reg.Descriptor.ID, reg.Descriptor.Postures); err != nil {
			return err
		}
	}
	for _, reg := range n.AttemptRegistrations {
		if err := claim(reg.Descriptor.ID, reg.Descriptor.Postures); err != nil {
			return err
		}
	}
	if n.ConcurrencyRegistration != nil {
		if err := claim(n.ConcurrencyRegistration.Descriptor.ID, n.ConcurrencyRegistration.Descriptor.Postures); err != nil {
			return err
		}
	}
	return nil
}
