package lineage

import (
	"context"
	"testing"
)

func TestTraceID_wrongTypeNotPanic(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), keyTraceID, 999)
	if got := TraceID(ctx); got != "" {
		t.Fatalf("TraceID = %q, want empty when value is not a string", got)
	}
}

func TestALegID_wrongTypeNotPanic(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), keyALegID, struct{}{})
	if got := ALegID(ctx); got != "" {
		t.Fatalf("ALegID = %q, want empty when value is not a string", got)
	}
}
