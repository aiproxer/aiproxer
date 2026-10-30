//go:build precommit

package runtimebundle

import (
	"strings"
	"testing"
)

// The whole-module Linux/Windows analysis runs in the dedicated CI owner gate.
func TestRuntimebundle_NoCompleteOwnerCallbackEscapes(t *testing.T) {
	t.Parallel()
	pkgs, analyzed := loadOwnerReachablePackagesAcrossContexts(t, nil)
	ownerPkg := packageByPath(t, pkgs, runtimebundlePkgPath)
	// Scope sentinels: a narrowed runtimebundle-only load must fail this gate.
	packageByPath(t, pkgs, aipruntimePkgPath)
	packageByPath(t, pkgs, aipstdPkgPath)
	owners := resolveProtectedOwners(t, ownerPkg)
	if hits := findOwnerCallbackEscapesInPackages(pkgs, owners); len(hits) > 0 {
		t.Fatalf("forbidden complete-owner callback escapes:\n%s", strings.Join(hits, "\n"))
	}
	assertOwnerReachableProductionInventory(t, pkgs, analyzed)
}
