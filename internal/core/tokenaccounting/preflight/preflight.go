// Package preflight evaluates token-accounting admission checks before a backend attempt.
package preflight

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type Mode string

const (
	ModeAdvisory Mode = "advisory"
	ModeStrict   Mode = "strict"
)

type Reason string

const (
	ReasonAllowed              Reason = "allowed"
	ReasonDisabled             Reason = "disabled"
	ReasonCountUnavailable     Reason = "count_unavailable"
	ReasonInputLimitExceeded   Reason = "input_limit_exceeded"
	ReasonContextLimitExceeded Reason = "context_limit_exceeded"
	ReasonOutputLimitExceeded  Reason = "output_limit_exceeded"
	ReasonUnknownOutputDenied  Reason = "unknown_output_denied"
)

// UnknownOutputPolicy bounds future output exposure when the client omits max output.
type UnknownOutputPolicy string

const (
	UnknownOutputRequireClientLimit  UnknownOutputPolicy = "require_client_limit"
	UnknownOutputConfiguredDefault   UnknownOutputPolicy = "configured_default"
	UnknownOutputModelBackendMaximum UnknownOutputPolicy = "model_backend_maximum"
	UnknownOutputClamp               UnknownOutputPolicy = "clamp"
	UnknownOutputDeny                UnknownOutputPolicy = "deny"
)

type Counter interface {
	CountCall(context.Context, app.CountCallInput) (app.CountResult, error)
}

type Config struct {
	Enabled              bool
	Mode                 Mode
	MaxInputTokens       int64
	MaxOutputTokens      int64
	MaxContextTokens     int64
	ClampMaxOutputTokens bool
	UnknownOutputPolicy  UnknownOutputPolicy
}

type Checker struct {
	counter Counter
	cfg     Config
}

type Input struct {
	Backend                  string
	Model                    string
	CallID                   string
	Call                     aipapi.Call
	RequestedMaxOutputTokens *int
	Facts                    modelcatalog.ModelFacts
}

type Decision struct {
	Allowed                 bool
	Reason                  Reason
	Warnings                []string
	Err                     error
	Count                   app.CountResult
	AdjustedMaxOutputTokens *int
	// RequireMaxOutputEnforcement is set when preflight applied a max-output
	// clamp that must be enforceable on the selected backend before Open.
	RequireMaxOutputEnforcement bool
}

func NewChecker(counter Counter, cfg Config) *Checker {
	if cfg.Mode == "" {
		cfg.Mode = ModeAdvisory
	}
	return &Checker{counter: counter, cfg: cfg}
}

