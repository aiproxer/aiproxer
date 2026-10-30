package bundle_test

// This guard extends the lexical core's no-repository-import rule to the package
// that reads the SDK's plane and bundle contracts.
//
// The core at internal/plugins/features/pathvirtualization proves it keeps no
// repository import in its own non-test sources. This package is the reason that
// guard has to stay narrow rather than being widened to the whole feature tree: a
// composition seam must import pkg/aipsdk, so it cannot live inside the core. That
// makes it the one place in the feature where a wrong import would slip past the
// core's guard, and therefore the place that needs its own.
//
// Two boundaries are asserted, and both are requirements rather than preferences:
//
//  1. No host authority. os, path, path/filepath, runtime, and syscall would make a
//     foreign client's path unparseable or let host filesystem and process state
//     decide what a pass publishes.
//
//  2. No internal/core and no sibling feature. A feature plugin that reached into
//     internal/core would make the composition seam part of the orchestration layer,
//     and a feature that reached a SIBLING feature would couple two independently
//     configurable plugins. The cross-feature ordering rule this feature does own
//     lives in the config subpackage and is called by the composition root, which is
//     the only holder of the whole registration list.

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

var forbiddenBundleImports = []string{
	"os",
	"path",
	"path/filepath",
	"runtime",
	"syscall",
}

// modulePath is the repository module prefix.
const modulePath = "github.com/aiproxer/aiproxer"

// featureRoot is this feature's own package tree, which is the only
// internal/plugins/features subtree this package may import from.
const featureRoot = modulePath + "/internal/plugins/features/pathvirtualization"

func TestTheBundlePackageHoldsNoForbiddenAuthority(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		checked++
		for _, spec := range file.Imports {
			imported, importErr := strconv.Unquote(spec.Path.Value)
			if importErr != nil {
				t.Fatalf("import path in %s is not a string literal: %v", name, importErr)
			}
			for _, forbidden := range forbiddenBundleImports {
				if imported == forbidden {
					t.Errorf("%s imports %q; the bundle must stay host-independent", name, imported)
				}
			}
			if strings.HasPrefix(imported, modulePath+"/internal/core") {
				t.Errorf("%s imports %q; a feature plugin must not depend on internal/core", name, imported)
			}
			if strings.HasPrefix(imported, modulePath+"/internal/plugins/features/") &&
				!strings.HasPrefix(imported, featureRoot) {
				t.Errorf("%s imports %q; one feature must not depend on another", name, imported)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no package sources found; the host-authority guard proved nothing")
	}
}
