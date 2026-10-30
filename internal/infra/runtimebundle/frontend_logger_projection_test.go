package runtimebundle

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
)

// TestBuildStandardHTTPInputProjectsProcessLogger proves the composition root
// hands the process logger to the mounted frontends. A frontend without a
// logger drops every internal failure, so the operator never sees why a
// request failed.
func TestBuildStandardHTTPInputProjectsProcessLogger(t *testing.T) {
	t.Parallel()
	log := testkit.DiscardLogger()
	cand := &candidateAssembly{process: candidateProcessRefs{ps: &ProcessServices{Logger: log}}}
	in := buildStandardHTTPInput(context.Background(), cand, nil, nil, "")
	if in.Frontends.Log != log {
		t.Fatalf("frontends logger = %v, want the process logger", in.Frontends.Log)
	}
}

// TestBuildStandardHTTPInputWithoutProcessKeepsFrontendLoggerNil proves a
// candidate without process services does not invent a logger: the frontend
// falls back to the standard library default logger instead of a fabricated
// host logger.
func TestBuildStandardHTTPInputWithoutProcessKeepsFrontendLoggerNil(t *testing.T) {
	t.Parallel()
	in := buildStandardHTTPInput(context.Background(), &candidateAssembly{}, nil, nil, "")
	if in.Frontends.Log != nil {
		t.Fatalf("frontends logger = %v, want nil", in.Frontends.Log)
	}
}
