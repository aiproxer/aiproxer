//go:build precommit

package archtest

import (
	"testing"
)

// Full release certification stays explicit; ordinary tests retain the bounded behavioral regressions.
func TestGOWORKOff_RootListBuildModuleGraph(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	plan := buildGOWORKOffCommandPlan(root, t.TempDir())
	runGOWORKOffCommandPlan(t, plan)
}
