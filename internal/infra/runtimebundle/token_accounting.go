package runtimebundle

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingobs "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/observability"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	tiktokenlocal "github.com/aiproxer/aiproxer/internal/infra/tokenizers/tiktoken"
)

type tokenAccountingRuntime struct {
	Counter       *accountingapp.Service
	Preflight     *accountingpreflight.Checker
	StreamUsage   *accountingstream.Reconstructor
	Observability *accountingobs.Stats
	Admin         *accountingapp.Service
}

type processAccountingStores struct {
	// Observability is optional token-accounting telemetry. The legacy financial
	// token ledger is intentionally not composed after Phase 8 cutover.
	Observability *accountingobs.Stats
}

const defaultAccountingCountTimeout = 750 * time.Millisecond

func buildProcessAccountingStores(_ context.Context, cfg *config.Config, _ func() time.Time) (*processAccountingStores, error) {
	if cfg == nil || !cfg.Accounting.Enabled {
		return nil, nil
	}
	// accounting.ledger.* remains accepted for backward-compatible YAML, but
	// durable/memory financial token ledgers are no longer opened or written.
	out := &processAccountingStores{}
	if cfg.Accounting.Observability.Enabled {
		out.Observability = accountingobs.NewStats()
	}
	return out, nil
}

func bindTokenAccountingRuntime(stores *processAccountingStores, cfg *config.Config, backends map[string]execbackend.Backend) (*tokenAccountingRuntime, error) {
	if cfg == nil || !cfg.Accounting.Enabled || stores == nil {
		return nil, nil
	}
	provider, local, err := buildTokenCounters(cfg, backends)
	if err != nil {
		return nil, err
	}
	countTimeout := defaultAccountingCountTimeout
	if raw := strings.TrimSpace(cfg.Accounting.CountTimeout); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: accounting count_timeout: %w", err)
		}
		countTimeout = parsed
	}
	provider = timeoutProviderCounter{timeoutCounter: timeoutCounter[accountingapp.ProviderCounter]{inner: provider, timeout: countTimeout}}
	if local != nil {
		local = timeoutLocalCounter{inner: local, timeout: countTimeout}
	}
	counter := accountingapp.NewService(accountingapp.ServiceConfig{Mode: accountingMode(cfg.Accounting.Mode)}, provider, local)
	out := &tokenAccountingRuntime{
		Counter: counter, Observability: stores.Observability,
	}
	out.Preflight = accountingpreflight.NewChecker(counter, accountingpreflight.Config{
		Enabled: true, Mode: preflightMode(cfg.Accounting.Preflight.Mode),
		MaxInputTokens: cfg.Accounting.Preflight.MaxInputTokens, MaxOutputTokens: cfg.Accounting.Preflight.MaxOutputTokens,
		MaxContextTokens: cfg.Accounting.Preflight.MaxContextTokens, ClampMaxOutputTokens: cfg.Accounting.Preflight.ClampMaxOutputTokens,
		UnknownOutputPolicy: accountingpreflight.UnknownOutputPolicy(strings.ToLower(strings.TrimSpace(cfg.Accounting.Preflight.UnknownOutputPolicy))),
	})
	out.StreamUsage = accountingstream.New(counter, accountingstream.Config{})
	if cfg.Accounting.Admin.Enabled {
		out.Admin = counter
	}
	return out, nil
}

