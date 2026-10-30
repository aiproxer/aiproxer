package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type preflightCountFunc func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error)

func (f preflightCountFunc) CountCall(ctx context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return f(ctx, in)
}

type tokenAccountingCatalogResolver struct{}

func (tokenAccountingCatalogResolver) Resolve(ctx context.Context, cand routing.AttemptCandidate, call aipapi.Call, backend aipapi.BackendCaps) modelcatalog.EffectiveFacts {
	_ = ctx
	_ = call
	return modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			Source:       modelcatalog.FactSourceCatalog,
			MatchKind:    modelcatalog.MatchExact,
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 40},
		},
		BackendCaps:   backend,
		EffectiveCaps: backend,
		Matched:       true,
		Match: modelcatalog.MatchResult{
			Kind:       modelcatalog.MatchExact,
			InputModel: cand.Primary.Model,
			MatchedID:  cand.Primary.Model,
		},
	}
}

func TestExecutorRequiredPreflightRejectsBeforeBackendOpen(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Preflight = accountingpreflight.NewChecker(preflightCountFunc(func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
		return accountingapp.CountResult{}, errors.New("counter unavailable")
	}), accountingpreflight.Config{Enabled: true, Mode: accountingpreflight.ModeStrict})
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	_, err = ex.Execute(context.Background(), &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4o-mini"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	})
	if err == nil {
		t.Fatal("expected preflight error")
	}
	if atomic.LoadInt32(&opens) != 0 {
		t.Fatalf("Backend.Open called %d times, want 0", opens)
	}
}

func TestExecutorAdvisoryPreflightUnavailableProceeds(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Preflight = accountingpreflight.NewChecker(preflightCountFunc(func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
		return accountingapp.CountResult{}, errors.New("counter unavailable")
	}), accountingpreflight.Config{Enabled: true, Mode: accountingpreflight.ModeAdvisory})
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4o-mini"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&opens) != 1 {
		t.Fatalf("Backend.Open called %d times, want 1", opens)
	}
}

func TestExecutorPreflightOutputClampAppliesBeforeBackendOpen(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var openedMaxOutput int
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Preflight = accountingpreflight.NewChecker(preflightCountFunc(func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
		return accountingapp.CountResult{InputTokens: 3, TotalTokens: 3}, nil
	}), accountingpreflight.Config{
		Enabled:              true,
		Mode:                 accountingpreflight.ModeStrict,
		MaxOutputTokens:      5,
		ClampMaxOutputTokens: true,
	})
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps:                    aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			EnforcesMaxOutputTokens: true,
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if call.Options.MaxOutputTokens == nil {
					t.Fatal("Backend.Open received nil MaxOutputTokens")
				}
				openedMaxOutput = *call.Options.MaxOutputTokens
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	requestedMaxOutput := 20
	stream, err := ex.Execute(context.Background(), &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4o-mini"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
		Options:  aipapi.GenerationOptions{MaxOutputTokens: &requestedMaxOutput},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if openedMaxOutput != 5 {
		t.Fatalf("backend MaxOutputTokens = %d, want clamped 5", openedMaxOutput)
	}
	if requestedMaxOutput != 20 {
		t.Fatalf("original request MaxOutputTokens mutated to %d, want 20", requestedMaxOutput)
	}
}

func TestExecutorRequestSizeRoutingUsesTokenAccountingPreflightWhenEnabled(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opened string
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.CatalogResolver = tokenAccountingCatalogResolver{}
	ex.Preflight = accountingpreflight.NewChecker(preflightCountFunc(func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
		return accountingapp.CountResult{InputTokens: 35, TotalTokens: 35}, nil
	}), accountingpreflight.Config{Enabled: true, Mode: accountingpreflight.ModeAdvisory})
	ex.RequestTokenEstimator = fixedRequestTokenEstimator{available: true, tokens: 5}
	ex.Backends = map[string]execbackend.Backend{
		"small": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opened = "small"
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
			},
		},
		"large": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opened = "large"
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "[max_context=10]small:gpt-4o-mini|[max_context=100]large:gpt-4o-mini"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(strings.Repeat("x", 100))}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if opened != "large" {
		t.Fatalf("opened backend = %q, want large", opened)
	}
}

func TestExecutorRequestSizePreflightUsesHybridParallelLeaf(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var countedMu sync.Mutex
	var counted []string
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Preflight = accountingpreflight.NewChecker(preflightCountFunc(func(_ context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
		countedMu.Lock()
		defer countedMu.Unlock()
		counted = append(counted, in.Backend+":"+in.Model)
		return accountingapp.CountResult{InputTokens: 3, TotalTokens: 3}, nil
	}), accountingpreflight.Config{Enabled: true, Mode: accountingpreflight.ModeAdvisory})
	ex.Backends = map[string]execbackend.Backend{
		"exec-a": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
			},
		},
		"exec-b": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
			},
		},
		"thinker": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "[max_context=100]exec-a:small![max_context=100]exec-b:large^[thinker]thinker:plan"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	countedMu.Lock()
	countedCopy := append([]string(nil), counted...)
	countedMu.Unlock()
	if len(countedCopy) == 0 || countedCopy[0] != "exec-a:small" {
		t.Fatalf("first preflight counted %v, want first exec-a:small", countedCopy)
	}
}
