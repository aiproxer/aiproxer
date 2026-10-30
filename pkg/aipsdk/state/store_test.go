package state_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

func TestDisabledStore(t *testing.T) {
	t.Parallel()
	var s state.Store = state.DisabledStore{}
	ctx := context.Background()
	_, err := s.Get(ctx, state.ScopeRequest, "ns", "k", new(string))
	if err == nil {
		t.Fatal("want ErrNotConfigured")
	}
	if err != state.ErrNotConfigured {
		t.Fatalf("got %v", err)
	}
	if err := s.Put(ctx, state.ScopeRequest, "ns", "k", "v", 0); err != state.ErrNotConfigured {
		t.Fatalf("put: %v", err)
	}
}
