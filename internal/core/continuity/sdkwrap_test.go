package continuity_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/continuity"
)

func TestSDKStore_roundTripALeg(t *testing.T) {
	t.Parallel()
	inner, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s := continuity.SDKStore(inner)
	ctx := context.Background()
	r, err := s.CreateALeg(ctx, "k-sdk")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.FetchALeg(ctx, r.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ALegID != r.ALegID {
		t.Fatalf("got %q want %q", got.ALegID, r.ALegID)
	}
}
