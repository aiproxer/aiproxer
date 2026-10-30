package runtimebundle_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

func TestBuild_collectsBackendCloseAfterConstruction(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var closeOrder []string
	reg := pluginreg.NewRegistry()
	registerClosableBackend(t, reg, "closer-a", &mu, &closeOrder, nil)
	registerClosableBackend(t, reg, "closer-b", &mu, &closeOrder, nil)

	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{Kind: "closer-a", ID: "a", Enabled: true, Config: empty},
			{Kind: "closer-b", ID: "b", Enabled: true, Config: empty},
		}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if built.Ledger().Len() < 2 {
		t.Fatalf("Ledger.Len=%d, want at least backend closers", built.Ledger().Len())
	}

	if err := built.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	mu.Lock()
	got := append([]string(nil), closeOrder...)
	mu.Unlock()
	want := []string{"closer-b", "closer-a"}
	if !slices.Equal(got, want) {
		t.Fatalf("close order=%v want %v", got, want)
	}
}

func TestBuild_nilBackendCloseRemainsNoOp(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("nil-close", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"nil-close"},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{Kind: "nil-close", ID: "only", Enabled: true, Config: empty},
		}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	be, ok := built.Executor().Backends["only"]
	if !ok {
		t.Fatal("expected backend instance")
	}
	if be.Close != nil {
		t.Fatal("nil Close must remain nil for backends without persistent resources")
	}
	if err := built.Close(); err != nil {
		t.Fatalf("existing closers must remain safe: %v", err)
	}
}

func TestBuild_backendConstructionFailureClosesCreatedReverseOrder(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var closeOrder []string
	closeErr := errors.New("close boom")
	constructErr := errors.New("construct boom")

	reg := pluginreg.NewRegistry()
	registerClosableBackend(t, reg, "ok-a", &mu, &closeOrder, closeErr)
	registerClosableBackend(t, reg, "ok-b", &mu, &closeOrder, nil)
	if err := reg.RegisterBackend("fail-c", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, constructErr
	}); err != nil {
		t.Fatal(err)
	}

	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{Kind: "ok-a", ID: "a", Enabled: true, Config: empty},
			{Kind: "ok-b", ID: "b", Enabled: true, Config: empty},
			{Kind: "fail-c", ID: "c", Enabled: true, Config: empty},
		}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err == nil {
		t.Fatal("expected construction failure")
	}
	if !errors.Is(err, constructErr) {
		t.Fatalf("originating construction error must remain visible, got %v", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("close error must be joined without masking construction error, got %v", err)
	}
	if !strings.Contains(err.Error(), "backend instance c") {
		t.Fatalf("error %q missing construction context", err.Error())
	}

	mu.Lock()
	got := append([]string(nil), closeOrder...)
	mu.Unlock()
	want := []string{"ok-b", "ok-a"}
	if !slices.Equal(got, want) {
		t.Fatalf("rollback close order=%v want %v", got, want)
	}
}

func TestBuild_laterModelRuntimeFailureRollsBackBackendClosers(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var closeOrder []string
	reg := pluginreg.NewRegistry()
	registerClosableBackend(t, reg, "rb-a", &mu, &closeOrder, nil)
	registerClosableBackend(t, reg, "rb-b", &mu, &closeOrder, nil)

	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Accounting: config.AccountingConfig{StrictAuthoritative: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{Kind: "rb-a", ID: "a", Enabled: true, Config: empty},
			{Kind: "rb-b", ID: "b", Enabled: true, Config: empty},
		}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err == nil {
		t.Fatal("expected later model-runtime assembly failure")
	}
	if !strings.Contains(err.Error(), "strict_authoritative requires billing finalizer") {
		t.Fatalf("originating assembly error must remain visible, got %v", err)
	}

	mu.Lock()
	got := append([]string(nil), closeOrder...)
	mu.Unlock()
	want := []string{"rb-b", "rb-a"}
	if !slices.Equal(got, want) {
		t.Fatalf("rollback close order=%v want %v", got, want)
	}
}

func registerClosableBackend(
	t *testing.T,
	reg *pluginreg.Registry,
	factoryID string,
	mu *sync.Mutex,
	closeOrder *[]string,
	closeErr error,
) {
	t.Helper()
	if err := reg.RegisterBackend(factoryID, func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{factoryID},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
			Close: func() error {
				mu.Lock()
				*closeOrder = append(*closeOrder, factoryID)
				mu.Unlock()
				return closeErr
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
}
