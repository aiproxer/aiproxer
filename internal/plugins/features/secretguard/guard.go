package secretguard

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

type guard struct {
	cfg Config
}

// NewGuard returns a Guard for the given validated config.
func NewGuard(cfg Config) sdk.Guard {
	return &guard{cfg: cfg}
}

func (g *guard) ID() string { return ID }

func (g *guard) Order() int {
	if g.cfg.Order != nil {
		return *g.cfg.Order
	}
	return 0
}

func (g *guard) FailureMode() sdk.FailureMode {
	return sdk.FailClosed
}

func (g *guard) Evaluate(ctx context.Context, call *aipapi.Call, _ sdk.Meta, services sdk.Services) (sdk.Decision, error) {
	if call == nil {
		return sdk.Decision{Outcome: sdk.OutcomePass}, nil
	}
	var generation *GenerationServices
	if services.Capability != nil {
		generation, _ = services.Capability.(*GenerationServices)
	}
	var matcher sdk.Matcher
	if services.MatcherResolver == nil {
		if generation == nil || !generation.betterLeaksEnabled {
			return sdk.Decision{Outcome: sdk.OutcomePass}, nil
		}
	} else {
		var err error
		matcher, err = services.MatcherResolver.Resolve(ctx)
		if err != nil {
			return sdk.Decision{}, err
		}
	}
	if matcher == nil && (generation == nil || !generation.betterLeaksEnabled) {
		return sdk.Decision{Outcome: sdk.OutcomePass}, nil
	}

	switch g.cfg.Action {
	case ActionBlock:
		return g.evalBlock(ctx, call, matcher, generation)
	case ActionRedact:
		return g.evalRedact(ctx, call, matcher, generation)
	case ActionLog:
		return g.evalLog(ctx, call, matcher, generation)
	default:
		return sdk.Decision{}, fmt.Errorf("%s: unknown action %q", ID, g.cfg.Action)
	}
}

func (g *guard) evalBlock(ctx context.Context, call *aipapi.Call, m sdk.Matcher, generation *GenerationServices) (sdk.Decision, error) {
	out, err := scanCall(ctx, call, m, modeScan, g.cfg.ScanMaxBytes, generation)
	if err != nil {
		return sdk.Decision{}, err
	}
	d := sdk.Decision{Findings: out.Findings}
	if out.ScanLimitHit {
		return scanLimitDecision(d, sdk.OutcomeBlock), nil
	}
	if len(out.Findings) > 0 {
		d.Outcome = sdk.OutcomeBlock
		return d, nil
	}
	d.Outcome = sdk.OutcomePass
	return d, nil
}

func (g *guard) evalRedact(ctx context.Context, call *aipapi.Call, m sdk.Matcher, generation *GenerationServices) (sdk.Decision, error) {
	// One-pass canonical location traversal: redact on a working clone so findings,
	// mutations, and scan-limit accounting share a single matcher walk. The live
	// call is only replaced when MutationCount > 0 (no partial commit on failure/limit).
	work := aipapi.CloneCall(*call)
	out, err := scanCall(ctx, &work, m, modeRedact, g.cfg.ScanMaxBytes, generation)
	if err != nil {
		if errors.Is(err, errBetterLeaksUnrewritable) {
			return sdk.Decision{
				Outcome:       sdk.OutcomeBlock,
				Findings:      out.Findings,
				MutationCount: 0,
				FailureKind:   FailureKindUnrewritableDetectedSecret,
				FailureReason: "detected secret cannot be safely rewritten",
			}, nil
		}
		if _, ok := errors.AsType[*unsupportedJSONTokenError](err); ok {
			return sdk.Decision{
				Outcome:       sdk.OutcomeBlock,
				Findings:      out.Findings,
				MutationCount: 0,
				FailureKind:   FailureKindUnsupportedJSONToken,
				FailureReason: "unsupported JSON token encountered",
			}, nil
		}
		return sdk.Decision{}, err
	}
	d := sdk.Decision{
		Findings:      out.Findings,
		MutationCount: out.MutationCount,
	}
	if out.ScanLimitHit {
		return scanLimitDecision(d, sdk.OutcomeBlock), nil
	}
	if out.MutationCount > 0 {
		*call = work
		d.Outcome = sdk.OutcomeRedacted
		return d, nil
	}
	if len(out.Findings) > 0 {
		d.Outcome = sdk.OutcomeLog
		return d, nil
	}
	d.Outcome = sdk.OutcomePass
	return d, nil
}

func (g *guard) evalLog(ctx context.Context, call *aipapi.Call, m sdk.Matcher, generation *GenerationServices) (sdk.Decision, error) {
	out, err := scanCall(ctx, call, m, modeLogScan, g.cfg.ScanMaxBytes, generation)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return sdk.Decision{}, err
		}
		if _, ok := errors.AsType[*betterLeaksScanError](err); ok {
			d := sdk.Decision{
				Outcome:       sdk.OutcomeLog,
				Findings:      out.Findings,
				ScanLimitHit:  out.ScanLimitHit,
				FailureKind:   sdk.FailureKindDetectorFailure,
				FailureReason: "betterleaks scan failed",
			}
			if out.ScanLimitHit {
				d = scanLimitDecision(d, sdk.OutcomeLog)
				d.FailureReason = "scan_max_bytes exceeded; betterleaks scan failed"
			}
			return d, nil
		}
		return sdk.Decision{}, err
	}
	d := sdk.Decision{Findings: out.Findings}
	if out.ScanLimitHit {
		return scanLimitDecision(d, sdk.OutcomeLog), nil
	}
	if len(out.Findings) > 0 {
		d.Outcome = sdk.OutcomeLog
		return d, nil
	}
	d.Outcome = sdk.OutcomePass
	return d, nil
}

func scanLimitDecision(base sdk.Decision, outcome sdk.Outcome) sdk.Decision {
	base.ScanLimitHit = true
	base.MutationCount = 0
	base.FailureKind = FailureKindScanLimit
	base.FailureReason = "scan_max_bytes exceeded"
	base.Outcome = outcome
	return base
}
