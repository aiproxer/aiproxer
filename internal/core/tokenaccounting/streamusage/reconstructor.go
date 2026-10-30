// Package streamusage reconstructs scoped usage for completed streaming calls.
package streamusage

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type Counter interface {
	CountCall(context.Context, app.CountCallInput) (app.CountResult, error)
	CountOutput(context.Context, app.CountOutputInput) (app.CountResult, error)
}

type Config struct {
	Policy        domain.BillingPolicy
	ProxyBillable bool
}

type Reconstructor struct {
	counter Counter
	policy  domain.BillingPolicy
	proxy   bool
}

type Input struct {
	Backend    string
	Model      string
	Call       aipapi.Call
	OutputText string
	Events     []aipapi.Event
}

type WarningCode string

const (
	WarningInputCountUnavailable  WarningCode = "input_count_unavailable"
	WarningOutputCountUnavailable WarningCode = "output_count_unavailable"
	WarningContextCanceled        WarningCode = "context_canceled"
)

type Warning struct {
	Code    WarningCode
	Message string
	Err     error
}

type Result struct {
	Events     []aipapi.Event
	Reconciled domain.Result
	Warnings   []Warning
	Err        error
}

func New(counter Counter, cfg Config) *Reconstructor {
	policy := cfg.Policy
	if policy == "" {
		policy = domain.PolicyProviderThenClient
	}
	return &Reconstructor{counter: counter, policy: policy, proxy: cfg.ProxyBillable}
}

func (r *Reconstructor) Reconstruct(ctx context.Context, input Input) (Result, error) {
	result := Result{}
	providerEvents := usageEvents(input.Events)
	result.Events = append(result.Events, providerEvents...)

	if err := ctx.Err(); err != nil {
		result.Warnings = append(result.Warnings, Warning{Code: WarningContextCanceled, Message: "stream usage reconstruction canceled", Err: err})
		result.Reconciled = domain.ReconcileEvents(r.policy, result.Events...)
		result.Err = err
		return result, err
	}

	local, warnings, err := r.localUsage(ctx, input)
	result.Warnings = append(result.Warnings, warnings...)
	if local != nil {
		result.Events = append(result.Events, usageEvent(*local))
		if r.proxy {
			proxy := *local
			proxy.Accounting.Plane = aipapi.UsagePlaneProxyBillable
			proxy.Accounting.Source = aipapi.UsageSourceProxyAdjusted
			result.Events = append(result.Events, usageEvent(proxy))
		}
	}

	result.Reconciled = domain.ReconcileEvents(r.policy, result.Events...)
	result.Err = err
	return result, err
}

func (r *Reconstructor) localUsage(ctx context.Context, input Input) (*aipapi.ScopedUsageDelta, []Warning, error) {
	if r.counter == nil {
		err := errors.New("stream usage counter is nil")
		return nil, []Warning{{Code: WarningInputCountUnavailable, Message: "input token count unavailable", Err: err}}, err
	}

	callResult, err := r.counter.CountCall(ctx, app.CountCallInput{
		Backend: input.Backend,
		Model:   input.Model,
		CallID:  input.Call.ID,
		Call:    input.Call,
	})
	if err != nil {
		return nil, []Warning{warningForCountError(WarningInputCountUnavailable, "input token count unavailable", err)}, err
	}

	outputResult, err := r.counter.CountOutput(ctx, app.CountOutputInput{
		Backend: input.Backend,
		Model:   input.Model,
		CallID:  input.Call.ID,
		Text:    input.OutputText,
		Events:  nonUsageEvents(input.Events),
	})
	if err != nil {
		usage := localUsageFromResults(callResult, app.CountResult{})
		return &usage, []Warning{warningForCountError(WarningOutputCountUnavailable, "output token count unavailable", err)}, err
	}

	usage := localUsageFromResults(callResult, outputResult)
	return &usage, nil, nil
}

func usageEvents(events []aipapi.Event) []aipapi.Event {
	out := []aipapi.Event{}
	for _, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta {
			out = append(out, normalizeProviderUsageEvent(ev))
		}
	}
	return out
}

func normalizeProviderUsageEvent(ev aipapi.Event) aipapi.Event {
	// Provider adapters must opt into billable authority explicitly. Inferring
	// provider-reported/authoritative metadata from an otherwise-unmarked event
	// can turn local or client-visible usage into billable usage.
	return ev.Clone()
}

func localUsageFromResults(callResult, outputResult app.CountResult) aipapi.ScopedUsageDelta {
	usage := aipapi.ScopedUsageDelta{
		InputTokens:      callResult.InputTokens,
		OutputTokens:     outputResult.OutputTokens,
		CacheReadTokens:  callResult.CacheReadTokens + outputResult.CacheReadTokens,
		CacheWriteTokens: callResult.CacheWriteTokens + outputResult.CacheWriteTokens,
		ReasoningTokens:  outputResult.ReasoningTokens,
		Accounting:       mergeAccounting(callResult.Accounting, outputResult.Accounting, aipapi.UsagePlaneClientVisible),
	}
	usage.TotalTokens = totalTokens(usage, callResult.TotalTokens, outputResult.TotalTokens)
	return usage
}

func usageEvent(usage aipapi.ScopedUsageDelta) aipapi.Event {
	return aipapi.Event{
		Kind:             aipapi.EventUsageDelta,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		ReasoningTokens:  usage.ReasoningTokens,
		TotalTokens:      usage.TotalTokens,
		UsagePresence:    usage.UsagePresence,
		Accounting:       usage.Accounting,
		UsageScopes:      []aipapi.ScopedUsageDelta{usage},
	}
}

func mergeAccounting(input, output aipapi.UsageAccountingMetadata, plane aipapi.UsagePlane) aipapi.UsageAccountingMetadata {
	accounting := output
	if accounting.Source == aipapi.UsageSourceUnknown {
		accounting.Source = input.Source
	}
	if accounting.Authority == aipapi.UsageAuthorityUnknown {
		accounting.Authority = input.Authority
	}
	if accounting.Tokenizer == (aipapi.TokenizerRef{}) {
		accounting.Tokenizer = input.Tokenizer
	}
	accounting.Plane = plane
	if accounting.Source == aipapi.UsageSourceUnknown {
		accounting.Source = aipapi.UsageSourceLocalEstimator
	}
	if accounting.Authority == aipapi.UsageAuthorityUnknown {
		accounting.Authority = aipapi.UsageAuthorityEstimated
	}
	return accounting
}

func totalTokens(usage aipapi.ScopedUsageDelta, inputTotal, outputTotal int) int {
	if inputTotal > 0 || outputTotal > 0 {
		return inputTotal + outputTotal
	}
	// Default inclusion schema: cache ⊂ input, reasoning ⊂ output.
	// Do not re-add subcomponents into the inferred total.
	return usage.InputTokens + usage.OutputTokens
}

func warningForCountError(code WarningCode, message string, err error) Warning {
	if errors.Is(err, context.Canceled) {
		return Warning{Code: WarningContextCanceled, Message: "stream usage reconstruction canceled", Err: err}
	}
	return Warning{Code: code, Message: fmt.Sprintf("%s: %v", message, err), Err: err}
}

func nonUsageEvents(events []aipapi.Event) []aipapi.Event {
	out := []aipapi.Event{}
	for _, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta {
			continue
		}
		out = append(out, ev.Clone())
	}
	return out
}
