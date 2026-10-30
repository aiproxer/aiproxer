package modelview_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/modelview"
)

func TestIdentity_DeriveStableDigest(t *testing.T) {
	t.Parallel()
	a := modelview.Derive(1, "fp", "reg-a", "cat-a")
	b := modelview.Derive(1, "fp", "reg-a", "cat-a")
	c := modelview.Derive(1, "fp", "reg-b", "cat-a")
	if a.Digest == "" || a.Digest != b.Digest {
		t.Fatalf("digest unstable: %q %q", a.Digest, b.Digest)
	}
	if a.Digest == c.Digest {
		t.Fatal("digest must change when registry generation changes")
	}
	if a.QuotedETag() != `"`+a.Digest+`"` {
		t.Fatalf("QuotedETag = %q", a.QuotedETag())
	}
}

func TestIdentity_ContextRoundTrip(t *testing.T) {
	t.Parallel()
	id := modelview.Derive(2, "safe-fp", "r1", "c1")
	ctx := modelview.WithIdentity(context.Background(), id)
	got, ok := modelview.FromContext(ctx)
	if !ok || got.Digest != id.Digest || got.ConfigGeneration != 2 {
		t.Fatalf("got=%+v ok=%v", got, ok)
	}
	if _, ok := modelview.FromContext(context.Background()); ok {
		t.Fatal("empty ctx must miss identity")
	}
}
