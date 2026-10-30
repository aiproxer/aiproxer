package modelregistry_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func TestRegistry_LookupByCanonicalIDPreservesBackendOrder(t *testing.T) {
	t.Parallel()

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "openrouter",
			Kind:            "openrouter",
			BackendPrefixes: []string{"openrouter"},
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "openai/gpt-4o", NativeID: "openai/gpt-4o"},
			}},
		},
		{
			BackendID:       "openai-direct",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"},
			}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	reg := built.Registry

	got, ok := reg.Lookup("openai/gpt-4o")
	if !ok {
		t.Fatal("Lookup(openai/gpt-4o) ok = false")
	}
	if len(got) != 2 {
		t.Fatalf("len(Lookup) = %d, want 2", len(got))
	}
	if got[0].BackendID != "openrouter" || got[1].BackendID != "openai-direct" {
		t.Fatalf("backend order = %q, %q", got[0].BackendID, got[1].BackendID)
	}
	if got[0].NativeID != "openai/gpt-4o" || got[1].NativeID != "gpt-4o" {
		t.Fatalf("native ids = %q, %q", got[0].NativeID, got[1].NativeID)
	}
}

func TestBuildAppliesFetchTimeoutPerBackend(t *testing.T) {
	t.Parallel()

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "first",
			Kind:            "test",
			BackendPrefixes: []string{"test-first"},
			FetchTimeout:    time.Second,
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "vendor/first", NativeID: "first"},
			}},
		},
		{
			BackendID:       "second",
			Kind:            "test",
			BackendPrefixes: []string{"test-second"},
			FetchTimeout:    time.Second,
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "vendor/second", NativeID: "second"},
			}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := built.Registry.All(); len(got) != 2 {
		t.Fatalf("models len = %d, want 2", len(got))
	}
}

func TestBuildPerBackendFetchTimeoutOmitsSlowBackend(t *testing.T) {
	t.Parallel()

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{{
		BackendID:       "slow",
		Kind:            "test",
		BackendPrefixes: []string{"test-slow"},
		FetchTimeout:    10 * time.Millisecond,
		Provider:        blockingUntilCancelInventoryProvider{},
	}}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v, want nil (fail-soft)", err)
	}
	if got := built.Registry.All(); len(got) != 0 {
		t.Fatalf("models len = %d, want 0", len(got))
	}
	if len(built.Discoveries) != 1 {
		t.Fatalf("discoveries len = %d, want 1", len(built.Discoveries))
	}
	d := built.Discoveries[0]
	if d.Status != modelinventory.DiscoveryStatusUnavailable {
		t.Fatalf("Status = %q", d.Status)
	}
	if d.ErrorCode != modelinventory.ErrorCodeTimeout {
		t.Fatalf("ErrorCode = %q, want timeout", d.ErrorCode)
	}
}

func TestRegistry_LookupReturnsDefensiveCopy(t *testing.T) {
	t.Parallel()

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "openai",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"},
			}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	reg := built.Registry

	got, ok := reg.Lookup("openai/gpt-4o")
	if !ok {
		t.Fatal("Lookup(openai/gpt-4o) ok = false")
	}
	got[0].BackendID = "mutated"

	got2, ok := reg.Lookup("openai/gpt-4o")
	if !ok {
		t.Fatal("second Lookup(openai/gpt-4o) ok = false")
	}
	if got2[0].BackendID != "openai" {
		t.Fatalf("Lookup returned mutable backing slice: backend = %q", got2[0].BackendID)
	}
}

