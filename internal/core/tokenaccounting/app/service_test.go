package app

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCountTextProviderFirstProviderSuccessWins(t *testing.T) {
	t.Parallel()

	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusSupported}, result: CountResult{
		InputTokens: 7,
		Accounting: aipapi.UsageAccountingMetadata{
			Source:    aipapi.UsageSourceProviderCountAPI,
			Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}}
	local := &fakeLocalCounter{result: CountResult{InputTokens: 3}}
	svc := NewService(ServiceConfig{Mode: ModeProviderFirst}, provider, local)

	got, err := svc.CountText(context.Background(), CountTextInput{Backend: "openai", Model: "gpt-4o", Text: "hello"})
	if err != nil {
		t.Fatalf("CountText returned error: %v", err)
	}
	if got.InputTokens != 7 {
		t.Fatalf("CountText InputTokens = %d, want 7", got.InputTokens)
	}
	if provider.countCalls != 1 || local.calls != 0 {
		t.Fatalf("calls provider=%d local=%d, want provider=1 local=0", provider.countCalls, local.calls)
	}
}

func TestCountTextProviderUnsupportedFallsBackLocal(t *testing.T) {
	t.Parallel()

	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusUnsupported}}
	local := &fakeLocalCounter{result: CountResult{InputTokens: 4}}
	svc := NewService(ServiceConfig{Mode: ModeProviderFirst}, provider, local)

	got, err := svc.CountText(context.Background(), CountTextInput{Backend: "anthropic", Model: "claude-3", Text: "hello"})
	if err != nil {
		t.Fatalf("CountText returned error: %v", err)
	}
	if got.InputTokens != 4 {
		t.Fatalf("CountText InputTokens = %d, want 4", got.InputTokens)
	}
	if got.Accounting.Source != aipapi.UsageSourceLocalTokenizer {
		t.Fatalf("source = %q, want %q", got.Accounting.Source, aipapi.UsageSourceLocalTokenizer)
	}
	if got.Accounting.Authority != aipapi.UsageAuthorityEstimated {
		t.Fatalf("authority = %q, want %q", got.Accounting.Authority, aipapi.UsageAuthorityEstimated)
	}
	if got.Accounting.Tokenizer.ModelUsed != "claude-3" {
		t.Fatalf("tokenizer model used = %q, want claude-3", got.Accounting.Tokenizer.ModelUsed)
	}
	assertFallback(t, got, FallbackReasonProviderUnsupported, ErrProviderUnsupported)
	if provider.countCalls != 0 || local.calls != 1 {
		t.Fatalf("calls provider=%d local=%d, want provider=0 local=1", provider.countCalls, local.calls)
	}
}

func TestCountTextProviderFirstProviderUnavailableFallsBackLocal(t *testing.T) {
	t.Parallel()

	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusUnavailable}}
	local := &fakeLocalCounter{result: CountResult{InputTokens: 4}}
	svc := NewService(ServiceConfig{Mode: ModeProviderFirst}, provider, local)

	got, err := svc.CountText(context.Background(), CountTextInput{Backend: "anthropic", Model: "claude-3", Text: "hello"})
	if err != nil {
		t.Fatalf("CountText returned error: %v", err)
	}
	assertFallback(t, got, FallbackReasonProviderUnavailable, ErrProviderUnavailable)
	if provider.countCalls != 0 || local.calls != 1 {
		t.Fatalf("calls provider=%d local=%d, want provider=0 local=1", provider.countCalls, local.calls)
	}
}

func TestCountTextProviderFirstProviderErrorFallsBackLocal(t *testing.T) {
	t.Parallel()

	providerErr := errors.New("count api failed")
	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusSupported}, err: providerErr}
	local := &fakeLocalCounter{result: CountResult{InputTokens: 4}}
	svc := NewService(ServiceConfig{Mode: ModeProviderFirst}, provider, local)

	got, err := svc.CountText(context.Background(), CountTextInput{Backend: "openai", Model: "gpt-4o", Text: "hello"})
	if err != nil {
		t.Fatalf("CountText returned error: %v", err)
	}
	assertFallback(t, got, FallbackReasonProviderError, ErrProviderUnavailable)
	if !errors.Is(got.Fallbacks[0].Err, providerErr) {
		t.Fatalf("fallback error = %v, want provider error", got.Fallbacks[0].Err)
	}
	if provider.countCalls != 1 || local.calls != 1 {
		t.Fatalf("calls provider=%d local=%d, want provider=1 local=1", provider.countCalls, local.calls)
	}
}

