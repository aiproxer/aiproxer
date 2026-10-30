package domain

import (
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// BillingPolicy selects which reconciled usage plane should be billable.
type BillingPolicy string

const (
	PolicyBillProvider              BillingPolicy = "bill_provider"
	PolicyBillClientVisible         BillingPolicy = "bill_client_visible"
	PolicyBillProxyBillable         BillingPolicy = "bill_proxy_billable"
	PolicyProviderThenClient        BillingPolicy = "provider_then_client"
	PolicyStrictProviderRequired    BillingPolicy = "strict_provider_required"
	PolicyRequireSamePlaneExactness BillingPolicy = "require_same_plane_exactness"
)

// ConflictCode identifies reconciliation mismatches that callers may audit.
type ConflictCode string

const (
	ConflictPlaneCandidates  ConflictCode = "plane_candidates"
	ConflictTransformedUsage ConflictCode = "transformed_usage"
)

// WarningCode identifies non-fatal input quality issues.
type WarningCode string

const (
	WarningUnknownPlane  WarningCode = "unknown_plane"
	WarningUnknownSource WarningCode = "unknown_source"
	WarningZeroUsage     WarningCode = "zero_usage"
)

type Conflict struct {
	Code    ConflictCode
	Plane   aipapi.UsagePlane
	Message string
}

type Warning struct {
	Code    WarningCode
	Plane   aipapi.UsagePlane
	Message string
}

type Result struct {
	Planes           map[aipapi.UsagePlane]aipapi.ScopedUsageDelta
	BillablePlane    aipapi.UsagePlane
	BillableUsage    aipapi.ScopedUsageDelta
	Conflicts        []Conflict
	Warnings         []Warning
	Complete         bool
	IncompleteReason string
}

func (r Result) UsageForPlane(plane aipapi.UsagePlane) (aipapi.ScopedUsageDelta, bool) {
	usage, ok := r.Planes[plane]
	return usage, ok
}

// Reconcile reconciles already-collected usage candidates. Exact duplicate
// non-policy-reserved entries are counted once. Per plane, actual candidates win
// over policy reservations; reservations are additive only when no actual usage
// exists for that plane.
func Reconcile(policy BillingPolicy, entries ...aipapi.ScopedUsageDelta) Result {
	result := Result{Planes: map[aipapi.UsagePlane]aipapi.ScopedUsageDelta{}}
	seen := map[usageIdentity]struct{}{}
	candidates := map[aipapi.UsagePlane][]aipapi.ScopedUsageDelta{}

	for _, entry := range entries {
		appendWarnings(&result, entry)
		plane := entry.Accounting.Plane
		if plane == aipapi.UsagePlaneUnknown {
			continue
		}
		if entry.Accounting.Source != aipapi.UsageSourcePolicyReserved {
			id := identityFor(entry)
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
		}
		candidates[plane] = append(candidates[plane], entry)
	}

	for _, plane := range []aipapi.UsagePlane{aipapi.UsagePlaneProviderBillable, aipapi.UsagePlaneClientVisible, aipapi.UsagePlaneProxyBillable} {
		selected, ok := selectPlaneUsage(plane, candidates[plane])
		if !ok {
			continue
		}
		result.Planes[plane] = selected
		if len(candidates[plane]) > 1 {
			result.Conflicts = append(result.Conflicts, Conflict{Code: ConflictPlaneCandidates, Plane: plane, Message: "multiple candidates for usage plane"})
		}
	}

	markTransformedUsage(&result)
	selectBillableUsage(&result, policy)
	return result
}

func ReconcileEvents(policy BillingPolicy, events ...aipapi.Event) Result {
	entries := []aipapi.ScopedUsageDelta{}
	for _, ev := range events {
		if ev.Kind != aipapi.EventUsageDelta {
			continue
		}
		if len(ev.UsageScopes) > 0 {
			entries = append(entries, ev.UsageScopes...)
			continue
		}
		entries = append(entries, aipapi.ScopedUsageDelta{
			InputTokens:      ev.InputTokens,
			OutputTokens:     ev.OutputTokens,
			CacheReadTokens:  ev.CacheReadTokens,
			CacheWriteTokens: ev.CacheWriteTokens,
			ReasoningTokens:  ev.ReasoningTokens,
			TotalTokens:      ev.TotalTokens,
			UsagePresence:    ev.UsagePresence,
			Accounting:       ev.Accounting,
		})
	}
	return Reconcile(policy, entries...)
}

func selectPlaneUsage(plane aipapi.UsagePlane, entries []aipapi.ScopedUsageDelta) (aipapi.ScopedUsageDelta, bool) {
	if len(entries) == 0 {
		return aipapi.ScopedUsageDelta{}, false
	}
	actuals := make([]aipapi.ScopedUsageDelta, 0, len(entries))
	reservations := make([]aipapi.ScopedUsageDelta, 0, len(entries))
	for _, entry := range entries {
		if entry.Accounting.Source == aipapi.UsageSourcePolicyReserved {
			reservations = append(reservations, entry)
			continue
		}
		actuals = append(actuals, entry)
	}
	if len(actuals) > 0 {
		return bestByAuthority(actuals), true
	}
	out := reservations[0]
	for _, entry := range reservations[1:] {
		addUsage(&out, entry)
	}
	out.Accounting.Plane = plane
	return out, true
}

func bestByAuthority(entries []aipapi.ScopedUsageDelta) aipapi.ScopedUsageDelta {
	best := entries[0]
	bestRank := authorityRank(best.Accounting.Authority)
	for _, entry := range entries[1:] {
		if rank := authorityRank(entry.Accounting.Authority); rank > bestRank {
			best = entry
			bestRank = rank
		}
	}
	return best
}

func selectBillableUsage(result *Result, policy BillingPolicy) {
	switch policy {
	case PolicyBillProvider, PolicyRequireSamePlaneExactness:
		setBillable(result, aipapi.UsagePlaneProviderBillable)
	case PolicyStrictProviderRequired:
		setStrictProviderBillable(result)
	case PolicyBillClientVisible:
		setBillable(result, aipapi.UsagePlaneClientVisible)
	case PolicyBillProxyBillable:
		setBillable(result, aipapi.UsagePlaneProxyBillable)
	case PolicyProviderThenClient:
		if _, ok := result.Planes[aipapi.UsagePlaneProviderBillable]; ok {
			setBillable(result, aipapi.UsagePlaneProviderBillable)
		} else {
			setBillable(result, aipapi.UsagePlaneClientVisible)
		}
	default:
		result.IncompleteReason = fmt.Sprintf("unknown billing policy %q", policy)
	}
	if policy == PolicyRequireSamePlaneExactness && hasTransformedUsage(*result) {
		result.Complete = false
		result.IncompleteReason = "usage planes differ under same-plane exactness policy"
	}
}

func setStrictProviderBillable(result *Result) {
	usage, ok := result.Planes[aipapi.UsagePlaneProviderBillable]
	if !ok {
		result.Complete = false
		result.IncompleteReason = fmt.Sprintf("missing required %s usage", aipapi.UsagePlaneProviderBillable)
		return
	}
	if !isStrictProviderUsage(usage) {
		result.Complete = false
		result.IncompleteReason = "strict provider policy requires authoritative provider-reported provider_billable usage"
		return
	}
	result.BillablePlane = aipapi.UsagePlaneProviderBillable
	result.BillableUsage = usage
	result.Complete = true
}

func isStrictProviderUsage(usage aipapi.ScopedUsageDelta) bool {
	if usage.Accounting.Authority != aipapi.UsageAuthorityAuthoritative {
		return false
	}
	switch usage.Accounting.Source {
	case aipapi.UsageSourceProviderReported, aipapi.UsageSourceProviderCountAPI:
		return true
	default:
		return false
	}
}

func setBillable(result *Result, plane aipapi.UsagePlane) {
	usage, ok := result.Planes[plane]
	if !ok {
		result.Complete = false
		result.IncompleteReason = fmt.Sprintf("missing required %s usage", plane)
		return
	}
	result.BillablePlane = plane
	result.BillableUsage = usage
	result.Complete = true
}

func markTransformedUsage(result *Result) {
	provider, okProvider := result.Planes[aipapi.UsagePlaneProviderBillable]
	client, okClient := result.Planes[aipapi.UsagePlaneClientVisible]
	if !okProvider || !okClient || sameUsage(provider, client) {
		return
	}
	result.Conflicts = append(result.Conflicts, Conflict{
		Code:    ConflictTransformedUsage,
		Plane:   aipapi.UsagePlaneClientVisible,
		Message: "client-visible usage differs from provider-billable usage",
	})
}

func appendWarnings(result *Result, entry aipapi.ScopedUsageDelta) {
	if entry.Accounting.Plane == aipapi.UsagePlaneUnknown {
		result.Warnings = append(result.Warnings, Warning{Code: WarningUnknownPlane, Message: "usage plane is unknown"})
	}
	if entry.Accounting.Source == aipapi.UsageSourceUnknown {
		result.Warnings = append(result.Warnings, Warning{Code: WarningUnknownSource, Plane: entry.Accounting.Plane, Message: "usage source is unknown"})
	}
	if zeroUsage(entry) {
		result.Warnings = append(result.Warnings, Warning{Code: WarningZeroUsage, Plane: entry.Accounting.Plane, Message: "usage token totals are zero"})
	}
}

func authorityRank(authority aipapi.UsageAuthority) int {
	switch authority {
	case aipapi.UsageAuthorityAuthoritative:
		return 5
	case aipapi.UsageAuthorityDelegated:
		return 4
	case aipapi.UsageAuthorityEstimated:
		return 3
	case aipapi.UsageAuthorityAdvisory:
		return 2
	case aipapi.UsageAuthorityUnavailable:
		return 1
	default:
		return 0
	}
}

func addUsage(dst *aipapi.ScopedUsageDelta, src aipapi.ScopedUsageDelta) {
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.CacheReadTokens += src.CacheReadTokens
	dst.CacheWriteTokens += src.CacheWriteTokens
	dst.ReasoningTokens += src.ReasoningTokens
	dst.TotalTokens += src.TotalTokens
	dst.UsagePresence = dst.UsagePresence.Union(src.UsagePresence)
}

func sameUsage(a, b aipapi.ScopedUsageDelta) bool {
	return a.InputTokens == b.InputTokens &&
		a.OutputTokens == b.OutputTokens &&
		a.CacheReadTokens == b.CacheReadTokens &&
		a.CacheWriteTokens == b.CacheWriteTokens &&
		a.ReasoningTokens == b.ReasoningTokens &&
		a.TotalTokens == b.TotalTokens
}

func zeroUsage(entry aipapi.ScopedUsageDelta) bool {
	return entry.InputTokens == 0 && entry.OutputTokens == 0 && entry.CacheReadTokens == 0 &&
		entry.CacheWriteTokens == 0 && entry.ReasoningTokens == 0 && entry.TotalTokens == 0
}

func hasTransformedUsage(result Result) bool {
	for _, conflict := range result.Conflicts {
		if conflict.Code == ConflictTransformedUsage {
			return true
		}
	}
	return false
}

type usageIdentity struct {
	Plane            aipapi.UsagePlane
	Source           aipapi.UsageSource
	Authority        aipapi.UsageAuthority
	Tokenizer        aipapi.TokenizerRef
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int
	TotalTokens      int
	UsagePresence    aipapi.UsagePresence
}

func identityFor(entry aipapi.ScopedUsageDelta) usageIdentity {
	return usageIdentity{
		Plane:            entry.Accounting.Plane,
		Source:           entry.Accounting.Source,
		Authority:        entry.Accounting.Authority,
		Tokenizer:        entry.Accounting.Tokenizer,
		InputTokens:      entry.InputTokens,
		OutputTokens:     entry.OutputTokens,
		CacheReadTokens:  entry.CacheReadTokens,
		CacheWriteTokens: entry.CacheWriteTokens,
		ReasoningTokens:  entry.ReasoningTokens,
		TotalTokens:      entry.TotalTokens,
		UsagePresence:    entry.UsagePresence,
	}
}
