package compactioncontinuity

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/policy"
)

// effectiveConfig resolves trusted session policy for the current callback.
// Package safety constants cap trusted overrides; generation config supplies
// defaults. The returned copy never mutates p.cfg or an in-flight job snapshot.
func (p *Plugin) effectiveConfig(ctx context.Context) (Config, bool) {
	if p == nil {
		return Config{}, false
	}
	defaults := policy.Defaults{
		Enabled: p.cfg.Extractor.Enabled,
		Preserve: policy.Categories{
			Plan: p.cfg.Preserve.Plan, UserDecisions: p.cfg.Preserve.UserDecisions,
			Constraints: p.cfg.Preserve.Constraints, Rationale: p.cfg.Preserve.Rationale,
			RejectedAlternatives: p.cfg.Preserve.RejectedAlternatives,
		},
		Extractor: policy.Extractor{
			Route: p.cfg.Extractor.Route, Inherit: p.cfg.Extractor.Inherit,
			Timeout: p.cfg.Extractor.Timeout, MaxInputTokens: p.cfg.Extractor.MaxInputTokens,
			MaxOutputTokens: p.cfg.Extractor.MaxOutputTokens,
		},
		Limits: policy.Limits{
			BarrierTimeout: p.cfg.Barrier.Timeout, CapsuleMaxTokens: p.cfg.Capsule.MaxTokens,
			CapsuleMaxBytes: p.cfg.Capsule.MaxBytes, SourceMaxBytes: p.cfg.Source.MaxBytes,
			ResultMaxBytes: p.cfg.Result.MaxBytes, ResultMaxCount: p.cfg.Result.MaxCount,
		},
	}
	maxima := policy.HardMaxima{
		Enabled:  p.cfg.Extractor.Enabled,
		Preserve: defaults.Preserve,
		// No operator route allowlist exists in the legacy feature config. A
		// route carried by the trusted session override is itself an approved
		// proxy-owned value; wire metadata never reaches this resolver.
		AllowTranscriptRead: false,
		AllowInherit:        p.cfg.Extractor.Inherit,
		Limits: policy.Limits{
			Timeout: MaxExtractorTimeout, MaxInputTokens: MaxInputTokens, MaxOutputTokens: MaxOutputTokens,
			BarrierTimeout: MaxBarrierTimeout, CapsuleMaxTokens: MaxCapsuleTokens, CapsuleMaxBytes: MaxCapsuleBytes,
			SourceMaxBytes: MaxSourceBytes, ResultMaxBytes: MaxResultBytes, ResultMaxCount: MaxResultCount,
		},
	}
	effective, err := policy.Resolve(ctx, defaults, maxima)
	if err != nil || !effective.Enabled {
		return Config{Extractor: ExtractorConfig{Enabled: false}}, false
	}
	out := p.cfg.Snapshot()
	out.Extractor.Enabled = effective.Enabled
	out.Extractor.Route = effective.Extractor.Route
	out.Extractor.Inherit = effective.Extractor.Inherit
	out.Extractor.Timeout = effective.Extractor.Timeout
	out.Extractor.MaxInputTokens = effective.Extractor.MaxInputTokens
	out.Extractor.MaxOutputTokens = effective.Extractor.MaxOutputTokens
	out.Preserve = PreserveConfig{
		Plan: effective.Preserve.Plan, UserDecisions: effective.Preserve.UserDecisions,
		Constraints: effective.Preserve.Constraints, Rationale: effective.Preserve.Rationale,
		RejectedAlternatives: effective.Preserve.RejectedAlternatives,
	}
	out.Barrier.Timeout = effective.Limits.BarrierTimeout
	out.Capsule.MaxTokens = effective.Limits.CapsuleMaxTokens
	out.Capsule.MaxBytes = effective.Limits.CapsuleMaxBytes
	out.Source.MaxBytes = effective.Limits.SourceMaxBytes
	out.Result.MaxBytes = effective.Limits.ResultMaxBytes
	out.Result.MaxCount = effective.Limits.ResultMaxCount
	return out, true
}