func TestCountTextProviderOnlyProviderError(t *testing.T) {
	t.Parallel()

	providerErr := errors.New("provider unavailable")
	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusSupported}, err: providerErr}
	local := &fakeLocalCounter{result: CountResult{InputTokens: 4}}
	svc := NewService(ServiceConfig{Mode: ModeProviderOnly}, provider, local)

	_, err := svc.CountText(context.Background(), CountTextInput{Backend: "openai", Model: "gpt-4o", Text: "hello"})
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("CountText error = %v, want ErrProviderUnavailable", err)
	}
	if !errors.Is(err, providerErr) {
		t.Fatalf("CountText error = %v, want wrapped provider error", err)
	}
	if provider.countCalls != 1 || local.calls != 0 {
		t.Fatalf("calls provider=%d local=%d, want provider=1 local=0", provider.countCalls, local.calls)
	}
}

func TestCountTextProviderOnlyProviderUnsupported(t *testing.T) {
	t.Parallel()

	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusUnsupported}}
	local := &fakeLocalCounter{result: CountResult{InputTokens: 4}}
	svc := NewService(ServiceConfig{Mode: ModeProviderOnly}, provider, local)

	_, err := svc.CountText(context.Background(), CountTextInput{Backend: "openai", Model: "gpt-4o", Text: "hello"})
	if !errors.Is(err, ErrProviderUnsupported) {
		t.Fatalf("CountText error = %v, want ErrProviderUnsupported", err)
	}
	if provider.countCalls != 0 || local.calls != 0 {
		t.Fatalf("calls provider=%d local=%d, want provider=0 local=0", provider.countCalls, local.calls)
	}
}

func TestCountTextProviderOnlyProviderUnavailable(t *testing.T) {
	t.Parallel()

	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusUnavailable}}
	local := &fakeLocalCounter{result: CountResult{InputTokens: 4}}
	svc := NewService(ServiceConfig{Mode: ModeProviderOnly}, provider, local)

	_, err := svc.CountText(context.Background(), CountTextInput{Backend: "openai", Model: "gpt-4o", Text: "hello"})
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("CountText error = %v, want ErrProviderUnavailable", err)
	}
	if provider.countCalls != 0 || local.calls != 0 {
		t.Fatalf("calls provider=%d local=%d, want provider=0 local=0", provider.countCalls, local.calls)
	}
}

func TestCountTextLocalOnlyDoesNotCallProvider(t *testing.T) {
	t.Parallel()

	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusSupported}, result: CountResult{InputTokens: 9}}
	local := &fakeLocalCounter{result: CountResult{InputTokens: 5}}
	svc := NewService(ServiceConfig{Mode: ModeLocalOnly}, provider, local)

	got, err := svc.CountText(context.Background(), CountTextInput{Backend: "openai", Model: "gpt-4o", Text: "hello"})
	if err != nil {
		t.Fatalf("CountText returned error: %v", err)
	}
	if got.InputTokens != 5 {
		t.Fatalf("CountText InputTokens = %d, want 5", got.InputTokens)
	}
	if provider.countCalls != 0 || provider.supportCalls != 0 || local.calls != 1 {
		t.Fatalf("calls provider_count=%d provider_support=%d local=%d, want 0 0 1", provider.countCalls, provider.supportCalls, local.calls)
	}
}

func TestCountTextDisabledReturnsStableErrorAndCallsNoPorts(t *testing.T) {
	t.Parallel()

	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusSupported}}
	local := &fakeLocalCounter{result: CountResult{InputTokens: 5}}
	svc := NewService(ServiceConfig{Mode: ModeDisabled}, provider, local)

	_, err := svc.CountText(context.Background(), CountTextInput{Backend: "openai", Model: "gpt-4o", Text: "hello"})
	if !errors.Is(err, ErrCountingDisabled) {
		t.Fatalf("CountText error = %v, want ErrCountingDisabled", err)
	}
	if provider.countCalls != 0 || provider.supportCalls != 0 || local.calls != 0 {
		t.Fatalf("calls provider_count=%d provider_support=%d local=%d, want all zero", provider.countCalls, provider.supportCalls, local.calls)
	}
}

func TestCountTextContextCancellationPreventsCalls(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusSupported}}
	local := &fakeLocalCounter{result: CountResult{InputTokens: 5}}
	svc := NewService(ServiceConfig{Mode: ModeProviderFirst}, provider, local)

	_, err := svc.CountText(ctx, CountTextInput{Backend: "openai", Model: "gpt-4o", Text: "hello"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CountText error = %v, want context.Canceled", err)
	}
	if provider.countCalls != 0 || provider.supportCalls != 0 || local.calls != 0 {
		t.Fatalf("calls provider_count=%d provider_support=%d local=%d, want all zero", provider.countCalls, provider.supportCalls, local.calls)
	}
}