func TestRegistry_ConcurrentLookup(t *testing.T) {
	t.Parallel()

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "openai",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"},
				{CanonicalID: "openai/gpt-4.1", NativeID: "gpt-4.1"},
			}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	reg := built.Registry

	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			for range 1000 {
				got, ok := reg.Lookup("openai/gpt-4o")
				if !ok || len(got) != 1 || got[0].BackendID != "openai" {
					t.Errorf("Lookup() = %+v, %v", got, ok)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestBuildRejectsInvalidInventory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []modelregistry.BackendInventory
		want error
	}{
		{
			name: "nil provider",
			in: []modelregistry.BackendInventory{{
				BackendID:       "openai",
				Kind:            "openai-responses",
				BackendPrefixes: []string{"openai-responses"},
			}},
			want: modelregistry.ErrMissingProvider,
		},
		{
			name: "missing backend prefix",
			in: []modelregistry.BackendInventory{{
				BackendID: "openai",
				Kind:      "openai-responses",
				Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
					{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"},
				}},
			}},
			want: modelregistry.ErrMissingBackendPrefix,
		},
		{
			name: "missing backend id",
			in: []modelregistry.BackendInventory{{
				Kind:            "openai-responses",
				BackendPrefixes: []string{"openai-responses"},
				Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
					{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"},
				}},
			}},
			want: modelregistry.ErrInvalidModel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := modelregistry.Build(context.Background(), tt.in, nil)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Build() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestBuild_failSoftMixedSuccessAndFailure(t *testing.T) {
	t.Parallel()

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "good",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider: modelinventory.StaticProvider{
				Source: modelinventory.SourceStaticInline,
				Models: []modelinventory.Model{{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"}},
			},
		},
		{
			BackendID:       "bad",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        modelinventory.ErrorProvider{Err: errors.New("secret token=xyz")},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := built.Registry.All(); len(got) != 1 || got[0].BackendID != "good" {
		t.Fatalf("models = %+v, want only good", got)
	}
	if len(built.Discoveries) != 2 {
		t.Fatalf("discoveries len = %d", len(built.Discoveries))
	}
	byID := map[string]modelregistry.BackendDiscovery{}
	for _, d := range built.Discoveries {
		byID[d.BackendID] = d
	}
	if byID["good"].Status != modelinventory.DiscoveryStatusOK || byID["good"].ModelCount != 1 {
		t.Fatalf("good discovery = %+v", byID["good"])
	}
	if byID["bad"].Status != modelinventory.DiscoveryStatusUnavailable {
		t.Fatalf("bad status = %q", byID["bad"].Status)
	}
	if byID["bad"].ErrorCode != modelinventory.ErrorCodeUnavailable {
		t.Fatalf("bad ErrorCode = %q", byID["bad"].ErrorCode)
	}
	if strings.Contains(byID["bad"].ErrorCode, "token=") {
		t.Fatalf("ErrorCode leaked raw text: %q", byID["bad"].ErrorCode)
	}
}

func TestBuild_failSoftLoadFailure_logsOnce(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "good",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider: modelinventory.StaticProvider{
				Models: []modelinventory.Model{{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"}},
			},
		},
		{
			BackendID:       "bad",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        modelinventory.ErrorProvider{Err: errors.New("secret token=xyz")},
		},
	}, logger)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(built.Registry.All()) != 1 {
		t.Fatalf("models = %+v", built.Registry.All())
	}
	got := logBuf.String()
	if !strings.Contains(got, "modelregistry: inventory load failed") {
		t.Fatalf("expected load-failed log, got %q", got)
	}
	if !strings.Contains(got, `"backend_id":"bad"`) {
		t.Fatalf("expected backend_id attr, got %q", got)
	}
	if !strings.Contains(got, `"error_code":"unavailable"`) {
		t.Fatalf("expected error_code attr, got %q", got)
	}
}

func TestBuild_nilLoggerDoesNotPanic(t *testing.T) {
	t.Parallel()

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "bad",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        modelinventory.ErrorProvider{Err: errors.New("boom")},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(built.Registry.All()) != 0 {
		t.Fatalf("models = %+v, want empty", built.Registry.All())
	}
	if len(built.Discoveries) != 1 || built.Discoveries[0].ErrorCode != modelinventory.ErrorCodeUnavailable {
		t.Fatalf("discoveries = %+v", built.Discoveries)
	}
}

func TestBuild_failSoftInvalidInventory_logsOnce(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "good",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider: modelinventory.StaticProvider{
				Models: []modelinventory.Model{{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"}},
			},
		},
		{
			BackendID:       "corrupt",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider: modelinventory.StaticProvider{
				Models: []modelinventory.Model{{CanonicalID: "", NativeID: "x"}},
			},
		},
	}, logger)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(built.Registry.All()) != 1 {
		t.Fatalf("models = %+v", built.Registry.All())
	}
	got := logBuf.String()
	if !strings.Contains(got, "modelregistry: invalid inventory omitted") {
		t.Fatalf("expected invalid-inventory log, got %q", got)
	}
	if !strings.Contains(got, `"backend_id":"corrupt"`) {
		t.Fatalf("expected backend_id attr, got %q", got)
	}
	if !strings.Contains(got, `"error_code":"invalid_inventory"`) {
		t.Fatalf("expected error_code attr, got %q", got)
	}
}

func TestBuild_failSoftMixedSuccessAndEmpty(t *testing.T) {
	t.Parallel()

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "good",
			Kind:            "test",
			BackendPrefixes: []string{"test-good"},
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "vendor/a", NativeID: "a"},
			}},
		},
		{
			BackendID:       "empty",
			Kind:            "test",
			BackendPrefixes: []string{"test-empty"},
			Provider:        modelinventory.StaticProvider{Source: modelinventory.SourceRemote, Models: nil},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := built.Registry.All(); len(got) != 1 || got[0].BackendID != "good" {
		t.Fatalf("models = %+v", got)
	}
	byID := map[string]modelregistry.BackendDiscovery{}
	for _, d := range built.Discoveries {
		byID[d.BackendID] = d
	}
	if byID["empty"].Status != modelinventory.DiscoveryStatusEmpty {
		t.Fatalf("empty status = %q", byID["empty"].Status)
	}
	if byID["empty"].ErrorCode != modelinventory.ErrorCodeEmpty {
		t.Fatalf("empty ErrorCode = %q", byID["empty"].ErrorCode)
	}
}

