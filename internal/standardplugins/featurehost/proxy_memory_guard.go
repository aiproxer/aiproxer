package featurehost

import (
	"context"
	"slices"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
)

// The adapter between the core's generic pre-open guard seam and the
// proxy-memory feature's own guard (design §2).
//
// The core owns the provider-open chokepoint and the content-free facts of
// the attempt; the feature owns the policy it applies. The feature package
// may not import the core and the composition root may not import the
// feature, so this adapter is the one place that speaks both vocabularies.

// proxyMemoryAttemptGuard adapts the feature's pre-open guard to the core's
// generic pre-open seam.
type proxyMemoryAttemptGuard struct {
	guard proxymemory.AttemptGuard
}

// CheckAttempt maps the core's content-free attempt facts to the feature's
// guard input and applies the feature's policy.
//
// The mapping keeps the three states apart on both sides: the installation
// fact and the provider's effort-value answer never collapse into a boolean.
func (g proxyMemoryAttemptGuard) CheckAttempt(ctx context.Context, in runtime.AttemptGuardInput) error {
	if g.guard == nil {
		return nil
	}
	return g.guard.CheckAttempt(ctx, proxymemory.AttemptGuardInput{
		Role:            in.AuxiliaryRole,
		CandidateKey:    in.CandidateKey,
		Backend:         in.Backend,
		Model:           in.Model,
		BaselineRoute:   in.BaselineRoute,
		BaselineOptions: in.BaselineOptions,
		ExpectedOptions: in.ExpectedOptions,
		AttemptOptions:  in.AttemptOptions,
		Installation:    proxyMemoryInstallationState(in.Installation),
		EffortSupport:   proxyMemoryEffortSupportState(in.EffortSupport),
	})
}

// proxyMemoryInstallationState maps the core's installation fact to the
// feature's vocabulary.
func proxyMemoryInstallationState(state modelregistry.InstallationState) proxymemory.InstallationState {
	switch state {
	case modelregistry.InstallationPresent:
		return proxymemory.InstallationPresent
	case modelregistry.InstallationAbsent:
		return proxymemory.InstallationAbsent
	}
	return proxymemory.InstallationUnknown
}

// proxyMemoryEffortSupportState maps the core's provider effort-value answer
// to the feature's vocabulary.
func proxyMemoryEffortSupportState(state execbackend.EffortSupportState) proxymemory.EffortSupportState {
	switch state {
	case execbackend.EffortSupportSupported:
		return proxymemory.EffortSupportSupported
	case execbackend.EffortSupportUnsupported:
		return proxymemory.EffortSupportUnsupported
	}
	return proxymemory.EffortSupportUnknown
}

// ShouldCheckAttempt reports whether the feature governs an attempt with the
// given auxiliary role. It delegates to the feature guard's optional
// GovernsRole selector; a nil feature guard governs nothing, and a guard
// without the selector governs everything, so an unknown guard is never
// silently bypassed.
func (g proxyMemoryAttemptGuard) ShouldCheckAttempt(auxiliaryRole string) bool {
	if g.guard == nil {
		return false
	}
	type roleScoped interface {
		GovernsRole(string) bool
	}
	if scoped, ok := g.guard.(roleScoped); ok {
		return scoped.GovernsRole(auxiliaryRole)
	}
	return true
}

// CheckNegotiatedDowngrade adapts the core's pre-apply downgrade facts to the
// feature's own guard extension.
//
// The mapping keeps the feature scope: the feature's implementation refuses
// only the auxiliary roles of its governed phases, so every other call keeps
// the runtime's existing behaviour.
func (g proxyMemoryAttemptGuard) CheckNegotiatedDowngrade(ctx context.Context, in runtime.AttemptDowngradeInput) error {
	guard, ok := g.guard.(proxymemory.NegotiatedDowngradeGuard)
	if !ok {
		return nil
	}
	return guard.CheckNegotiatedDowngrade(ctx, proxymemory.NegotiatedDowngradeInput{
		Role:                    in.AuxiliaryRole,
		CandidateKey:            in.CandidateKey,
		Backend:                 in.Backend,
		Model:                   in.Model,
		Downgraded:              slices.Clone(in.Downgraded),
		ReasoningEffort:         in.ReasoningEffort,
		ExpectedReasoningEffort: in.ExpectedReasoningEffort,
	})
}

var (
	_ runtime.AttemptGuard          = proxyMemoryAttemptGuard{}
	_ runtime.AttemptDowngradeGuard = proxyMemoryAttemptGuard{}
	_ runtime.AttemptGuardRoleScope = proxyMemoryAttemptGuard{}
)
