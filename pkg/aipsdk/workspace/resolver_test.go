package workspace_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestDisabledResolver(t *testing.T) {
	t.Parallel()
	var r workspace.Resolver = workspace.DisabledResolver{}
	_, err := r.Resolve(context.Background())
	if err != workspace.ErrResolverNotConfigured {
		t.Fatalf("got %v", err)
	}
}
