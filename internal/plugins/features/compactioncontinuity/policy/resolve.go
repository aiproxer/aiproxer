package policy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	LabelPrefix           = "compaction_continuity."
	LabelEnabled          = LabelPrefix + "enabled"
	LabelRoute            = LabelPrefix + "extractor.route"
	LabelInherit          = LabelPrefix + "extractor.inherit"
	LabelTimeout          = LabelPrefix + "extractor.timeout"
	LabelMaxInputTokens   = LabelPrefix + "extractor.max_input_tokens"
	LabelMaxOutputTokens  = LabelPrefix + "extractor.max_output_tokens"
	LabelBarrierTimeout   = LabelPrefix + "barrier.timeout"
	LabelCapsuleMaxTokens = LabelPrefix + "capsule.max_tokens"
	LabelCapsuleMaxBytes  = LabelPrefix + "capsule.max_bytes"
	LabelSourceMaxBytes   = LabelPrefix + "source.max_bytes"
	LabelResultMaxBytes   = LabelPrefix + "result.max_bytes"
	LabelResultMaxCount   = LabelPrefix + "result.max_count"
	LabelPlan             = LabelPrefix + "preserve.plan"
	LabelUserDecisions    = LabelPrefix + "preserve.user_decisions"
	LabelConstraints      = LabelPrefix + "preserve.constraints"
	LabelRationale        = LabelPrefix + "preserve.rationale"
	LabelRejected         = LabelPrefix + "preserve.rejected_alternatives"
)

var (
	ErrInvalidDefaults = errors.New("continuity policy: invalid defaults")
	ErrUnapprovedRoute = errors.New("continuity policy: default extractor route is not approved")
)

type overrideContextKey struct{}

// WithTrustedOverride attaches a proxy-owned override. Callers must invoke it
// only after the secure-session/session-open policy has authenticated and
// authorized the value. Resolve still requires an authoritative session view,
// so a frontend cannot self-authorize by supplying this value on a wire path.
func WithTrustedOverride(ctx context.Context, o Override) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	if o.RouteSet && strings.TrimSpace(o.Route) != "" {
		o.routeApproved = true
	}
	return context.WithValue(ctx, overrideContextKey{}, cloneOverride(o))
}

// Resolve applies operator maxima, then trusted session values, then defaults.
// Invalid/unauthorized session values are ignored; invalid global policy is an
// error because silently selecting a different egress route is unsafe.
func Resolve(ctx context.Context, defaults Defaults, maxima HardMaxima) (Effective, error) {
	if err := validateDefaults(defaults); err != nil {
		return Effective{}, err
	}
	effective := Effective{
		Enabled:   defaults.Enabled && maxima.Enabled,
		Preserve:  intersectCategories(defaults.Preserve, maxima.Preserve),
		Extractor: defaults.Extractor,
		Limits:    defaults.Limits,
	}
	applyHardLimits(&effective, maxima)
	if !effective.Enabled {
		return effective, nil
	}

	views, trusted := trustedViews(ctx)
	effective.TrustedSession = trusted
	if trusted {
		override := overrideFromContext(ctx)
		mergeOverride(&override, views.Labels)
		applyOverride(&effective, override, maxima)
	}
	if effective.Enabled && !approvedRoute(effective.Extractor.Route, effective.Extractor.Inherit, maxima) {
		return Effective{}, ErrUnapprovedRoute
	}
	if auth, ok := TranscriptAuthorizationFromContext(ctx); ok && maxima.AllowTranscriptRead {
		effective.TranscriptRead = true
		_ = auth // authorization is available through the explicit read helper.
	}
	return effective, nil
}

func validateDefaults(d Defaults) error {
	if d.Extractor.Timeout < 0 || d.Extractor.MaxInputTokens < 0 || d.Extractor.MaxOutputTokens < 0 || d.Limits.BarrierTimeout < 0 {
		return fmt.Errorf("%w: negative extractor or barrier limit", ErrInvalidDefaults)
	}
	if d.Extractor.Route != "" && d.Extractor.Inherit {
		return fmt.Errorf("%w: route and inherit are both set", ErrInvalidDefaults)
	}
	return nil
}

func applyHardLimits(e *Effective, m HardMaxima) {
	e.Extractor.Timeout = capDuration(e.Extractor.Timeout, m.Limits.Timeout)
	e.Extractor.MaxInputTokens = capInt(e.Extractor.MaxInputTokens, m.Limits.MaxInputTokens)
	e.Extractor.MaxOutputTokens = capInt(e.Extractor.MaxOutputTokens, m.Limits.MaxOutputTokens)
	e.Limits.Timeout = e.Extractor.Timeout
	e.Limits.MaxInputTokens = e.Extractor.MaxInputTokens
	e.Limits.MaxOutputTokens = e.Extractor.MaxOutputTokens
	e.Limits.BarrierTimeout = capDuration(e.Limits.BarrierTimeout, m.Limits.BarrierTimeout)
	e.Limits.CapsuleMaxTokens = capInt(e.Limits.CapsuleMaxTokens, m.Limits.CapsuleMaxTokens)
	e.Limits.CapsuleMaxBytes = capInt(e.Limits.CapsuleMaxBytes, m.Limits.CapsuleMaxBytes)
	e.Limits.SourceMaxBytes = capInt(e.Limits.SourceMaxBytes, m.Limits.SourceMaxBytes)
	e.Limits.ResultMaxBytes = capInt(e.Limits.ResultMaxBytes, m.Limits.ResultMaxBytes)
	e.Limits.ResultMaxCount = capInt(e.Limits.ResultMaxCount, m.Limits.ResultMaxCount)
}

