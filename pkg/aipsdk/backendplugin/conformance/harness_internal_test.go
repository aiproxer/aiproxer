package conformance

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type ctxCaptureInstance struct {
	got context.Context
}

func (c *ctxCaptureInstance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{}, nil
}

func (c *ctxCaptureInstance) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}

func (c *ctxCaptureInstance) Execute(s backendplugin.ExecuteStream) error {
	c.got = s.Context()
	return nil
}

func (c *ctxCaptureInstance) Close(context.Context) error { return nil }

// runExecute must thread the runner ctx into the stream instead of substituting
// context.Background, so cancellation/values reach plugin Execute implementations.
func TestRunExecute_ThreadsRunnerContext(t *testing.T) {
	t.Parallel()
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	inst := &ctxCaptureInstance{}
	if _, err := runExecute(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if inst.got == nil {
		t.Fatal("stream Context() returned nil")
	}
	if inst.got.Value(ctxKey{}) != "marker" {
		t.Fatal("stream Context() must be the runner ctx, not context.Background()")
	}
}