func (c *Checker) Check(ctx context.Context, in Input) Decision {
	if c == nil || !c.cfg.Enabled {
		return Decision{Allowed: true, Reason: ReasonDisabled}
	}
	if c.counter == nil {
		return c.failOrWarn(ReasonCountUnavailable, "token count unavailable: nil counter", nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	count, err := c.counter.CountCall(ctx, app.CountCallInput{
		Backend: in.Backend,
		Model:   in.Model,
		CallID:  in.CallID,
		Call:    in.Call,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Decision{Allowed: false, Reason: ReasonCountUnavailable, Err: err}
		}
		return c.failOrWarn(ReasonCountUnavailable, fmt.Sprintf("token count unavailable: %v", err), err)
	}
	if count.InputTokens < 0 {
		return c.failOrWarn(ReasonCountUnavailable, fmt.Sprintf("token count invalid: input tokens %d", count.InputTokens), nil)
	}

	out := Decision{Allowed: true, Reason: ReasonAllowed, Count: count}
	if c.cfg.MaxInputTokens > 0 && int64(count.InputTokens) > c.cfg.MaxInputTokens {
		return Decision{Allowed: false, Reason: ReasonInputLimitExceeded, Count: count, Err: fmt.Errorf("input token limit exceeded")}
	}

	modelOutputLimit := in.Facts.OutputLimit
	outputLimit := effectiveLimitFact(modelOutputLimit, c.cfg.MaxOutputTokens)
	effectiveOutput, adjusted, ok := c.evaluateOutputLimit(in.RequestedMaxOutputTokens, modelOutputLimit, outputLimit, count)
	if !ok {
		return adjusted
	}
	if adjusted.AdjustedMaxOutputTokens != nil {
		out.AdjustedMaxOutputTokens = adjusted.AdjustedMaxOutputTokens
		out.RequireMaxOutputEnforcement = adjusted.RequireMaxOutputEnforcement
	}
	if len(adjusted.Warnings) > 0 {
		out.Warnings = append(out.Warnings, adjusted.Warnings...)
	}
	// Persist the exposure bound on Count.OutputTokens so authority spend
	// reservations see non-zero future output when the client omitted max.
	if effectiveOutput > 0 && out.Count.OutputTokens == 0 {
		if effectiveOutput > int64(math.MaxInt) {
			out.Count.OutputTokens = math.MaxInt
		} else {
			out.Count.OutputTokens = int(effectiveOutput)
		}
	}

	contextLimit := effectiveLimitFact(in.Facts.ContextLimit, c.cfg.MaxContextTokens)
	if limitPresent(contextLimit) && exceedsLimit(int64(count.InputTokens), effectiveOutput, contextLimit.Tokens) {
		return Decision{Allowed: false, Reason: ReasonContextLimitExceeded, Count: count, Err: fmt.Errorf("context token limit exceeded")}
	}
	return out
}

// Enabled reports whether this preflight checker is non-nil and enabled.
func (c *Checker) Enabled() bool {
	return c != nil && c.cfg.Enabled
}

// WireInput supplies bounded wire facts and replay source to CheckWire (Task 10.4).
type WireInput struct {
	Backend                  string
	Model                    string
	CallID                   string
	ProfileID                string
	Source                   largebody.Source
	RequestedMaxOutputTokens *int
	Facts                    modelcatalog.ModelFacts
	Semantics                largebody.ExactTokenizerSemantics
	MaxScanBytes             int64
	MaxPermitHoldCPU         time.Duration
}

// CheckWire evaluates token preflight over bounded wire facts and replay source without
// constructing a full aipapi.Call or materializing prompt text (Requirements 6.2, 15.5, 21; Task 10.4).
// If the counter only supports CountCall, or exact tokenizer semantics do not exist,
// or counting is expensive/unbounded, CheckWire returns ReasonCountUnavailable,
// signaling that dynamic assessment must decline to canonical processing under the same permit.
func (c *Checker) CheckWire(ctx context.Context, in WireInput) Decision {
	if c == nil || !c.cfg.Enabled {
		return Decision{Allowed: true, Reason: ReasonDisabled}
	}
	if in.Source == nil {
		return Decision{
			Allowed:  false,
			Reason:   ReasonCountUnavailable,
			Err:      fmt.Errorf("token count unavailable: nil wire source"),
			Warnings: []string{"token count unavailable: nil wire source"},
		}
	}

	gateRes := largebody.EvaluateWireTokenCounting(ctx, largebody.WireTokenCountGateInput{
		Source:            in.Source,
		Model:             in.Model,
		Backend:           in.Backend,
		CallID:            in.CallID,
		ProfileID:         in.ProfileID,
		Counter:           c.counter,
		Semantics:         in.Semantics,
		MaxScanBytes:      in.MaxScanBytes,
		MaxPermitHoldCPU:  in.MaxPermitHoldCPU,
		AccountingEnabled: true,
	})
	if !gateRes.Proceed {
		return Decision{
			Allowed:  false,
			Reason:   ReasonCountUnavailable,
			Err:      gateRes.Err,
			Warnings: []string{fmt.Sprintf("wire token count unavailable: %v", gateRes.Err)},
		}
	}

	count := app.CountResult{
		InputTokens:        gateRes.Count.InputTokens,
		TotalTokens:        gateRes.Count.TotalTokens,
		TotalTokensPresent: gateRes.Count.TotalTokens > 0 || gateRes.Count.InputTokens > 0,
		Accounting:         gateRes.Count.Accounting,
	}

	out := Decision{Allowed: true, Reason: ReasonAllowed, Count: count}
	if c.cfg.MaxInputTokens > 0 && int64(count.InputTokens) > c.cfg.MaxInputTokens {
		return Decision{Allowed: false, Reason: ReasonInputLimitExceeded, Count: count, Err: fmt.Errorf("input token limit exceeded")}
	}

	modelOutputLimit := in.Facts.OutputLimit
	outputLimit := effectiveLimitFact(modelOutputLimit, c.cfg.MaxOutputTokens)
	effectiveOutput, adjusted, ok := c.evaluateOutputLimit(in.RequestedMaxOutputTokens, modelOutputLimit, outputLimit, count)
	if !ok {
		return adjusted
	}
	if adjusted.AdjustedMaxOutputTokens != nil {
		out.AdjustedMaxOutputTokens = adjusted.AdjustedMaxOutputTokens
		out.RequireMaxOutputEnforcement = adjusted.RequireMaxOutputEnforcement
	}
	if len(adjusted.Warnings) > 0 {
		out.Warnings = append(out.Warnings, adjusted.Warnings...)
	}
	if effectiveOutput > 0 && out.Count.OutputTokens == 0 {
		if effectiveOutput > int64(math.MaxInt) {
			out.Count.OutputTokens = math.MaxInt
		} else {
			out.Count.OutputTokens = int(effectiveOutput)
		}
	}

	contextLimit := effectiveLimitFact(in.Facts.ContextLimit, c.cfg.MaxContextTokens)
	if limitPresent(contextLimit) && exceedsLimit(int64(count.InputTokens), effectiveOutput, contextLimit.Tokens) {
		return Decision{Allowed: false, Reason: ReasonContextLimitExceeded, Count: count, Err: fmt.Errorf("context token limit exceeded")}
	}
	return out
}

func (c *Checker) evaluateOutputLimit(
	requested *int,
	modelLimit modelcatalog.LimitFact,
	mergedLimit modelcatalog.LimitFact,
	count app.CountResult,
) (int64, Decision, bool) {
	if requested == nil {
		return c.resolveUnknownOutput(modelLimit, count)
	}

	effectiveOutput := int64(requestedOutputTokens(requested))
	if !limitPresent(mergedLimit) {
		return effectiveOutput, Decision{}, true
	}

	if int64(*requested) > mergedLimit.Tokens {
		if c.cfg.ClampMaxOutputTokens {
			clamped := int(mergedLimit.Tokens)
			return int64(clamped), Decision{
				Allowed:                     true,
				Reason:                      ReasonAllowed,
				Count:                       count,
				AdjustedMaxOutputTokens:     &clamped,
				RequireMaxOutputEnforcement: true,
			}, true
		}
		if c.cfg.Mode == ModeStrict {
			return 0, Decision{Allowed: false, Reason: ReasonOutputLimitExceeded, Count: count, Err: fmt.Errorf("output token limit exceeded")}, false
		}
		warning := fmt.Sprintf("requested max output tokens %d exceeds output limit %d", *requested, mergedLimit.Tokens)
		return effectiveOutput, Decision{Allowed: true, Reason: ReasonAllowed, Count: count, Warnings: []string{warning}}, true
	}

	return effectiveOutput, Decision{}, true
}

func (c *Checker) resolveUnknownOutput(modelLimit modelcatalog.LimitFact, count app.CountResult) (int64, Decision, bool) {
	policy := c.effectiveUnknownOutputPolicy(modelLimit)
	if policy == "" {
		// Legacy unbound: no configured/model bound and no explicit policy.
		return 0, Decision{}, true
	}
	switch policy {
	case UnknownOutputRequireClientLimit, UnknownOutputDeny:
		return 0, Decision{
			Allowed: false,
			Reason:  ReasonUnknownOutputDenied,
			Count:   count,
			Err:     fmt.Errorf("unknown output exposure denied by policy %q", policy),
		}, false
	case UnknownOutputConfiguredDefault:
		if c.cfg.MaxOutputTokens <= 0 {
			return 0, Decision{
				Allowed: false,
				Reason:  ReasonUnknownOutputDenied,
				Count:   count,
				Err:     fmt.Errorf("configured_default unknown-output policy requires max_output_tokens"),
			}, false
		}
		bound := int(c.cfg.MaxOutputTokens)
		if c.cfg.MaxOutputTokens > math.MaxInt {
			bound = math.MaxInt
		}
		return int64(bound), Decision{Allowed: true, Reason: ReasonAllowed, Count: count, AdjustedMaxOutputTokens: &bound, RequireMaxOutputEnforcement: true}, true
	case UnknownOutputModelBackendMaximum, UnknownOutputClamp:
		if !limitPresent(modelLimit) {
			// Clamp may fall back to configured default when model limit is absent.
			if policy == UnknownOutputClamp && c.cfg.MaxOutputTokens > 0 {
				bound := int(c.cfg.MaxOutputTokens)
				if c.cfg.MaxOutputTokens > math.MaxInt {
					bound = math.MaxInt
				}
				return int64(bound), Decision{Allowed: true, Reason: ReasonAllowed, Count: count, AdjustedMaxOutputTokens: &bound, RequireMaxOutputEnforcement: true}, true
			}
			return 0, Decision{
				Allowed: false,
				Reason:  ReasonUnknownOutputDenied,
				Count:   count,
				Err:     fmt.Errorf("%s unknown-output policy requires a model/backend output limit", policy),
			}, false
		}
		bound := int(modelLimit.Tokens)
		if modelLimit.Tokens > math.MaxInt {
			bound = math.MaxInt
		}
		return int64(bound), Decision{Allowed: true, Reason: ReasonAllowed, Count: count, AdjustedMaxOutputTokens: &bound, RequireMaxOutputEnforcement: true}, true
	default:
		return 0, Decision{
			Allowed: false,
			Reason:  ReasonUnknownOutputDenied,
			Count:   count,
			Err:     fmt.Errorf("unknown output policy %q", policy),
		}, false
	}
}

// effectiveUnknownOutputPolicy resolves empty config to the Phase A default:
// model_backend_maximum when a model limit exists, else configured_default when
// MaxOutputTokens > 0. When neither bound exists, empty policy keeps legacy
// allow-unbound behavior (callers must set an explicit deny/require_client_limit
// policy to reject omitted max output).
func (c *Checker) effectiveUnknownOutputPolicy(modelLimit modelcatalog.LimitFact) UnknownOutputPolicy {
	raw := UnknownOutputPolicy(strings.ToLower(strings.TrimSpace(string(c.cfg.UnknownOutputPolicy))))
	switch raw {
	case UnknownOutputRequireClientLimit, UnknownOutputConfiguredDefault, UnknownOutputModelBackendMaximum, UnknownOutputClamp, UnknownOutputDeny:
		return raw
	}
	if limitPresent(modelLimit) {
		return UnknownOutputModelBackendMaximum
	}
	if c.cfg.MaxOutputTokens > 0 {
		return UnknownOutputConfiguredDefault
	}
	return ""
}

func (c *Checker) failOrWarn(reason Reason, warning string, err error) Decision {
	if c.cfg.Mode == ModeStrict {
		return Decision{Allowed: false, Reason: reason, Err: err}
	}
	return Decision{Allowed: true, Reason: ReasonAllowed, Warnings: []string{warning}, Err: err}
}

func requestedOutputTokens(tokens *int) int {
	if tokens == nil || *tokens < 0 {
		return 0
	}
	return *tokens
}

func limitPresent(limit modelcatalog.LimitFact) bool {
	return limit.State == modelcatalog.LimitPresent && limit.Tokens > 0
}

func effectiveLimitFact(fact modelcatalog.LimitFact, configured int64) modelcatalog.LimitFact {
	if configured <= 0 {
		return fact
	}
	if !limitPresent(fact) || configured < fact.Tokens {
		return modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: configured}
	}
	return fact
}

func exceedsLimit(input, output, limit int64) bool {
	if input > math.MaxInt64-output {
		return true
	}
	return input+output > limit
}