func TestCountTextPropagatesContextToPorts(t *testing.T) {
	t.Parallel()

	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "sentinel")
	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusUnsupported}}
	local := &fakeLocalCounter{result: CountResult{InputTokens: 5}}
	svc := NewService(ServiceConfig{Mode: ModeProviderFirst}, provider, local)

	_, err := svc.CountText(ctx, CountTextInput{Backend: "openai", Model: "gpt-4o", Text: "hello"})
	if err != nil {
		t.Fatalf("CountText returned error: %v", err)
	}
	if provider.supportCtx.Value(contextKey{}) != "sentinel" {
		t.Fatalf("provider support did not receive original context")
	}
	if local.ctx.Value(contextKey{}) != "sentinel" {
		t.Fatalf("local did not receive original context")
	}
}

func TestCountTextContextCancellationAfterProviderPreventsFallback(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusUnsupported}, afterSupports: cancel}
	local := &fakeLocalCounter{result: CountResult{InputTokens: 5}}
	svc := NewService(ServiceConfig{Mode: ModeProviderFirst}, provider, local)

	_, err := svc.CountText(ctx, CountTextInput{Backend: "openai", Model: "gpt-4o", Text: "hello"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CountText error = %v, want context.Canceled", err)
	}
	if local.calls != 0 {
		t.Fatalf("local calls = %d, want 0", local.calls)
	}
}

func TestCountTextLocalFallbackMetadataDefaultsPreserveStrongerMetadata(t *testing.T) {
	t.Parallel()

	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusSupported}, err: errors.New("boom")}
	local := &fakeLocalCounter{result: CountResult{
		InputTokens: 6,
		Accounting: aipapi.UsageAccountingMetadata{
			Source:    aipapi.UsageSourceLocalEstimator,
			Authority: aipapi.UsageAuthorityAdvisory,
			Tokenizer: aipapi.TokenizerRef{ModelUsed: "tokenizer-model"},
		},
	}}
	svc := NewService(ServiceConfig{Mode: ModeProviderFirst}, provider, local)

	got, err := svc.CountText(context.Background(), CountTextInput{Backend: "openai", Model: "gpt-4o", Text: "hello"})
	if err != nil {
		t.Fatalf("CountText returned error: %v", err)
	}
	if got.Accounting.Source != aipapi.UsageSourceLocalEstimator {
		t.Fatalf("source = %q, want existing local estimator", got.Accounting.Source)
	}
	if got.Accounting.Authority != aipapi.UsageAuthorityAdvisory {
		t.Fatalf("authority = %q, want existing advisory", got.Accounting.Authority)
	}
	if got.Accounting.Tokenizer.ModelUsed != "tokenizer-model" {
		t.Fatalf("tokenizer model used = %q, want tokenizer-model", got.Accounting.Tokenizer.ModelUsed)
	}
}

func TestCountTextLocalFirstFallbackToProviderPreservesLocalFailure(t *testing.T) {
	t.Parallel()

	localErr := errors.New("tokenizer missing")
	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusSupported}, result: CountResult{InputTokens: 10}}
	local := &fakeLocalCounter{err: localErr}
	svc := NewService(ServiceConfig{Mode: ModeLocalFirst}, provider, local)

	got, err := svc.CountText(context.Background(), CountTextInput{Backend: "openai", Model: "gpt-4o", Text: "hello"})
	if err != nil {
		t.Fatalf("CountText returned error: %v", err)
	}
	assertFallback(t, got, FallbackReasonLocalError, ErrLocalUnavailable)
	if !errors.Is(got.Fallbacks[0].Err, localErr) {
		t.Fatalf("fallback error = %v, want local error", got.Fallbacks[0].Err)
	}
	if local.calls != 1 || provider.countCalls != 1 {
		t.Fatalf("calls local=%d provider=%d, want local=1 provider=1", local.calls, provider.countCalls)
	}
}

func TestCountTextLocalFirstReturnsLocalErrorWhenProviderUnsupported(t *testing.T) {
	t.Parallel()

	localErr := errors.New("tokenizer missing")
	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusUnsupported}}
	local := &fakeLocalCounter{err: localErr}
	svc := NewService(ServiceConfig{Mode: ModeLocalFirst}, provider, local)

	_, err := svc.CountText(context.Background(), CountTextInput{Backend: "openai", Model: "gpt-4o", Text: "hello"})
	if !errors.Is(err, ErrLocalUnavailable) {
		t.Fatalf("CountText error = %v, want ErrLocalUnavailable", err)
	}
	if !errors.Is(err, localErr) {
		t.Fatalf("CountText error = %v, want wrapped local error", err)
	}
	if local.calls != 1 || provider.countCalls != 0 {
		t.Fatalf("calls local=%d provider=%d, want local=1 provider=0", local.calls, provider.countCalls)
	}
}

