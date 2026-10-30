package backendplugin

import "strings"

// ValidatePresence ensures counters and presence flags agree and raw JSON is bounded.
func (u UsageEvidence) ValidatePresence() error {
	check := func(v *int64, present bool) error {
		if v != nil && !present {
			return ErrInvalidInvocation
		}
		return nil
	}
	if err := check(u.InputTokens, u.Presence.InputTokens); err != nil {
		return err
	}
	if err := check(u.OutputTokens, u.Presence.OutputTokens); err != nil {
		return err
	}
	if err := check(u.CacheReadTokens, u.Presence.CacheReadTokens); err != nil {
		return err
	}
	if err := check(u.CacheWriteTokens, u.Presence.CacheWriteTokens); err != nil {
		return err
	}
	if err := check(u.ReasoningTokens, u.Presence.ReasoningTokens); err != nil {
		return err
	}
	if err := check(u.TotalTokens, u.Presence.TotalTokens); err != nil {
		return err
	}
	return u.RawUsageJSON.Validate(DefaultMaxRawJSONBytes)
}

// Validate reports finalize request completeness.
func (r FinalizeBillingRequest) Validate() error {
	if strings.TrimSpace(r.InstanceID) == "" ||
		strings.TrimSpace(r.ALegID) == "" ||
		strings.TrimSpace(r.BLegID) == "" ||
		strings.TrimSpace(r.ModelID) == "" ||
		strings.TrimSpace(r.IdempotencyKey) == "" {
		return ErrInvalidInvocation
	}
	return nil
}
