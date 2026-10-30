package acp

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func TestModelIndex_ReplaceClearsRemoved(t *testing.T) {
	t.Parallel()

	idx := NewModelIndex(func(native string) string { return "vendor/" + native })
	idx.Replace([]modelinventory.Model{
		{CanonicalID: "vendor/a", NativeID: "a"},
		{CanonicalID: "vendor/b", NativeID: "b"},
	})
	idx.Replace([]modelinventory.Model{
		{CanonicalID: "vendor/a", NativeID: "a"},
	})
	if idx.IsKnownNative("b") {
		t.Fatal("removed slug must be cleared")
	}
	idx.Replace(nil)
	if idx.IsKnownNative("a") {
		t.Fatal("empty snapshot must clear allowlist")
	}
}

func TestModelIndex_CanonicalFallback(t *testing.T) {
	t.Parallel()

	idx := NewModelIndex(func(native string) string { return "vendor/" + native })
	idx.Replace([]modelinventory.Model{{NativeID: "slug"}})
	native, ok := idx.NativeForCanonical("vendor/slug")
	if !ok || native != "slug" {
		t.Fatalf("NativeForCanonical = %q,%v, want slug,true", native, ok)
	}
}

func TestModelIndex_EmptyCanonicalFallbackSkipped(t *testing.T) {
	t.Parallel()

	idx := NewModelIndex(func(string) string { return "" })
	idx.Replace([]modelinventory.Model{{NativeID: "pretty"}})
	if !idx.IsKnownNative("pretty") {
		t.Fatal("native must still be indexed")
	}
	if _, ok := idx.NativeForCanonical("pretty"); ok {
		t.Fatal("empty fallback must not create canonical entry")
	}
}

func TestModelIndex_NilFallbackIndexesNativeOnly(t *testing.T) {
	t.Parallel()

	idx := NewModelIndex(nil)
	idx.Replace([]modelinventory.Model{{NativeID: "only-native"}})
	if !idx.IsKnownNative("only-native") {
		t.Fatal("native must be indexed")
	}
	if _, ok := idx.NativeForCanonical("only-native"); ok {
		t.Fatal("nil fallback must not invent canonical")
	}
}

func TestModelIndex_NilReceiver(t *testing.T) {
	t.Parallel()

	var idx *ModelIndex
	idx.Replace([]modelinventory.Model{{NativeID: "x"}})
	if idx.IsKnownNative("x") {
		t.Fatal("nil receiver IsKnownNative must be false")
	}
	if _, ok := idx.NativeForCanonical("x"); ok {
		t.Fatal("nil receiver NativeForCanonical must be false")
	}
}

func TestTrackingInventory_LoadModelsDoesNotUpdateIndex(t *testing.T) {
	t.Parallel()

	idx := NewModelIndex(func(native string) string { return "vendor/" + native })
	ti := NewTrackingInventory(modelinventory.StaticProvider{
		Source: modelinventory.SourceStaticInline,
		Models: []modelinventory.Model{{CanonicalID: "vendor/a", NativeID: "a"}},
	}, idx, "test")
	if _, err := ti.LoadModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if idx.IsKnownNative("a") {
		t.Fatal("LoadModels must not update index before AcceptInventory")
	}
	ti.AcceptInventory([]modelinventory.Model{{CanonicalID: "vendor/a", NativeID: "a"}})
	if !idx.IsKnownNative("a") {
		t.Fatal("AcceptInventory must update index")
	}
}

func TestTrackingInventory_AcceptInventory(t *testing.T) {
	t.Parallel()

	idx := NewModelIndex(nil)
	ti := NewTrackingInventory(nil, idx, "test")
	ti.AcceptInventory([]modelinventory.Model{{CanonicalID: "v/a", NativeID: "a"}})
	if !idx.IsKnownNative("a") {
		t.Fatal("AcceptInventory must update index")
	}
	ti.AcceptInventory(nil)
	if idx.IsKnownNative("a") {
		t.Fatal("AcceptInventory(nil) must clear index")
	}
}

func TestTrackingInventory_nilInner(t *testing.T) {
	t.Parallel()

	ti := NewTrackingInventory(nil, NewModelIndex(nil), "testlabel")
	_, err := ti.LoadModels(context.Background())
	var op *modelinventory.OperationalError
	if !errors.As(err, &op) || op.Code != modelinventory.ErrorCodeUnavailable {
		t.Fatalf("LoadModels nil inner = %v, want OperationalError unavailable", err)
	}
	if op.Err == nil || op.Err.Error() != "testlabel: nil inventory provider" {
		t.Fatalf("error text = %v", op.Err)
	}
}

