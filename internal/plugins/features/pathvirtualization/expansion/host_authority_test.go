package expansion_test

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// forbiddenHostImports lists the packages the expansion pass must never depend on.
//
// The pass reads untrusted model output and decides whether a client-facing
// filesystem path may be released. A host path helper here would make a foreign
// client path unparseable, and os or runtime would let host filesystem and process
// state decide whether a tool call is refused, so none of them can appear here. The
// lexical core keeps the same guarantee for its own sources; this guard extends it
// to the step that consumes canonical tool contracts.
var forbiddenHostImports = []string{
	"os",
	"path",
	"path/filepath",
	"runtime",
	"syscall",
}

// modulePath is the repository module prefix.
const modulePath = "github.com/aiproxer/aiproxer"

// TestExpansionHasNoHostAuthority parses this package's own non-test sources and
// proves the expansion pass holds no host OS, host separator, or filesystem
// authority, and no dependency on orchestration the feature must not reach.
func TestExpansionHasNoHostAuthority(t *testing.T) {
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
					t.Errorf("%s imports %q; the expansion pass must stay host-independent", name, imported)
				}
			}
			if strings.HasPrefix(imported, modulePath+"/internal/core") {
				t.Errorf("%s imports %q; a feature plugin must not depend on internal/core", name, imported)
			}
			// The feature consumes the SDK's generic finalizer contract. It must not
			// reach a SIBLING feature for anything, least of all to read another
			// feature's order constant: the ordering relationship is asserted in this
			// package's tests and realized by one number.
			if strings.HasPrefix(imported, modulePath+"/internal/plugins/features/") &&
				!strings.HasPrefix(imported, modulePath+"/internal/plugins/features/pathvirtualization") {
				t.Errorf("%s imports %q; one feature must not depend on another", name, imported)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no package sources found; the host-authority guard proved nothing")
	}
}
