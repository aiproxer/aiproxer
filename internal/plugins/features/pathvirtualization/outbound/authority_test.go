package outbound_test

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// modulePath is the repository module prefix.
const modulePath = "github.com/aiproxer/aiproxer"

// forbiddenHostImports lists packages this pass must never depend on.
//
// The pass publishes a backend-bound candidate derived from a workspace root a
// client controls. A host path helper here would make a foreign client path
// unparseable on the wrong operating system, and os or runtime would let host
// filesystem and process state decide what a backend sees, so none of them can
// appear. The lexical core and the shared rewriter keep the same guard on their own
// sources; this one covers the contribution that joins them to the canonical SDK.
var forbiddenHostImports = []string{
	"os",
	"path",
	"path/filepath",
	"runtime",
	"syscall",
}

// ownTree is this feature's own prefix. A contribution may reach the lexical core
// and the shared rewriter - that dependency is the feature's whole design - so the
// sibling-feature rule below is applied only outside it.
const ownTree = modulePath + "/internal/plugins/features/pathvirtualization"

// forbiddenDependencyPrefixes are repository zones this feature must not reach.
//
// internal/core is the runtime the pass plugs into, so importing it would invert the
// dependency direction design.md "Ownership" forbids outright. internal/infra owns
// composition and standard-distribution registration (Task 9.2), and a sibling
// feature owns its own policy, so none of the three can appear here either.
var forbiddenDependencyPrefixes = []string{
	modulePath + "/internal/core",
	modulePath + "/internal/infra",
	modulePath + "/internal/plugins/features/",
}

// TestOutboundPassHasNoHostOrCoreAuthority parses this package's own non-test
// sources and proves the contribution holds no host OS, host separator, or
// filesystem authority, and depends on no other feature or on the runtime it plugs
// into.
func TestOutboundPassHasNoHostOrCoreAuthority(t *testing.T) {
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
			for _, forbidden := range forbiddenHostImports {
				if imported == forbidden {
					t.Errorf("%s imports %q; the outbound pass must stay host-independent", name, imported)
				}
			}
			if !strings.HasPrefix(imported, modulePath) || strings.HasPrefix(imported, ownTree) {
				continue
			}
			for _, forbidden := range forbiddenDependencyPrefixes {
				if strings.HasPrefix(imported, forbidden) {
					t.Errorf("%s imports %q; a feature plugin must not depend on %s",
						name, imported, strings.TrimPrefix(forbidden, modulePath+"/"))
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no package sources found; the authority guard proved nothing")
	}
}
