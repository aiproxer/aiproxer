package runtimebundle_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
)

// TestGeneration_SystemPromptLaunchSequenceSharedAcrossRebuilds proves the
// launch chronology is process-owned, not generation-owned: two overlapping
// generations compiled from the same ProcessServices borrow one sequence, and a
// request through either advances the same ordinals. A per-generation counter
// would restart here and let a reload break attribution ordering.
func TestGeneration_SystemPromptLaunchSequenceSharedAcrossRebuilds(t *testing.T) {
	t.Parallel()
	ps, cleanup := newDigestKeyProcess(t, true, false)
	defer cleanup()
	compile := func(name string) *runtime.Executor {
		bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
			Process: ps,
			Compose: stubCompose,
		})
		if err != nil {
			t.Fatalf("CompileGeneration %s: %v", name, err)
		}
		t.Cleanup(func() { _ = bundle.Close() })
		exec := runtimebundle.GenerationExecutorOf(bundle)
		if exec == nil {
			t.Fatalf("%s executor is nil", name)
		}
		return exec
	}
	firstGen := compile("first")
	// Keep the first generation open: reload retirement can overlap the new
	// generation, and both must draw from one chronology.
	secondGen := compile("second")
	seq := firstGen.SystemPromptLaunchSeq
	if seq == nil {
		t.Fatal("process-owned launch sequence is nil")
	}
	if secondGen.SystemPromptLaunchSeq != seq {
		t.Fatal("generations must share one process-owned launch sequence")
	}
	if ps.SystemPromptLaunchSeq != seq {
		t.Fatal("generation must borrow the ProcessServices launch sequence")
	}
	start := seq.Next()
	if _, err := firstGen.Execute(context.Background(), nil); err == nil {
		t.Fatal("nil call must be rejected after stamping the launch ordinal")
	}
	if _, err := secondGen.Execute(context.Background(), nil); err == nil {
		t.Fatal("nil call must be rejected after stamping the launch ordinal")
	}
	end := seq.Next()
	if end != start+3 {
		t.Fatalf("launch ordinals restarted across generations: start=%d end=%d want %d", start, end, start+3)
	}
}