func applyOverride(e *Effective, o Override, m HardMaxima) {
	if o.Enabled != nil && !*o.Enabled {
		e.Enabled = false
	}
	if o.Preserve != nil {
		e.Preserve = intersectCategories(*o.Preserve, m.Preserve)
	}
	if o.PreservePatch != nil {
		categories := e.Preserve
		applyCategoryPatch(&categories, *o.PreservePatch)
		e.Preserve = intersectCategories(categories, m.Preserve)
	}
	if o.RouteSet && o.routeApproved && !o.InheritValue() {
		route := strings.TrimSpace(o.Route)
		if route != "" && (len(m.ApprovedRoutes) == 0 || contains(m.ApprovedRoutes, route)) {
			e.Extractor.Route = route
			e.Extractor.Inherit = false
		}
	}
	if o.Inherit != nil && *o.Inherit && m.AllowInherit {
		e.Extractor.Route = ""
		e.Extractor.Inherit = true
	}
	applyLimitOverride(e, o.Limits, m)
}

func applyCategoryPatch(categories *Categories, patch CategoryPatch) {
	if patch.Plan != nil {
		categories.Plan = *patch.Plan
	}
	if patch.UserDecisions != nil {
		categories.UserDecisions = *patch.UserDecisions
	}
	if patch.Constraints != nil {
		categories.Constraints = *patch.Constraints
	}
	if patch.Rationale != nil {
		categories.Rationale = *patch.Rationale
	}
	if patch.RejectedAlternatives != nil {
		categories.RejectedAlternatives = *patch.RejectedAlternatives
	}
}

func (o Override) InheritValue() bool { return o.Inherit != nil && *o.Inherit }

func applyLimitOverride(e *Effective, o LimitOverride, m HardMaxima) {
	e.Extractor.Timeout = tighterDuration(e.Extractor.Timeout, o.Timeout, m.Limits.Timeout)
	e.Extractor.MaxInputTokens = tighterInt(e.Extractor.MaxInputTokens, o.MaxInputTokens, m.Limits.MaxInputTokens)
	e.Extractor.MaxOutputTokens = tighterInt(e.Extractor.MaxOutputTokens, o.MaxOutputTokens, m.Limits.MaxOutputTokens)
	e.Limits.Timeout = e.Extractor.Timeout
	e.Limits.MaxInputTokens = e.Extractor.MaxInputTokens
	e.Limits.MaxOutputTokens = e.Extractor.MaxOutputTokens
	e.Limits.BarrierTimeout = tighterDuration(e.Limits.BarrierTimeout, o.BarrierTimeout, m.Limits.BarrierTimeout)
	e.Limits.CapsuleMaxTokens = tighterInt(e.Limits.CapsuleMaxTokens, o.CapsuleMaxTokens, m.Limits.CapsuleMaxTokens)
	e.Limits.CapsuleMaxBytes = tighterInt(e.Limits.CapsuleMaxBytes, o.CapsuleMaxBytes, m.Limits.CapsuleMaxBytes)
	e.Limits.SourceMaxBytes = tighterInt(e.Limits.SourceMaxBytes, o.SourceMaxBytes, m.Limits.SourceMaxBytes)
	e.Limits.ResultMaxBytes = tighterInt(e.Limits.ResultMaxBytes, o.ResultMaxBytes, m.Limits.ResultMaxBytes)
	e.Limits.ResultMaxCount = tighterInt(e.Limits.ResultMaxCount, o.ResultMaxCount, m.Limits.ResultMaxCount)
}

func tighterInt(current int, proposed *int, hard int) int {
	if proposed == nil || *proposed <= 0 || (current > 0 && *proposed > current) {
		return current
	}
	return capInt(*proposed, hard)
}

func tighterDuration(current time.Duration, proposed *time.Duration, hard time.Duration) time.Duration {
	if proposed == nil || *proposed <= 0 || (current > 0 && *proposed > current) {
		return current
	}
	return capDuration(*proposed, hard)
}

func capInt(v, hard int) int {
	if hard > 0 && (v == 0 || hard < v) {
		return hard
	}
	return v
}

func capDuration(v, hard time.Duration) time.Duration {
	if hard > 0 && (v == 0 || hard < v) {
		return hard
	}
	return v
}

func intersectCategories(a, b Categories) Categories {
	return Categories{Plan: a.Plan && b.Plan, UserDecisions: a.UserDecisions && b.UserDecisions, Constraints: a.Constraints && b.Constraints, Rationale: a.Rationale && b.Rationale, RejectedAlternatives: a.RejectedAlternatives && b.RejectedAlternatives}
}

func approvedRoute(route string, inherit bool, m HardMaxima) bool {
	if inherit {
		return m.AllowInherit
	}
	if strings.TrimSpace(route) == "" {
		return false
	}
	return len(m.ApprovedRoutes) == 0 || contains(m.ApprovedRoutes, strings.TrimSpace(route))
}

func contains(values []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}
