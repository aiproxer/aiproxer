//go:build precommit

package archtest

import (
	"testing"
)

func TestRuntimehostOwnership_ProductionCallerGraph(t *testing.T) {
	assertRuntimehostOwnershipForContexts(t, archSupportedBuildContexts)
}

func TestRuntimehostOwnership_RogueConstructorCallerDetected(t *testing.T) {
	assertRuntimehostRogueConstructorForContexts(t, archSupportedBuildContexts)
}
