package domain

import "time"

// RuleMode is strict (deny at capacity) or advisory (allow with advisory outcome).
type RuleMode string

const (
	RuleModeStrict   RuleMode = "strict"
	RuleModeAdvisory RuleMode = "advisory"
)

// IsKnown reports whether m is a documented rule mode.
func (m RuleMode) IsKnown() bool {
	return m == RuleModeStrict || m == RuleModeAdvisory
}

// FailureBehavior classifies post-admission / infrastructure failure posture.
type FailureBehavior string

const (
	FailureBehaviorDefault    FailureBehavior = ""
	FailureBehaviorFailClosed FailureBehavior = "fail_closed"
	FailureBehaviorFailOpen   FailureBehavior = "fail_open"
)

// IsKnown reports whether b is a documented failure behavior.
func (b FailureBehavior) IsKnown() bool {
	switch b {
	case FailureBehaviorDefault, FailureBehaviorFailClosed, FailureBehaviorFailOpen:
		return true
	default:
		return false
	}
}

// AuxPolicy controls whether auxiliary requests inherit the parent lease.
type AuxPolicy string

const (
	// AuxPolicyInherit (default / empty) reuses the parent lease and does not
	// consume an additional top-level principal slot (requirement 10.10).
	AuxPolicyInherit AuxPolicy = ""
	// AuxPolicyInheritExplicit is the explicit form of inherit.
	AuxPolicyInheritExplicit AuxPolicy = "inherit"
	// AuxPolicyAcquireOwn forces a distinct top-level lease for the auxiliary call.
	AuxPolicyAcquireOwn AuxPolicy = "acquire_own"
)

// InheritsParent reports whether aux policy defaults to parent inheritance.
func (p AuxPolicy) InheritsParent() bool {
	return p == AuxPolicyInherit || p == AuxPolicyInheritExplicit
}

// Rule is one maximum-active-logical-request concurrency rule.
type Rule struct {
	ID              string
	Namespace       string
	Version         string
	Mode            RuleMode
	Limit           int
	Match           DimensionsMatcher
	LeaseTTL        time.Duration
	RenewBefore     time.Duration
	FailureBehavior FailureBehavior
}

// Matches reports whether the rule's safe dimension matcher accepts actual.
func (r Rule) Matches(actual Dimensions) bool {
	return r.Match.Matches(actual)
}

// EffectiveTTL returns the lease TTL, defaulting to one minute when unset.
func (r Rule) EffectiveTTL() time.Duration {
	if r.LeaseTTL > 0 {
		return r.LeaseTTL
	}
	return time.Minute
}

// EffectiveRenewBefore returns the renew-before offset, defaulting to 15s.
func (r Rule) EffectiveRenewBefore() time.Duration {
	if r.RenewBefore > 0 {
		return r.RenewBefore
	}
	return 15 * time.Second
}

// ValidateTiming checks configured lease_ttl and renew_before (requirement 10.1).
func (r Rule) ValidateTiming() error {
	return ValidateTiming(r.EffectiveTTL(), r.EffectiveRenewBefore())
}