func TestTrackingInventory_NilReceiver(t *testing.T) {
	t.Parallel()

	var ti *TrackingInventory
	ti.SetInner(modelinventory.StaticProvider{})
	if ti.StaticInventory() {
		t.Fatal("nil receiver StaticInventory must be false")
	}
	_, err := ti.LoadModels(context.Background())
	var op *modelinventory.OperationalError
	if !errors.As(err, &op) || op.Code != modelinventory.ErrorCodeUnavailable {
		t.Fatalf("LoadModels nil receiver = %v, want OperationalError unavailable", err)
	}
	if op.Err == nil || op.Err.Error() != "nil tracking inventory" {
		t.Fatalf("error text = %v", op.Err)
	}
	ti.AcceptInventory([]modelinventory.Model{{NativeID: "x"}})
	if ti.Index() != nil {
		t.Fatal("nil receiver Index must be nil")
	}
}

func TestTrackingInventory_nilContext(t *testing.T) {
	t.Parallel()

	// Inner that would panic/ignore nil — TrackingInventory must reject first.
	ti := NewTrackingInventory(nilAcceptingNilContextProvider{}, NewModelIndex(nil), "test")
	//nolint:staticcheck // SA1012: nil Context is the API contract under test.
	_, err := ti.LoadModels(nil)
	if !errors.Is(err, modelinventory.ErrNilContext) {
		t.Fatalf("LoadModels(nil) = %v, want ErrNilContext", err)
	}
}

type nilAcceptingNilContextProvider struct{}

func (nilAcceptingNilContextProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, errors.New("inner saw nil context")
	}
	return modelinventory.Snapshot{}, nil
}

func TestTrackingInventory_canceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ti := NewTrackingInventory(modelinventory.StaticProvider{
		Source: modelinventory.SourceStaticInline,
		Models: []modelinventory.Model{{CanonicalID: "v/a", NativeID: "a"}},
	}, NewModelIndex(nil), "test")
	_, err := ti.LoadModels(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("LoadModels canceled = %v, want context.Canceled", err)
	}
}

func TestTrackingInventory_StaticInventory(t *testing.T) {
	t.Parallel()

	ti := NewTrackingInventory(modelinventory.StaticProvider{
		Source: modelinventory.SourceStaticInline,
		Models: []modelinventory.Model{},
	}, NewModelIndex(nil), "test")
	if !ti.StaticInventory() {
		t.Fatal("StaticInventory must delegate to inner StaticProvider")
	}
	ti.SetInner(nil)
	if ti.StaticInventory() {
		t.Fatal("nil inner StaticInventory must be false")
	}
}

func TestTrackingInventory_SetInnerPreservesIndex(t *testing.T) {
	t.Parallel()

	idx := NewModelIndex(func(native string) string { return "vendor/" + native })
	ti := NewTrackingInventory(modelinventory.StaticProvider{
		Source: modelinventory.SourceStaticInline,
		Models: []modelinventory.Model{
			{CanonicalID: "vendor/a", NativeID: "a"},
			{CanonicalID: "vendor/b", NativeID: "b"},
		},
	}, idx, "test")
	if _, err := ti.LoadModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	indexBefore := ti.Index()
	ti.AcceptInventory([]modelinventory.Model{
		{CanonicalID: "vendor/a", NativeID: "a"},
		{CanonicalID: "vendor/b", NativeID: "b"},
	})

	ti.SetInner(modelinventory.StaticProvider{
		Source: modelinventory.SourceStaticInline,
		Models: []modelinventory.Model{{CanonicalID: "vendor/a", NativeID: "a"}},
	})
	if ti.Index() != indexBefore {
		t.Fatal("SetInner must preserve tracking index object")
	}
	snap, err := ti.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// LoadModels alone must not shrink the allowlist; AcceptInventory commits.
	if !ti.Index().IsKnownNative("b") {
		t.Fatal("LoadModels must leave prior allowlist until AcceptInventory")
	}
	ti.AcceptInventory(snap.Models)
	if ti.Index().IsKnownNative("b") {
		t.Fatal("AcceptInventory override must clear unadvertised b")
	}
	if !ti.Index().IsKnownNative("a") {
		t.Fatal("AcceptInventory override must keep a")
	}
}