func TestBuild_allUnavailableSucceedsWithEmptyRegistry(t *testing.T) {
	t.Parallel()

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "a",
			Kind:            "test",
			BackendPrefixes: []string{"test-a"},
			Provider:        modelinventory.ErrorProvider{Err: errors.New("down")},
		},
		{
			BackendID:       "b",
			Kind:            "test",
			BackendPrefixes: []string{"test-b"},
			Provider:        modelinventory.StaticProvider{Models: nil},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := built.Registry.All(); len(got) != 0 {
		t.Fatalf("models len = %d, want 0", len(got))
	}
	if len(built.Discoveries) != 2 {
		t.Fatalf("discoveries len = %d", len(built.Discoveries))
	}
}

func TestBuild_failSoftInvalidInventoryKeepsValidSibling(t *testing.T) {
	t.Parallel()

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "good",
			Kind:            "test",
			BackendPrefixes: []string{"test-good"},
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "vendor/a", NativeID: "a"},
			}},
		},
		{
			BackendID:       "corrupt",
			Kind:            "test",
			BackendPrefixes: []string{"test-corrupt"},
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "", NativeID: "secret-native-id"},
			}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := built.Registry.All(); len(got) != 1 || got[0].BackendID != "good" {
		t.Fatalf("models = %+v, want only good", got)
	}
	byID := map[string]modelregistry.BackendDiscovery{}
	for _, d := range built.Discoveries {
		byID[d.BackendID] = d
	}
	if byID["corrupt"].Status != modelinventory.DiscoveryStatusUnavailable {
		t.Fatalf("corrupt status = %q", byID["corrupt"].Status)
	}
	if byID["corrupt"].ErrorCode != modelinventory.ErrorCodeInvalidInventory {
		t.Fatalf("corrupt ErrorCode = %q", byID["corrupt"].ErrorCode)
	}
	if byID["corrupt"].ModelCount != 0 {
		t.Fatalf("corrupt ModelCount = %d", byID["corrupt"].ModelCount)
	}
	if strings.Contains(byID["corrupt"].ErrorCode, "secret") {
		t.Fatalf("ErrorCode leaked raw text: %q", byID["corrupt"].ErrorCode)
	}
}

