package backendplugin

import (
	"slices"
	"strings"
)

// AccountingEvidenceNegotiated reports whether the V1 host-only sideband is
// available for the current session. It is intentionally separate from the
// typed V2 negotiation gate: V1 carries only the six legacy token counters.
func AccountingEvidenceNegotiated(neg Negotiation) bool {
	return neg.Compatible && neg.NegotiatedMinor >= ProtocolMinorAccountingEvidence && slices.Contains(neg.EnabledFeatures, FeatureAccountingEvidence)
}

// ValidateAccountingEvidence validates the bounded, provider-billable sideband
// payload. Presence is authoritative: a nil counter is omitted, not zero.
func ValidateAccountingEvidence(e AccountingEvidence) error {
	if e.Plane != AccountingPlaneProviderBillable || e.Source == AccountingSourceUnknown || e.Authority == AccountingAuthorityUnknown {
		return ErrInvalidFrame
	}
	if strings.TrimSpace(e.DedupeKey) == "" || len(e.DedupeKey) > int(DefaultMaxAccountingDedupeKeyBytes) {
		return ErrInvalidFrame
	}
	values := []*int64{e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheWriteTokens, e.ReasoningTokens, e.TotalTokens}
	present := []bool{e.Presence.InputTokens, e.Presence.OutputTokens, e.Presence.CacheReadTokens, e.Presence.CacheWriteTokens, e.Presence.ReasoningTokens, e.Presence.TotalTokens}
	any := false
	for i, value := range values {
		if value != nil && *value < 0 {
			return ErrInvalidFrame
		}
		if present[i] != (value != nil) {
			return ErrInvalidFrame
		}
		any = any || present[i]
	}
	if !any {
		return ErrInvalidFrame
	}
	return nil
}
