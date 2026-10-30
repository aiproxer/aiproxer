package diag

import (
	"context"
	"testing"
)

func TestRouteTraceBufferFrom_wrongTypeNotPanic(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), ctxBufKey{}, "not-a-buffer")
	if got := RouteTraceBufferFrom(ctx); got != nil {
		t.Fatalf("RouteTraceBufferFrom = %v, want nil", got)
	}
}