func buildTokenCounters(cfg *config.Config, backends map[string]execbackend.Backend) (accountingapp.ProviderCounter, accountingapp.LocalCounter, error) {
	mode := accountingMode(cfg.Accounting.Mode)
	provider := newBackendProviderCounter(backends)
	if mode == accountingapp.ModeProviderOnly && len(provider.counters) == 0 {
		return nil, nil, fmt.Errorf("runtimebundle: accounting provider_required requires at least one backend provider token counter")
	}
	if mode == accountingapp.ModeProviderOnly {
		return provider, nil, nil
	}
	fallback, err := tiktokenlocal.NewCounter(tiktokenlocal.Config{
		DefaultEncoding: cfg.Accounting.Tokenizer.DefaultEncoding, ModelMappings: cfg.Accounting.Tokenizer.ModelMappings,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("runtimebundle: accounting local tokenizer: %w", err)
	}
	return provider, newBackendLocalCounter(backends, fallback), nil
}

type backendLocalCounter struct {
	overrides map[string]accountingapp.LocalCounter
	fallback  accountingapp.LocalCounter
}

func newBackendLocalCounter(backends map[string]execbackend.Backend, fallback accountingapp.LocalCounter) accountingapp.LocalCounter {
	overrides := map[string]accountingapp.LocalCounter{}
	for id, be := range backends {
		if be.LocalCounter != nil {
			overrides[id] = be.LocalCounter
		}
	}
	if len(overrides) == 0 {
		return fallback
	}
	return &backendLocalCounter{overrides: overrides, fallback: fallback}
}

func (c *backendLocalCounter) lookup(backend string) accountingapp.LocalCounter {
	if counter, ok := c.overrides[backend]; ok {
		return counter
	}
	return c.fallback
}

func (c *backendLocalCounter) CountText(ctx context.Context, input accountingapp.CountTextInput) (accountingapp.CountResult, error) {
	counter := c.lookup(input.Backend)
	if counter == nil {
		return accountingapp.CountResult{}, accountingapp.ErrLocalUnavailable
	}
	return counter.CountText(ctx, input)
}

func (c *backendLocalCounter) CountCall(ctx context.Context, input accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	counter := c.lookup(input.Backend)
	if counter == nil {
		return accountingapp.CountResult{}, accountingapp.ErrLocalUnavailable
	}
	return counter.CountCall(ctx, input)
}

func (c *backendLocalCounter) CountOutput(ctx context.Context, input accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	counter := c.lookup(input.Backend)
	if counter == nil {
		return accountingapp.CountResult{}, accountingapp.ErrLocalUnavailable
	}
	return counter.CountOutput(ctx, input)
}

type backendProviderCounter struct {
	counters map[string]accountingapp.ProviderCounter
}

func newBackendProviderCounter(backends map[string]execbackend.Backend) *backendProviderCounter {
	out := &backendProviderCounter{counters: map[string]accountingapp.ProviderCounter{}}
	for id, be := range backends {
		if be.ProviderCounter != nil {
			out.counters[id] = be.ProviderCounter
		}
	}
	return out
}

func (c *backendProviderCounter) lookup(backend string) (accountingapp.ProviderCounter, bool) {
	v, ok := c.counters[backend]
	return v, ok
}

func (c *backendProviderCounter) SupportsCount(ctx context.Context, input accountingapp.ProviderCountInput) accountingapp.ProviderSupport {
	if counter, ok := c.lookup(input.Backend); ok {
		return counter.SupportsCount(ctx, input)
	}
	return accountingapp.ProviderSupport{Status: accountingapp.SupportStatusUnsupported, Message: "backend has no provider token counter"}
}

func (c *backendProviderCounter) CountText(ctx context.Context, input accountingapp.CountTextInput) (accountingapp.CountResult, error) {
	if counter, ok := c.lookup(input.Backend); ok {
		return counter.CountText(ctx, input)
	}
	return accountingapp.CountResult{}, accountingapp.ErrProviderUnsupported
}

func (c *backendProviderCounter) CountCall(ctx context.Context, input accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	if counter, ok := c.lookup(input.Backend); ok {
		return counter.CountCall(ctx, input)
	}
	return accountingapp.CountResult{}, accountingapp.ErrProviderUnsupported
}

func (c *backendProviderCounter) CountOutput(ctx context.Context, input accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	if counter, ok := c.lookup(input.Backend); ok {
		return counter.CountOutput(ctx, input)
	}
	return accountingapp.CountResult{}, accountingapp.ErrProviderUnsupported
}

func accountingMode(raw string) accountingapp.Mode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "local_only":
		return accountingapp.ModeLocalOnly
	case "provider_required":
		return accountingapp.ModeProviderOnly
	default:
		return accountingapp.ModeProviderFirst
	}
}

func preflightMode(raw string) accountingpreflight.Mode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "required":
		return accountingpreflight.ModeStrict
	default:
		return accountingpreflight.ModeAdvisory
	}
}

var (
	_ accountingpreflight.Counter = (*accountingapp.Service)(nil)
	_ accountingstream.Counter    = (*accountingapp.Service)(nil)
)

type timeoutCountOps interface {
	CountText(context.Context, accountingapp.CountTextInput) (accountingapp.CountResult, error)
	CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error)
	CountOutput(context.Context, accountingapp.CountOutputInput) (accountingapp.CountResult, error)
}

type timeoutCounter[T timeoutCountOps] struct {
	inner   T
	timeout time.Duration
}

func (c timeoutCounter[T]) CountText(ctx context.Context, input accountingapp.CountTextInput) (accountingapp.CountResult, error) {
	return withCountTimeout(ctx, c.timeout, func(ctx context.Context) (accountingapp.CountResult, error) {
		return c.inner.CountText(ctx, input)
	})
}

func (c timeoutCounter[T]) CountCall(ctx context.Context, input accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return withCountTimeout(ctx, c.timeout, func(ctx context.Context) (accountingapp.CountResult, error) {
		return c.inner.CountCall(ctx, input)
	})
}

func (c timeoutCounter[T]) CountOutput(ctx context.Context, input accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return withCountTimeout(ctx, c.timeout, func(ctx context.Context) (accountingapp.CountResult, error) {
		return c.inner.CountOutput(ctx, input)
	})
}

type timeoutProviderCounter struct {
	timeoutCounter[accountingapp.ProviderCounter]
}

func (c timeoutProviderCounter) SupportsCount(ctx context.Context, input accountingapp.ProviderCountInput) accountingapp.ProviderSupport {
	return c.inner.SupportsCount(ctx, input)
}

type timeoutLocalCounter = timeoutCounter[accountingapp.LocalCounter]

func withCountTimeout(ctx context.Context, timeout time.Duration, fn func(context.Context) (accountingapp.CountResult, error)) (accountingapp.CountResult, error) {
	if timeout <= 0 {
		return fn(ctx)
	}
	child, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return fn(child)
}
