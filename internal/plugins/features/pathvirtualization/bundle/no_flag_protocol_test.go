package bundle_test

// The feature's reachability lever is its own typed configuration, not a process
// flag, and this file pins that at the level where a flag would have to appear.
//
// The classification matters enough to state. Task 9.2 is BEHAVIORAL: the feature
// becomes reachable in stock composition, which is precisely why a flag would be
// attractive - an operator-visible kill switch for a rollout that mutates outbound
// requests. The repo's own precedent is that every other behavioral task in this
// specification declined it (5.3, 6.1, 6.2, 7.1, 7.2, 8.1, 8.2, 9.1), and this file
// checks that the decline is real rather than assumed.
//
// The reason is that requirements.md 7.1 already provides the lever, in the right
// place and with better semantics. The `enabled` key under the feature's own
// subtree is read at generation-compilation time, so turning it off produces a
// generation that publishes NOTHING - not one that runs a pass which checks a
// boolean and returns early. That distinction is the whole argument:
//
//   - a flag checked inside a pass leaves the pass in every request and every
//     completion, so the "off" state still costs the hot path and still shows up in
//     inventory;
//   - a configuration key removes the participant from the plane set, so the off
//     state is genuinely absent, and the diagnostic inventory shows it as absent
//     rather than as present-and-disabled;
//   - a process flag is not scoped to a generation, so it cannot express "this
//     generation has it off" at all, and a reload could race a request against it.
//
// A flag would also be a service-locator-shaped global, which the repo's
// construction rules forbid outright, and it would put rollout state somewhere no
// configuration audit can see.
//
// The check is structural for the same reason the shared-authority check is: a
// process flag is a call to a package-level accessor, and asserting its absence at
// runtime would mean asserting that a global is not being consulted, which no
// behavioral test can distinguish from "it is consulted and happens to be false".

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// forbiddenFlagAPIs are the package-level accessors a process flag would have to be
// read through. None may appear in the feature's own composition seam.
var forbiddenFlagAPIs = []string{
	"Getenv",
	"LookupEnv",
	"Bool",
	"BoolVar",
}

// forbiddenFlagImports are the packages a process flag arrives through.
var forbiddenFlagImports = []string{
	"os",
	"flag",
}

// TestTheFeatureHasNoProcessFlag pins that the composition seam reads no
// environment variable and calls no flag accessor.
func TestTheFeatureHasNoProcessFlag(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bundle.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse bundle.go: %v", err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		for _, forbidden := range forbiddenFlagAPIs {
			if sel.Sel.Name == forbidden {
				t.Errorf("the bundle calls %s; rollout is configured per generation, never through process state", forbidden)
			}
		}
		return true
	})
	for _, spec := range file.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("import path is not a string literal: %v", err)
		}
		for _, forbidden := range forbiddenFlagImports {
			if imported == forbidden {
				t.Errorf("the bundle imports %q; rollout is configured per generation, never through process state", imported)
			}
		}
	}
}

// TestTheFeatureTreeHasNoProcessFlag widens the same rule to the feature's whole
// production tree, so a flag cannot be introduced in a sibling subpackage where the
// composition seam would not see it.
func TestTheFeatureTreeHasNoProcessFlag(t *testing.T) {
	t.Parallel()
	// The test runs with the bundle package directory as its working directory,
	// so the parent is the feature root whose whole production tree this guard
	// covers. filepath.Dir(".") would stay in the bundle package and check only
	// its own sources while claiming the whole tree.
	root := ".."
	checked := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		checked++
		for _, spec := range file.Imports {
			imported, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				t.Errorf("import path in %s is not a string literal: %v", path, unquoteErr)
				continue
			}
			if imported == "os" || imported == "flag" {
				t.Errorf("%s imports %q; the feature's rollout lever is its typed configuration subtree", path, imported)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no production sources found; the flag-protocol guard proved nothing")
	}
}