func TestBuild_failSoftInvalidInventoryDoesNotAcceptDuringBuild(t *testing.T) {
	t.Parallel()

	corrupt := &trackingAcceptInventoryProvider{models: []modelinventory.Model{
		{CanonicalID: "vendor/good-first", NativeID: "good-first"},
		{CanonicalID: "not-a-vendor-model", NativeID: "bad"},
		{CanonicalID: "vendor/good-after", NativeID: "good-after"},
	}}
	good := &trackingAcceptInventoryProvider{models: []modelinventory.Model{
		{CanonicalID: "vendor/a", NativeID: "a"},
	}}

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "good",
			Kind:            "test",
			BackendPrefixes: []string{"test-good"},
			Provider:        good,
		},
		{
			BackendID:       "corrupt",
			Kind:            "test",
			BackendPrefixes: []string{"test-corrupt"},
			Provider:        corrupt,
		},
	}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := built.Registry.All(); len(got) != 1 || got[0].BackendID != "good" {
		t.Fatalf("models = %+v, want only good", got)
	}
	// Allowlists stay untouched during Build; Runtime.publish/syncAllowlistsUnion commits.
	if len(corrupt.Accepted()) != 0 {
		t.Fatalf("corrupt AcceptInventory during Build = %+v, want none", corrupt.Accepted())
	}
	if len(good.Accepted()) != 0 {
		t.Fatalf("good AcceptInventory during Build = %+v, want none", good.Accepted())
	}
}

func TestBuild_failSoftInvalidInventoryOmitsAllRowsAtomically(t *testing.T) {
	t.Parallel()

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{{
		BackendID:       "mixed",
		Kind:            "test",
		BackendPrefixes: []string{"test-mixed"},
		Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
			{CanonicalID: "vendor/good-first", NativeID: "good-first"},
			{CanonicalID: "not-a-vendor-model", NativeID: "bad"},
			{CanonicalID: "vendor/good-after", NativeID: "good-after"},
		}},
	}}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := built.Registry.All(); len(got) != 0 {
		t.Fatalf("models = %+v, want atomic omission of all rows", got)
	}
	if len(built.Discoveries) != 1 {
		t.Fatalf("discoveries len = %d", len(built.Discoveries))
	}
	d := built.Discoveries[0]
	if d.Status != modelinventory.DiscoveryStatusUnavailable || d.ErrorCode != modelinventory.ErrorCodeInvalidInventory || d.ModelCount != 0 {
		t.Fatalf("discovery = %+v", d)
	}
}

func TestBuild_failSoftAllInvalidSucceedsWithEmptyRegistry(t *testing.T) {
	t.Parallel()

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "a",
			Kind:            "test",
			BackendPrefixes: []string{"test-a"},
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "", NativeID: "a"},
			}},
		},
		{
			BackendID:       "b",
			Kind:            "test",
			BackendPrefixes: []string{"test-b"},
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "gpt-4o", NativeID: "gpt-4o"},
			}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := built.Registry.All(); len(got) != 0 {
		t.Fatalf("models len = %d, want 0", len(got))
	}
	if len(built.Discoveries) != 2 {
		t.Fatalf("discoveries len = %d", len(built.Discoveries))
	}
	for _, d := range built.Discoveries {
		if d.Status != modelinventory.DiscoveryStatusUnavailable || d.ErrorCode != modelinventory.ErrorCodeInvalidInventory {
			t.Fatalf("discovery = %+v", d)
		}
	}
}

