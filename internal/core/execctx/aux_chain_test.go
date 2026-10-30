package execctx_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
)

func TestIncAuxiliaryDepth_capsAtMax(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for i := 1; i <= execctx.MaxAuxiliaryDepth; i++ {
		var ok bool
		ctx, ok = execctx.IncAuxiliaryDepth(ctx)
		if !ok {
			t.Fatalf("iteration %d: unexpected false", i)
		}
		if got := execctx.AuxiliaryDepth(ctx); got != i {
			t.Fatalf("depth want %d got %d", i, got)
		}
	}
	_, ok := execctx.IncAuxiliaryDepth(ctx)
	if ok {
		t.Fatal("expected depth increment to fail at max")
	}
}

func TestSuppressedPluginIDs_exactMatch(t *testing.T) {
	t.Parallel()
	ctx := execctx.WithSuppressedPluginIDs(context.Background(), []string{"b", "a", "b"})
	if execctx.IsSuppressedPluginID(ctx, "nope") {
		t.Fatal("unexpected suppression")
	}
	if !execctx.IsSuppressedPluginID(ctx, "a") || !execctx.IsSuppressedPluginID(ctx, "b") {
		t.Fatal("want suppression for a and b")
	}
}
