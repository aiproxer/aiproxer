package aipruntime

import (
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
)

// EnterpriseCapability names a capability that only an Enterprise composition
// provides: billing admits accounting.pricing and accounting.billing
// configuration, usage-limits admits accounting.authority.rules.
type EnterpriseCapability string

const (
	// CapabilityBilling is the monetary billing composition capability.
	CapabilityBilling EnterpriseCapability = "billing"
	// CapabilityUsageLimits is the budget and limit enforcement capability.
	CapabilityUsageLimits EnterpriseCapability = "usage-limits"
)

// AdminMount is one operator handler for a protected admin path: a public
// alias of the runtimebundle seam (mounted by task 4.2).
type AdminMount = runtimebundle.AdminMount
