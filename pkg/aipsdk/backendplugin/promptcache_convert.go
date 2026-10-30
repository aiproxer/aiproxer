package backendplugin

import (
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

var (
	promptCacheLifecycleToProto = map[promptcache.LifecycleKind]backendpluginv1.PromptCacheLifecycle{
		promptcache.LifecycleUnknown:          backendpluginv1.PromptCacheLifecycle_PROMPT_CACHE_LIFECYCLE_UNKNOWN,
		promptcache.LifecycleSlidingExpiry:    backendpluginv1.PromptCacheLifecycle_PROMPT_CACHE_LIFECYCLE_SLIDING_EXPIRY,
		promptcache.LifecycleFixedExpiry:      backendpluginv1.PromptCacheLifecycle_PROMPT_CACHE_LIFECYCLE_FIXED_EXPIRY,
		promptcache.LifecycleMinimumResidency: backendpluginv1.PromptCacheLifecycle_PROMPT_CACHE_LIFECYCLE_MINIMUM_RESIDENCY,
		promptcache.LifecycleBestEffort:       backendpluginv1.PromptCacheLifecycle_PROMPT_CACHE_LIFECYCLE_BEST_EFFORT,
	}
	promptCacheLifecycleFromProto = map[backendpluginv1.PromptCacheLifecycle]promptcache.LifecycleKind{
		backendpluginv1.PromptCacheLifecycle_PROMPT_CACHE_LIFECYCLE_UNKNOWN:           promptcache.LifecycleUnknown,
		backendpluginv1.PromptCacheLifecycle_PROMPT_CACHE_LIFECYCLE_SLIDING_EXPIRY:    promptcache.LifecycleSlidingExpiry,
		backendpluginv1.PromptCacheLifecycle_PROMPT_CACHE_LIFECYCLE_FIXED_EXPIRY:      promptcache.LifecycleFixedExpiry,
		backendpluginv1.PromptCacheLifecycle_PROMPT_CACHE_LIFECYCLE_MINIMUM_RESIDENCY: promptcache.LifecycleMinimumResidency,
		backendpluginv1.PromptCacheLifecycle_PROMPT_CACHE_LIFECYCLE_BEST_EFFORT:       promptcache.LifecycleBestEffort,
	}
	promptCacheStatusToProto = map[promptcache.RenewStatus]backendpluginv1.PromptCacheRenewStatus{
		promptcache.Renewed:       backendpluginv1.PromptCacheRenewStatus_PROMPT_CACHE_RENEW_STATUS_RENEWED,
		promptcache.StillResident: backendpluginv1.PromptCacheRenewStatus_PROMPT_CACHE_RENEW_STATUS_STILL_RESIDENT,
		promptcache.ColdRecreated: backendpluginv1.PromptCacheRenewStatus_PROMPT_CACHE_RENEW_STATUS_COLD_RECREATED,
		promptcache.Stale:         backendpluginv1.PromptCacheRenewStatus_PROMPT_CACHE_RENEW_STATUS_STALE,
		promptcache.Unsupported:   backendpluginv1.PromptCacheRenewStatus_PROMPT_CACHE_RENEW_STATUS_UNSUPPORTED,
		promptcache.ControlFailed: backendpluginv1.PromptCacheRenewStatus_PROMPT_CACHE_RENEW_STATUS_CONTROL_FAILED,
	}
	promptCacheStatusFromProto = map[backendpluginv1.PromptCacheRenewStatus]promptcache.RenewStatus{
		backendpluginv1.PromptCacheRenewStatus_PROMPT_CACHE_RENEW_STATUS_RENEWED:        promptcache.Renewed,
		backendpluginv1.PromptCacheRenewStatus_PROMPT_CACHE_RENEW_STATUS_STILL_RESIDENT: promptcache.StillResident,
		backendpluginv1.PromptCacheRenewStatus_PROMPT_CACHE_RENEW_STATUS_COLD_RECREATED: promptcache.ColdRecreated,
		backendpluginv1.PromptCacheRenewStatus_PROMPT_CACHE_RENEW_STATUS_STALE:          promptcache.Stale,
		backendpluginv1.PromptCacheRenewStatus_PROMPT_CACHE_RENEW_STATUS_UNSUPPORTED:    promptcache.Unsupported,
		backendpluginv1.PromptCacheRenewStatus_PROMPT_CACHE_RENEW_STATUS_CONTROL_FAILED: promptcache.ControlFailed,
	}
)

func PromptCacheProfileFromProto(p *backendpluginv1.PromptCacheProfile) (promptcache.Profile, error) {
	return promptCacheProfileFromProto(p)
}

func PromptCacheProfileToProto(p promptcache.Profile) (*backendpluginv1.PromptCacheProfile, error) {
	return promptCacheProfileToProto(p)
}

func promptCacheProfileFromProto(p *backendpluginv1.PromptCacheProfile) (promptcache.Profile, error) {
	if p == nil {
		return promptcache.Profile{}, nil
	}
	out := promptcache.Profile{ObservationSupported: p.GetObservationSupported(), RenewalSupported: p.GetRenewalSupported()}
	for _, kind := range p.GetLifecycleKinds() {
		value, ok := promptCacheLifecycleFromProto[kind]
		if !ok {
			return promptcache.Profile{}, ErrUnknownEnum
		}
		out.LifecycleKinds = append(out.LifecycleKinds, value)
	}
	return out.Normalize()
}

func promptCacheProfileToProto(p promptcache.Profile) (*backendpluginv1.PromptCacheProfile, error) {
	normalized, err := p.Normalize()
	if err != nil {
		return nil, err
	}
	if !normalized.ObservationSupported && !normalized.RenewalSupported && len(normalized.LifecycleKinds) == 0 {
		return nil, nil
	}
	out := &backendpluginv1.PromptCacheProfile{ObservationSupported: normalized.ObservationSupported, RenewalSupported: normalized.RenewalSupported}
	for _, kind := range normalized.LifecycleKinds {
		out.LifecycleKinds = append(out.LifecycleKinds, promptCacheLifecycleToProto[kind])
	}
	return out, nil
}

func promptCacheTimingFromProto(t *backendpluginv1.PromptCacheTiming) (promptcache.Timing, error) {
	if t == nil || t.ObservedAtUnixMs == nil {
		return promptcache.Timing{}, promptcache.ErrInvalid
	}
	out := promptcache.Timing{ObservedAt: time.UnixMilli(t.GetObservedAtUnixMs()).UTC()}
	if t.ExpiresAtUnixMs != nil {
		v := time.UnixMilli(t.GetExpiresAtUnixMs()).UTC()
		out.ExpiresAt = &v
	}
	if t.MinimumResidentUntilUnixMs != nil {
		v := time.UnixMilli(t.GetMinimumResidentUntilUnixMs()).UTC()
		out.MinimumResidentUntil = &v
	}
	if err := out.Validate(); err != nil {
		return promptcache.Timing{}, err
	}
	return out, nil
}

func promptCacheTimingToProto(t promptcache.Timing) (*backendpluginv1.PromptCacheTiming, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	observed := t.ObservedAt.UnixMilli()
	out := &backendpluginv1.PromptCacheTiming{ObservedAtUnixMs: &observed}
	if t.ExpiresAt != nil {
		v := t.ExpiresAt.UnixMilli()
		out.ExpiresAtUnixMs = &v
	}
	if t.MinimumResidentUntil != nil {
		v := t.MinimumResidentUntil.UnixMilli()
		out.MinimumResidentUntilUnixMs = &v
	}
	return out, nil
}

func PromptCacheEvidenceFromProto(e *backendpluginv1.PromptCacheEvidence) (promptcache.CacheEvidence, error) {
	return promptCacheEvidenceFromProto(e)
}

func PromptCacheEvidenceToProto(e promptcache.CacheEvidence) (*backendpluginv1.PromptCacheEvidence, error) {
	return promptCacheEvidenceToProto(e)
}

func promptCacheEvidenceFromProto(e *backendpluginv1.PromptCacheEvidence) (promptcache.CacheEvidence, error) {
	if e == nil {
		return promptcache.CacheEvidence{}, nil
	}
	out := promptcache.CacheEvidence{InputTokens: optInt64(e.InputTokens), OutputTokens: optInt64(e.OutputTokens), CacheReadTokens: optInt64(e.CacheReadTokens), CacheWriteTokens: optInt64(e.CacheWriteTokens), TotalTokens: optInt64(e.TotalTokens)}
	return out, out.Validate()
}

func promptCacheEvidenceToProto(e promptcache.CacheEvidence) (*backendpluginv1.PromptCacheEvidence, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return &backendpluginv1.PromptCacheEvidence{InputTokens: optInt64(e.InputTokens), OutputTokens: optInt64(e.OutputTokens), CacheReadTokens: optInt64(e.CacheReadTokens), CacheWriteTokens: optInt64(e.CacheWriteTokens), TotalTokens: optInt64(e.TotalTokens)}, nil
}

func PromptCacheObservationFromProto(o *backendpluginv1.PromptCacheObservation) (*promptcache.Observation, error) {
	return promptCacheObservationFromProto(o)
}

func PromptCacheObservationToProto(o *promptcache.Observation) (*backendpluginv1.PromptCacheObservation, error) {
	return promptCacheObservationToProto(o)
}

func promptCacheObservationFromProto(o *backendpluginv1.PromptCacheObservation) (*promptcache.Observation, error) {
	if o == nil {
		return nil, nil
	}
	timing, err := promptCacheTimingFromProto(o.GetTiming())
	if err != nil {
		return nil, err
	}
	evidence, err := promptCacheEvidenceFromProto(o.GetEvidence())
	if err != nil {
		return nil, err
	}
	lifecycle, ok := promptCacheLifecycleFromProto[o.GetLifecycle()]
	if !ok {
		return nil, ErrUnknownEnum
	}
	out := &promptcache.Observation{ALegID: o.GetALegId(), BLegID: o.GetBLegId(), BackendInstanceID: o.GetBackendInstanceId(), TargetID: promptcache.TargetID(o.GetTargetId()), GenerationID: promptcache.GenerationID(o.GetGenerationId()), Lifecycle: lifecycle, Timing: timing, Renewable: o.GetRenewable(), Handle: append(promptcache.Handle(nil), o.GetHandle()...), Evidence: evidence}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

func promptCacheObservationToProto(o *promptcache.Observation) (*backendpluginv1.PromptCacheObservation, error) {
	if o == nil {
		return nil, nil
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	timing, err := promptCacheTimingToProto(o.Timing)
	if err != nil {
		return nil, err
	}
	evidence, err := promptCacheEvidenceToProto(o.Evidence)
	if err != nil {
		return nil, err
	}
	lifecycle, ok := promptCacheLifecycleToProto[o.Lifecycle]
	if !ok {
		return nil, ErrUnknownEnum
	}
	return &backendpluginv1.PromptCacheObservation{ALegId: o.ALegID, BLegId: o.BLegID, BackendInstanceId: o.BackendInstanceID, TargetId: string(o.TargetID), GenerationId: string(o.GenerationID), Lifecycle: lifecycle, Timing: timing, Renewable: o.Renewable, Handle: append([]byte(nil), o.Handle...), Evidence: evidence}, nil
}

func promptCacheRenewStatusFromProto(s backendpluginv1.PromptCacheRenewStatus) (promptcache.RenewStatus, error) {
	v, ok := promptCacheStatusFromProto[s]
	if !ok {
		return "", ErrUnknownEnum
	}
	return v, nil
}

func promptCacheRenewStatusToProto(s promptcache.RenewStatus) (backendpluginv1.PromptCacheRenewStatus, error) {
	v, ok := promptCacheStatusToProto[s]
	if !ok {
		return 0, ErrUnknownEnum
	}
	return v, nil
}

func promptCacheRenewResultFromProto(p *backendpluginv1.RenewPromptCacheResponse) (promptcache.RenewResult, *AccountingEvidence, error) {
	if p == nil {
		return promptcache.RenewResult{}, nil, ErrInvalidInvocation
	}
	status, err := promptCacheRenewStatusFromProto(p.GetStatus())
	if err != nil {
		return promptcache.RenewResult{}, nil, err
	}
	observation, err := promptCacheObservationFromProto(p.GetObservation())
	if err != nil {
		return promptcache.RenewResult{}, nil, err
	}
	evidence, err := promptCacheEvidenceFromProto(p.GetEvidence())
	if err != nil {
		return promptcache.RenewResult{}, nil, err
	}
	accounting, err := accountingEvidenceFromProto(p.GetAccounting())
	if err != nil {
		return promptcache.RenewResult{}, nil, err
	}
	out := promptcache.RenewResult{Status: status, Observation: observation, Evidence: evidence}
	if err := out.Validate(); err != nil {
		return promptcache.RenewResult{}, nil, err
	}
	return out, accounting, nil
}

func PromptCacheRenewResultFromProto(p *backendpluginv1.RenewPromptCacheResponse) (promptcache.RenewResult, *AccountingEvidence, error) {
	return promptCacheRenewResultFromProto(p)
}

func PromptCacheRenewResponseFromProto(p *backendpluginv1.RenewPromptCacheResponse) (promptcache.RenewResponse, error) {
	result, accounting, err := promptCacheRenewResultFromProto(p)
	if err != nil {
		return promptcache.RenewResponse{}, err
	}
	response := promptcache.RenewResponse{Result: result, Accounting: promptCacheAccountingFromABI(accounting)}
	if err := response.Validate(); err != nil {
		return promptcache.RenewResponse{}, err
	}
	return response, nil
}

func promptCacheAccountingFromABI(e *AccountingEvidence) *promptcache.AccountingEvidence {
	if e == nil {
		return nil
	}
	return &promptcache.AccountingEvidence{InputTokens: e.InputTokens, OutputTokens: e.OutputTokens, CacheReadTokens: e.CacheReadTokens, CacheWriteTokens: e.CacheWriteTokens, ReasoningTokens: e.ReasoningTokens, TotalTokens: e.TotalTokens, Presence: e.Presence, Source: promptcache.AccountingSource(e.Source), Authority: promptcache.AccountingAuthority(e.Authority), Plane: promptcache.AccountingPlane(e.Plane), DedupeKey: e.DedupeKey}
}

func promptCacheAccountingToABI(e *promptcache.AccountingEvidence) (*AccountingEvidence, error) {
	if e == nil {
		return nil, nil
	}
	out := &AccountingEvidence{InputTokens: e.InputTokens, OutputTokens: e.OutputTokens, CacheReadTokens: e.CacheReadTokens, CacheWriteTokens: e.CacheWriteTokens, ReasoningTokens: e.ReasoningTokens, TotalTokens: e.TotalTokens, Presence: e.Presence, Source: AccountingSource(e.Source), Authority: AccountingAuthority(e.Authority), Plane: AccountingPlane(e.Plane), DedupeKey: e.DedupeKey}
	if err := ValidateAccountingEvidence(*out); err != nil {
		return nil, err
	}
	return out, nil
}

func PromptCacheRenewResponseToProto(response promptcache.RenewResponse) (*backendpluginv1.RenewPromptCacheResponse, error) {
	accounting, err := promptCacheAccountingToABI(response.Accounting)
	if err != nil {
		return nil, err
	}
	return promptCacheRenewResultToProto(response.Result, accounting)
}

func PromptCacheRenewResultToProto(r promptcache.RenewResult, accounting *AccountingEvidence) (*backendpluginv1.RenewPromptCacheResponse, error) {
	return promptCacheRenewResultToProto(r, accounting)
}

func promptCacheRenewResultToProto(r promptcache.RenewResult, accounting *AccountingEvidence) (*backendpluginv1.RenewPromptCacheResponse, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	status, err := promptCacheRenewStatusToProto(r.Status)
	if err != nil {
		return nil, err
	}
	observation, err := promptCacheObservationToProto(r.Observation)
	if err != nil {
		return nil, err
	}
	evidence, err := promptCacheEvidenceToProto(r.Evidence)
	if err != nil {
		return nil, err
	}
	accountingWire, err := accountingEvidenceToProto(accounting)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.RenewPromptCacheResponse{Status: status, Observation: observation, Evidence: evidence, Accounting: accountingWire}, nil
}
