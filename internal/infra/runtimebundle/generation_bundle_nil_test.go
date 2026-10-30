package runtimebundle_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
)

// TestGenerationBundle_AbsentReceiverTerminalDecisionProvider characterizes
// Requirement 1.1 (Task 1.1, Task 2.1): invoking TerminalDecisionProvider on an absent (*GenerationBundle)(nil)
// must return a nil terminaldecision.Provider without panicking.
func TestGenerationBundle_AbsentReceiverTerminalDecisionProvider(t *testing.T) {
	t.Parallel()

	var b *runtimebundle.GenerationBundle
	require.Nil(t, b.TerminalDecisionProvider())
}
