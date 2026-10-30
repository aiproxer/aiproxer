package billing

import (
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// The provider-maintenance vocabulary is owned by the neutral public metering
// leaf (pkg/aipsdk/metering). It is a usage-evidence contract, not a monetary
// binding contract, and the money core reaches the maintenance evidence
// through that leaf. This package keeps the historical public names as type
// aliases, so every existing consumer keeps one type identity, one
// fingerprint, one error value and one durable byte layout.
var (
	// ErrInvalidMaintenance is the canonical invalid-record sentinel for the
	// provider-maintenance DTO and for the carved usage-evidence vocabulary
	// (internal/core/usageevidence aliases it as ErrInvalidRecord).
	ErrInvalidMaintenance = sdkmetering.ErrInvalidMaintenance
)

type (
	// MaintenanceQuantity is one provider-reported token quantity with
	// explicit presence.
	MaintenanceQuantity = sdkmetering.MaintenanceQuantity
	// MaintenanceMoney is provider-reported cost with explicit presence.
	MaintenanceMoney = sdkmetering.MaintenanceMoney
	// MaintenanceSource names where a maintenance quantity came from.
	MaintenanceSource = sdkmetering.MaintenanceSource
	// MaintenanceAuthority names the trust level of a maintenance quantity.
	MaintenanceAuthority = sdkmetering.MaintenanceAuthority
	// MaintenanceEvidence is the complete scalar evidence vocabulary of one
	// provider-maintenance observation.
	MaintenanceEvidence = sdkmetering.MaintenanceEvidence
	// MaintenanceUsage is provider-authoritative usage from a control-plane
	// maintenance call.
	MaintenanceUsage = sdkmetering.MaintenanceUsage
	// MaintenanceObserver delivers immutable maintenance evidence to its
	// durable owner.
	MaintenanceObserver = sdkmetering.MaintenanceObserver
	// MaintenanceObserverFunc adapts a function to MaintenanceObserver.
	MaintenanceObserverFunc = sdkmetering.MaintenanceObserverFunc
)

const (
	MaintenanceSourceUnknown          = sdkmetering.MaintenanceSourceUnknown
	MaintenanceSourceProviderReported = sdkmetering.MaintenanceSourceProviderReported
	MaintenanceSourceProviderCountAPI = sdkmetering.MaintenanceSourceProviderCountAPI
	MaintenanceSourceLocalTokenizer   = sdkmetering.MaintenanceSourceLocalTokenizer
	MaintenanceSourceLocalEstimator   = sdkmetering.MaintenanceSourceLocalEstimator
	MaintenanceSourceUnavailable      = sdkmetering.MaintenanceSourceUnavailable

	MaintenanceAuthorityUnknown       = sdkmetering.MaintenanceAuthorityUnknown
	MaintenanceAuthorityAuthoritative = sdkmetering.MaintenanceAuthorityAuthoritative
	MaintenanceAuthorityDelegated     = sdkmetering.MaintenanceAuthorityDelegated
	MaintenanceAuthorityEstimated     = sdkmetering.MaintenanceAuthorityEstimated
	MaintenanceAuthorityUnavailable   = sdkmetering.MaintenanceAuthorityUnavailable
)

// compile-time proof that the aliased observer keeps the historical method
// shape at this package boundary.
var _ MaintenanceObserver = MaintenanceObserverFunc(nil)
