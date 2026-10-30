package runtimebundle_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
)

func hostServeCleanup(t *testing.T, host *runtimebundle.Host) {
	t.Helper()
	t.Cleanup(func() {
		if host != nil {
			_ = host.Close(context.Background())
		}
	})
}

func compileCandidateAfterHost(t *testing.T, host *runtimebundle.Host) *runtimebundle.CandidateHTTPCompile {
	t.Helper()
	if host == nil || runtimebundle.HostProcess(host) == nil {
		t.Fatal("expected Process after BuildHost")
	}
	cand, err := runtimebundle.CompileCandidate(t.Context(), runtimebundle.GenerationCompileInput{
		Process: runtimebundle.HostProcess(host),
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate: %v", err)
	}
	t.Cleanup(func() { _ = cand.Close() })
	return cand
}
