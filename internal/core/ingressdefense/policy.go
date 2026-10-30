package ingressdefense

import (
	"fmt"
	"net/netip"
	"time"
)

// Reason is a finite denial or transition classification suitable for bounded
// metric labels. The set is closed: a reason never carries a request path,
// source address, prefix text, credential, principal, header, User-Agent, or
// any other attacker-controlled value.
type Reason string

const (
	// ReasonImpossiblePath classifies a deterministic impossible-path rejection.
	ReasonImpossiblePath Reason = "impossible_path"
	// ReasonAuthFailureThreshold classifies quarantine started or escalated by
	// the authentication-failure threshold being reached inside the window.
	ReasonAuthFailureThreshold Reason = "auth_failure_threshold"
	// ReasonActiveQuarantine classifies refusal while a quarantine is active.
	ReasonActiveQuarantine Reason = "active_quarantine"
	// ReasonClientIPError classifies a request whose source address could not be
	// resolved into a usable identity.
	ReasonClientIPError Reason = "client_ip_error"
)

// AllReasons returns the closed reason set in stable metric-label order.
func AllReasons() []Reason {
	return []Reason{
		ReasonImpossiblePath,
		ReasonAuthFailureThreshold,
		ReasonActiveQuarantine,
		ReasonClientIPError,
	}
}

// Policy is the immutable, provider- and protocol-neutral request policy of one
// generation. It carries only bounded scalars plus the read-only adaptive
// exemption allowlist, and holds no source state, no credential, no request
// content, and no aggregate network policy. Callers must treat a compiled value
// as read-only; the state machine never mutates it.
type Policy struct {
	// Enabled reports whether self-defense request enforcement runs.
	Enabled bool
	// AuthFailures is the counted unauthenticated-failure threshold that starts
	// or escalates a quarantine when reached inside FailureWindow.
	AuthFailures int
	// FailureWindow bounds the counted authentication failures of one source.
	FailureWindow time.Duration
	// InitialQuarantine is the first-offense duration that later offenses grow
	// from exponentially.
	InitialQuarantine time.Duration
	// MaxQuarantine is the ceiling that the exponential growth saturates at.
	MaxQuarantine time.Duration
	// AdaptiveExemptCIDRs is the read-only allowlist of sources exempt from
	// adaptive state mutation and quarantine. It is never a ban list, it never
	// overrides fixed network policy, and it never creates prefix-keyed state.
	AdaptiveExemptCIDRs []netip.Prefix
}

// Validate reports whether the policy satisfies the bounded domain invariants
// the state machine depends on: a finite positive threshold and window, a
// positive first-offense duration, a ceiling that is not below it, and a
// normalized exemption allowlist. It applies to a filled, enabled policy: the
// zero value a nil CompiledSelfDefense projects is not a valid request policy and
// the state machine records nothing for a disabled one.
func (p Policy) Validate() error {
	if p.AuthFailures < 1 {
		return fmt.Errorf("ingressdefense: policy.auth_failures: must be at least 1, got %d", p.AuthFailures)
	}
	if p.FailureWindow <= 0 {
		return fmt.Errorf("ingressdefense: policy.failure_window: must be positive, got %s", p.FailureWindow)
	}
	if p.InitialQuarantine <= 0 {
		return fmt.Errorf("ingressdefense: policy.initial_quarantine: must be positive, got %s", p.InitialQuarantine)
	}
	if p.MaxQuarantine <= 0 {
		return fmt.Errorf("ingressdefense: policy.max_quarantine: must be positive, got %s", p.MaxQuarantine)
	}
	if p.MaxQuarantine < p.InitialQuarantine {
		return fmt.Errorf("ingressdefense: policy.max_quarantine: must be at least initial_quarantine (%s), got %s",
			p.InitialQuarantine, p.MaxQuarantine)
	}
	for i, prefix := range p.AdaptiveExemptCIDRs {
		if !prefix.IsValid() {
			return fmt.Errorf("ingressdefense: policy.exempt_cidrs[%d]: invalid prefix %v", i, prefix)
		}
		if prefix.Addr().Is4In6() {
			return fmt.Errorf("ingressdefense: policy.exempt_cidrs[%d]: must be unmapped, got %v", i, prefix)
		}
		if prefix != prefix.Masked() {
			return fmt.Errorf("ingressdefense: policy.exempt_cidrs[%d]: must be masked, got %v want %v", i, prefix, prefix.Masked())
		}
	}
	return nil
}

// AdaptiveExempt reports whether addr falls inside the adaptive exemption
// allowlist. Exemption suppresses adaptive state mutation and quarantine only:
// it never overrides fixed network policy and never makes an impossible-path
// request valid.
func (p Policy) AdaptiveExempt(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range p.AdaptiveExemptCIDRs {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// StateLimits size the process-owned adaptive state. They are restart-required
// in v1 because one store is shared by every generation, so they are kept out
// of Policy and must never be projected into a request generation.
type StateLimits struct {
	// MaxEntries is the hard cap on tracked source entries. The state must stay
	// bounded at it through deterministic eviction or replacement.
	MaxEntries int
	// StateTTL is the hostile-inactivity lifetime after which one source entry
	// and its accumulated offense level expire.
	StateTTL time.Duration
}

// Validate reports whether the limits bound the process state: a positive
// capacity and a positive inactivity TTL.
func (l StateLimits) Validate() error {
	if l.MaxEntries < 1 {
		return fmt.Errorf("ingressdefense: state_limits.max_entries: must be at least 1, got %d", l.MaxEntries)
	}
	if l.StateTTL <= 0 {
		return fmt.Errorf("ingressdefense: state_limits.state_ttl: must be positive, got %s", l.StateTTL)
	}
	return nil
}

// Transition is the result of one recorded hostile event for one exact source
// address. It is the only value that crosses into HTTP/auth adapters and
// metrics, so it deliberately omits the offense level and any score: a
// quarantine denial must not disclose them.
type Transition struct {
	// QuarantineStarted reports whether the event started or extended an active
	// quarantine for the source.
	QuarantineStarted bool
	// QuarantineUntil is the absolute quarantine deadline, zero when no
	// quarantine is active for the source.
	QuarantineUntil time.Time
	// Reason is the closed classification of the recorded event.
	Reason Reason
	// EntryCount is the number of tracked source entries after the event.
	EntryCount int
}
