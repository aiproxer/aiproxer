package runtimebundle

import (
	"context"
	"fmt"
)

// externalBillingBindingConfigured reports whether production carries a
// complete external monetary binding: the same runtime chokepoints as the
// internal reference composition (cheap credit screen, atomic exposure
// admission, terminal evidence sink, billing identity) but no internal
// durable store. Durability stays owned by the binding behind its terminal
// acknowledgement.
func externalBillingBindingConfigured(prod ProductionOptions) bool {
	return prod.BillingCreditGate != nil &&
		prod.BillingExposureAdmission != nil &&
		prod.BillingTerminalUsageSink != nil &&
		prod.BillingIdentity.AccountID != nil
}

// billingCompositionConfigured reports whether production carries any
// monetary port. A partial binding still counts, so billed-deployment guards
// run for it too.
func billingCompositionConfigured(prod ProductionOptions) bool {
	return prod.BillingCreditGate != nil ||
		prod.BillingExposureAdmission != nil ||
		prod.BillingTerminalUsageSink != nil ||
		prod.BillingIdentity.AccountID != nil
}

// configureExternalBillingLifecycle runs once per process build. It snapshots
// the caller-owned admin registrations at the process boundary and, when a
// complete public binding is present, starts the binding-owned resources once
// and registers their close with process cleanup. A reload reuses the same
// ProcessServices, so neither the snapshot nor the start/close runs twice.
func configureExternalBillingLifecycle(owner *processResourceOwner, opts *BuildOptions) error {
	if opts == nil {
		return fmt.Errorf("runtimebundle: external billing lifecycle requires build options")
	}
	// Snapshot caller-owned admin registrations once at the process boundary.
	opts.Production.AdminMounts = append([]AdminMount(nil), opts.Production.AdminMounts...)
	if !externalBillingBindingConfigured(opts.Production) {
		return nil
	}
	return configureExternalBilling(owner, opts.Production)
}

// configureExternalBilling starts binding-owned resources once and registers
// their close with process cleanup. It runs during process build, so a start
// failure fails the build before any generation is published and the active
// generation is never replaced. Borrowed handles are declared only and are
// never closed. The sink is structural: any terminal sink carrying the owned
// starter composes, with no import of the concrete binding package.
func configureExternalBilling(owner *processResourceOwner, prod ProductionOptions) error {
	starter, ok := prod.BillingTerminalUsageSink.(interface {
		StartOwnedResources(context.Context) error
	})
	if !ok || starter == nil {
		return fmt.Errorf("%w: external billing sink cannot start owned resources", ErrAuthoritativeBillingRequired)
	}
	if err := starter.StartOwnedResources(context.Background()); err != nil {
		return err
	}
	if closer, ok := prod.BillingTerminalUsageSink.(interface{ Close() error }); ok && closer != nil {
		owner.Own(func() error { return closer.Close() })
	}
	return nil
}
