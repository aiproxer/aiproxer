package billingport

import (
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// The provider-maintenance attachment is an inert carriage port: the runtime's
// keep-warm accounting hook hands immutable provider-reported usage to one
// observer, and the durable owner is supplied by the binding. These are
// aliases of the canonical public definitions, so the runtime port, the public
// binding field, and the Enterprise durable observer share one type identity
// with zero conversions and unchanged fingerprints/byte layout.
//
// The canonical definitions live in the neutral public metering leaf, not in
// the public billing package: the money core reaches the maintenance evidence
// through this port, and the billing package is free to depend on the request
// types without dragging the wire types into the money core.
type (
	ProviderMaintenanceUsage             = sdkmetering.MaintenanceUsage
	ProviderMaintenanceEvidence          = sdkmetering.MaintenanceEvidence
	ProviderMaintenanceQuantity          = sdkmetering.MaintenanceQuantity
	ProviderMaintenanceMoney             = sdkmetering.MaintenanceMoney
	ProviderMaintenanceSource            = sdkmetering.MaintenanceSource
	ProviderMaintenanceAuthority         = sdkmetering.MaintenanceAuthority
	ProviderMaintenanceUsageObserver     = sdkmetering.MaintenanceObserver
	ProviderMaintenanceUsageObserverFunc = sdkmetering.MaintenanceObserverFunc
)

const (
	ProviderMaintenanceSourceProviderReported = sdkmetering.MaintenanceSourceProviderReported
	ProviderMaintenanceSourceProviderCountAPI = sdkmetering.MaintenanceSourceProviderCountAPI
	ProviderMaintenanceSourceLocalTokenizer   = sdkmetering.MaintenanceSourceLocalTokenizer
	ProviderMaintenanceSourceLocalEstimator   = sdkmetering.MaintenanceSourceLocalEstimator
	ProviderMaintenanceSourceUnavailable      = sdkmetering.MaintenanceSourceUnavailable

	ProviderMaintenanceAuthorityAuthoritative = sdkmetering.MaintenanceAuthorityAuthoritative
	ProviderMaintenanceAuthorityDelegated     = sdkmetering.MaintenanceAuthorityDelegated
	ProviderMaintenanceAuthorityEstimated     = sdkmetering.MaintenanceAuthorityEstimated
	ProviderMaintenanceAuthorityUnavailable   = sdkmetering.MaintenanceAuthorityUnavailable
)
