package execctx_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
)

func TestDetachedSessionRoundTripPreservesTrustedRoleAndLineage(t *testing.T) {
	t.Parallel()

	want := execctx.DetachedSession{
		ParentSessionID:     "parent-session",
		ParentALegID:        "parent-a-leg",
		ParentTraceID:       "parent-trace",
		ParentBranchBinding: "parent-branch",
		AuxiliaryRole:       "compaction_continuity_extractor",
	}
	got, ok := execctx.DetachedSessionFromContext(execctx.WithDetachedSession(context.Background(), want))
	if !ok {
		t.Fatal("detached session marker missing")
	}
	if got != want {
		t.Fatalf("detached session metadata = %+v, want %+v", got, want)
	}
}
