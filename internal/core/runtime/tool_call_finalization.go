package runtime

import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func (e *Executor) setToolCallFinalizers(fs []toolcall.Finalizer, maxArgsBytes int) {
	if e == nil {
		return
	}
	e.toolCallFinalizers = append([]toolcall.Finalizer(nil), fs...)
	e.ToolCallFinalizationMaxArgsBytes = maxArgsBytes
}

// SetToolCallFinalizers installs completed-call finalizers used by the per-B-leg
// assembler. Composition roots normally merge these via the request runtime
// snapshot; harnesses may call this directly when they do not run feature-bundle merge.
func (e *Executor) SetToolCallFinalizers(fs []toolcall.Finalizer, maxArgsBytes int) {
	e.setToolCallFinalizers(fs, maxArgsBytes)
}

func (e *Executor) resolveToolCallFinalizers() ([]toolcall.Finalizer, int) {
	if e == nil {
		return nil, 0
	}
	maxArgs := e.ToolCallFinalizationMaxArgsBytes
	// Test harness override takes precedence; production uses the snapshot.
	if len(e.toolCallFinalizers) > 0 {
		return e.toolCallFinalizers, maxArgs
	}
	if e.RuntimeSnapshot != nil {
		return e.RuntimeSnapshot.ToolCallFinalizersExecution(), maxArgs
	}
	return nil, maxArgs
}
