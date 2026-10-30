package modelinventory_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func TestStaticProvider_LoadModels(t *testing.T) {
	t.Parallel()

	want := []modelinventory.Model{
		{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"},
		{CanonicalID: "anthropic/claude-sonnet", NativeID: "claude-sonnet"},
	}
	p := modelinventory.StaticProvider{Source: modelinventory.SourceStaticInline, Models: want}

	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels() error = %v", err)
	}
	if snap.Source != modelinventory.SourceStaticInline {
		t.Fatalf("Source = %q", snap.Source)
	}
	if len(snap.Models) != len(want) {
		t.Fatalf("models len = %d, want %d", len(snap.Models), len(want))
	}
	snap.Models[0].CanonicalID = "mutated/model"

	snap2, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels() second error = %v", err)
	}
	if snap2.Models[0].CanonicalID != want[0].CanonicalID {
		t.Fatalf("provider returned mutable backing slice: got %q", snap2.Models[0].CanonicalID)
	}
}

func TestStaticProvider_LoadModelsNilContext(t *testing.T) {
	t.Parallel()

	//nolint:staticcheck // defensive nil-context handling is part of the provider contract
	_, err := (modelinventory.StaticProvider{}).LoadModels(nil)
	if !errors.Is(err, modelinventory.ErrNilContext) {
		t.Fatalf("LoadModels(nil) error = %v, want ErrNilContext", err)
	}
}

func TestStaticProvider_LoadModelsReportsLoadTimeWhenLoadedAtUnset(t *testing.T) {
	t.Parallel()

	before := time.Now()
	snap, err := modelinventory.StaticProvider{
		Models: []modelinventory.Model{{CanonicalID: "vendor/model", NativeID: "model"}},
	}.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	if snap.LoadedAt.Before(before) || snap.LoadedAt.After(after) {
		t.Fatalf("LoadedAt = %s, want between %s and %s", snap.LoadedAt, before, after)
	}
}

func TestStaticProvider_ImplementsStaticInventoryMarker(t *testing.T) {
	t.Parallel()

	var _ modelinventory.StaticInventory = modelinventory.StaticProvider{}
}

func TestErrorProvider_ImplementsStaticInventoryMarker(t *testing.T) {
	t.Parallel()

	var _ modelinventory.StaticInventory = modelinventory.ErrorProvider{}
	if !(modelinventory.ErrorProvider{}).StaticInventory() {
		t.Fatal("ErrorProvider.StaticInventory() = false, want true")
	}
}

func TestErrorProvider_LoadModelsReturnsOperationalError(t *testing.T) {
	t.Parallel()

	underlying := errors.New("secret construction failure: token=abc")
	_, err := modelinventory.ErrorProvider{Err: underlying}.LoadModels(context.Background())
	if !modelinventory.IsOperational(err) {
		t.Fatalf("IsOperational = false for %v", err)
	}
	var op *modelinventory.OperationalError
	if !errors.As(err, &op) {
		t.Fatalf("error type = %T, want *OperationalError", err)
	}
	if op.Code != modelinventory.ErrorCodeUnavailable {
		t.Fatalf("Code = %q", op.Code)
	}
	if !errors.Is(err, underlying) {
		t.Fatalf("Unwrap missing underlying: %v", err)
	}
}

func TestErrorProvider_LoadModelsNilErrIsOperationalUnavailable(t *testing.T) {
	t.Parallel()

	_, err := (modelinventory.ErrorProvider{}).LoadModels(context.Background())
	if !modelinventory.IsOperational(err) {
		t.Fatalf("IsOperational = false for %v", err)
	}
	disc := modelinventory.DiscoveryFromLoadError(err)
	if disc.Status != modelinventory.DiscoveryStatusUnavailable {
		t.Fatalf("Status = %q", disc.Status)
	}
	if disc.ErrorCode != modelinventory.ErrorCodeUnavailable {
		t.Fatalf("ErrorCode = %q", disc.ErrorCode)
	}
	if disc.ModelCount != 0 {
		t.Fatalf("ModelCount = %d", disc.ModelCount)
	}
}

func TestErrorProvider_LoadModelsHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := modelinventory.ErrorProvider{Err: errors.New("hidden")}.LoadModels(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("LoadModels() error = %v, want context.Canceled", err)
	}
}

func TestDiscoveryFromSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		snap modelinventory.Snapshot
		want modelinventory.Discovery
	}{
		{
			name: "ok",
			snap: modelinventory.Snapshot{
				Source: modelinventory.SourceRemote,
				Models: []modelinventory.Model{{CanonicalID: "vendor/m", NativeID: "m"}},
			},
			want: modelinventory.Discovery{
				Status:     modelinventory.DiscoveryStatusOK,
				Source:     modelinventory.SourceRemote,
				ModelCount: 1,
			},
		},
		{
			name: "empty",
			snap: modelinventory.Snapshot{Source: modelinventory.SourceStaticInline},
			want: modelinventory.Discovery{
				Status:     modelinventory.DiscoveryStatusEmpty,
				Source:     modelinventory.SourceStaticInline,
				ModelCount: 0,
				ErrorCode:  modelinventory.ErrorCodeEmpty,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := modelinventory.DiscoveryFromSnapshot(tt.snap)
			if got != tt.want {
				t.Fatalf("DiscoveryFromSnapshot() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDiscoveryFromLoadError_stableCodesWithoutRawText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "operational unavailable",
			err:  &modelinventory.OperationalError{Code: modelinventory.ErrorCodeUnavailable, Err: errors.New("api key leaked")},
			want: modelinventory.ErrorCodeUnavailable,
		},
		{
			name: "timeout",
			err:  context.DeadlineExceeded,
			want: modelinventory.ErrorCodeTimeout,
		},
		{
			name: "canceled",
			err:  context.Canceled,
			want: modelinventory.ErrorCodeCanceled,
		},
		{
			name: "plain error defaults unavailable",
			err:  errors.New("disk secret path /home/user/.keys"),
			want: modelinventory.ErrorCodeUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := modelinventory.DiscoveryFromLoadError(tt.err)
			if got.Status != modelinventory.DiscoveryStatusUnavailable {
				t.Fatalf("Status = %q", got.Status)
			}
			if got.ErrorCode != tt.want {
				t.Fatalf("ErrorCode = %q, want %q", got.ErrorCode, tt.want)
			}
			if got.ErrorCode != "" && containsSensitive(got.ErrorCode) {
				t.Fatalf("ErrorCode leaked sensitive text: %q", got.ErrorCode)
			}
		})
	}
}

func TestIsOperational(t *testing.T) {
	t.Parallel()

	if modelinventory.IsOperational(nil) {
		t.Fatal("nil should not be operational")
	}
	if !modelinventory.IsOperational(&modelinventory.OperationalError{Code: modelinventory.ErrorCodeUnavailable}) {
		t.Fatal("OperationalError should be operational")
	}
	if !modelinventory.IsOperational(context.DeadlineExceeded) {
		t.Fatal("deadline exceeded should be operational")
	}
	if modelinventory.IsOperational(errors.New("plain")) {
		t.Fatal("plain error should not match IsOperational without Operational marker")
	}
}

func containsSensitive(s string) bool {
	return strings.Contains(s, "secret") || strings.Contains(s, "api key") || strings.Contains(s, "/home/")
}