func TestBuild_failSoftEmptyNativeIDIsInvalidInventory(t *testing.T) {
	t.Parallel()

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{{
		BackendID:       "bad",
		Kind:            "test",
		BackendPrefixes: []string{"test-bad"},
		Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
			{CanonicalID: "vendor/model", NativeID: ""},
		}},
	}}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := built.Registry.All(); len(got) != 0 {
		t.Fatalf("models = %+v", got)
	}
	if built.Discoveries[0].ErrorCode != modelinventory.ErrorCodeInvalidInventory {
		t.Fatalf("ErrorCode = %q", built.Discoveries[0].ErrorCode)
	}
}

func TestBuild_parentContextCancelIsFatal(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	provider := cancelAwareInventoryProvider{
		started: started,
		models:  []modelinventory.Model{{CanonicalID: "vendor/a", NativeID: "a"}},
	}
	go func() {
		<-started
		cancel()
	}()

	_, err := modelregistry.Build(ctx, []modelregistry.BackendInventory{{
		BackendID:       "slow",
		Kind:            "test",
		BackendPrefixes: []string{"test-slow"},
		Provider:        provider,
	}}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Build() error = %v, want context.Canceled", err)
	}
}

func TestBuild_failSoftPerBackendTimeoutKeepsSuccessfulSibling(t *testing.T) {
	t.Parallel()

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "good",
			Kind:            "test",
			BackendPrefixes: []string{"test-good"},
			FetchTimeout:    time.Second,
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "vendor/good", NativeID: "good"},
			}},
		},
		{
			BackendID:       "slow",
			Kind:            "test",
			BackendPrefixes: []string{"test-slow"},
			FetchTimeout:    10 * time.Millisecond,
			Provider:        blockingUntilCancelInventoryProvider{},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := built.Registry.All(); len(got) != 1 || got[0].BackendID != "good" {
		t.Fatalf("models = %+v, want only good", got)
	}
	byID := map[string]modelregistry.BackendDiscovery{}
	for _, d := range built.Discoveries {
		byID[d.BackendID] = d
	}
	if byID["good"].Status != modelinventory.DiscoveryStatusOK {
		t.Fatalf("good status = %q", byID["good"].Status)
	}
	if byID["slow"].Status != modelinventory.DiscoveryStatusUnavailable || byID["slow"].ErrorCode != modelinventory.ErrorCodeTimeout {
		t.Fatalf("slow discovery = %+v", byID["slow"])
	}
}

// trackingAcceptInventoryProvider mirrors ACP TrackingInventory: LoadModels
// only fetches; AcceptInventory is how core commits the allowlist after publish.
type trackingAcceptInventoryProvider struct {
	models   []modelinventory.Model
	accepted []modelinventory.Model
}

func (p *trackingAcceptInventoryProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	return modelinventory.Snapshot{
		Source: modelinventory.SourceRemote,
		Models: append([]modelinventory.Model(nil), p.models...),
	}, nil
}

func (p *trackingAcceptInventoryProvider) AcceptInventory(models []modelinventory.Model) {
	p.accepted = append([]modelinventory.Model(nil), models...)
	if p.accepted == nil {
		p.accepted = []modelinventory.Model{}
	}
}

func (p *trackingAcceptInventoryProvider) Accepted() []modelinventory.Model {
	return append([]modelinventory.Model(nil), p.accepted...)
}

// blockingUntilCancelInventoryProvider never succeeds: it waits for ctx cancel
// (e.g. per-backend FetchTimeout) and returns ctx.Err(). Used to exercise timeout
// classification without racing wall-clock delay vs deadline.
type blockingUntilCancelInventoryProvider struct{}

func (blockingUntilCancelInventoryProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	<-ctx.Done()
	return modelinventory.Snapshot{}, ctx.Err()
}

type cancelAwareInventoryProvider struct {
	started chan struct{}
	models  []modelinventory.Model
}

func (p cancelAwareInventoryProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	close(p.started)
	<-ctx.Done()
	return modelinventory.Snapshot{}, ctx.Err()
}
