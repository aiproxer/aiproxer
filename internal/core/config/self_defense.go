package config

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
)

const (
	defaultSelfDefenseAuthFailures      = 5
	defaultSelfDefenseWindow            = time.Minute
	defaultSelfDefenseInitialQuarantine = time.Minute
	defaultSelfDefenseMaxQuarantine     = 2 * time.Hour
	defaultSelfDefenseStateTTL          = 24 * time.Hour
	defaultSelfDefenseMaxEntries        = 100000

	minSelfDefenseAuthFailures = 2
	maxSelfDefenseAuthFailures = 100
	minSelfDefenseWindow       = time.Second
	maxSelfDefenseWindow       = time.Hour
	minSelfDefenseInitial      = time.Second
	maxSelfDefenseInitial      = time.Hour
	maxSelfDefenseQuarantine   = 24 * time.Hour
	minSelfDefenseStateTTL     = time.Minute
	maxSelfDefenseStateTTL     = 7 * 24 * time.Hour
	minSelfDefenseMaxEntries   = 1024
	maxSelfDefenseMaxEntries   = 1_000_000
)

// CompiledSelfDefense is the immutable, provider-neutral generation projection
// produced by pure config compilation. It carries no mutable process state and
// performs no I/O.
type CompiledSelfDefense struct {
	enabled           bool
	impossiblePaths   bool
	authFailures      int
	failureWindow     time.Duration
	initialQuarantine time.Duration
	maxQuarantine     time.Duration
	stateTTL          time.Duration
	maxEntries        int
	exemptCIDRs       []netip.Prefix
}

// CompileSelfDefense validates the typed self-defense configuration and resolves
// documented defaults without constructing process state, binding a listener,
// or performing network I/O.
func CompileSelfDefense(in SelfDefenseConfig) (*CompiledSelfDefense, error) {
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	impossiblePaths := true
	if in.ImpossiblePaths != nil {
		impossiblePaths = *in.ImpossiblePaths
	}

	authFailures := defaultSelfDefenseAuthFailures
	if in.Adaptive.AuthFailures != nil {
		authFailures = *in.Adaptive.AuthFailures
	}
	if authFailures < minSelfDefenseAuthFailures || authFailures > maxSelfDefenseAuthFailures {
		return nil, fmt.Errorf("access.self_defense.adaptive.auth_failures: must be between %d and %d, got %d",
			minSelfDefenseAuthFailures, maxSelfDefenseAuthFailures, authFailures)
	}

	failureWindow, err := parsePositiveDurationOrDefault(in.Adaptive.Window, defaultSelfDefenseWindow, "access.self_defense.adaptive.window")
	if err != nil {
		return nil, err
	}
	if failureWindow < minSelfDefenseWindow || failureWindow > maxSelfDefenseWindow {
		return nil, fmt.Errorf("access.self_defense.adaptive.window: must be between %s and %s, got %s",
			minSelfDefenseWindow, maxSelfDefenseWindow, failureWindow)
	}

	initialQuarantine, err := parsePositiveDurationOrDefault(in.Adaptive.InitialQuarantine, defaultSelfDefenseInitialQuarantine, "access.self_defense.adaptive.initial_quarantine")
	if err != nil {
		return nil, err
	}
	if initialQuarantine < minSelfDefenseInitial || initialQuarantine > maxSelfDefenseInitial {
		return nil, fmt.Errorf("access.self_defense.adaptive.initial_quarantine: must be between %s and %s, got %s",
			minSelfDefenseInitial, maxSelfDefenseInitial, initialQuarantine)
	}

	maxQuarantine, err := parsePositiveDurationOrDefault(in.Adaptive.MaxQuarantine, defaultSelfDefenseMaxQuarantine, "access.self_defense.adaptive.max_quarantine")
	if err != nil {
		return nil, err
	}
	if maxQuarantine > maxSelfDefenseQuarantine {
		return nil, fmt.Errorf("access.self_defense.adaptive.max_quarantine: must be at most %s, got %s", maxSelfDefenseQuarantine, maxQuarantine)
	}
	// initial_quarantine is already bounded to >= 1s above, so the cross-field
	// floor also rejects any max_quarantine below 1s without a separate bound.
	if maxQuarantine < initialQuarantine {
		return nil, fmt.Errorf("access.self_defense.adaptive.max_quarantine: must be >= initial_quarantine (%s), got %s",
			initialQuarantine, maxQuarantine)
	}

	stateTTL, err := parsePositiveDurationOrDefault(in.Adaptive.StateTTL, defaultSelfDefenseStateTTL, "access.self_defense.adaptive.state_ttl")
	if err != nil {
		return nil, err
	}
	if stateTTL < minSelfDefenseStateTTL || stateTTL > maxSelfDefenseStateTTL {
		return nil, fmt.Errorf("access.self_defense.adaptive.state_ttl: must be between %s and %s, got %s",
			minSelfDefenseStateTTL, maxSelfDefenseStateTTL, stateTTL)
	}

	maxEntries := defaultSelfDefenseMaxEntries
	if in.Adaptive.MaxEntries != nil {
		maxEntries = *in.Adaptive.MaxEntries
	}
	if maxEntries < minSelfDefenseMaxEntries || maxEntries > maxSelfDefenseMaxEntries {
		return nil, fmt.Errorf("access.self_defense.adaptive.max_entries: must be between %d and %d, got %d",
			minSelfDefenseMaxEntries, maxSelfDefenseMaxEntries, maxEntries)
	}

	exemptCIDRs, err := compilePrefixes("access.self_defense.adaptive.exempt_cidrs", in.Adaptive.ExemptCIDRs)
	if err != nil {
		return nil, err
	}

	compiled := &CompiledSelfDefense{
		enabled:           enabled,
		impossiblePaths:   impossiblePaths,
		authFailures:      authFailures,
		failureWindow:     failureWindow,
		initialQuarantine: initialQuarantine,
		maxQuarantine:     maxQuarantine,
		stateTTL:          stateTTL,
		maxEntries:        maxEntries,
		exemptCIDRs:       exemptCIDRs,
	}
	if err := compiled.Policy().Validate(); err != nil {
		return nil, fmt.Errorf("access.self_defense: %w", err)
	}
	if err := compiled.StateLimits().Validate(); err != nil {
		return nil, fmt.Errorf("access.self_defense: %w", err)
	}
	return compiled, nil
}