func TestCountCallUsesProviderFirstSemantics(t *testing.T) {
	t.Parallel()

	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusSupported}, result: CountResult{InputTokens: 11}}
	local := &fakeLocalCounter{result: CountResult{InputTokens: 2}}
	svc := NewService(ServiceConfig{Mode: ModeProviderFirst}, provider, local)

	got, err := svc.CountCall(context.Background(), CountCallInput{Backend: "openai", Model: "gpt-4o", Call: aipapi.Call{ID: "call-1"}})
	if err != nil {
		t.Fatalf("CountCall returned error: %v", err)
	}
	if got.InputTokens != 11 {
		t.Fatalf("CountCall InputTokens = %d, want 11", got.InputTokens)
	}
	if provider.countCallCalls != 1 || local.callCalls != 0 {
		t.Fatalf("calls provider=%d local=%d, want provider=1 local=0", provider.countCallCalls, local.callCalls)
	}
}

func TestCountOutputUsesLocalOnlySemantics(t *testing.T) {
	t.Parallel()

	provider := &fakeProviderCounter{support: ProviderSupport{Status: SupportStatusSupported}, result: CountResult{OutputTokens: 12}}
	local := &fakeLocalCounter{result: CountResult{OutputTokens: 8}}
	svc := NewService(ServiceConfig{Mode: ModeLocalOnly}, provider, local)

	got, err := svc.CountOutput(context.Background(), CountOutputInput{
		Backend: "openai",
		Model:   "gpt-4o",
		Text:    "hello",
		Events:  []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "hello"}},
	})
	if err != nil {
		t.Fatalf("CountOutput returned error: %v", err)
	}
	if got.OutputTokens != 8 {
		t.Fatalf("CountOutput OutputTokens = %d, want 8", got.OutputTokens)
	}
	if provider.countOutputCalls != 0 || provider.supportCalls != 0 || local.outputCalls != 1 {
		t.Fatalf("calls provider=%d provider_support=%d local=%d, want 0 0 1", provider.countOutputCalls, provider.supportCalls, local.outputCalls)
	}
}

type fakeProviderCounter struct {
	support ProviderSupport
	result  CountResult
	err     error

	afterSupports func()

	supportCtx context.Context
	countCtx   context.Context

	supportCalls     int
	countCalls       int
	countCallCalls   int
	countOutputCalls int
}

func (f *fakeProviderCounter) SupportsCount(ctx context.Context, _ ProviderCountInput) ProviderSupport {
	f.supportCalls++
	f.supportCtx = ctx
	if f.afterSupports != nil {
		f.afterSupports()
	}
	return f.support
}

func (f *fakeProviderCounter) CountText(ctx context.Context, _ CountTextInput) (CountResult, error) {
	f.countCalls++
	f.countCtx = ctx
	return f.result, f.err
}

func (f *fakeProviderCounter) CountCall(ctx context.Context, _ CountCallInput) (CountResult, error) {
	f.countCallCalls++
	f.countCtx = ctx
	return f.result, f.err
}

func (f *fakeProviderCounter) CountOutput(ctx context.Context, _ CountOutputInput) (CountResult, error) {
	f.countOutputCalls++
	f.countCtx = ctx
	return f.result, f.err
}

type fakeLocalCounter struct {
	result CountResult
	err    error

	ctx context.Context

	calls       int
	callCalls   int
	outputCalls int
}

func (f *fakeLocalCounter) CountText(ctx context.Context, _ CountTextInput) (CountResult, error) {
	f.calls++
	f.ctx = ctx
	return f.result, f.err
}

func (f *fakeLocalCounter) CountCall(ctx context.Context, _ CountCallInput) (CountResult, error) {
	f.callCalls++
	f.ctx = ctx
	return f.result, f.err
}

func (f *fakeLocalCounter) CountOutput(ctx context.Context, _ CountOutputInput) (CountResult, error) {
	f.outputCalls++
	f.ctx = ctx
	return f.result, f.err
}

func assertFallback(t *testing.T, got CountResult, reason FallbackReason, err error) {
	t.Helper()
	if len(got.Fallbacks) != 1 {
		t.Fatalf("fallback count = %d, want 1", len(got.Fallbacks))
	}
	if got.Fallbacks[0].Reason != reason {
		t.Fatalf("fallback reason = %q, want %q", got.Fallbacks[0].Reason, reason)
	}
	if !errors.Is(got.Fallbacks[0].Err, err) {
		t.Fatalf("fallback error = %v, want %v", got.Fallbacks[0].Err, err)
	}
}