// Enabled reports whether request enforcement is enabled in this projection.
func (c *CompiledSelfDefense) Enabled() bool { return c != nil && c.enabled }

// ImpossiblePaths reports whether the fixed impossible-path matcher is enabled.
// The toggle stays a request-graph concern; the core policy owns only adaptive
// source-defense behavior.
func (c *CompiledSelfDefense) ImpossiblePaths() bool { return c != nil && c.impossiblePaths }

// Policy projects the compiled reloadable request policy onto the single
// authoritative core domain type. The returned value is a copy: callers cannot
// mutate the compiled projection through the returned exemption allowlist.
func (c *CompiledSelfDefense) Policy() ingressdefense.Policy {
	if c == nil {
		return ingressdefense.Policy{}
	}
	return ingressdefense.Policy{
		Enabled:             c.enabled,
		AuthFailures:        c.authFailures,
		FailureWindow:       c.failureWindow,
		InitialQuarantine:   c.initialQuarantine,
		MaxQuarantine:       c.maxQuarantine,
		AdaptiveExemptCIDRs: c.AdaptiveExemptCIDRs(),
	}
}

// StateLimits projects the process-owned adaptive-state sizing. These limits are
// restart-required in v1 and are deliberately separate from Policy so they are
// never carried into a per-request generation projection.
func (c *CompiledSelfDefense) StateLimits() ingressdefense.StateLimits {
	if c == nil {
		return ingressdefense.StateLimits{}
	}
	return ingressdefense.StateLimits{MaxEntries: c.maxEntries, StateTTL: c.stateTTL}
}

// AuthFailures returns the configured unauthenticated-401 threshold.
func (c *CompiledSelfDefense) AuthFailures() int {
	if c == nil {
		return 0
	}
	return c.authFailures
}

// FailureWindow returns the auth-failure counting window.
func (c *CompiledSelfDefense) FailureWindow() time.Duration {
	if c == nil {
		return 0
	}
	return c.failureWindow
}

// InitialQuarantine returns the first-offense quarantine duration.
func (c *CompiledSelfDefense) InitialQuarantine() time.Duration {
	if c == nil {
		return 0
	}
	return c.initialQuarantine
}

// MaxQuarantine returns the exponential quarantine ceiling.
func (c *CompiledSelfDefense) MaxQuarantine() time.Duration {
	if c == nil {
		return 0
	}
	return c.maxQuarantine
}

// StateTTL returns the hostile-inactivity lifetime used to size process state.
func (c *CompiledSelfDefense) StateTTL() time.Duration {
	if c == nil {
		return 0
	}
	return c.stateTTL
}

// MaxEntries returns the process-state entry capacity.
func (c *CompiledSelfDefense) MaxEntries() int {
	if c == nil {
		return 0
	}
	return c.maxEntries
}

// AdaptiveExemptCIDRs returns a defensive copy of the adaptive-exemption prefixes.
func (c *CompiledSelfDefense) AdaptiveExemptCIDRs() []netip.Prefix {
	if c == nil || c.exemptCIDRs == nil {
		return nil
	}
	return append([]netip.Prefix(nil), c.exemptCIDRs...)
}
